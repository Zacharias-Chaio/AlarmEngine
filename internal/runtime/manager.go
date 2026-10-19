// Package runtime manages the restartable NATS client, alarm ingestion and duration refresh.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"alarmengine/internal/config"
	"alarmengine/internal/natsclient"
	"alarmengine/internal/platform"
	"alarmengine/internal/store"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// seenCapacity 幂等去重缓存容量（标准 §7：消费方以 alarm_id 去重）。
const seenCapacity = 8192

// subjectNamespace 平台告警标准命名空间前缀。
const subjectNamespace = platform.AlarmNamespace

// alarmIDPattern alarm_id 字符集白名单（标准要求 UUIDv4）。限制字符集可阻断恶意
// 字符串进入前端 DOM/日志的注入面；前端已改用 data 属性，这里是纵深防御。
var alarmIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// Manager 是运行时的组合根：自身只保留生命周期与告警摄取编排，
// 客户端管理（clientManager）、规则引擎状态（ruleEngine）、摄取内存态（ingestState）
// 各自成类型、各自持锁，避免单一类型堆叠 9 把锁。
type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc
	db     *gorm.DB
	log    *slog.Logger

	// 后台任务跟踪（P2：优雅退出时等待全部 goroutine 结束）。
	wg sync.WaitGroup

	// 物模型定义缓存（设备/事件/服务定义，engine.go 维护）。
	defs *definitions

	clientManager // NATS 客户端生命周期与设置副本
	ruleEngine    // 规则评估/冷却/设备串行化/规则缓存
	ingestState   // 幂等去重 + 最近摄取时间

	// closed 关闭标志：Stop 后阻止未受 WaitGroup 跟踪的定时任务再访问数据库。
	closed atomic.Bool
}

func NewManager(ctx context.Context, settings config.App, db *gorm.DB) (*Manager, error) {
	if err := config.Validate(settings); err != nil {
		return nil, err
	}
	// 自持取消：Stop() 必须能终止后台任务，不能依赖调用方传入可取消的 ctx
	// （否则用 context.Background() 构造的 Manager 在 Stop 时永久阻塞）。
	ctx, cancel := context.WithCancel(ctx)
	manager := &Manager{
		ctx: ctx, cancel: cancel, db: db, log: slog.Default().With("module", "runtime"),
		defs:        &definitions{},
		ingestState: newIngestState(),
		ruleEngine: ruleEngine{
			lastDispatch:  make(map[string]time.Time),
			ruleHoldState: make(map[string]bool),
		},
	}
	// 组合：客户端管理需要消息回调、任务跟踪与定义拉取钩子。
	manager.clientManager = clientManager{
		ctx:            ctx,
		log:            manager.log,
		defs:           manager.defs,
		startBootstrap: manager.bootstrapDefinitions,
		track:          manager.goTracked,
		onMessage:      manager.handleAlarmMessage,
		settings:       settings,
	}
	manager.sweepStaleActions() // 先恢复滞留终态，再启动客户端
	// 启动播种规则边沿：只写状态不下发，避免重启后首条消息把存量条件误判为上升沿。
	// 必须在告警订阅启动前执行（此时无消息并发写状态）。
	manager.seedRuleHoldState()
	if err := manager.reloadClient(settings); err != nil {
		manager.log.Warn("启动 NATS 客户端失败，告警消息接收与保护动作下发暂不可用，将自动退避重试", "error", err)
		manager.scheduleClientRetry()
	}
	manager.goTracked(manager.retentionLoop)
	manager.goTracked(manager.sweepStaleLoop)
	return manager, nil
}

// goTracked 启动受 WaitGroup 跟踪的后台任务；Stop 时等待全部退出（P2 优雅退出）。
func (m *Manager) goTracked(fn func()) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		fn()
	}()
}

// Status 运行时状态快照：健康检查与运维可观测（NATS 连接、定义同步、最近摄取时间）。
type Status struct {
	NatsEnabled          bool      `json:"natsEnabled"`
	NatsConnected        bool      `json:"natsConnected"`
	DefinitionsSyncedAt  time.Time `json:"definitionsSyncedAt"`
	DefinitionsLastError string    `json:"definitionsLastError,omitempty"`
	LastIngestAt         time.Time `json:"lastIngestAt"`
}

// Status 返回运行时状态快照。
func (m *Manager) Status() Status {
	client := m.currentClient()
	settings := m.currentSettings()
	defs := m.defs.snapshotView()
	status := Status{
		NatsEnabled:          settings.Nats.Enabled,
		NatsConnected:        client != nil,
		DefinitionsSyncedAt:  defs.SyncedAt,
		DefinitionsLastError: defs.LastError,
	}
	status.LastIngestAt = m.lastIngestAt()
	return status
}

// lockDevice 获取目标设备的串行化锁（返回解锁函数）。
// 同一设备的多条控制必须按规则执行顺序下发，并发规则命中时不能依赖 goroutine 调度顺序。
func (m *Manager) lockDevice(deviceID string) func() {
	m.deviceMu.Lock()
	if m.deviceLocks == nil {
		m.deviceLocks = make(map[string]*sync.Mutex)
	}
	mu, ok := m.deviceLocks[deviceID]
	if !ok {
		mu = &sync.Mutex{}
		m.deviceLocks[deviceID] = mu
	}
	m.deviceMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (m *Manager) Stop() {
	m.closed.Store(true)
	if m.cancel != nil {
		m.cancel() // 自持取消：不依赖调用方 ctx，Stop 必须能终止后台任务
	} // 阻止未受 wg 跟踪的定时任务（推定成功）在关闭后继续写库
	m.clientMu.Lock()
	client := m.client
	m.client = nil
	m.clientMu.Unlock()
	if client != nil {
		client.Close()
	}
	m.wg.Wait() // P2：等待全部受跟踪后台任务退出（各自监听 m.ctx）
}

// retentionLoop 数据保留策略（P1：防止历史告警无限累积拖慢统计与列表）：
// 每日清理已结案且结案时间早于保留天数的告警；days=0 表示永久保留（默认）。
func (m *Manager) retentionLoop() {
	m.runRetention()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.runRetention()
		}
	}
}

func (m *Manager) runRetention() {
	days := m.currentSettings().Retention.Days
	if days <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	// 已结案告警。
	result := m.db.Where("handled = ? AND cleared_at IS NOT NULL AND cleared_at < ?", true, cutoff).
		Delete(&store.Alarm{})
	if result.Error != nil {
		m.log.Warn("执行数据保留策略失败", "error", result.Error)
		return
	}
	if result.RowsAffected > 0 {
		m.log.Info("已按保留策略清理历史告警", "days", days, "count", result.RowsAffected)
	}
	// 终态动作记录同样按保留天数清理（pending/accepted 为中间态，留给启动清扫器处理，
	// 否则保护记录表会随规则命中次数无限增长）。
	actions := m.db.Where("dispatched_at < ? AND status IN ?", cutoff,
		[]string{store.ActionFailure, store.ActionSuccess, store.ActionUnknown}).
		Delete(&store.ActionRecord{})
	if actions.Error != nil {
		m.log.Warn("清理历史动作记录失败", "error", actions.Error)
		return
	}
	if actions.RowsAffected > 0 {
		m.log.Info("已按保留策略清理保护记录", "days", days, "count", actions.RowsAffected)
	}
}

// 说明：告警持续时间不再周期落库，改为 API 读取时派生（见 api.deriveDuration），
// 避免每 5s 对全部 active 行做集合 UPDATE 造成的 WAL 写放大。

// handleAlarmMessage 摄取一条标准告警消息（powerpulse-alarm-standard.md）：
// 校验 → 幂等去重 → trigger/event 落库（trigger 按定位键合并聚合）→ recover 按定位键恢复。
// 去重放在字段校验之后：非法消息不占用 alarm_id，发布方修正后重投不会被幂等丢弃。
func (m *Manager) handleAlarmMessage(msg natsclient.AlarmMessage, subject string, receivedAt time.Time) {
	if msg.AlarmID == "" {
		m.log.Warn("忽略缺少 alarm_id 的告警消息", "subject", subject)
		return
	}
	if !alarmIDPattern.MatchString(msg.AlarmID) {
		m.log.Warn("忽略非法 alarm_id 的告警消息（字符集白名单外）", "alarm_id", msg.AlarmID, "subject", subject)
		return
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
	if !m.markSeen(msg.AlarmID) {
		return // 重复消息（网络重发/多订阅），幂等丢弃
	}
	m.touch()

	occurredAt := time.UnixMilli(msg.Timestamp).UTC()
	if msg.Timestamp <= 0 {
		occurredAt = receivedAt
	}

	// 内部信号钩子：cmd.result 闭环对账、model.changed 定义维护（后者不上看板）。
	if m.handleInternalSignal(msg) {
		return
	}

	if msg.Kind == natsclient.KindRecover {
		m.recoverAlarm(msg, occurredAt)
		m.evaluateRules(msg) // active 计数可能下降（下降沿不动作，但需更新边沿状态）
		return
	}
	// 合并触发沿时 upsertAlarm 返回实际落库的告警 ID，用它对齐动作记录的"触发告警"。
	// 事件源/级别源均按 DB 活性判定，因此这里不再依赖"当前消息"参与匹配。
	if resolvedID := m.upsertAlarm(msg, occurredAt); resolvedID != "" {
		msg.AlarmID = resolvedID
	}
	m.evaluateRules(msg)
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
// event 与新定位键的 trigger 落新记录。返回实际落库的告警 ID（合并时返回既有行 ID），
// 供调用方把动作记录关联到真实告警行。规则匹配与保护动作下发见 engine.go。
func (m *Manager) upsertAlarm(msg natsclient.AlarmMessage, occurredAt time.Time) string {
	alarm, ok := m.buildAlarm(msg, occurredAt)
	if !ok {
		return ""
	}

	// trigger 定位键去重：同定位键活动告警存在时合并（重复触发沿/聚合窗口），保留首次触发时间。
	if msg.Kind == natsclient.KindTrigger {
		var existing store.Alarm
		if err := locateQuery(m.db, msg).Where("state = ?", store.AlarmStateActive).
			Order("occurred_at desc").First(&existing).Error; err == nil {
			// 条件列更新（P0）：仅更新合并涉事列并以 SQL 表达式累加 count，
			// 避免整行 Save 与并发的人工确认/恢复互相冲掉对方写入（lost-update）。
			// orphan=false：设计承诺"触发源恢复/重触发时自愈"——重触发即证明源仍在，
			// 必须复位孤儿标志，否则该告警会永久显示孤儿且不可结案。
			updates := map[string]any{
				"severity":      alarm.Severity,
				"value":         alarm.Value,
				"value_invalid": alarm.ValueInvalid,
				"threshold":     alarm.Threshold,
				"message":       alarm.Message,
				"name":          alarm.Name,
				"orphan":        false,
				"count":         gorm.Expr("count + ?", alarm.Count),
			}
			if err := m.db.Model(&store.Alarm{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
				m.log.Warn("合并告警失败", "id", existing.ID, "error", err)
				return ""
			}
			if existing.Orphan {
				m.log.Info("告警源已重触发，解除孤儿", "id", existing.ID)
			}
			m.log.Info("告警触发消息已合并", "id", existing.ID, "count", existing.Count+alarm.Count)
			return existing.ID
		}
	}

	if alarm.ID == "" {
		alarm.ID = uuid.NewString()
	}
	if err := m.db.Create(&alarm).Error; err != nil {
		// 重启后重复投递等场景：主键冲突按幂等处理。
		m.log.Warn("落库告警失败（可能为重复消息）", "id", alarm.ID, "error", err)
		return ""
	}
	m.log.Info("收到告警消息", "id", alarm.ID, "category", alarm.Type, "kind", alarm.Kind,
		"source", alarm.Source, "severity", alarm.Severity, "count", alarm.Count)
	return alarm.ID
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
			// 触发值无法解析：标记无效，前端显示"—"而不是误导性的 0。
			alarm.ValueInvalid = true
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
			// event_id 优先，但兼容同一事件此前只有 event_key 的存量行：
			// 否则同一事件会因"有无 event_id"被拆成两条活动告警，旧行永不恢复。
			return query.Where("event_id = ? OR (event_id = '' AND event_key = ?)", msg.Device.EventID, msg.Device.EventKey)
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
	duration := at.Sub(alarm.OccurredAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	// 条件列更新（P0）：仅未结案行可被恢复，避免整行 Save 覆盖并发的确认/合并写入。
	result := m.db.Model(&store.Alarm{}).
		Where("id = ? AND state <> ?", alarm.ID, store.AlarmStateCleared).
		Updates(map[string]any{"state": store.AlarmStateCleared, "recovered_at": at, "duration_ms": duration})
	if result.Error != nil {
		m.log.Warn("恢复告警失败", "id", alarm.ID, "error", result.Error)
		return
	}
	if result.RowsAffected == 0 {
		// 并发窗口中已被其他路径恢复/结案：不是"本次恢复"，不要误报。
		m.log.Info("告警已被并发恢复或结案，跳过", "id", alarm.ID)
		return
	}
	m.log.Info("告警已自动恢复", "id", alarm.ID, "name", alarm.Name)
}
