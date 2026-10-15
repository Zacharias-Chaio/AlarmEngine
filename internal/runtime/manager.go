// Package runtime manages the restartable NATS client, alarm ingestion and duration refresh.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"alarmengine/internal/config"
	"alarmengine/internal/natsclient"
	"alarmengine/internal/store"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// durationRefreshInterval 活动告警持续时间刷新周期。
const durationRefreshInterval = 5 * time.Second

// seenCapacity 幂等去重缓存容量（标准 §7：消费方以 alarm_id 去重）。
const seenCapacity = 8192

// subjectNamespace 平台告警标准命名空间前缀。
const subjectNamespace = "powerpulse.alarm."

// Manager keeps alarm ingestion available while its NATS client is replaced or restarted.
type Manager struct {
	ctx context.Context
	db  *gorm.DB
	log *slog.Logger

	mu     sync.RWMutex
	client *natsclient.Client

	restartMu  sync.Mutex
	restarting bool

	// alarm_id 幂等去重缓存（内存 FIFO 环）。
	seenMu    sync.Mutex
	seen      map[string]struct{}
	seenOrder []string
}

func NewManager(ctx context.Context, settings config.App, db *gorm.DB) (*Manager, error) {
	if err := config.Validate(settings); err != nil {
		return nil, err
	}
	manager := &Manager{
		ctx: ctx, db: db, log: slog.Default().With("module", "runtime"),
		seen: make(map[string]struct{}),
	}
	if err := manager.reloadClient(settings); err != nil {
		manager.log.Warn("启动 NATS 客户端失败，告警消息接收与保护动作发布暂不可用", "error", err)
	}
	go manager.refreshDurationsLoop()
	return manager, nil
}

// ReloadBus hot-reloads the NATS client without disturbing HTTP handlers.
func (m *Manager) ReloadBus(settings config.App) error {
	if err := config.Validate(settings); err != nil {
		return err
	}
	return m.reloadClient(settings)
}

func (m *Manager) reloadClient(settings config.App) error {
	candidate, err := natsclient.NewClient(m.ctx, settings, m.handleAlarmMessage)
	if err != nil {
		return err
	}
	m.mu.Lock()
	previous := m.client
	m.client = candidate
	m.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	m.log.Info("NATS 客户端配置已加载", "enabled", settings.Nats.Enabled)
	return nil
}

// Restart replaces runtime resources asynchronously while the HTTP server stays available.
func (m *Manager) Restart(settings config.App) bool {
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	if m.restarting {
		return false
	}
	m.restarting = true
	go func() {
		defer func() {
			m.restartMu.Lock()
			m.restarting = false
			m.restartMu.Unlock()
		}()
		if err := m.ReloadBus(settings); err != nil {
			m.log.Error("软件重启时加载 NATS 客户端失败", "error", err)
			return
		}
		m.log.Info("告警运行时重启完成")
	}()
	return true
}

func (m *Manager) Stop() {
	m.mu.Lock()
	client := m.client
	m.client = nil
	m.mu.Unlock()
	if client != nil {
		client.Close()
	}
}

// refreshDurationsLoop 周期刷新未清除告警（含孤儿）的持续时间。
func (m *Manager) refreshDurationsLoop() {
	ticker := time.NewTicker(durationRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			if err := store.RefreshActiveDurations(m.db, now); err != nil {
				m.log.Warn("刷新告警持续时间失败", "error", err)
			}
		}
	}
}

// markSeen 幂等去重：返回 false 表示重复消息应丢弃。
func (m *Manager) markSeen(alarmID string) bool {
	if alarmID == "" {
		return true
	}
	m.seenMu.Lock()
	defer m.seenMu.Unlock()
	if _, dup := m.seen[alarmID]; dup {
		return false
	}
	m.seen[alarmID] = struct{}{}
	m.seenOrder = append(m.seenOrder, alarmID)
	if len(m.seenOrder) > seenCapacity {
		evict := m.seenOrder[0]
		m.seenOrder = m.seenOrder[1:]
		delete(m.seen, evict)
	}
	return true
}

// handleAlarmMessage 摄取一条标准告警消息（powerpulse-alarm-standard.md）：
// 幂等去重 → 校验 → trigger/event 落库（trigger 按定位键合并聚合）→ recover 按定位键恢复。
func (m *Manager) handleAlarmMessage(msg natsclient.AlarmMessage, subject string, receivedAt time.Time) {
	if msg.AlarmID == "" {
		m.log.Warn("忽略缺少 alarm_id 的告警消息", "subject", subject)
		return
	}
	if !m.markSeen(msg.AlarmID) {
		return // 重复消息（网络重发/多订阅），幂等丢弃
	}
	if module := moduleFromSubject(subject); module != "" && msg.Source.Module != "" && module != msg.Source.Module {
		m.log.Warn("告警消息主题与 source.module 不一致，以负载为准",
			"subject", subject, "module", msg.Source.Module, "alarm_id", msg.AlarmID)
	}
	if !validCategory(msg.Category) {
		m.log.Warn("忽略未知告警类别", "category", msg.Category, "alarm_id", msg.AlarmID)
		return
	}
	if !validKind(msg.Kind) {
		m.log.Warn("忽略未知告警生命周期", "kind", msg.Kind, "alarm_id", msg.AlarmID)
		return
	}

	occurredAt := time.UnixMilli(msg.Timestamp).UTC()
	if msg.Timestamp <= 0 {
		occurredAt = receivedAt
	}

	if msg.Kind == natsclient.KindRecover {
		m.recoverAlarm(msg, occurredAt)
		return
	}
	m.upsertAlarm(msg, occurredAt)
}

func validCategory(category string) bool {
	return category == natsclient.CategoryDevice || category == natsclient.CategorySoftware || category == natsclient.CategorySystem
}

func validKind(kind string) bool {
	return kind == natsclient.KindTrigger || kind == natsclient.KindRecover || kind == natsclient.KindEvent
}

// moduleFromSubject 从 powerpulse.alarm.{module}.... 主题提取模块名；非标准命名空间返回空。
func moduleFromSubject(subject string) string {
	if len(subject) <= len(subjectNamespace) || subject[:len(subjectNamespace)] != subjectNamespace {
		return ""
	}
	rest := subject[len(subjectNamespace):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			return rest[:i]
		}
	}
	return rest
}

// toFloat 标准触发值为 any 类型，尽力转数值；不可转返回 (0,false)。
func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// upsertAlarm 处理 kind=trigger/event：trigger 按定位键合并更新活动告警（聚合计数累加），
// event 与新定位键的 trigger 落新记录。TODO(告警引擎): 规则匹配 + 保护动作下发。
func (m *Manager) upsertAlarm(msg natsclient.AlarmMessage, occurredAt time.Time) {
	alarm, ok := m.buildAlarm(msg, occurredAt)
	if !ok {
		return
	}

	// trigger 定位键去重：同定位键活动告警存在时合并（重复触发沿/聚合窗口），保留首次触发时间。
	if msg.Kind == natsclient.KindTrigger {
		var existing store.Alarm
		if err := locateQuery(m.db, msg).Where("state = ?", store.AlarmStateActive).
			Order("occurred_at desc").First(&existing).Error; err == nil {
			existing.Severity = alarm.Severity
			existing.Value = alarm.Value
			existing.Threshold = alarm.Threshold
			existing.Unit = alarm.Unit
			existing.Message = alarm.Message
			existing.Name = alarm.Name
			existing.Count += alarm.Count
			if existing.Module == "" {
				existing.Module = alarm.Module
				existing.Instance = alarm.Instance
			}
			if err := m.db.Save(&existing).Error; err != nil {
				m.log.Warn("合并告警失败", "id", existing.ID, "error", err)
				return
			}
			m.log.Info("告警触发消息已合并", "id", existing.ID, "count", existing.Count)
			return
		}
	}

	if alarm.ID == "" {
		alarm.ID = uuid.NewString()
	}
	if err := m.db.Create(&alarm).Error; err != nil {
		// 重启后重复投递等场景：主键冲突按幂等处理。
		m.log.Warn("落库告警失败（可能为重复消息）", "id", alarm.ID, "error", err)
		return
	}
	m.log.Info("收到告警消息", "id", alarm.ID, "category", alarm.Type, "kind", alarm.Kind,
		"source", alarm.Source, "severity", alarm.Severity, "count", alarm.Count)
}

// buildAlarm 将标准消息映射为存储模型；明细缺失时告警并放弃。
func (m *Manager) buildAlarm(msg natsclient.AlarmMessage, occurredAt time.Time) (store.Alarm, bool) {
	alarm := store.Alarm{
		ID:         msg.AlarmID,
		Kind:       msg.Kind,
		Module:     msg.Source.Module,
		Instance:   msg.Source.Instance,
		Severity:   msg.Level,
		State:      store.AlarmStateActive,
		OccurredAt: occurredAt,
		Count:      msg.Count,
	}
	if alarm.Severity < 0 || alarm.Severity > 3 {
		m.log.Warn("告警级别越界，按提示处理", "level", msg.Level, "alarm_id", msg.AlarmID)
		alarm.Severity = 0
	}
	if alarm.Count < 1 {
		alarm.Count = 1
	}

	switch msg.Category {
	case natsclient.CategoryDevice:
		d := msg.Device
		if d == nil {
			m.log.Warn("设备告警缺少 device 明细，已忽略", "alarm_id", msg.AlarmID)
			return store.Alarm{}, false
		}
		alarm.Type = store.AlarmTypeDevice
		alarm.Name = d.EventName
		if alarm.Name == "" {
			alarm.Name = d.EventKey
		}
		alarm.Source = d.DeviceName
		if alarm.Source == "" {
			alarm.Source = d.DeviceID
		}
		alarm.Point = d.Point
		alarm.DeviceID = d.DeviceID
		alarm.DeviceName = d.DeviceName
		alarm.EventID = d.EventID
		alarm.EventKey = d.EventKey
		alarm.Threshold = d.Rule.Threshold
		if value, ok := toFloat(d.Value); ok {
			alarm.Value = value
			alarm.Message = fmt.Sprintf("%s(%s) 当前值 %v，阈值 %v（%s）",
				orDefault(d.PointName, d.Point), d.Point, value, d.Rule.Threshold, orDefault(d.Rule.Type, "rule"))
		} else {
			alarm.Message = fmt.Sprintf("%s(%s) 触发，阈值 %v（%s）",
				orDefault(d.PointName, d.Point), d.Point, d.Rule.Threshold, orDefault(d.Rule.Type, "rule"))
		}
	case natsclient.CategorySoftware:
		s := msg.Software
		if s == nil {
			m.log.Warn("软件告警缺少 software 明细，已忽略", "alarm_id", msg.AlarmID)
			return store.Alarm{}, false
		}
		alarm.Type = store.AlarmTypeSoftware
		alarm.Name = s.Code
		alarm.Source = orDefault(msg.Source.Module, msg.Source.Instance)
		alarm.SoftwareID = msg.Source.Instance
		alarm.Code = s.Code
		alarm.Component = s.Component
		alarm.Message = s.Message
	case natsclient.CategorySystem:
		s := msg.System
		if s == nil {
			m.log.Warn("系统告警缺少 system 明细，已忽略", "alarm_id", msg.AlarmID)
			return store.Alarm{}, false
		}
		alarm.Type = store.AlarmTypeSystem
		alarm.Name = s.Code
		alarm.Source = "系统"
		alarm.Code = s.Code
		alarm.Component = s.Component
		alarm.Message = s.Message
	}
	return alarm, true
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// locateQuery 构造定位键查询（标准 §5.2：recover 与 trigger 定位键一致）：
// device → module + device_id + (event_id 优先，缺省 event_key)；
// software/system → module + code + component。
func locateQuery(db *gorm.DB, msg natsclient.AlarmMessage) *gorm.DB {
	switch msg.Category {
	case natsclient.CategoryDevice:
		if msg.Device == nil {
			return db.Where("1 = 0")
		}
		query := db.Where("type = ? AND module = ? AND device_id = ?",
			store.AlarmTypeDevice, msg.Source.Module, msg.Device.DeviceID)
		if msg.Device.EventID != "" {
			return query.Where("event_id = ?", msg.Device.EventID)
		}
		return query.Where("event_key = ?", msg.Device.EventKey)
	default: // software / system
		code, component := "", ""
		if msg.Software != nil {
			code, component = msg.Software.Code, msg.Software.Component
		}
		if msg.System != nil {
			code, component = msg.System.Code, msg.System.Component
		}
		return db.Where("type = ? AND module = ? AND code = ? AND component = ?",
			msg.Category, msg.Source.Module, code, component)
	}
}

// recoverAlarm 处理 kind=recover：按定位键恢复最近一条未恢复告警（仅置状态与恢复时间，不动人工标记）。
func (m *Manager) recoverAlarm(msg natsclient.AlarmMessage, at time.Time) {
	var alarm store.Alarm
	if err := locateQuery(m.db, msg).Where("state <> ?", store.AlarmStateCleared).
		Order("occurred_at desc").First(&alarm).Error; err != nil {
		m.log.Info("忽略无匹配活动告警的恢复消息", "alarm_id", msg.AlarmID, "module", msg.Source.Module)
		return
	}
	alarm.State = store.AlarmStateCleared
	alarm.RecoveredAt = &at
	alarm.DurationMs = at.Sub(alarm.OccurredAt).Milliseconds()
	if alarm.DurationMs < 0 {
		alarm.DurationMs = 0
	}
	if err := m.db.Save(&alarm).Error; err != nil {
		m.log.Warn("恢复告警失败", "id", alarm.ID, "error", err)
		return
	}
	m.log.Info("告警已自动恢复", "id", alarm.ID, "name", alarm.Name)
}

// PublishProtection 通过 NATS 客户端发布保护动作（预留：规则匹配成功后调用）。
func (m *Manager) PublishProtection(action natsclient.ProtectionAction) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		m.log.Warn("NATS 客户端未启用，保护动作未发布", "rule", action.RuleID)
		return
	}
	client.PublishAction(action)
}
