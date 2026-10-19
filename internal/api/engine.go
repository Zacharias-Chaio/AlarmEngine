package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"alarmengine/internal/domain"
	"alarmengine/internal/store"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// ListRules 返回全部告警引擎规则。
func (s *Server) ListRules(w http.ResponseWriter, r *http.Request) {
	var list []store.AlarmRule
	if err := s.DB.Order("id asc").Find(&list).Error; err != nil {
		internalError(w, err)
		return
	}
	ok(w, list)
}

// SaveRule 创建或更新规则（upsert）：事件沿匹配 + 保护动作（.cmd）。
func (s *Server) SaveRule(w http.ResponseWriter, r *http.Request) {
	var rule store.AlarmRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		fail(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if err := domain.ValidateRule(&rule); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var exist store.AlarmRule
	if err := s.DB.First(&exist, "id = ?", rule.ID).Error; err == nil {
		// 乐观锁（P2）：以客户端提交的版本号作为前置条件（rule.Version），
		// 与数据库当前版本不一致即冲突（409）——防止旧客户端数据整行覆盖。
		// 版本号用 SQL 表达式自增（version + 1）而非旧读值 +1，避免并发交错下版本原地踏步。
		result := s.DB.Model(&store.AlarmRule{}).
			Where("id = ? AND version = ?", rule.ID, rule.Version).
			Updates(ruleUpdateMap(&rule))
		if result.Error != nil {
			internalError(w, result.Error)
			return
		}
		if result.RowsAffected == 0 {
			fail(w, http.StatusConflict, "规则已被其他会话修改，请刷新后重试")
			return
		}
		// WHERE 命中即说明库中版本等于客户端提交值，新版本 = 提交值 + 1。
		rule.Version++
		if s.Runtime != nil {
			s.Runtime.InvalidateRules() // 保存即生效：规则缓存与运行态同步失效
		}
		ok(w, rule)
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		internalError(w, err)
		return
	}
	// 记录不存在：仅当客户端明确是"新建"（version=0）时创建；
	// version>0 说明客户端拿着已存在规则在保存，而它已被删除 → 404，避免静默"复活"。
	if rule.Version > 0 {
		fail(w, http.StatusNotFound, "规则已被删除，请刷新后重新创建")
		return
	}
	// Create 必须走显式列 map：结构体 Create 时 GORM 会用字段 default tag 覆盖零值，
	// 导致 enabled:false（停用规则）被写成 true 并立即参与匹配下发。
	rule.Version = 0
	now := time.Now().UTC()
	if err := s.DB.Model(&store.AlarmRule{}).Create(ruleCreateMap(&rule, now)).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			fail(w, http.StatusConflict, "规则已被并发创建，请刷新后重试")
			return
		}
		internalError(w, err)
		return
	}
	if s.Runtime != nil {
		s.Runtime.InvalidateRules()
	}
	ok(w, rule)
}

// ruleCreateMap 规则创建列集：显式列避开 GORM 默认值对零值字段的覆盖（enabled:false 必须原样落库）。
// monitors/controls 为 Valuer 类型（JSON 落库），map 创建经驱动 Valuer 序列化。
func ruleCreateMap(rule *store.AlarmRule, now time.Time) map[string]any {
	return map[string]any{
		"id":          rule.ID,
		"name":        rule.Name,
		"description": rule.Description,
		"version":     0,
		"enabled":     rule.Enabled,
		"match_mode":  rule.MatchMode,
		"monitors":    rule.Monitors,
		"controls":    rule.Controls,
		"cooldown_ms": rule.CooldownMs,
		"created_at":  now,
		"updated_at":  now,
	}
}

// ruleUpdateMap 规则更新列集（与模型字段保持同步；显式列避免结构体 Updates 跳过零值）。
// monitors/controls 为 Valuer 类型（JSON 落库），map 更新经驱动 Valuer 序列化。
// version 用 SQL 表达式自增，避免并发交错下"旧读 +1"原地踏步。
func ruleUpdateMap(rule *store.AlarmRule) map[string]any {
	return map[string]any{
		"name":        rule.Name,
		"description": rule.Description,
		"enabled":     rule.Enabled,
		"match_mode":  rule.MatchMode,
		"monitors":    rule.Monitors,
		"controls":    rule.Controls,
		"cooldown_ms": rule.CooldownMs,
		"version":     gorm.Expr("version + 1"),
	}
}

// DeleteRule 删除指定规则。
func (s *Server) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	res := s.DB.Delete(&store.AlarmRule{}, "id = ?", id)
	if res.Error != nil {
		internalError(w, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		fail(w, http.StatusNotFound, "规则不存在: id="+id)
		return
	}
	if s.Runtime != nil {
		s.Runtime.ResetRuleState(id) // 清理边沿/冷却内存态，避免同 ID 重建时残留
	}
	ok(w, map[string]string{"id": id})
}

// actionView 动作记录的用户可读视图：在原始记录上附加设备名/服务名/告警名。
// 前端据此展示"设备名 · 服务名 = 值"，内部 ID 仅保留在原始字段（供排查/接口兼容）。
type actionView struct {
	store.ActionRecord
	DeviceName  string `json:"deviceName,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	AlarmName   string `json:"alarmName,omitempty"`
}

// ListActions 返回保护动作下发记录（分页，默认最新 50 条，可按状态过滤）。
// 与其他列表接口统一分页口径：page 从 1 起、pageSize 默认 50、上限 200，响应含 total。
func (s *Server) ListActions(w http.ResponseWriter, r *http.Request) {
	query := s.DB.Model(&store.ActionRecord{})
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		internalError(w, err)
		return
	}
	var list []store.ActionRecord
	if err := query.Order("dispatched_at desc").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		internalError(w, err)
		return
	}
	ok(w, Paged{List: s.enrichActions(list), Total: total, Page: page, PageSize: pageSize})
}

// enrichActions 为动作记录补齐可读名称：设备/服务取自定义缓存，告警名取自告警表。
// 定义缓存或告警缺失时留空，前端回退展示设备 ID / "—"。
func (s *Server) enrichActions(list []store.ActionRecord) []actionView {
	views := make([]actionView, 0, len(list))
	if len(list) == 0 {
		return views
	}
	deviceNames := make(map[string]string)
	serviceNames := make(map[string]string) // key: deviceID|serviceKey
	if s.Runtime != nil {
		for _, def := range s.Runtime.DefinitionsSnapshot().Devices {
			device := def.Device
			if device.DeviceID == "" {
				continue
			}
			deviceNames[device.DeviceID] = device.DeviceName
			for _, method := range def.Methods {
				if method.Key != "" {
					serviceNames[device.DeviceID+"|"+method.Key] = method.Name
				}
			}
		}
	}
	alarmNames := make(map[string]string)
	ids := make([]string, 0, len(list))
	for _, record := range list {
		if record.AlarmID != "" {
			ids = append(ids, record.AlarmID)
		}
	}
	if len(ids) > 0 {
		var alarms []store.Alarm
		if err := s.DB.Select("id", "name").Where("id IN ?", ids).Find(&alarms).Error; err == nil {
			for _, alarm := range alarms {
				alarmNames[alarm.ID] = alarm.Name
			}
		}
	}
	for _, record := range list {
		view := actionView{ActionRecord: record}
		view.DeviceName = deviceNames[record.DeviceID]
		view.ServiceName = serviceNames[record.DeviceID+"|"+record.ServiceKey]
		view.AlarmName = alarmNames[record.AlarmID]
		views = append(views, view)
	}
	return views
}

// GetDefinitions 返回物模型定义缓存快照（规则编辑器下拉数据源）。
func (s *Server) GetDefinitions(w http.ResponseWriter, r *http.Request) {
	if s.Runtime == nil {
		fail(w, http.StatusServiceUnavailable, "运行时未就绪")
		return
	}
	ok(w, s.Runtime.DefinitionsSnapshot())
}

// RefreshDefinitions 手动触发定义同步（异步，前端轮询快照观察结果）。
// "NATS 未启用"属于配置前置条件不满足（412），不是服务不可用（503）。
func (s *Server) RefreshDefinitions(w http.ResponseWriter, r *http.Request) {
	if s.Runtime == nil {
		fail(w, http.StatusServiceUnavailable, "运行时未就绪")
		return
	}
	if err := s.Runtime.RefreshDefinitions(); err != nil {
		fail(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	okStatus(w, http.StatusAccepted, map[string]string{"status": "syncing"})
}
