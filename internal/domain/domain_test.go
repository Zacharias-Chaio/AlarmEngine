package domain

import (
	"strings"
	"testing"

	"alarmengine/internal/store"
)

func validRule() store.AlarmRule {
	return store.AlarmRule{
		ID: "r1", Name: "规则", MatchMode: store.RuleMatchAny, CooldownMs: 5000,
		Monitors: store.RuleMonitors{
			{Kind: "level", Levels: []int{2, 3}},
			{Kind: "event", DeviceID: "bms-001", EventKey: "temp_high"},
		},
		Controls: store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 10}},
	}
}

func TestValidateRule(t *testing.T) {
	rule := validRule()
	if err := ValidateRule(&rule); err != nil {
		t.Fatalf("合法规则应通过: %v", err)
	}
	// 规范化：level 清事件字段、event 清级别、空 matchMode 补 anyone、空 type 补 device。
	if rule.Monitors[0].DeviceID != "" || rule.Monitors[0].EventKey != "" {
		t.Fatalf("level 源应裁剪事件字段: %+v", rule.Monitors[0])
	}
	if len(rule.Monitors[1].Levels) != 0 || rule.Monitors[1].Type != store.AlarmTypeDevice {
		t.Fatalf("event 源应裁剪级别并补默认类型: %+v", rule.Monitors[1])
	}

	cases := []struct {
		name   string
		mutate func(*store.AlarmRule)
		want   string
	}{
		{"缺 ID", func(r *store.AlarmRule) { r.ID = " " }, "缺少 ID"},
		{"ID 非法", func(r *store.AlarmRule) { r.ID = "a/b" }, "ID 仅允许"},
		{"缺名称", func(r *store.AlarmRule) { r.Name = "" }, "缺少规则名称"},
		{"聚合模式非法", func(r *store.AlarmRule) { r.MatchMode = "bad" }, "聚合模式"},
		{"无监测源", func(r *store.AlarmRule) { r.Monitors = nil }, "至少配置一个监测源"},
		{"级别为空", func(r *store.AlarmRule) { r.Monitors[0].Levels = nil }, "必须选择告警级别"},
		{"级别越界", func(r *store.AlarmRule) { r.Monitors[0].Levels = []int{4} }, "告警级别取值"},
		{"事件缺字段", func(r *store.AlarmRule) { r.Monitors[1].EventKey = "" }, "事件监测源必须"},
		{"未知类型", func(r *store.AlarmRule) { r.Monitors[1].Kind = "other" }, "未知监测源类型"},
		{"无控制源", func(r *store.AlarmRule) { r.Controls = nil }, "至少配置一个控制源"},
		{"非设备控制", func(r *store.AlarmRule) { r.Controls[0].Kind = "sms" }, "通知类型暂仅支持设备"},
		{"控制缺字段", func(r *store.AlarmRule) { r.Controls[0].ServiceKey = "" }, "设备控制源必须"},
		{"冷却为负", func(r *store.AlarmRule) { r.CooldownMs = -1 }, "冷却时间范围"},
		{"冷却超限", func(r *store.AlarmRule) { r.CooldownMs = MaxCooldownMs + 1 }, "冷却时间范围"},
	}
	for _, testCase := range cases {
		bad := validRule()
		testCase.mutate(&bad)
		err := ValidateRule(&bad)
		if err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("%s: 期望包含 %q，实际 %v", testCase.name, testCase.want, err)
		}
	}
}

func TestHandleReason(t *testing.T) {
	base := store.Alarm{ID: "a1", State: store.AlarmStateActive, Kind: store.AlarmKindTrigger, Type: store.AlarmTypeDevice}
	if reason, clear := HandleReason(base); reason == "" || clear {
		t.Fatalf("活动设备告警不可结案: reason=%q clear=%v", reason, clear)
	}
	// 已恢复 → 可结案且不需清状态。
	recovered := base
	recovered.State = store.AlarmStateCleared
	if reason, clear := HandleReason(recovered); reason != "" || clear {
		t.Fatalf("已恢复告警应可直接结案: reason=%q clear=%v", reason, clear)
	}
	// 一次性事件 + 已确认 → 可结案且需置已恢复。
	event := base
	event.Kind = store.AlarmKindEvent
	event.Acknowledged = true
	if reason, clear := HandleReason(event); reason != "" || !clear {
		t.Fatalf("一次性事件确认后应可结案并置恢复: reason=%q clear=%v", reason, clear)
	}
	// 软件 trigger + 已确认 → 可结案。
	software := base
	software.Type = store.AlarmTypeSoftware
	software.Acknowledged = true
	if reason, clear := HandleReason(software); reason != "" || !clear {
		t.Fatalf("软件 trigger 确认后应可结案: reason=%q clear=%v", reason, clear)
	}
	// 孤儿 → 不可结案。
	orphan := base
	orphan.Orphan = true
	if reason, _ := HandleReason(orphan); reason == "" {
		t.Fatalf("孤儿告警不可结案")
	}
	// 已结案 → 跳过。
	handled := base
	handled.Handled = true
	if reason, _ := HandleReason(handled); reason == "" {
		t.Fatalf("已结案告警应跳过")
	}
}

func TestAcknowledgeReason(t *testing.T) {
	if reason := AcknowledgeReason(store.Alarm{}); reason != "" {
		t.Fatalf("未确认未结案应可确认: %q", reason)
	}
	if reason := AcknowledgeReason(store.Alarm{Acknowledged: true}); reason == "" {
		t.Fatalf("已确认应跳过")
	}
	if reason := AcknowledgeReason(store.Alarm{Handled: true}); reason == "" {
		t.Fatalf("已结案应跳过")
	}
}
