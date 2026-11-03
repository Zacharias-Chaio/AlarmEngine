package api

import (
	"testing"
	"time"

	"alarmengine/internal/store"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newAlarmTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&store.Alarm{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	return &Server{DB: db}
}

func loadAlarm(t *testing.T, s *Server, id string) store.Alarm {
	t.Helper()
	var alarm store.Alarm
	if err := s.DB.First(&alarm, "id = ?", id).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	return alarm
}

func TestApplyAcknowledgeStateMachine(t *testing.T) {
	s := newAlarmTestServer(t)
	s.DB.Create(&store.Alarm{ID: "a1", State: store.AlarmStateActive, OccurredAt: time.Now().Add(-time.Hour)})

	alarm := loadAlarm(t, s, "a1")
	if reason, err := s.applyAcknowledge(&alarm); err != nil || reason != "" {
		t.Fatalf("首次确认应成功: reason=%q err=%v", reason, err)
	}
	fresh := loadAlarm(t, s, "a1")
	if !fresh.Acknowledged || fresh.AcknowledgedAt == nil {
		t.Fatalf("确认应落库: %+v", fresh)
	}

	// 重复确认跳过（保留首次确认时间）。
	if reason, _ := s.applyAcknowledge(&alarm); reason == "" {
		t.Fatal("重复确认应跳过")
	}

	// 已结案跳过。
	s.DB.Create(&store.Alarm{ID: "a2", State: store.AlarmStateCleared, Handled: true, OccurredAt: time.Now().Add(-time.Hour)})
	handled := loadAlarm(t, s, "a2")
	if reason, _ := s.applyAcknowledge(&handled); reason == "" {
		t.Fatal("已结案确认应跳过")
	}
}

func TestApplyHandleStateMachine(t *testing.T) {
	s := newAlarmTestServer(t)
	base := time.Now().Add(-time.Hour)
	s.DB.Create(&store.Alarm{ID: "orphan", State: store.AlarmStateActive, Orphan: true, OccurredAt: base})
	s.DB.Create(&store.Alarm{ID: "active", State: store.AlarmStateActive, OccurredAt: base})
	s.DB.Create(&store.Alarm{ID: "event-unacked", State: store.AlarmStateActive, Kind: store.AlarmKindEvent, OccurredAt: base})
	s.DB.Create(&store.Alarm{ID: "event-acked", State: store.AlarmStateActive, Kind: store.AlarmKindEvent, Acknowledged: true, OccurredAt: base})
	s.DB.Create(&store.Alarm{ID: "cleared", State: store.AlarmStateCleared, OccurredAt: base})

	if reason, _ := s.applyHandle(p(t, s, "orphan")); reason == "" {
		t.Fatal("孤儿不可结案")
	}
	if reason, _ := s.applyHandle(p(t, s, "active")); reason == "" {
		t.Fatal("活动中非事件不可结案")
	}
	if reason, _ := s.applyHandle(p(t, s, "event-unacked")); reason == "" {
		t.Fatal("事件未确认不可结案")
	}

	eventAcked := p(t, s, "event-acked")
	if reason, err := s.applyHandle(eventAcked); err != nil || reason != "" {
		t.Fatalf("已确认事件应可结案: reason=%q err=%v", reason, err)
	}
	row := loadAlarm(t, s, "event-acked")
	if !row.Handled || row.RecoveredAt == nil || row.State != store.AlarmStateCleared || row.DurationMs <= 0 {
		t.Fatalf("事件结案字段错误: %+v", row)
	}

	if reason, err := s.applyHandle(p(t, s, "cleared")); err != nil || reason != "" {
		t.Fatalf("已恢复应可结案: reason=%q err=%v", reason, err)
	}
}

func TestApplyHandleConcurrentConflict(t *testing.T) {
	s := newAlarmTestServer(t)
	s.DB.Create(&store.Alarm{ID: "race", State: store.AlarmStateCleared, OccurredAt: time.Now().Add(-time.Hour)})
	alarm := loadAlarm(t, s, "race")

	// 模拟并发：读取后被其他写入方结案。
	s.DB.Model(&store.Alarm{}).Where("id = ?", "race").Update("handled", true)

	if reason, _ := s.applyHandle(&alarm); reason == "" {
		t.Fatal("并发结案冲突应跳过并给出原因")
	}
}

func p(t *testing.T, s *Server, id string) *store.Alarm {
	t.Helper()
	alarm := loadAlarm(t, s, id)
	return &alarm
}

// P1-4：非设备类 trigger（软件/系统）无恢复沿，确认后应允许人工结案，避免永久卡死。
func TestHandleSoftwareTriggerAfterAck(t *testing.T) {
	s := newAlarmTestServer(t)
	now := time.Now().UTC()
	if err := s.DB.Create(&store.Alarm{
		ID: "sw-1", Kind: store.AlarmKindTrigger, Type: store.AlarmTypeSoftware,
		State: store.AlarmStateActive, OccurredAt: now, Name: "模块异常",
	}).Error; err != nil {
		t.Fatalf("预置告警失败: %v", err)
	}
	alarm := loadAlarm(t, s, "sw-1")
	// 未确认不可结案。
	if reason, err := s.applyHandle(&alarm); err != nil || reason == "" {
		t.Fatalf("未确认应拒绝结案: reason=%q err=%v", reason, err)
	}
	// 确认后允许结案。
	if _, err := s.applyAcknowledge(&alarm); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	alarm = loadAlarm(t, s, "sw-1")
	if reason, err := s.applyHandle(&alarm); err != nil || reason != "" {
		t.Fatalf("确认后应允许结案: reason=%q err=%v", reason, err)
	}
	alarm = loadAlarm(t, s, "sw-1")
	if !alarm.Handled || alarm.State != store.AlarmStateCleared {
		t.Fatalf("结案后状态错误: %+v", alarm)
	}
}

// 软件 trigger 结案后，同定位键新触发沿不合并进已结案行，应落新记录（新告警显示）。
// 合并查询限定 state=active（manager.upsertAlarm），已结案行不参与聚合。
func TestSoftwareTriggerAfterHandleCreatesNewRowOnRetrigger(t *testing.T) {
	s := newAlarmTestServer(t)
	base := time.Now().UTC().Add(-2 * time.Hour)

	// 第一段：软件 trigger → 确认 → 结案。
	first := store.Alarm{
		ID: "sw-1", Kind: store.AlarmKindTrigger, Type: store.AlarmTypeSoftware,
		State: store.AlarmStateActive, OccurredAt: base, Name: "连接异常",
		Module: "gateway", Code: "gateway.conn.lost", Component: "mq",
		Acknowledged: true,
	}
	if err := s.DB.Create(&first).Error; err != nil {
		t.Fatalf("预置告警失败: %v", err)
	}
	if reason, err := s.applyHandle(p(t, s, "sw-1")); err != nil || reason != "" {
		t.Fatalf("确认后的软件 trigger 应可结案: reason=%q err=%v", reason, err)
	}

	// 第二段：同定位键（module+code+component）新 trigger 到达，落库判定。
	// 模拟 upsertAlarm 的合并查询：仅匹配 active 行，已结案行应查不到。
	var activeCount int64
	if err := s.DB.Model(&store.Alarm{}).
		Where("type = ? AND module = ? AND code = ? AND component = ? AND state = ?",
			store.AlarmTypeSoftware, "gateway", "gateway.conn.lost", "mq", store.AlarmStateActive).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("查询活动行失败: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("已结案后不应存在同定位键活动行，实际 %d", activeCount)
	}

	// 新 trigger 落库（走与 buildAlarm/upsertAlarm 相同的路径：无活动行可合并 → Create 新行）。
	second := store.Alarm{
		ID: "sw-2", Kind: store.AlarmKindTrigger, Type: store.AlarmTypeSoftware,
		State: store.AlarmStateActive, OccurredAt: base.Add(time.Hour), Name: "连接异常",
		Module: "gateway", Code: "gateway.conn.lost", Component: "mq",
	}
	if err := s.DB.Create(&second).Error; err != nil {
		t.Fatalf("新触发沿落库失败: %v", err)
	}

	// 新行是独立的告警：有自己的生命周期，第一行保持已结案不被打扰。
	oldRow := loadAlarm(t, s, "sw-1")
	newRow := loadAlarm(t, s, "sw-2")
	if oldRow.Handled != true || oldRow.State != store.AlarmStateCleared {
		t.Fatalf("已结案行不应被新触发沿改写: %+v", oldRow)
	}
	if newRow.Handled || newRow.State != store.AlarmStateActive {
		t.Fatalf("新触发沿应为独立活动告警: %+v", newRow)
	}
	if !newRow.OccurredAt.After(oldRow.OccurredAt) {
		t.Fatalf("新行触发时间应晚于已结案行: old=%v new=%v", oldRow.OccurredAt, newRow.OccurredAt)
	}

	// 新行同样走"确认后可结案"路径。
	if reason, _ := s.applyHandle(p(t, s, "sw-2")); reason == "" {
		t.Fatal("未确认的新行不可结案")
	}
	if _, err := s.applyAcknowledge(p(t, s, "sw-2")); err != nil {
		t.Fatalf("确认新行失败: %v", err)
	}
	if reason, err := s.applyHandle(p(t, s, "sw-2")); err != nil || reason != "" {
		t.Fatalf("新行确认后应可结案: reason=%q err=%v", reason, err)
	}
}
