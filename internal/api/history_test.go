package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"alarmengine/internal/store"
)

// P1-1：日/周趋势桶必须按本地零点切分（Truncate(24h) 会按 UTC 零点截断）。
func TestTrendBucketsUseLocalMidnight(t *testing.T) {
	for _, granularity := range []string{"day", "week"} {
		buckets := trendBuckets(granularity)
		if len(buckets) == 0 {
			t.Fatalf("%s 桶为空", granularity)
		}
		for _, b := range buckets {
			if b.start.Hour() != 0 || b.start.Minute() != 0 || b.start.Second() != 0 {
				t.Fatalf("%s 桶应从本地零点开始: %v", granularity, b.start)
			}
			if b.start.Location() != time.Local {
				t.Fatalf("%s 桶时区错误: %v", granularity, b.start.Location())
			}
		}
	}
	// 周桶按周一对齐
	for _, b := range trendBuckets("week") {
		if b.start.Weekday() != time.Monday {
			t.Fatalf("周桶应按周一对齐: %v", b.start)
		}
	}
}

// 热力图：单次遍历按本地日期落桶；窗口外行忽略。
func TestHeatmapBucketIndex(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)
	rows := []occurrenceRow{
		{OccurredAt: today},
		{OccurredAt: today.Add(time.Hour)},
		{OccurredAt: yesterday},
		{OccurredAt: today.AddDate(0, 0, -8)}, // 窗口外
	}
	stats := buildHeatmap(rows)
	if stats.Grid[6][10] != 1 || stats.Grid[6][11] != 1 {
		t.Fatalf("今天桶计数错误: %+v", stats.Grid[6])
	}
	if stats.Grid[5][10] != 1 {
		t.Fatalf("昨天桶计数错误: %+v", stats.Grid[5])
	}
	total := 0
	for _, day := range stats.Grid {
		for _, v := range day {
			total += v
		}
	}
	if total != 3 {
		t.Fatalf("窗口外行不应计入，实际 %d", total)
	}
}

// 未登记类型不应触发 nil 切片越界。
func TestBuildTrendSkipsUnknownType(t *testing.T) {
	rows := []occurrenceRow{{OccurredAt: time.Now(), Type: "unknown-type"}}
	series := buildTrend(rows, trendBuckets("day"))
	if len(series) == 0 {
		t.Fatalf("系列不应为空")
	}
}

// 保护记录分页：返回 list/total/page/pageSize。
func TestListActionsPaged(t *testing.T) {
	s := newRuleTestServer(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if err := s.DB.Create(&store.ActionRecord{
			ID: "ar-p" + string(rune('0'+i)), DispatchedAt: now.Add(time.Duration(-i) * time.Minute),
			Status: store.ActionAccepted,
		}).Error; err != nil {
			t.Fatalf("预置动作记录失败: %v", err)
		}
	}
	rec := httptest.NewRecorder()
	s.ListActions(rec, httptest.NewRequest(http.MethodGet, "/api/actions?page=1&pageSize=2", nil))
	var payload struct {
		Code int `json:"code"`
		Data struct {
			List     []map[string]any `json:"list"`
			Total    int              `json:"total"`
			Page     int              `json:"page"`
			PageSize int              `json:"pageSize"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应解析失败: %v (%s)", err, rec.Body.String())
	}
	if payload.Data.Total != 3 || len(payload.Data.List) != 2 || payload.Data.Page != 1 || payload.Data.PageSize != 2 {
		t.Fatalf("分页响应错误: %+v", payload.Data)
	}
}

// C组：start/end 非法时间参数应 400（与分布图口径一致），不再静默忽略。
func TestListHistoryInvalidTimeRange(t *testing.T) {
	s := newAlarmTestServer(t)
	rec := httptest.NewRecorder()
	s.ListHistory(rec, httptest.NewRequest(http.MethodGet, "/api/history?start=not-a-time", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 start 应 400，实际 %d (%s)", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.ListHistory(rec, httptest.NewRequest(http.MethodGet, "/api/history?end=2026-13-99", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 end 应 400，实际 %d", rec.Code)
	}
}
