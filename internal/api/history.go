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
	query, err := applyTimeRange(query, r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
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
		internalError(w, err)
		return
	}
	page, pageSize := paging(r)
	list := make([]store.Alarm, 0, pageSize)
	if err := query.Order("occurred_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		internalError(w, err)
		return
	}
	now := time.Now()
	for i := range list {
		deriveDuration(&list[i], now)
	}
	ok(w, Paged{List: list, Total: total, Page: page, PageSize: pageSize})
}

// applyTimeRange 将 start/end 查询参数（RFC3339）转换为发生时间范围条件。
// 与分布图接口一致：非法时间参数直接 400，而不是静默忽略（否则用户以为筛选生效）。
func applyTimeRange(query *gorm.DB, r *http.Request) (*gorm.DB, error) {
	if start := r.URL.Query().Get("start"); start != "" {
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return nil, fmt.Errorf("start 时间格式非法（需 RFC3339）: %q", start)
		}
		query = query.Where("occurred_at >= ?", t)
	}
	if end := r.URL.Query().Get("end"); end != "" {
		t, err := time.Parse(time.RFC3339, end)
		if err != nil {
			return nil, fmt.Errorf("end 时间格式非法（需 RFC3339）: %q", end)
		}
		query = query.Where("occurred_at <= ?", t)
	}
	return query, nil
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
	Days  []string `json:"days"`  // 行标签（旧 → 新），如 "10-08 周三"
	Dates []string `json:"dates"` // 行对应完整日期 YYYY-MM-DD（热力图日期点击联动分布图）
	Grid  [][]int  `json:"grid"`  // [day][hour] 告警数
}

// HistoryStatistics 告警统计：KPI / 级别占比 / 多系列趋势 / 排行 / 时段热力图。
type HistoryStatistics struct {
	Overview StatOverview  `json:"overview"`
	Severity []NameValue   `json:"severity"`
	Trend    []TrendSeries `json:"trend"`
	Top      []TopGroup    `json:"top"`
	Heatmap  *HeatmapStats `json:"heatmap"`
}

// HistoryStatistics 统计：SQL 聚合（趋势粒度可选 day/week/month）。
// P1：概览/级别/排行全部下推数据库 GROUP BY，趋势与热力图仅按窗口加载时间列，
// 不再全表全列加载进内存。
func (s *Server) HistoryStatistics(w http.ResponseWriter, r *http.Request) {
	granularity := strings.TrimSpace(r.URL.Query().Get("granularity"))
	statistics, err := s.buildStatistics(granularity)
	if err != nil {
		internalError(w, err)
		return
	}
	ok(w, statistics)
}

// occurrenceRow 时间轻量行（趋势/热力图窗口内加载）。
type occurrenceRow struct {
	OccurredAt time.Time
	Type       string
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

	// 仅取级别+时间两列（窗口为单日，P1 瘦身加载）。
	var rows []struct {
		Severity   int
		OccurredAt time.Time
	}
	if err := s.DB.Model(&store.Alarm{}).
		Select("severity, occurred_at").
		Where("occurred_at >= ? AND occurred_at < ?", start, end).
		Scan(&rows).Error; err != nil {
		internalError(w, err)
		return
	}

	counts := [4][24]int{}
	for _, row := range rows {
		level := row.Severity
		if level < 0 || level > 3 { // 防御：入库已钳制，历史脏数据按提示归档
			level = 0
		}
		counts[level][row.OccurredAt.Local().Hour()]++
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

func (s *Server) buildStatistics(granularity string) (HistoryStatistics, error) {
	typeNames := map[string]string{
		store.AlarmTypeSystem:   "系统告警",
		store.AlarmTypeSoftware: "软件告警",
		store.AlarmTypeDevice:   "设备告警",
	}
	statistics := HistoryStatistics{
		Severity: make([]NameValue, 0, 4),
		Top:      make([]TopGroup, 0, 3),
	}

	// 概览：总量/活跃/MTTA/MTTR 单条聚合（时间差经 julianday 换算毫秒；时间列恒为 UTC 同格式，字符串比较安全）。
	var overviewRow struct {
		Total  int
		Active int
		AckN   int
		AckMs  int64
		RecN   int
		RecMs  int64
	}
	if err := s.DB.Model(&store.Alarm{}).Select(`COUNT(*) AS total,
		COALESCE(SUM(CASE WHEN state = 'active' THEN 1 ELSE 0 END), 0) AS active,
		COALESCE(SUM(CASE WHEN acknowledged = 1 AND acknowledged_at IS NOT NULL AND acknowledged_at >= occurred_at THEN 1 ELSE 0 END), 0) AS ack_n,
		COALESCE(SUM(CASE WHEN acknowledged = 1 AND acknowledged_at IS NOT NULL AND acknowledged_at >= occurred_at THEN CAST((julianday(acknowledged_at) - julianday(occurred_at)) * 86400000 AS INTEGER) ELSE 0 END), 0) AS ack_ms,
		COALESCE(SUM(CASE WHEN recovered_at IS NOT NULL OR cleared_at IS NOT NULL THEN 1 ELSE 0 END), 0) AS rec_n,
		COALESCE(SUM(CASE
			WHEN recovered_at IS NOT NULL AND recovered_at >= occurred_at THEN CAST((julianday(recovered_at) - julianday(occurred_at)) * 86400000 AS INTEGER)
			WHEN cleared_at IS NOT NULL AND cleared_at >= occurred_at THEN CAST((julianday(cleared_at) - julianday(occurred_at)) * 86400000 AS INTEGER)
			ELSE 0 END), 0) AS rec_ms`).
		Scan(&overviewRow).Error; err != nil {
		return statistics, err
	}
	statistics.Overview = StatOverview{Total: overviewRow.Total, Active: overviewRow.Active}
	if overviewRow.AckN > 0 {
		mtta := overviewRow.AckMs / int64(overviewRow.AckN)
		statistics.Overview.MTTAMs = &mtta
	}
	if overviewRow.RecN > 0 {
		mttr := overviewRow.RecMs / int64(overviewRow.RecN)
		statistics.Overview.MTTRMs = &mttr
	}

	// 级别占比。
	var severityRows []struct {
		Severity int
		Count    int
	}
	if err := s.DB.Model(&store.Alarm{}).
		Select("severity, COUNT(*) AS count").
		Group("severity").Scan(&severityRows).Error; err != nil {
		return statistics, err
	}
	severityCount := map[int]int{}
	for _, row := range severityRows {
		severityCount[row.Severity] = row.Count
	}
	for level := 0; level <= 3; level++ {
		if count, exists := severityCount[level]; exists {
			statistics.Severity = append(statistics.Severity, NameValue{Name: alarmLevelNames[level], Value: count})
		}
	}

	// 排行：类型计数 + 软件/设备子项分组（展示回退口径与旧实现一致）。
	var typeRows []struct {
		Type  string
		Count int
	}
	if err := s.DB.Model(&store.Alarm{}).
		Select("type, COUNT(*) AS count").
		Group("type").Scan(&typeRows).Error; err != nil {
		return statistics, err
	}
	typeCount := map[string]int{}
	for _, row := range typeRows {
		typeCount[row.Type] = row.Count
	}
	children := map[string][]NameValue{
		store.AlarmTypeSoftware: nil,
		store.AlarmTypeDevice:   nil,
	}
	var softwareRows []struct {
		Name  string
		Count int
	}
	if err := s.DB.Model(&store.Alarm{}).
		Select("COALESCE(NULLIF(software_id, ''), NULLIF(source, ''), '未知软件') AS name, COUNT(*) AS count").
		Where("type = ?", store.AlarmTypeSoftware).
		Group("name").Scan(&softwareRows).Error; err != nil {
		return statistics, err
	}
	for _, row := range softwareRows {
		children[store.AlarmTypeSoftware] = append(children[store.AlarmTypeSoftware], NameValue{Name: row.Name, Value: row.Count})
	}
	var deviceRows []struct {
		Name  string
		Count int
	}
	if err := s.DB.Model(&store.Alarm{}).
		Select("COALESCE(NULLIF(source, ''), NULLIF(device_name, ''), '未知设备') AS name, COUNT(*) AS count").
		Where("type = ?", store.AlarmTypeDevice).
		Group("name").Scan(&deviceRows).Error; err != nil {
		return statistics, err
	}
	for _, row := range deviceRows {
		children[store.AlarmTypeDevice] = append(children[store.AlarmTypeDevice], NameValue{Name: row.Name, Value: row.Count})
	}
	for _, typ := range []string{store.AlarmTypeSystem, store.AlarmTypeSoftware, store.AlarmTypeDevice} {
		group := TopGroup{Key: typ, Name: typeNames[typ], Value: typeCount[typ], Children: children[typ]}
		if len(group.Children) > 1 {
			sort.Slice(group.Children, func(i, j int) bool { return group.Children[i].Value > group.Children[j].Value })
		}
		statistics.Top = append(statistics.Top, group)
	}

	// 趋势与热力图：按各自窗口加载时间轻量行，内存分桶（本地时区口径与旧实现一致）。
	buckets := trendBuckets(granularity)
	var trendRows []occurrenceRow
	if err := s.DB.Model(&store.Alarm{}).
		Select("occurred_at, type").
		Where("occurred_at >= ?", buckets[0].start).
		Scan(&trendRows).Error; err != nil {
		return statistics, err
	}
	statistics.Trend = buildTrend(trendRows, buckets)

	heatmapStart := time.Now().AddDate(0, 0, -6)
	heatmapStart = time.Date(heatmapStart.Year(), heatmapStart.Month(), heatmapStart.Day(), 0, 0, 0, 0, heatmapStart.Location())
	var heatmapRows []occurrenceRow
	if err := s.DB.Model(&store.Alarm{}).
		Select("occurred_at").
		Where("occurred_at >= ?", heatmapStart).
		Scan(&heatmapRows).Error; err != nil {
		return statistics, err
	}
	statistics.Heatmap = buildHeatmap(heatmapRows)
	return statistics, nil
}

// localDayStart 本地零点。注意不能用 Truncate(24h)：它按 UTC 零点截断，
// 东八区会得到本地 08:00，导致趋势桶与"今日统计/热力图/分布图"的日界口径相差 8 小时。
func localDayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// trendBucket 趋势聚合桶。
type trendBucket struct {
	start time.Time
	end   time.Time
	label string
}

// trendBuckets 按粒度（day=近14天 / week=近12周 / month=近12月）生成时间桶。
// 日桶取本地零点；周桶按周一对齐（日历周）；月桶按自然月。
func trendBuckets(granularity string) []trendBucket {
	now := time.Now()
	var buckets []trendBucket
	switch granularity {
	case "week":
		// 本周一（本地）：time.Weekday 周日=0，距周一的天数为 (weekday+6)%7。
		monday := localDayStart(now).AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
		for i := 11; i >= 0; i-- {
			start := monday.AddDate(0, 0, -i*7)
			buckets = append(buckets, trendBucket{start: start, end: start.AddDate(0, 0, 7), label: start.Format("01-02")})
		}
	case "month":
		for i := 11; i >= 0; i-- {
			start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -i, 0)
			buckets = append(buckets, trendBucket{start: start, end: start.AddDate(0, 1, 0), label: start.Format("2006-01")})
		}
	default: // day
		for i := 13; i >= 0; i-- {
			start := localDayStart(now.AddDate(0, 0, -i))
			buckets = append(buckets, trendBucket{start: start, end: start.AddDate(0, 0, 1), label: start.Format("01-02")})
		}
	}
	return buckets
}

// buildTrend 多系列趋势：全部 + 系统/软件/设备分项曲线。
func buildTrend(rows []occurrenceRow, buckets []trendBucket) []TrendSeries {
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
	for _, row := range rows {
		at := row.OccurredAt.Local()
		// 未登记的告警类型没有对应系列（历史脏数据/新增类型），跳过避免 nil 切片越界。
		if _, ok := counts[row.Type]; !ok {
			continue
		}
		for i, b := range buckets {
			if !at.Before(b.start) && at.Before(b.end) {
				counts["total"][i]++
				counts[row.Type][i]++
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
// 单次遍历落桶（O(n)）：先按本地日期求天索引，再按小时累加。
func buildHeatmap(rows []occurrenceRow) *HeatmapStats {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	stats := &HeatmapStats{
		Days:  make([]string, 7),
		Dates: make([]string, 7),
		Grid:  make([][]int, 7),
	}
	for d := 0; d < 7; d++ {
		dayStart := todayStart.AddDate(0, 0, -(6 - d))
		stats.Days[d] = dayStart.Format("01-02") + " " + weekdayNames[dayStart.Weekday()]
		stats.Dates[d] = dayStart.Format("2006-01-02")
		stats.Grid[d] = make([]int, 24)
	}
	for _, row := range rows {
		at := row.OccurredAt.Local()
		dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
		// Grid 以"6 天前 = 0、今天 = 6"排列：用与 todayStart 的天数差换算索引。
		index := 6 + int(dayStart.Sub(todayStart).Hours()/24)
		if index < 0 || index > 6 {
			continue // 窗口外（含未来/跨时区抖动）
		}
		stats.Grid[index][at.Hour()]++
	}
	return stats
}
