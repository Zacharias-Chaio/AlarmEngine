package runtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"alarmengine/internal/natsclient"
	"alarmengine/internal/store"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&store.Alarm{}, &store.AlarmRule{}, &store.ActionRecord{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 内存 SQLite 每个连接是独立库：限制单连接，避免迁移表与查询落到不同实例。
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	manager := &Manager{
		ctx:         context.Background(),
		db:          db,
		log:         slog.Default().With("module", "runtime-test"),
		defs:        &definitions{},
		ingestState: newIngestState(),
	}
	manager.ruleEngine = ruleEngine{
		lastDispatch:  make(map[string]time.Time),
		ruleHoldState: make(map[string]bool),
	}
	manager.clientManager = clientManager{
		ctx:       context.Background(),
		log:       manager.log,
		defs:      manager.defs,
		onMessage: manager.handleAlarmMessage,
		track:     manager.goTracked,
	}
	return manager
}

func deviceTrigger(alarmID, eventID, eventKey string, level int, ts int64) natsclient.AlarmMessage {
	return natsclient.AlarmMessage{
		AlarmID:   alarmID,
		Source:    natsclient.AlarmSource{Module: "thingsmodel", Instance: "thingsmodel-001"},
		Category:  natsclient.CategoryDevice,
		Kind:      natsclient.KindTrigger,
		Level:     level,
		Timestamp: ts,
		Device: &natsclient.AlarmDevice{
			DeviceID: "bms-001", DeviceName: "1号电池簇",
			EventID: eventID, EventKey: eventKey, EventName: "电池温度过高",
			Point: "temperature", PointName: "电池温度", Value: 62.5,
			Rule: natsclient.AlarmRuleSpec{Type: "upper", Threshold: 60},
		},
	}
}

func TestIngestTriggerRecover(t *testing.T) {
	m := newTestManager(t)
	ts := time.Now().Add(-10 * time.Minute).UnixMilli()

	// 1. trigger 落库
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, ts), "powerpulse.alarm.thingsmodel", time.Now())
	var alarm store.Alarm
	if err := m.db.First(&alarm, "id = ?", "a-1").Error; err != nil {
		t.Fatalf("trigger 未落库: %v", err)
	}
	if alarm.State != store.AlarmStateActive || alarm.Severity != 3 || alarm.Type != store.AlarmTypeDevice {
		t.Fatalf("trigger 落库字段错误: %+v", alarm)
	}
	if alarm.EventKey != "temp_high" || alarm.Threshold != 60 || alarm.Value != 62.5 {
		t.Fatalf("设备明细映射错误: %+v", alarm)
	}
	firstOccurred := alarm.OccurredAt

	// 2. 重复消息（同 alarm_id）幂等丢弃
	m.handleAlarmMessage(deviceTrigger("a-1", "event_1", "temp_high", 3, ts), "powerpulse.alarm.thingsmodel", time.Now())
	var count int64
	m.db.Model(&store.Alarm{}).Count(&count)
	if count != 1 {
		t.Fatalf("幂等去重失败，记录数=%d", count)
	}

	// 3. 同定位键新触发沿 → 合并 + count 累加，保留首次触发时间
	merged := deviceTrigger("a-2", "event_1", "temp_high", 3, time.Now().UnixMilli())
	merged.Count = 5
	m.handleAlarmMessage(merged, "powerpulse.alarm.thingsmodel", time.Now())
	if err := m.db.First(&alarm, "id = ?", "a-1").Error; err != nil {
		t.Fatalf("应合并至原记录: %v", err)
	}
	if alarm.Count != 6 {
		t.Fatalf("聚合计数累加错误: %d", alarm.Count)
	}
	if !alarm.OccurredAt.Equal(firstOccurred) {
		t.Fatalf("合并不应重置首次触发时间")
	}

	// 4. recover 按 event_id 定位键恢复（alarm_id 与 trigger 不同）
	now := time.Now()
	msg := natsclient.AlarmMessage{
		AlarmID: "a-3", Source: natsclient.AlarmSource{Module: "thingsmodel"},
		Category: natsclient.CategoryDevice, Kind: natsclient.KindRecover, Level: 3, Timestamp: now.UnixMilli(),
		Device: &natsclient.AlarmDevice{DeviceID: "bms-001", EventID: "event_1", EventKey: "temp_high"},
	}
	m.handleAlarmMessage(msg, "powerpulse.alarm.thingsmodel", now)
	if err := m.db.First(&alarm, "id = ?", "a-1").Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if alarm.State != store.AlarmStateCleared || alarm.RecoveredAt == nil {
		t.Fatalf("recover 未恢复告警: %+v", alarm)
	}
	if alarm.DurationMs <= 0 {
		t.Fatalf("恢复后持续时长错误: %d", alarm.DurationMs)
	}
}

func TestIngestEventAndSoftware(t *testing.T) {
	m := newTestManager(t)
	ts := time.Now().Add(-1 * time.Minute).UnixMilli()

	// 软件 event 告警：code/component 定位、message 透传
	msg := natsclient.AlarmMessage{
		AlarmID: "s-1", Source: natsclient.AlarmSource{Module: "thingsmodel", Instance: "thingsmodel-001"},
		Category: natsclient.CategorySoftware, Kind: natsclient.KindEvent, Level: 2, Timestamp: ts,
		Count:    3,
		Software: &natsclient.AlarmSoftware{Code: "thingsmodel.cmd.result", Component: "runtime", Message: "控制失败"},
	}
	m.handleAlarmMessage(msg, "powerpulse.alarm.thingsmodel", time.Now())
	var alarm store.Alarm
	if err := m.db.First(&alarm, "id = ?", "s-1").Error; err != nil {
		t.Fatalf("event 未落库: %v", err)
	}
	if alarm.Kind != store.AlarmKindEvent || alarm.Code != "thingsmodel.cmd.result" || alarm.Component != "runtime" {
		t.Fatalf("event 映射错误: %+v", alarm)
	}
	if alarm.Source != "thingsmodel" || alarm.SoftwareID != "thingsmodel-001" || alarm.Count != 3 {
		t.Fatalf("软件来源映射错误: %+v", alarm)
	}

	// event 同 code/component 再来一条（新 alarm_id）→ 新记录（一次性事件不合并）
	msg2 := msg
	msg2.AlarmID = "s-2"
	m.handleAlarmMessage(msg2, "powerpulse.alarm.thingsmodel", time.Now())
	var count int64
	m.db.Model(&store.Alarm{}).Count(&count)
	if count != 2 {
		t.Fatalf("一次性事件应新建记录，实际 %d", count)
	}
}

// 软件 trigger 生命周期：确认结案后，同定位键新触发沿应落新行（合并仅限 active 行）。
func TestSoftwareTriggerLifecycleAfterHandle(t *testing.T) {
	m := newTestManager(t)
	ts := time.Now().Add(-2 * time.Hour).UnixMilli()

	trigger := func(alarmID string, t int64) natsclient.AlarmMessage {
		return natsclient.AlarmMessage{
			AlarmID: alarmID, Source: natsclient.AlarmSource{Module: "gateway", Instance: "gateway-001"},
			Category: natsclient.CategorySoftware, Kind: natsclient.KindTrigger, Level: 2, Timestamp: t,
			Software: &natsclient.AlarmSoftware{Code: "gateway.conn.lost", Component: "mq", Message: "消息队列连接断开"},
		}
	}

	// 1. 首次触发落库。
	m.handleAlarmMessage(trigger("sw-1", ts), "powerpulse.alarm.gateway", time.Now())
	var first store.Alarm
	if err := m.db.First(&first, "id = ?", "sw-1").Error; err != nil {
		t.Fatalf("首次触发未落库: %v", err)
	}
	if first.State != store.AlarmStateActive || first.Kind != store.AlarmKindTrigger {
		t.Fatalf("首次触发字段错误: %+v", first)
	}

	// 2. 同定位键重复触发（新 alarm_id）→ 合并进原行（active 期间正常聚合）。
	m.handleAlarmMessage(trigger("sw-1r", time.Now().Add(-90*time.Minute).UnixMilli()), "powerpulse.alarm.gateway", time.Now())
	var merged store.Alarm
	if err := m.db.First(&merged, "id = ?", "sw-1").Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if merged.Count != 2 {
		t.Fatalf("active 期间应合并累加，实际 count=%d", merged.Count)
	}

	// 3. 人工结案：确认 + 处理（软件 trigger 无恢复沿，确认后视为即时恢复）。
	if err := m.db.Model(&store.Alarm{}).Where("id = ?", "sw-1").Updates(map[string]any{
		"acknowledged": true,
	}).Error; err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	now := time.Now().UTC()
	if err := m.db.Model(&store.Alarm{}).Where("id = ?", "sw-1").Updates(map[string]any{
		"handled": true, "cleared_at": now, "state": store.AlarmStateCleared, "recovered_at": now,
	}).Error; err != nil {
		t.Fatalf("结案失败: %v", err)
	}

	// 4. 结案后同定位键新触发沿 → 不合并进已结案行，落新行作为新告警。
	m.handleAlarmMessage(trigger("sw-2", time.Now().UnixMilli()), "powerpulse.alarm.gateway", time.Now())
	var newAlarm store.Alarm
	if err := m.db.First(&newAlarm, "id = ?", "sw-2").Error; err != nil {
		t.Fatalf("结案后新触发沿应落新行: %v", err)
	}
	if newAlarm.State != store.AlarmStateActive || newAlarm.Handled {
		t.Fatalf("新行应为独立活动告警: %+v", newAlarm)
	}
	var total int64
	m.db.Model(&store.Alarm{}).Count(&total)
	if total != 2 {
		t.Fatalf("应存在两行（已结案+新活动），实际 %d", total)
	}

	// 5. 已结案行不被新触发沿改写。
	handledRow := loadAlarmRow(m, t, "sw-1")
	if !handledRow.Handled || handledRow.State != store.AlarmStateCleared || handledRow.Count != 2 {
		t.Fatalf("已结案行不应被新触发沿改写: %+v", handledRow)
	}
}

func loadAlarmRow(m *Manager, t *testing.T, id string) store.Alarm {
	t.Helper()
	var alarm store.Alarm
	if err := m.db.First(&alarm, "id = ?", id).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	return alarm
}

func TestIngestSystemAndValidation(t *testing.T) {
	m := newTestManager(t)
	// 系统告警：来源固定为“系统”
	msg := natsclient.AlarmMessage{
		AlarmID: "sys-1", Source: natsclient.AlarmSource{Module: "gateway", Instance: "gateway-001"},
		Category: natsclient.CategorySystem, Kind: natsclient.KindTrigger, Level: 2,
		Timestamp: time.Now().UnixMilli(),
		System:    &natsclient.AlarmSystem{Code: "powerpulse.node.offline", Component: "core", Message: "节点离线"},
	}
	m.handleAlarmMessage(msg, "powerpulse.alarm.gateway", time.Now())
	var alarm store.Alarm
	if err := m.db.First(&alarm, "id = ?", "sys-1").Error; err != nil {
		t.Fatalf("system 告警未落库: %v", err)
	}
	if alarm.Source != "系统" || alarm.Type != store.AlarmTypeSystem {
		t.Fatalf("system 来源映射错误: %+v", alarm)
	}

	// 非法 category / kind / 缺 alarm_id 均被拒绝
	before := rowCount(m, t)
	bad := msg
	bad.AlarmID = "bad-1"
	bad.Category = "unknown"
	m.handleAlarmMessage(bad, "powerpulse.alarm.gateway", time.Now())
	bad2 := msg
	bad2.AlarmID = "bad-2"
	bad2.Kind = "unknown"
	m.handleAlarmMessage(bad2, "powerpulse.alarm.gateway", time.Now())
	m.handleAlarmMessage(natsclient.AlarmMessage{Category: "system", Kind: "trigger"}, "powerpulse.alarm.gateway", time.Now())
	if rowCount(m, t) != before {
		t.Fatalf("非法消息不应落库")
	}
}

func rowCount(m *Manager, t *testing.T) int64 {
	t.Helper()
	var count int64
	m.db.Model(&store.Alarm{}).Count(&count)
	return count
}

// 验证标准消息 JSON 序列化字段名与文档一致。
func TestAlarmMessageJSONFields(t *testing.T) {
	raw := `{"alarm_id":"x1","source":{"module":"thingsmodel","instance":"i1"},"category":"device","kind":"trigger","level":2,"timestamp":1,"count":2,` +
		`"device":{"device_id":"d1","device_name":"n1","event_id":"e1","event_key":"k1","event_name":"ev","point":"p","point_name":"pn","value":12.5,"rule":{"type":"upper","threshold":10,"time":0}}}`
	var msg natsclient.AlarmMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("标准消息解析失败: %v", err)
	}
	if msg.AlarmID != "x1" || msg.Source.Module != "thingsmodel" || msg.Device.DeviceID != "d1" || msg.Device.Rule.Threshold != 10 || msg.Count != 2 {
		t.Fatalf("字段映射不符标准: %+v", msg)
	}
}

func TestModuleFromSubject(t *testing.T) {
	cases := map[string]string{
		"powerpulse.alarm.thingsmodel":   "thingsmodel",
		"powerpulse.alarm.gateway":       "gateway",
		"powerpulse.alarm.other.subject": "other",
		"other.subject":                  "",
		"powerpulse.alarm.":              "",
	}
	for subject, want := range cases {
		if got := moduleFromSubject(subject); got != want {
			t.Fatalf("moduleFromSubject(%q)=%q, want %q", subject, got, want)
		}
	}
}

// 触发值转换：数值/字符串/布尔可转，其它返回 (0,false)。
func TestToFloat(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{float64(1.5), 1.5, true},
		{float32(2.5), 2.5, true},
		{int(3), 3, true},
		{int64(4), 4, true},
		{"5.5", 5.5, true},
		{"bad", 0, false},
		{true, 1, true},
		{false, 0, true},
		{nil, 0, false},
		{struct{}{}, 0, false},
	}
	for _, testCase := range cases {
		got, ok := toFloat(testCase.in)
		if got != testCase.want || ok != testCase.ok {
			t.Fatalf("toFloat(%#v) = (%v,%v)，期望 (%v,%v)", testCase.in, got, ok, testCase.want, testCase.ok)
		}
	}
}
