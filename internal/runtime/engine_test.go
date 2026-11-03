package runtime

import (
	"testing"
	"time"

	"alarmengine/internal/natsclient"
	"alarmengine/internal/store"
)

func softwareEvent(alarmID, code string, context map[string]any) natsclient.AlarmMessage {
	return natsclient.AlarmMessage{
		AlarmID: alarmID, Source: natsclient.AlarmSource{Module: "thingsmodel", Instance: "thingsmodel-001"},
		Category: natsclient.CategorySoftware, Kind: natsclient.KindEvent, Level: 2,
		Timestamp: time.Now().UnixMilli(),
		Software:  &natsclient.AlarmSoftware{Code: code, Component: "runtime", Context: context},
	}
}

func mustCreateRule(t *testing.T, m *Manager, rule store.AlarmRule) {
	t.Helper()
	if err := m.db.Create(&rule).Error; err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
}

func waitActionRecord(t *testing.T, m *Manager, count int) []store.ActionRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var records []store.ActionRecord
		if err := m.db.Order("dispatched_at asc").Find(&records).Error; err == nil && len(records) >= count {
			return records
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待动作记录超时（期望 %d 条）", count)
	return nil
}

// 规则匹配 → .cmd 下发（受理成功/同步失败）与冷却抑制。
func TestRuleMatchDispatchAndCooldown(t *testing.T) {
	m := newTestManager(t)
	m.control = func(req natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		if req.DeviceID != "pcs-001" || req.ServiceKey != "setPower" || req.Value != 80 {
			return natsclient.ControlAck{Status: "failure", Message: "参数错误"}, req.RequestID, nil
		}
		return natsclient.ControlAck{Status: "accepted"}, req.RequestID, nil
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-1", Name: "电池过温降功率", Enabled: true, MatchMode: store.RuleMatchAny,
		Monitors:   store.RuleMonitors{{Kind: "event", DeviceID: "bms-001", EventKey: "temp_high", EventName: "电池温度过高"}},
		Controls:   store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 80}},
		CooldownMs: 10000,
	})

	ts := time.Now().Add(-time.Minute).UnixMilli()
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, ts), "powerpulse.alarm.thingsmodel", time.Now())
	records := waitActionRecord(t, m, 1)
	if records[0].Status != store.ActionAccepted || records[0].RequestID == "" {
		t.Fatalf("动作记录应为已受理且带 request_id: %+v", records[0])
	}
	if records[0].AlarmID != "a-1" || records[0].RuleID != "rule-1" {
		t.Fatalf("动作记录关联错误: %+v", records[0])
	}

	// 条件持续满足期间的重触发沿 → 无新上升沿，不重复下发。
	m.handleAlarmMessage(deviceTrigger("a-2", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	time.Sleep(50 * time.Millisecond)
	var total int64
	m.db.Model(&store.ActionRecord{}).Count(&total)
	if total != 1 {
		t.Fatalf("条件持续满足时不应重复下发，实际 %d 条", total)
	}

	// 恢复 → 条件解除（下降沿）；冷却期内重触发 → 上升沿成立但被冷却抑制。
	recoverMsg := natsclient.AlarmMessage{
		AlarmID: "a-3", Source: natsclient.AlarmSource{Module: "thingsmodel"},
		Category: natsclient.CategoryDevice, Kind: natsclient.KindRecover, Level: 3,
		Timestamp: time.Now().UnixMilli(),
		Device:    &natsclient.AlarmDevice{DeviceID: "bms-001", EventID: "event_1", EventKey: "temp_high"},
	}
	m.handleAlarmMessage(recoverMsg, "powerpulse.alarm.thingsmodel", time.Now())
	m.handleAlarmMessage(deviceTrigger("a-4", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	time.Sleep(50 * time.Millisecond)
	m.db.Model(&store.ActionRecord{}).Count(&total)
	if total != 1 {
		t.Fatalf("冷却期内上升沿不应重复下发，实际 %d 条", total)
	}
}

// 冷却时间 0 = 不限流：重触发即重发。
func TestRuleDispatchWithoutCooldown(t *testing.T) {
	m := newTestManager(t)
	m.control = func(natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		return natsclient.ControlAck{Status: "accepted"}, "req-x", nil
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-2", Name: "无冷却", Enabled: true, MatchMode: store.RuleMatchAny,
		Monitors:   store.RuleMonitors{{Kind: "event", DeviceID: "bms-001", EventKey: "temp_high"}},
		Controls:   store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 1}},
		CooldownMs: 0,
	})
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	recoverMsg := natsclient.AlarmMessage{
		AlarmID: "a-3", Source: natsclient.AlarmSource{Module: "thingsmodel"},
		Category: natsclient.CategoryDevice, Kind: natsclient.KindRecover, Level: 3,
		Timestamp: time.Now().UnixMilli(),
		Device:    &natsclient.AlarmDevice{DeviceID: "bms-001", EventID: "event_1", EventKey: "temp_high"},
	}
	m.handleAlarmMessage(recoverMsg, "powerpulse.alarm.thingsmodel", time.Now())
	m.handleAlarmMessage(deviceTrigger("a-2", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	waitActionRecord(t, m, 2)
}

// cmd.result 失败终态：request_id 闭环回填 + 告警上板。
func TestCmdResultClosesLoop(t *testing.T) {
	m := newTestManager(t)
	now := time.Now().UTC()
	if err := m.db.Create(&store.ActionRecord{
		ID: "ar-1", RequestID: "req-9", RuleID: "rule-1", RuleName: "电池过温降功率",
		AlarmID: "a-1", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 80,
		Status: store.ActionAccepted, DispatchedAt: now,
	}).Error; err != nil {
		t.Fatalf("预置动作记录失败: %v", err)
	}

	msg := softwareEvent("s-1", natsclient.EventCodeCmdResult, map[string]any{
		"request_id": "req-9", "device_id": "pcs-001", "service_key": "setPower", "message": "网关回报超时",
	})
	m.handleAlarmMessage(msg, "powerpulse.alarm.thingsmodel", time.Now())

	var record store.ActionRecord
	if err := m.db.First(&record, "id = ?", "ar-1").Error; err != nil {
		t.Fatalf("查询动作记录失败: %v", err)
	}
	if record.Status != store.ActionFailure || record.Message != "网关回报超时" || record.FinalizedAt == nil {
		t.Fatalf("失败终态回填错误: %+v", record)
	}
	// cmd.result 同时应作为告警上板（软件事件不抑制）。
	var alarmCount int64
	m.db.Model(&store.Alarm{}).Where("id = ?", "s-1").Count(&alarmCount)
	if alarmCount != 1 {
		t.Fatalf("cmd.result 应上告警看板")
	}
}

// model.changed 墓碑：设备删除 → 活动告警自动恢复，且该提示消息本身不上板。
func TestModelChangedTombstone(t *testing.T) {
	m := newTestManager(t)
	ts := time.Now().Add(-time.Hour).UnixMilli()
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, ts), "powerpulse.alarm.thingsmodel", time.Now())

	m.handleAlarmMessage(softwareEvent("s-2", natsclient.EventCodeModelChanged, map[string]any{
		"device_id": "bms-001", "action": "deleted",
	}), "powerpulse.alarm.thingsmodel", time.Now())

	var alarm store.Alarm
	if err := m.db.First(&alarm, "id = ?", "a-1").Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	if alarm.State != store.AlarmStateCleared || alarm.RecoveredAt == nil || alarm.DurationMs <= 0 {
		t.Fatalf("墓碑应自动恢复告警: %+v", alarm)
	}
	var boardCount int64
	m.db.Model(&store.Alarm{}).Where("id = ?", "s-2").Count(&boardCount)
	if boardCount != 0 {
		t.Fatalf("model.changed 提示不应上板")
	}
}

// 定义对账：event_key 消失 → 孤儿；定义恢复 → 解除孤儿。
func TestReconcileOrphan(t *testing.T) {
	m := newTestManager(t)
	ts := time.Now().Add(-time.Hour).UnixMilli()
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, ts), "powerpulse.alarm.thingsmodel", time.Now())

	// 新定义不再包含 temp_high → 孤儿。
	if err := m.reconcileDeviceAlarms("bms-001", []natsclient.EventDefinition{{Key: "volt_low", Name: "电压过低"}}); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	var alarm store.Alarm
	m.db.First(&alarm, "id = ?", "a-1")
	if !alarm.Orphan {
		t.Fatalf("事件定义移除后应为孤儿")
	}

	// 定义恢复 → 解除孤儿。
	if err := m.reconcileDeviceAlarms("bms-001", []natsclient.EventDefinition{{Key: "temp_high", Name: "电池温度过高"}}); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	m.db.First(&alarm, "id = ?", "a-1")
	if alarm.Orphan {
		t.Fatalf("事件定义恢复后应解除孤儿")
	}
}

// 推定成功：仅 accepted 状态会被改写为 success。
func TestPresumeSuccess(t *testing.T) {
	m := newTestManager(t)
	now := time.Now().UTC()
	m.db.Create(&store.ActionRecord{ID: "ar-1", RequestID: "r", Status: store.ActionAccepted, DispatchedAt: now})
	m.db.Create(&store.ActionRecord{ID: "ar-2", RequestID: "r2", Status: store.ActionFailure, DispatchedAt: now})

	m.presumeSuccess("ar-1")
	m.presumeSuccess("ar-2")

	var first, second store.ActionRecord
	m.db.First(&first, "id = ?", "ar-1")
	m.db.First(&second, "id = ?", "ar-2")
	if first.Status != store.ActionSuccess || first.FinalizedAt == nil {
		t.Fatalf("accepted 应推定成功: %+v", first)
	}
	if second.Status != store.ActionFailure {
		t.Fatalf("failure 不应被推定改写: %+v", second)
	}
}

// REQ 失败/超时：动作记录落失败、不重试。
func TestDispatchTransportFailure(t *testing.T) {
	m := newTestManager(t)
	m.control = func(natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		return natsclient.ControlAck{}, "", errStubTimeout{}
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-3", Name: "超时", Enabled: true, MatchMode: store.RuleMatchAny,
		Monitors: store.RuleMonitors{{Kind: "event", DeviceID: "bms-001", EventKey: "temp_high"}},
		Controls: store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 1}},
	})
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	records := waitActionRecord(t, m, 1)
	if records[0].Status != store.ActionFailure || records[0].FinalizedAt == nil {
		t.Fatalf("传输失败应落失败终态: %+v", records[0])
	}
}

// 级别监测源：active 计数 > 0 成立；恢复后计数归零；重现构成新上升沿。
func TestLevelMonitorRule(t *testing.T) {
	m := newTestManager(t)
	m.control = func(natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		return natsclient.ControlAck{Status: "accepted"}, "req-l", nil
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-l", Name: "紧急告警统一动作", Enabled: true, MatchMode: store.RuleMatchAny,
		Monitors:   store.RuleMonitors{{Kind: "level", Levels: []int{3}}},
		Controls:   store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 50}},
		CooldownMs: 0,
	})

	// 1. 任意设备 P3 触发 → 计数 1 → 上升沿 → 下发。
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	waitActionRecord(t, m, 1)

	// 2. 另一设备 P3 再触发 → 计数 2，但无新上升沿 → 不重复下发。
	other := deviceTrigger("a-2", "event_9", "volt_drop", 3, time.Now().UnixMilli())
	other.Device.DeviceID = "bms-002"
	m.handleAlarmMessage(other, "powerpulse.alarm.thingsmodel", time.Now())
	time.Sleep(50 * time.Millisecond)
	var total int64
	m.db.Model(&store.ActionRecord{}).Count(&total)
	if total != 1 {
		t.Fatalf("条件持续满足不应重复下发，实际 %d 条", total)
	}

	// 3. P2 告警触发（不在级别集合）→ 不影响 [3] 的计数，且本身不构成新沿。
	m.handleAlarmMessage(deviceTrigger("a-3", "event_2", "temp_warn", 2, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	time.Sleep(50 * time.Millisecond)

	// 4. 全部恢复 → 计数 0 → 条件解除；再出现 P3 → 新上升沿 → 再次下发（冷却 0）。
	m.handleAlarmMessage(natsclient.AlarmMessage{
		AlarmID: "r-a-1", Source: natsclient.AlarmSource{Module: "thingsmodel"},
		Category: natsclient.CategoryDevice, Kind: natsclient.KindRecover, Timestamp: time.Now().UnixMilli(),
		Device: &natsclient.AlarmDevice{DeviceID: "bms-001", EventKey: "temp_high"},
	}, "powerpulse.alarm.thingsmodel", time.Now())
	// a-2 恢复（bms-002）
	m.handleAlarmMessage(natsclient.AlarmMessage{
		AlarmID: "r-a-2", Source: natsclient.AlarmSource{Module: "thingsmodel"},
		Category: natsclient.CategoryDevice, Kind: natsclient.KindRecover, Timestamp: time.Now().UnixMilli(),
		Device: &natsclient.AlarmDevice{DeviceID: "bms-002", EventKey: "volt_drop"},
	}, "powerpulse.alarm.thingsmodel", time.Now())
	m.handleAlarmMessage(deviceTrigger("a-4", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	waitActionRecord(t, m, 2)
}

// all 聚合：全部源成立才触发。
func TestLevelAllModeRule(t *testing.T) {
	m := newTestManager(t)
	m.control = func(natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		return natsclient.ControlAck{Status: "accepted"}, "req-a", nil
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-all", Name: "P2 且 P3 同时存在", Enabled: true, MatchMode: store.RuleMatchAll,
		Monitors:   store.RuleMonitors{{Kind: "level", Levels: []int{3}}, {Kind: "level", Levels: []int{2}}},
		Controls:   store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 0}},
		CooldownMs: 0,
	})

	// 仅 P3 → all 不成立，不下发。
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	time.Sleep(50 * time.Millisecond)
	var total int64
	m.db.Model(&store.ActionRecord{}).Count(&total)
	if total != 0 {
		t.Fatalf("all 模式下单源成立不应下发")
	}

	// P2 出现 → 全部源成立 → 下发。
	m.handleAlarmMessage(deviceTrigger("a-2", "event_2", "temp_warn", 2, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	waitActionRecord(t, m, 1)
}

// 多控制源：规则成立时全部并发下发，各自独立闭环。
func TestMultiControlDispatch(t *testing.T) {
	m := newTestManager(t)
	m.control = func(req natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		return natsclient.ControlAck{Status: "accepted"}, "req-" + req.DeviceID, nil
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-multi", Name: "多目标动作", Enabled: true, MatchMode: store.RuleMatchAny,
		Monitors: store.RuleMonitors{{Kind: "level", Levels: []int{3}}},
		Controls: store.RuleControls{
			{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 0},
			{Kind: "device", DeviceID: "bms-001", ServiceKey: "contactorOff", Value: 1},
		},
		CooldownMs: 0,
	})
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	records := waitActionRecord(t, m, 2)
	targets := map[string]bool{}
	for _, record := range records {
		if record.Status != store.ActionAccepted {
			t.Fatalf("控制源应全部受理: %+v", record)
		}
		targets[record.DeviceID] = true
	}
	if !targets["pcs-001"] || !targets["bms-001"] {
		t.Fatalf("两个控制目标都应下发: %+v", targets)
	}
}

type errStubTimeout struct{}

func (errStubTimeout) Error() string { return "nats: 超时" }

// P0-3 回归：无关告警消息不得重置事件源的边沿状态（否则条件未解除会重复下发）。
// 事件源按"源告警仍在活动"判定（DB 活性），只有 recover 产生下降沿。
func TestUnrelatedMessageDoesNotResetEdge(t *testing.T) {
	m := newTestManager(t)
	m.control = func(req natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		return natsclient.ControlAck{Status: "accepted"}, req.RequestID, nil
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-edge", Name: "边沿回归", Enabled: true, MatchMode: store.RuleMatchAny,
		Monitors:   store.RuleMonitors{{Kind: "event", DeviceID: "bms-001", EventKey: "temp_high"}},
		Controls:   store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 1}},
		CooldownMs: 0,
	})
	ts := time.Now().UnixMilli()

	// 1) 首次命中 → 下发 1 条。
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, ts), "powerpulse.alarm.thingsmodel", time.Now())
	waitActionRecord(t, m, 1)

	// 2) 无关告警（另一设备/另一事件）→ 不得重置本规则边沿。
	other := deviceTrigger("a-2", "event_9", "volt_low", 2, time.Now().UnixMilli())
	other.Device.DeviceID = "bms-002"
	m.handleAlarmMessage(other, "powerpulse.alarm.thingsmodel", time.Now())

	// 3) 同条件重触发（a-1 仍 active，条件从未解除）→ 不得重复下发。
	m.handleAlarmMessage(deviceTrigger("a-3", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	time.Sleep(80 * time.Millisecond)
	var total int64
	m.db.Model(&store.ActionRecord{}).Count(&total)
	if total != 1 {
		t.Fatalf("无关消息重置边沿导致重复下发，动作记录=%d（期望 1）", total)
	}

	// 4) 恢复 → 条件解除；再触发 → 新的上升沿 → 重新下发（冷却 0）。
	recoverMsg := natsclient.AlarmMessage{
		AlarmID: "r-a-1", Source: natsclient.AlarmSource{Module: "thingsmodel"},
		Category: natsclient.CategoryDevice, Kind: natsclient.KindRecover, Level: 3, Timestamp: ts,
		Device: &natsclient.AlarmDevice{DeviceID: "bms-001", EventID: "event_1", EventKey: "temp_high"},
	}
	m.handleAlarmMessage(recoverMsg, "powerpulse.alarm.thingsmodel", time.Now())
	m.handleAlarmMessage(deviceTrigger("a-4", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	waitActionRecord(t, m, 2)
}

// 合并触发沿：动作记录应关联实际落库的告警行（而非消息自身的 alarm_id），
// 否则保护记录的"触发告警"无法解析出名称。
func TestMergedTriggerActionLinksExistingAlarm(t *testing.T) {
	m := newTestManager(t)
	m.control = func(req natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		return natsclient.ControlAck{Status: "accepted"}, req.RequestID, nil
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-merge", Name: "合并触发", Enabled: true, MatchMode: store.RuleMatchAny,
		Monitors:   store.RuleMonitors{{Kind: "event", DeviceID: "bms-001", EventKey: "temp_high"}},
		Controls:   store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 1}},
		CooldownMs: 0,
	})
	// 预置同定位键的存量活动告警（event_id 优先于 event_key 定位）。
	if err := m.db.Create(&store.Alarm{
		ID: "pre-1", Name: "电池温度过高", Kind: store.AlarmKindTrigger, Type: store.AlarmTypeDevice,
		Module: "thingsmodel", DeviceID: "bms-001", EventID: "event_1", EventKey: "temp_high",
		Severity: 3, State: store.AlarmStateActive, OccurredAt: time.Now().Add(-time.Hour).UTC(),
	}).Error; err != nil {
		t.Fatalf("预置活动告警失败: %v", err)
	}
	m.handleAlarmMessage(deviceTrigger("a-new", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	records := waitActionRecord(t, m, 1)
	if records[0].AlarmID != "pre-1" {
		t.Fatalf("合并触发应关联存量告警 pre-1，实际 %q", records[0].AlarmID)
	}
}

// C3：level 计数只认状态型 trigger 且非孤儿——一次性事件与孤儿不得让规则永久成立。
func TestLevelMonitorExcludesEventAndOrphan(t *testing.T) {
	m := newTestManager(t)
	m.control = func(req natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		return natsclient.ControlAck{Status: "accepted"}, req.RequestID, nil
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-l2", Name: "P3 统一动作", Enabled: true, MatchMode: store.RuleMatchAny,
		Monitors:   store.RuleMonitors{{Kind: "level", Levels: []int{3}}},
		Controls:   store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 10}},
		CooldownMs: 0,
	})
	now := time.Now().UTC()
	// 存量活跃的 P3 一次性事件（软件告警，无恢复沿）。
	if err := m.db.Create(&store.Alarm{
		ID: "ev-1", Kind: store.AlarmKindEvent, Type: store.AlarmTypeSoftware, Severity: 3,
		State: store.AlarmStateActive, OccurredAt: now, Name: "控制失败",
	}).Error; err != nil {
		t.Fatalf("预置事件告警失败: %v", err)
	}
	// 存量活跃的 P3 设备孤儿告警（源已失联，永不恢复）。
	if err := m.db.Create(&store.Alarm{
		ID: "or-1", Kind: store.AlarmKindTrigger, Type: store.AlarmTypeDevice, Severity: 3,
		State: store.AlarmStateActive, Orphan: true, OccurredAt: now,
		DeviceID: "bms-001", EventKey: "temp_high",
	}).Error; err != nil {
		t.Fatalf("预置孤儿告警失败: %v", err)
	}

	// 一条 P2 消息触发评估：事件/孤儿不计数 → 条件不成立 → 不下发。
	m.handleAlarmMessage(deviceTrigger("a-p2", "event_2", "temp_warn", 2, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	time.Sleep(50 * time.Millisecond)
	var total int64
	m.db.Model(&store.ActionRecord{}).Count(&total)
	if total != 0 {
		t.Fatalf("一次性事件/孤儿不应使级别条件成立，实际下发 %d 条", total)
	}

	// 真实状态型 P3 → 条件成立 → 正常下发（确认修复没有破坏正常路径）。
	m.handleAlarmMessage(deviceTrigger("a-p3", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	waitActionRecord(t, m, 1)
}

// C4：启动播种后，存量已成立条件不应在首条消息时被判为上升沿重复下发。
func TestSeedPreventsRestartDuplicateDispatch(t *testing.T) {
	newSeeded := func(t *testing.T, seed bool) *Manager {
		m := newTestManager(t)
		m.control = func(req natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
			return natsclient.ControlAck{Status: "accepted"}, req.RequestID, nil
		}
		mustCreateRule(t, m, store.AlarmRule{
			ID: "rule-seed", Name: "存量条件", Enabled: true, MatchMode: store.RuleMatchAny,
			Monitors:   store.RuleMonitors{{Kind: "level", Levels: []int{3}}},
			Controls:   store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 5}},
			CooldownMs: 0,
		})
		// 模拟重启前已有的活动告警（直接落库，不经过消息评估）。
		if err := m.db.Create(&store.Alarm{
			ID: "pre-1", Kind: store.AlarmKindTrigger, Type: store.AlarmTypeDevice, Severity: 3,
			State: store.AlarmStateActive, OccurredAt: time.Now().Add(-time.Hour).UTC(),
			DeviceID: "bms-001", EventKey: "temp_high",
		}).Error; err != nil {
			t.Fatalf("预置活动告警失败: %v", err)
		}
		if seed {
			m.seedRuleHoldState()
		}
		return m
	}

	// 播种后：条件已处于成立态，首条消息不应再下发。
	seeded := newSeeded(t, true)
	seeded.handleAlarmMessage(deviceTrigger("a-new", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	time.Sleep(50 * time.Millisecond)
	var total int64
	seeded.db.Model(&store.ActionRecord{}).Count(&total)
	if total != 0 {
		t.Fatalf("播种后存量条件不应重复下发，实际 %d 条", total)
	}

	// 对照：未播种（旧行为）会重复下发——证明该用例确实覆盖到边沿判定。
	unseeded := newSeeded(t, false)
	unseeded.handleAlarmMessage(deviceTrigger("a-new", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	waitActionRecord(t, unseeded, 1)
}

// H2：推定成功后再收到迟到的失败终态，应允许覆盖为 failure。
func TestLateCmdResultOverridesPresumedSuccess(t *testing.T) {
	m := newTestManager(t)
	now := time.Now().UTC()
	if err := m.db.Create(&store.ActionRecord{
		ID: "ar-late", RequestID: "req-late", RuleID: "rule-x", RuleName: "迟到失败",
		DeviceID: "pcs-001", ServiceKey: "setPower", Value: 1,
		Status: store.ActionAccepted, DispatchedAt: now,
	}).Error; err != nil {
		t.Fatalf("预置动作记录失败: %v", err)
	}
	m.presumeSuccess("ar-late")
	var presumed store.ActionRecord
	m.db.First(&presumed, "id = ?", "ar-late")
	if presumed.Status != store.ActionSuccess {
		t.Fatalf("应先推定成功: %+v", presumed)
	}

	msg := softwareEvent("s-late", natsclient.EventCodeCmdResult, map[string]any{
		"request_id": "req-late", "message": "网关回报超时",
	})
	m.handleAlarmMessage(msg, "powerpulse.alarm.thingsmodel", time.Now())

	var final store.ActionRecord
	m.db.First(&final, "id = ?", "ar-late")
	if final.Status != store.ActionFailure || final.Message != "网关回报超时" {
		t.Fatalf("迟到的失败终态应覆盖推定成功: %+v", final)
	}
}

// H8：墓碑（model.changed deleted）后的重算只刷新边沿，不得下发保护动作。
func TestTombstoneEvaluationDoesNotDispatch(t *testing.T) {
	m := newTestManager(t)
	m.control = func(req natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
		return natsclient.ControlAck{Status: "accepted"}, req.RequestID, nil
	}
	mustCreateRule(t, m, store.AlarmRule{
		ID: "rule-tomb", Name: "墓碑重算", Enabled: true, MatchMode: store.RuleMatchAny,
		Monitors:   store.RuleMonitors{{Kind: "level", Levels: []int{3}}},
		Controls:   store.RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "setPower", Value: 1}},
		CooldownMs: 0,
	})
	// 另一设备存在活动 P3 告警（使 level 条件在墓碑后仍成立）。
	if err := m.db.Create(&store.Alarm{
		ID: "other-1", Kind: store.AlarmKindTrigger, Type: store.AlarmTypeDevice, Severity: 3,
		State: store.AlarmStateActive, OccurredAt: time.Now().UTC(),
		DeviceID: "bms-002", EventKey: "volt_drop",
	}).Error; err != nil {
		t.Fatalf("预置活动告警失败: %v", err)
	}

	m.handleAlarmMessage(softwareEvent("s-tomb", natsclient.EventCodeModelChanged, map[string]any{
		"device_id": "bms-001", "action": "deleted",
	}), "powerpulse.alarm.thingsmodel", time.Now())

	time.Sleep(50 * time.Millisecond)
	var total int64
	m.db.Model(&store.ActionRecord{}).Count(&total)
	if total != 0 {
		t.Fatalf("墓碑重算不应下发保护动作，实际 %d 条", total)
	}
	// 边沿状态应已刷新为成立，后续同一条件不会立即再触发。
	holds := false
	m.ruleHoldMu.Lock()
	holds = m.ruleHoldState["rule-tomb"]
	m.ruleHoldMu.Unlock()
	if !holds {
		t.Fatalf("墓碑重算应把边沿状态刷新为成立")
	}
}

// P1-3：孤儿告警被同定位键重触发合并时必须复位 orphan（设计承诺"重触发自愈"）。
func TestMergeResetsOrphan(t *testing.T) {
	m := newTestManager(t)
	now := time.Now().UTC()
	if err := m.db.Create(&store.Alarm{
		ID: "or-merge", Name: "电压过低", Kind: store.AlarmKindTrigger, Type: store.AlarmTypeDevice,
		Module: "thingsmodel", DeviceID: "bms-001", EventID: "event_9", EventKey: "volt_low", Severity: 2,
		State: store.AlarmStateActive, Orphan: true, OccurredAt: now,
	}).Error; err != nil {
		t.Fatalf("预置孤儿告警失败: %v", err)
	}
	m.handleAlarmMessage(deviceTrigger("a-or", "event_9", "volt_low", 2, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())

	var alarm store.Alarm
	if err := m.db.First(&alarm, "id = ?", "or-merge").Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if alarm.Orphan {
		t.Fatalf("重触发合并后应解除孤儿")
	}
	if alarm.Count < 2 {
		t.Fatalf("合并应累加计数，实际 %d", alarm.Count)
	}
}

// C组：event_id 与 event_key 混用的触发消息不应拆成两条活动告警。
func TestLocateMergesEventIDAndEventKey(t *testing.T) {
	m := newTestManager(t)
	now := time.Now().UTC()
	if err := m.db.Create(&store.Alarm{
		ID: "legacy-1", Kind: store.AlarmKindTrigger, Type: store.AlarmTypeDevice, Module: "thingsmodel",
		DeviceID: "bms-001", EventKey: "temp_high", Severity: 3, State: store.AlarmStateActive, OccurredAt: now,
	}).Error; err != nil {
		t.Fatalf("预置存量告警失败: %v", err)
	}
	// 同一事件、带 event_id 的新触发沿 → 应合并到存量行（event_id 为空）。
	m.handleAlarmMessage(deviceTrigger("a-eid", "event_1", "temp_high", 3, time.Now().UnixMilli()), "powerpulse.alarm.thingsmodel", time.Now())
	var count int64
	m.db.Model(&store.Alarm{}).Count(&count)
	if count != 1 {
		t.Fatalf("event_id/event_key 混用应合并为一条，实际 %d 条", count)
	}
}

// C组：触发值无法解析时标记 valueInvalid，前端据此显示"—"。
func TestTriggerValueValidFlag(t *testing.T) {
	m := newTestManager(t)
	msg := deviceTrigger("a-v1", "event_1", "temp_high", 3, time.Now().UnixMilli())
	msg.Device.Value = "not-a-number"
	m.handleAlarmMessage(msg, "powerpulse.alarm.thingsmodel", time.Now())
	var alarm store.Alarm
	if err := m.db.First(&alarm, "id = ?", "a-v1").Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !alarm.ValueInvalid {
		t.Fatalf("不可解析的触发值应标记 valueInvalid=true")
	}
	okMsg := deviceTrigger("a-v2", "event_2", "temp_warn", 2, time.Now().UnixMilli())
	m.handleAlarmMessage(okMsg, "powerpulse.alarm.thingsmodel", time.Now())
	var okAlarm store.Alarm
	if err := m.db.First(&okAlarm, "id = ?", "a-v2").Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if okAlarm.ValueInvalid {
		t.Fatalf("可解析的触发值不应标记 valueInvalid")
	}
}
