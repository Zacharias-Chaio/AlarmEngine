// stats.go - 发送统计：按工况/类别/生命周期/级别计数，周期报告与终期汇总。
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// stats 仅在主循环 goroutine 内使用，无需加锁。
type stats struct {
	start      time.Time
	sent       int64
	errors     int64
	periodSent int64
	lastReport time.Time
	byScenario map[string]int64
	byCategory map[string]int64
	byKind     map[string]int64
	byLevel    map[string]int64
}

func newStats(now time.Time) *stats {
	return &stats{
		start:      now,
		lastReport: now,
		byScenario: map[string]int64{},
		byCategory: map[string]int64{},
		byKind:     map[string]int64{},
		byLevel:    map[string]int64{},
	}
}

func (st *stats) recordError() { st.errors++ }

// record 计入一条已发送消息；非标准消息仅计入工况维度。
func (st *stats) record(f frame) {
	st.sent++
	st.periodSent++
	st.byScenario[f.tag]++
	if f.msg == nil {
		return
	}
	st.byCategory[f.msg.Category]++
	st.byKind[f.msg.Kind]++
	st.byLevel[fmt.Sprintf("L%d", f.msg.Level)]++
}

// report 周期统计行，并重置周期计数。
func (st *stats) report(now time.Time) string {
	elapsed := now.Sub(st.start)
	avg := float64(st.sent) / max(elapsed.Seconds(), 1)
	rate := float64(st.periodSent) / max(now.Sub(st.lastReport).Seconds(), 1)
	line := fmt.Sprintf("[STAT] %s 运行 %s 总计 %d 条 | 周期 +%d（%.1f/s）平均 %.1f/s | 失败 %d",
		now.Format("15:04:05"), formatDuration(elapsed), st.sent, st.periodSent, rate, avg, st.errors)
	if body := st.sections(); body != "" {
		line += " | " + body
	}
	st.periodSent = 0
	st.lastReport = now
	return line
}

// summary 终期汇总（多行）。
func (st *stats) summary(now time.Time) string {
	elapsed := now.Sub(st.start)
	avg := float64(st.sent) / max(elapsed.Seconds(), 1)
	lines := []string{
		fmt.Sprintf("[DONE] 运行 %s 共发送 %d 条 失败 %d 条 平均 %.2f 条/s", formatDuration(elapsed), st.sent, st.errors, avg),
	}
	if body := st.sections(); body != "" {
		lines = append(lines, "[DONE] "+body)
	}
	return strings.Join(lines, "\n")
}

func (st *stats) sections() string {
	var parts []string
	if len(st.byCategory) > 0 {
		parts = append(parts, "类别 "+counts(st.byCategory, []string{"device", "software", "system"}))
	}
	if len(st.byKind) > 0 {
		parts = append(parts, "生命周期 "+counts(st.byKind, []string{"trigger", "recover", "event"}))
	}
	if len(st.byLevel) > 0 {
		parts = append(parts, "级别 "+counts(st.byLevel, []string{"L0", "L1", "L2", "L3"}))
	}
	if len(st.byScenario) > 0 {
		parts = append(parts, "工况 "+counts(st.byScenario, nil))
	}
	return strings.Join(parts, " | ")
}

// counts 生成 k=v 列表：固定顺序在前，未知键按字典序追加。
func counts(m map[string]int64, order []string) string {
	keys := make([]string, 0, len(m))
	seen := make(map[string]bool, len(m))
	for _, key := range order {
		if _, ok := m[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	rest := make([]string, 0)
	for key := range m {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%d", key, m[key]))
	}
	return strings.Join(pairs, " ")
}

func formatDuration(d time.Duration) string {
	return d.Truncate(time.Second).String()
}
