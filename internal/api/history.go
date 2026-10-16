package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"alarmengine/internal/store"

	"gorm.io/gorm"
)

// alarmLevelNames 告警级别名（与前端 ALARM_LEVELS 一致）。
var alarmLevelNames = map[int]string{0: "提示", 1: "一般", 2: "严重", 3: "紧急"}

// ListHistory 历史告警查询，支持时间范围/类型/来源/严重程度/处理状态筛选。
func (s *Server) ListHistory(w http.ResponseWriter, r *http.Request) {
	query := s.DB.Model(&store.Alarm{})
	query = applyTimeRange(query, r)
	if alarmType := strings.TrimSpace(r.URL.Query().Get("type")); alarmType != "" {
		query = query.Where("type = ?", alarmType)
	}
	if source := strings.TrimSpace(r.URL.Query().Get("source")); source != "" {
		query = query.Where("source = ?", source)
	}
	if severity := r.URL.Query().Get("severity"); severity != "" {
		if value, err := strconv.Atoi(severity); err == nil {
			query = query.Where("severity = ?", value)
		}
	}
	if handled := r.URL.Query().Get("handled"); handled != "" {
		query = query.Where("handled = ?", handled == "true")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	page, pageSize := paging(r)
	list := make([]store.Alarm, 0, pageSize)
	if err := query.Order("occurred_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, Paged{List: list, Total: total, Page: page, PageSize: pageSize})
}

// applyTimeRange 将 start/end 查询参数（RFC3339）转换为发生时间范围条件。
func applyTimeRange(query *gorm.DB, r *http.Request) *gorm.DB {
	if start := r.URL.Query().Get("start"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			query = query.Where("occurred_at >= ?", t)
		}
	}
	if end := r.URL.Query().Get("end"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			query = query.Where("occurred_at <= ?", t)
		}
	}
	return query
}

// NameValue 统计项：名称 + 数值。
type NameValue struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

// TrendPoint 趋势点：时间标签 + 数量。
type TrendPoint struct {
	Label string `json:"label"`
	Value int    `json:"value"`
}

// TopGroup 告警排行分组：系统告警 / 软件告警 / 设备告警（软件按实例、设备按具体设备展开子项）。
type TopGroup struct {
	Key      string      `json:"key"` // 分组键：system | software | device
	Name     string      `json:"name"`
	Value    int         `json:"value"`
	Children []NameValue `json:"children,omitempty"`
}

// StatOverview 统计 KPI：总量与响应时效（MTTA=平均确认时长，MTTR=平均恢复时长）。
type StatOverview struct {
	Total  int    `json:"total"`  // 告警总数（按当前筛选）
	Active int    `json:"active"` // 活动中（触发持续中，含孤儿）
	MTTAMs *int64 `json:"mttaMs"` // 平均确认时长（毫秒），无样本时 null
	MTTRMs *int64 `json:"mttrMs"` // 平均恢复时长（毫秒），无样本时 null
}

// TrendSeries 趋势曲线：全部 + 系统/软件/设备分项。
type TrendSeries struct {
	Key    string       `json:"key"`
	Name   string       `json:"name"`
	Points []TrendPoint `json:"points"`
}

// HeatmapStats 告警时段热力图：最近 7 天 × 24 小时。
type HeatmapStats struct {
	Days []string `json:"days"` // 行标签（旧 → 新），如 "10-08 周三"
	Grid [][]int  `json:"grid"` // [day][hour] 告警数
}

// HistoryStatistics 告警统计：KPI / 级别占比 / 多系列趋势 / 排行 / 时段热力图。
type HistoryStatistics struct {
	Overview StatOverview  `json:"overview"`
	Severity []NameValue   `json:"severity"`
	Trend    []TrendSeries `json:"trend"`
	Top      []TopGroup    `json:"top"`
	Heatmap  *HeatmapStats `json:"heatmap"`
}

// HistoryStatistics 统计：全库告警纯聚合（趋势粒度可选 day/week/month）。
func (s *Server) HistoryStatistics(w http.ResponseWriter, r *http.Request) {
	var alarms []store.Alarm
	if err := s.DB.Find(&alarms).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	granularity := strings.TrimSpace(r.URL.Query().Get("granularity"))
	ok(w, buildStatistics(alarms, granularity))
}

// DailySeries 24H 分布单级别系列：24 小时逐时计数（label 为 "08:00" 形式）。
type DailySeries struct {
	Level  int          `json:"level"` // 告警级别 0-3
	Name   string       `json:"name"`
	Points []TrendPoint `json:"points"`
}

// DailyDistribution 指定日期 24 小时 × 级别告警分布（24H告警分布图散点数据源）。
type DailyDistribution struct {
	Date   string        `json:"date"` // YYYY-MM-DD（本地时区）
	Series []DailySeries `json:"series"`
}

// DailyDistribution 按日期聚合：该日每小时、每级别的告警数。
// date 参数格式 YYYY-MM-DD，缺省/非法返回 400，空缺当日无数据时各点为 0。
func (s *Server) DailyDistribution(w http.ResponseWriter, r *http.Request) {
	dateStr := strings.TrimSpace(r.URL.Query().Get("date"))
	var start time.Time
	if dateStr != "" {
		parsed, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
		if err != nil {
			fail(w, http.StatusBadRequest, "date 参数格式应为 YYYY-MM-DD")
			return
		}
		start = parsed
	} else {
		now := time.Now()
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	}
	end := start.AddDate(0, 0, 1)

	var alarms []store.Alarm
	if err := s.DB.Where("occurred_at >= ? AND occurred_at < ?", start, end).Find(&alarms).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}

	counts := [4][24]int{}
	for _, alarm := range alarms {
		level := alarm.Severity
		if level < 0 || level > 3 { // 防御：入库已钳制，历史脏数据按提示归档
			level = 0
		}
		counts[level][alarm.OccurredAt.Local().Hour()]++
	}

	dist := DailyDistribution{Date: start.Format("2006-01-02"), Series: make([]DailySeries, 0, 4)}
	for level := 0; level <= 3; level++ {
		points := make([]TrendPoint, 24)
		for h := 0; h < 24; h++ {
			points[h] = TrendPoint{Label: fmt.Sprintf("%02d:00", h), Value: counts[level][h]}
		}
		dist.Series = append(dist.Series, DailySeries{Level: level, Name: alarmLevelNames[level], Points: points})
	}
	ok(w, dist)
}

func buildStatistics(alarms []store.Alarm, granularity string) HistoryStatistics {
	typeNames := map[string]string{
		store.AlarmTypeSystem:   "系统告警",
		store.AlarmTypeSoftware: "软件告警",
		store.AlarmTypeDevice:   "设备告警",
	}
	overview := StatOverview{}
	var ackSum, ackCount, recoverSum, recoverCount int64
	severityCount := map[int]int{}
	typeCount := map[string]int{}
	deviceCount := map[string]int{}
	softwareCount := map[string]int{}
	for _, alarm := range alarms {
		overview.Total++
		if alarm.State == store.AlarmStateActive {
			overview.Active++
		}
		if alarm.Acknowledged && alarm.AcknowledgedAt != nil {
			if ms := alarm.AcknowledgedAt.Sub(alarm.OccurredAt).Milliseconds(); ms >= 0 {
				ackSum += ms
				ackCount++
			}
		}
		if end := resolvedAt(alarm); end != nil {
			if ms := end.Sub(alarm.OccurredAt).Milliseconds(); ms >= 0 {
				recoverSum += ms
				recoverCount++
			}
		}
		severityCount[alarm.Severity]++
		typeCount[alarm.Type]++
		switch alarm.Type {
		case store.AlarmTypeDevice:
			device := alarm.Source
			if device == "" {
				device = alarm.DeviceName
			}
			if device == "" {
				device = "未知设备"
			}
			deviceCount[device]++
		case store.AlarmTypeSoftware:
			software := alarm.SoftwareID
			if software == "" {
				software = alarm.Source
			}
			if software == "" {
				software = "未知软件"
			}
			softwareCount[software]++
		}
	}
	if ackCount > 0 {
		mtta := ackSum / ackCount
		overview.MTTAMs = &mtta
	}
	if recoverCount > 0 {
		mttr := recoverSum / recoverCount
		overview.MTTRMs = &mttr
	}

	statistics := HistoryStatistics{
		Overview: overview,
		Severity: make([]NameValue, 0, 4),
		Top:      make([]TopGroup, 0, 3),
	}
	for level := 0; level <= 3; level++ {
		if count, ok := severityCount[level]; ok {
			statistics.Severity = append(statistics.Severity, NameValue{Name: alarmLevelNames[level], Value: count})
		}
	}

	// 告警排行：系统告警 → 软件告警 → 设备告警（软件/设备带子项，前端可展开收起；系统无子项）
	for _, typ := range []string{store.AlarmTypeSystem, store.AlarmTypeSoftware, store.AlarmTypeDevice} {
		group := TopGroup{Key: typ, Name: typeNames[typ], Value: typeCount[typ]}
		switch typ {
		case store.AlarmTypeSoftware:
			for software, count := range softwareCount {
				group.Children = append(group.Children, NameValue{Name: software, Value: count})
			}
		case store.AlarmTypeDevice:
			for device, count := range deviceCount {
				group.Children = append(group.Children, NameValue{Name: device, Value: count})
			}
		}
		if len(group.Children) > 1 {
			sort.Slice(group.Children, func(i, j int) bool { return group.Children[i].Value > group.Children[j].Value })
		}
		statistics.Top = append(statistics.Top, group)
	}

	statistics.Trend = buildTrend(alarms, granularity)
	statistics.Heatmap = buildHeatmap(alarms)
	return statistics
}

// resolvedAt 返回告警的恢复时间：自动恢复优先，人工结案兜底。
func resolvedAt(alarm store.Alarm) *time.Time {
	if alarm.RecoveredAt != nil {
		return alarm.RecoveredAt
	}
	return alarm.ClearedAt
}

// trendBucket 趋势聚合桶。
type trendBucket struct {
	start time.Time
	end   time.Time
	label string
}

// trendBuckets 按粒度（day=近14天 / week=近12周 / month=近12月）生成时间桶。
func trendBuckets(granularity string) []trendBucket {
	now := time.Now()
	var buckets []trendBucket
	switch granularity {
	case "week":
		for i := 11; i >= 0; i-- {
			start := now.AddDate(0, 0, -i*7).Truncate(24 * time.Hour)
			buckets = append(buckets, trendBucket{start: start, end: start.AddDate(0, 0, 7), label: start.Format("01-02")})
		}
	case "month":
		for i := 11; i >= 0; i-- {
			start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -i, 0)
			buckets = append(buckets, trendBucket{start: start, end: start.AddDate(0, 1, 0), label: start.Format("2006-01")})
		}
	default: // day
		for i := 13; i >= 0; i-- {
			start := now.AddDate(0, 0, -i).Truncate(24 * time.Hour)
			buckets = append(buckets, trendBucket{start: start, end: start.AddDate(0, 0, 1), label: start.Format("01-02")})
		}
	}
	return buckets
}

// buildTrend 多系列趋势：全部 + 系统/软件/设备分项曲线。
func buildTrend(alarms []store.Alarm, granularity string) []TrendSeries {
	buckets := trendBuckets(granularity)
	seriesDefs := []struct{ key, name string }{
		{"total", "全部"},
		{store.AlarmTypeSystem, "系统"},
		{store.AlarmTypeSoftware, "软件"},
		{store.AlarmTypeDevice, "设备"},
	}
	counts := make(map[string][]int, len(seriesDefs))
	for _, def := range seriesDefs {
		counts[def.key] = make([]int, len(buckets))
	}
	for _, alarm := range alarms {
		at := alarm.OccurredAt.Local()
		for i, b := range buckets {
			if !at.Before(b.start) && at.Before(b.end) {
				counts["total"][i]++
				counts[alarm.Type][i]++
				break
			}
		}
	}
	series := make([]TrendSeries, 0, len(seriesDefs))
	for _, def := range seriesDefs {
		points := make([]TrendPoint, len(buckets))
		for i, b := range buckets {
			points[i] = TrendPoint{Label: b.label, Value: counts[def.key][i]}
		}
		series = append(series, TrendSeries{Key: def.key, Name: def.name, Points: points})
	}
	return series
}

// weekdayNames 中文星期标签（time.Weekday 顺序：周日=0）。
var weekdayNames = []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// buildHeatmap 最近 7 天 × 24 小时告警分布；深色代表告警密集。
func buildHeatmap(alarms []store.Alarm) *HeatmapStats {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	stats := &HeatmapStats{
		Days: make([]string, 7),
		Grid: make([][]int, 7),
	}
	for d := 0; d < 7; d++ {
		dayStart := todayStart.AddDate(0, 0, -(6 - d))
		stats.Days[d] = dayStart.Format("01-02") + " " + weekdayNames[dayStart.Weekday()]
		stats.Grid[d] = make([]int, 24)
		for _, alarm := range alarms {
			at := alarm.OccurredAt.Local()
			if !at.Before(dayStart) && at.Before(dayStart.AddDate(0, 0, 1)) {
				stats.Grid[d][at.Hour()]++
			}
		}
	}
	return stats
}
