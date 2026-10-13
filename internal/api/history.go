package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"alarmengine/internal/store"
)

// ListHistory 历史告警查询，支持时间范围/类型/来源/严重程度/处理状态筛选。
func (s *Server) ListHistory(w http.ResponseWriter, r *http.Request) {
	query := s.DB.Model(&store.Alarm{})
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

// TopGroup 告警排行分组：系统告警 / 软件告警 / 设备告警（设备告警展开具体设备）。
type TopGroup struct {
	Name     string      `json:"name"`
	Value    int         `json:"value"`
	Children []NameValue `json:"children,omitempty"`
}

// HistoryStatistics 历史告警统计：严重程度分布 / 来源分布 / 趋势 / 告警排行。
type HistoryStatistics struct {
	Severity []NameValue  `json:"severity"`
	Sources  []NameValue  `json:"sources"`
	Trend    []TrendPoint `json:"trend"`
	Top      []TopGroup   `json:"top"`
}

// HistoryStatistics 统计：纯聚合，不含业务逻辑。
func (s *Server) HistoryStatistics(w http.ResponseWriter, r *http.Request) {
	var alarms []store.Alarm
	if err := s.DB.Find(&alarms).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	granularity := strings.TrimSpace(r.URL.Query().Get("granularity"))
	ok(w, buildStatistics(alarms, granularity))
}

func buildStatistics(alarms []store.Alarm, granularity string) HistoryStatistics {
	severityNames := map[int]string{0: "提示", 1: "一般", 2: "严重", 3: "紧急"}
	typeNames := map[string]string{
		store.AlarmTypeSystem:   "系统告警",
		store.AlarmTypeSoftware: "软件告警",
		store.AlarmTypeDevice:   "设备告警",
	}
	severityCount := map[int]int{}
	sourceCount := map[string]int{}
	typeCount := map[string]int{}
	deviceCount := map[string]int{}
	for _, alarm := range alarms {
		severityCount[alarm.Severity]++
		source := alarm.Source
		if source == "" {
			source = alarm.DeviceName
		}
		if source == "" {
			source = "未知"
		}
		sourceCount[source]++
		typeCount[alarm.Type]++
		if alarm.Type == store.AlarmTypeDevice {
			device := alarm.Source
			if device == "" {
				device = alarm.DeviceName
			}
			if device == "" {
				device = "未知设备"
			}
			deviceCount[device]++
		}
	}

	statistics := HistoryStatistics{
		Severity: make([]NameValue, 0, 4),
		Sources:  make([]NameValue, 0, len(sourceCount)),
		Top:      make([]TopGroup, 0, 3),
	}
	for level := 0; level <= 3; level++ {
		if count, ok := severityCount[level]; ok {
			statistics.Severity = append(statistics.Severity, NameValue{Name: severityNames[level], Value: count})
		}
	}
	for source, count := range sourceCount {
		statistics.Sources = append(statistics.Sources, NameValue{Name: source, Value: count})
	}
	sort.Slice(statistics.Sources, func(i, j int) bool { return statistics.Sources[i].Value > statistics.Sources[j].Value })

	// 告警排行：系统告警 → 软件告警 → 设备告警（设备展开具体设备）
	for _, typ := range []string{store.AlarmTypeSystem, store.AlarmTypeSoftware, store.AlarmTypeDevice} {
		group := TopGroup{Name: typeNames[typ], Value: typeCount[typ]}
		if typ == store.AlarmTypeDevice {
			for device, count := range deviceCount {
				group.Children = append(group.Children, NameValue{Name: device, Value: count})
			}
			sort.Slice(group.Children, func(i, j int) bool { return group.Children[i].Value > group.Children[j].Value })
		}
		statistics.Top = append(statistics.Top, group)
	}

	statistics.Trend = buildTrend(alarms, granularity)
	return statistics
}

// buildTrend 按粒度（day/week/month）聚合告警趋势。
func buildTrend(alarms []store.Alarm, granularity string) []TrendPoint {
	now := time.Now().UTC()
	type bucket struct {
		start time.Time
		end   time.Time
		label string
	}
	var buckets []bucket
	switch granularity {
	case "week":
		for i := 11; i >= 0; i-- {
			start := now.AddDate(0, 0, -i*7).Truncate(24 * time.Hour)
			buckets = append(buckets, bucket{start: start, end: start.AddDate(0, 0, 7), label: start.Format("01-02")})
		}
	case "month":
		for i := 11; i >= 0; i-- {
			start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -i, 0)
			buckets = append(buckets, bucket{start: start, end: start.AddDate(0, 1, 0), label: start.Format("2006-01")})
		}
	default: // day
		for i := 13; i >= 0; i-- {
			start := now.AddDate(0, 0, -i).Truncate(24 * time.Hour)
			buckets = append(buckets, bucket{start: start, end: start.AddDate(0, 0, 1), label: start.Format("01-02")})
		}
	}

	points := make([]TrendPoint, 0, len(buckets))
	for _, b := range buckets {
		count := 0
		for _, alarm := range alarms {
			at := alarm.OccurredAt.UTC()
			if !at.Before(b.start) && at.Before(b.end) {
				count++
			}
		}
		points = append(points, TrendPoint{Label: b.label, Value: count})
	}
	return points
}
