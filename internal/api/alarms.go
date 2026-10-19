package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"alarmengine/internal/domain"
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
		// 持续时长改为读取时派生，排序也按派生口径（活动=now-occurred，已恢复=recovered-occurred）。
		query = query.Order("MAX(0, julianday(COALESCE(recovered_at, strftime('%Y-%m-%d %H:%M:%f','now'))) - julianday(occurred_at)) DESC")
	} else {
		query = query.Order("occurred_at desc")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		internalError(w, err)
		return
	}
	page, pageSize := paging(r)
	list := make([]store.Alarm, 0, pageSize)
	if err := query.Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		internalError(w, err)
		return
	}
	now := time.Now()
	for i := range list {
		deriveDuration(&list[i], now)
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

// severityAggRow 看板按级别聚合行（SQL GROUP BY，避免全表加载逐行统计）。
type severityAggRow struct {
	Severity  int
	Unhandled int
	Active    int
	Unacked   int
	Orphan    int
}

func (s *Server) AlarmSummary(w http.ResponseWriter, r *http.Request) {
	summary := AlarmSummary{BySeverity: map[int]SeverityStats{}, BySource: map[string]int{}}
	var severityRows []severityAggRow
	if err := s.DB.Model(&store.Alarm{}).
		Select(`severity,
			COUNT(*) AS unhandled,
			COALESCE(SUM(CASE WHEN state = 'active' AND orphan = 0 THEN 1 ELSE 0 END), 0) AS active,
			COALESCE(SUM(CASE WHEN state = 'active' AND orphan = 0 AND acknowledged = 0 THEN 1 ELSE 0 END), 0) AS unacked,
			COALESCE(SUM(CASE WHEN orphan = 1 THEN 1 ELSE 0 END), 0) AS orphan`).
		Where("handled = ?", false).
		Group("severity").
		Scan(&severityRows).Error; err != nil {
		internalError(w, err)
		return
	}
	for _, row := range severityRows {
		summary.BySeverity[row.Severity] = SeverityStats{
			Active: row.Active, Unacked: row.Unacked, Unhandled: row.Unhandled,
		}
		summary.Orphan += row.Orphan
	}
	var sourceRows []struct {
		Source string
		Count  int
	}
	if err := s.DB.Model(&store.Alarm{}).
		Select("source, COUNT(*) AS count").
		Where("handled = ?", false).
		Group("source").
		Scan(&sourceRows).Error; err != nil {
		internalError(w, err)
		return
	}
	for _, row := range sourceRows {
		summary.BySource[row.Source] = row.Count
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

	// 今日/昨日同期计数走 COUNT 聚合（P1：索引范围扫描，不加载行）。
	sameDuration := now.Sub(todayStart)
	var todayCount, yesterdayCount int64
	if err := s.DB.Model(&store.Alarm{}).Where("occurred_at >= ?", todayStart).Count(&todayCount).Error; err != nil {
		return stats
	}
	if err := s.DB.Model(&store.Alarm{}).
		Where("occurred_at >= ? AND occurred_at < ?", yesterdayStart, yesterdayStart.Add(sameDuration)).
		Count(&yesterdayCount).Error; err != nil {
		return stats
	}
	stats.Count = int(todayCount)
	stats.YesterdaySame = int(yesterdayCount)

	// 24h 逐小时趋势：仅取时间列（窗口有界，单列加载）。
	var recent []struct{ OccurredAt time.Time }
	if err := s.DB.Model(&store.Alarm{}).Select("occurred_at").
		Where("occurred_at >= ?", hourStart).Scan(&recent).Error; err != nil {
		return stats
	}
	// 24h 逐小时趋势：单次遍历按小时索引落桶（O(n)），避免双层循环 O(n×24)。
	for _, row := range recent {
		at := row.OccurredAt.Local()
		index := int(at.Truncate(time.Hour).Sub(hourStart) / time.Hour)
		if index < 0 || index >= len(stats.Hours) {
			continue
		}
		stats.Hours[index].Value++
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(w, http.StatusNotFound, "告警不存在: id="+id)
			return
		}
		internalError(w, err)
		return
	}
	deriveDuration(&alarm, time.Now())
	ok(w, alarm)
}

// AcknowledgeAlarm 人工确认告警（操作员认领，进入处理阶段）。
func (s *Server) AcknowledgeAlarm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var alarm store.Alarm
	if err := s.DB.First(&alarm, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(w, http.StatusNotFound, "告警不存在: id="+id)
			return
		}
		internalError(w, err)
		return
	}
	if reason, err := s.applyAcknowledge(&alarm); err != nil {
		internalError(w, err)
		return
	} else if reason != "" {
		fail(w, http.StatusConflict, reason)
		return
	}
	ok(w, alarm)
}

// applyAcknowledge 对单条告警执行人工确认：返回跳过原因（空串表示成功）。
// 重复确认保留首次确认时间（MTTA 统计依据）；已确认/已结案的告警跳过。
// 条件列更新（P0）：仅未确认未结案行可写入，避免与摄取侧合并整行覆盖互踩。
func (s *Server) applyAcknowledge(alarm *store.Alarm) (skipReason string, err error) {
	if reason := domain.AcknowledgeReason(*alarm); reason != "" {
		return reason, nil
	}
	now := time.Now().UTC()
	result := s.DB.Model(&store.Alarm{}).
		Where("id = ? AND handled = ? AND acknowledged = ?", alarm.ID, false, false).
		Updates(map[string]any{"acknowledged": true, "acknowledged_at": now})
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return s.rereadSkipReason(alarm.ID, "告警状态已变化，请刷新后重试"), nil
	}
	alarm.Acknowledged = true
	alarm.AcknowledgedAt = &now
	return "", nil
}

// rereadSkipReason 条件更新零行命中时重读当前状态，给出准确的跳过原因。
func (s *Server) rereadSkipReason(id, fallback string) string {
	var fresh store.Alarm
	if err := s.DB.First(&fresh, "id = ?", id).Error; err != nil {
		return fallback
	}
	return domain.RereadSkipReason(fresh, fallback)
}

// HandleAlarm 人工处理告警（处理员结案，进入历史告警）。
// 规则：活动中与孤儿（无法监测）告警不可关闭；一次性事件（kind=event）确认后允许直接结案，视为即时恢复。
func (s *Server) HandleAlarm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var alarm store.Alarm
	if err := s.DB.First(&alarm, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(w, http.StatusNotFound, "告警不存在: id="+id)
			return
		}
		internalError(w, err)
		return
	}
	if reason, err := s.applyHandle(&alarm); err != nil {
		internalError(w, err)
		return
	} else if reason != "" {
		fail(w, http.StatusConflict, reason)
		return
	}
	ok(w, alarm)
}

// applyHandle 对单条告警执行处理结案：返回跳过原因（空串表示成功）。
// 规则与单条处理一致：孤儿不可关闭；活动中仅一次性事件（已确认）可视为即时恢复直接结案。
// 条件列更新（P0）：按前置校验组装涉事列，Where 守卫状态前置条件，避免整行 Save 丢更新。
func (s *Server) applyHandle(alarm *store.Alarm) (skipReason string, err error) {
	// 状态机判定收敛在 domain 包（可复用、可单测）：返回跳过原因，或需同步置为已恢复。
	reason, clearState := domain.HandleReason(*alarm)
	if reason != "" {
		return reason, nil
	}
	now := time.Now().UTC()
	updates := map[string]any{"handled": true, "cleared_at": now}
	query := s.DB.Model(&store.Alarm{}).Where("id = ? AND handled = ?", alarm.ID, false)
	if clearState {
		updates["state"] = store.AlarmStateCleared
		updates["recovered_at"] = now
		query = query.Where("state = ? AND acknowledged = ?", store.AlarmStateActive, true)
	}
	// 持续时长按恢复时间计算；未记录恢复时间时按当前时刻兜底。
	end := now
	if alarm.RecoveredAt != nil {
		end = *alarm.RecoveredAt
	}
	duration := end.Sub(alarm.OccurredAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	updates["duration_ms"] = duration
	result := query.Updates(updates)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return s.rereadSkipReason(alarm.ID, "告警状态已变化，请刷新后重试"), nil
	}
	alarm.Handled = true
	alarm.ClearedAt = &now
	alarm.DurationMs = duration
	if state, exists := updates["state"]; exists {
		alarm.State = state.(string)
		alarm.RecoveredAt = &now
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
