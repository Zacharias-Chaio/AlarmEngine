package api

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"alarmengine/internal/store"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// ListAlarms 返回未结案告警列表（handled=false），支持类型/来源/等级/展示状态筛选与排序。
// 展示状态三态：活动中=触发持续中（含未确认/已确认）；已恢复=自动恢复未结案；孤儿=触发源失联。
func (s *Server) ListAlarms(w http.ResponseWriter, r *http.Request) {
	query := s.DB.Model(&store.Alarm{}).Where("handled = ?", false)
	if severity := r.URL.Query().Get("severity"); severity != "" {
		if value, err := strconv.Atoi(severity); err == nil {
			query = query.Where("severity = ?", value)
		}
	}
	if alarmType := strings.TrimSpace(r.URL.Query().Get("type")); alarmType != "" {
		query = query.Where("type = ?", alarmType)
	}
	if source := strings.TrimSpace(r.URL.Query().Get("source")); source != "" {
		query = query.Where("source = ?", source)
	}
	if state := strings.TrimSpace(r.URL.Query().Get("state")); state != "" {
		switch state {
		case "active": // 活动中：触发持续中（不论是否已确认）
			query = query.Where("state = ? AND orphan = ?", store.AlarmStateActive, false)
		case "recovered": // 已恢复：自动恢复、未结案
			query = query.Where("state = ?", store.AlarmStateCleared)
		case "orphan": // 孤儿：触发源失联（配置变更/设备离线等）
			query = query.Where("orphan = ?", true)
		}
	}
	if r.URL.Query().Get("sort") == "duration" {
		query = query.Order("duration_ms desc")
	} else {
		query = query.Order("occurred_at desc")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	page, pageSize := paging(r)
	list := make([]store.Alarm, 0, pageSize)
	if err := query.Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, Paged{List: list, Total: total, Page: page, PageSize: pageSize})
}

// SeverityStats 单级别统计口径：活动中 / 未确认 / 未处理（未处理即该级别未结案总数）。
type SeverityStats struct {
	Active    int `json:"active"`    // 活动中（触发持续中且非孤儿）
	Unacked   int `json:"unacked"`   // 未确认（触发持续中且未人工确认）
	Unhandled int `json:"unhandled"` // 未处理（未结案）
}

// TodayStats 今日告警统计：总数、24h 逐小时趋势、较昨日同期环比。
type TodayStats struct {
	Count         int          `json:"count"`         // 今日 0 点至今全部告警（含已结案）
	YesterdaySame int          `json:"yesterdaySame"` // 昨日同期（昨日 0 点至昨日此刻）
	ChangePct     *float64     `json:"changePct"`     // 环比百分比（昨日为 0 时为 null）
	Hours         []TrendPoint `json:"hours"`         // 最近 24 小时逐小时趋势
}

// AlarmSummary 告警信息看板统计。
type AlarmSummary struct {
	BySeverity map[int]SeverityStats `json:"bySeverity"` // 按级别 × 三口径
	BySource   map[string]int        `json:"bySource"`   // 未结案按来源（筛选项）
	Orphan     int                   `json:"orphan"`     // 孤儿告警数
	Today      TodayStats            `json:"today"`
}

func (s *Server) AlarmSummary(w http.ResponseWriter, r *http.Request) {
	var unresolved []store.Alarm
	if err := s.DB.Where("handled = ?", false).Find(&unresolved).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	summary := AlarmSummary{
		BySeverity: map[int]SeverityStats{},
		BySource:   map[string]int{},
	}
	for _, alarm := range unresolved {
		stats := summary.BySeverity[alarm.Severity]
		stats.Unhandled++
		if alarm.State == store.AlarmStateActive {
			if !alarm.Orphan {
				stats.Active++
			}
			if !alarm.Acknowledged {
				stats.Unacked++
			}
		}
		summary.BySeverity[alarm.Severity] = stats
		summary.BySource[alarm.Source]++
		if alarm.Orphan {
			summary.Orphan++
		}
	}
	summary.Today = s.buildTodayStats()
	ok(w, summary)
}

// buildTodayStats 今日统计：今日 0 点至今；环比 = 今日截至当前 vs 昨日同期；24h 逐小时趋势。
func (s *Server) buildTodayStats() TodayStats {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterdayStart := todayStart.AddDate(0, 0, -1)

	stats := TodayStats{Hours: make([]TrendPoint, 24)}
	hourStart := now.Truncate(time.Hour).Add(-23 * time.Hour)
	for i := range stats.Hours {
		start := hourStart.Add(time.Duration(i) * time.Hour)
		stats.Hours[i] = TrendPoint{Label: start.Format("01-02 15:00")}
	}

	var recent []store.Alarm
	if err := s.DB.Where("occurred_at >= ?", yesterdayStart).Find(&recent).Error; err != nil {
		return stats
	}

	sameDuration := now.Sub(todayStart)
	for _, alarm := range recent {
		at := alarm.OccurredAt.Local()
		switch {
		case !at.Before(todayStart):
			stats.Count++
		case !at.Before(yesterdayStart) && at.Before(yesterdayStart.Add(sameDuration)):
			stats.YesterdaySame++
		}
		// 最近 24 小时逐小时聚合（含当前整点）。
		for i := range stats.Hours {
			start := hourStart.Add(time.Duration(i) * time.Hour)
			if !at.Before(start) && at.Before(start.Add(time.Hour)) {
				stats.Hours[i].Value++
			}
		}
	}

	if stats.YesterdaySame > 0 {
		pct := math.Round(float64(stats.Count-stats.YesterdaySame)/float64(stats.YesterdaySame)*1000) / 10
		stats.ChangePct = &pct
	}
	return stats
}

// GetAlarm 返回单条告警详情。
func (s *Server) GetAlarm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var alarm store.Alarm
	if err := s.DB.First(&alarm, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			fail(w, http.StatusNotFound, "告警不存在: id="+id)
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, alarm)
}

// AcknowledgeAlarm 人工确认告警（操作员认领，进入处理阶段）。
func (s *Server) AcknowledgeAlarm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var alarm store.Alarm
	if err := s.DB.First(&alarm, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			fail(w, http.StatusNotFound, "告警不存在: id="+id)
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if reason, err := s.applyAcknowledge(&alarm); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	} else if reason != "" {
		fail(w, http.StatusConflict, reason)
		return
	}
	ok(w, alarm)
}

// applyAcknowledge 对单条告警执行人工确认：返回跳过原因（空串表示成功）。
// 重复确认保留首次确认时间（MTTA 统计依据）；已确认/已结案的告警跳过。
func (s *Server) applyAcknowledge(alarm *store.Alarm) (skipReason string, err error) {
	if alarm.Handled {
		return "告警已结案，无需确认", nil
	}
	if alarm.Acknowledged {
		return "告警已确认", nil
	}
	now := time.Now().UTC()
	alarm.AcknowledgedAt = &now
	alarm.Acknowledged = true
	if err := s.DB.Save(alarm).Error; err != nil {
		return "", err
	}
	return "", nil
}

// HandleAlarm 人工处理告警（处理员结案，进入历史告警）。
// 规则：活动中与孤儿（无法监测）告警不可关闭；一次性事件（kind=event）确认后允许直接结案，视为即时恢复。
func (s *Server) HandleAlarm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var alarm store.Alarm
	if err := s.DB.First(&alarm, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			fail(w, http.StatusNotFound, "告警不存在: id="+id)
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if reason, err := s.applyHandle(&alarm); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	} else if reason != "" {
		fail(w, http.StatusConflict, reason)
		return
	}
	ok(w, alarm)
}

// applyHandle 对单条告警执行处理结案：返回跳过原因（空串表示成功）。
// 规则与单条处理一致：孤儿不可关闭；活动中仅一次性事件（已确认）可视为即时恢复直接结案。
func (s *Server) applyHandle(alarm *store.Alarm) (skipReason string, err error) {
	if alarm.Handled {
		return "告警已处理结案", nil
	}
	if alarm.Orphan {
		return "孤儿告警（触发源失联）不可关闭，请先恢复来源监测", nil
	}
	now := time.Now().UTC()
	if alarm.State == store.AlarmStateActive {
		if alarm.Kind == store.AlarmKindEvent && alarm.Acknowledged {
			// 一次性事件无恢复沿：确认后允许直接结案，视为即时恢复。
			alarm.State = store.AlarmStateCleared
			alarm.RecoveredAt = &now
		} else {
			return "活动中的告警不可关闭，需等待告警自动恢复后再处理", nil
		}
	}
	alarm.Handled = true
	alarm.ClearedAt = &now
	// 持续时长按恢复时间计算；未记录恢复时间时按当前时刻兜底。
	end := now
	if alarm.RecoveredAt != nil {
		end = *alarm.RecoveredAt
	}
	alarm.DurationMs = end.Sub(alarm.OccurredAt).Milliseconds()
	if alarm.DurationMs < 0 {
		alarm.DurationMs = 0
	}
	if err := s.DB.Save(alarm).Error; err != nil {
		return "", err
	}
	return "", nil
}

// batchRequest 批量操作请求体：{"ids":["...","..."]}。
type batchRequest struct {
	IDs []string `json:"ids"`
}

// maxBatchSize 批量操作单次请求上限：防超大请求拖慢服务（前端按页勾选，每页最多 15 条）。
const maxBatchSize = 200

// BatchResult 批量操作单条结果：成功标记与跳过原因。
type BatchResult struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"` // 跳过/失败原因（成功时为空）
}

// BatchResponse 批量操作响应：成功数 / 跳过数 / 逐条明细。
type BatchResponse struct {
	OK      int           `json:"ok"`
	Skipped int           `json:"skipped"`
	Results []BatchResult `json:"results"`
}

// BatchAcknowledgeAlarms 批量确认告警：逐条执行，已确认/已结案跳过。
func (s *Server) BatchAcknowledgeAlarms(w http.ResponseWriter, r *http.Request) {
	s.batchOperate(w, r, s.applyAcknowledge)
}

// BatchHandleAlarms 批量处理告警：逐条执行，不可结案（活动中/孤儿等）跳过。
func (s *Server) BatchHandleAlarms(w http.ResponseWriter, r *http.Request) {
	s.batchOperate(w, r, s.applyHandle)
}

// batchOperate 批量操作通用流程：解析 ids → 去重 → 逐条执行 apply → 汇总结果。
// 单条失败不影响其余条目；全部结果通过 results 明细返回。
func (s *Server) batchOperate(w http.ResponseWriter, r *http.Request, apply func(*store.Alarm) (string, error)) {
	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		fail(w, http.StatusBadRequest, "请求体需为 {\"ids\":[...]} 且不能为空")
		return
	}
	if len(req.IDs) > maxBatchSize {
		fail(w, http.StatusBadRequest, "单次批量操作最多 "+strconv.Itoa(maxBatchSize)+" 条")
		return
	}
	resp := BatchResponse{Results: make([]BatchResult, 0, len(req.IDs))}
	seen := make(map[string]bool, len(req.IDs))
	for _, id := range req.IDs {
		if seen[id] {
			continue // 幂等去重
		}
		seen[id] = true
		var alarm store.Alarm
		if err := s.DB.First(&alarm, "id = ?", id).Error; err != nil {
			resp.Skipped++
			resp.Results = append(resp.Results, BatchResult{ID: id, OK: false, Reason: "告警不存在"})
			continue
		}
		reason, err := apply(&alarm)
		switch {
		case err != nil:
			resp.Skipped++
			resp.Results = append(resp.Results, BatchResult{ID: id, OK: false, Reason: "执行失败：" + err.Error()})
		case reason != "":
			resp.Skipped++
			resp.Results = append(resp.Results, BatchResult{ID: id, OK: false, Reason: reason})
		default:
			resp.OK++
			resp.Results = append(resp.Results, BatchResult{ID: id, OK: true})
		}
	}
	ok(w, resp)
}
