// engine.go - 告警引擎核心：物模型定义缓存（bootstrap/model.changed 维护）、
// 设备告警沿规则匹配与保护动作下发（.cmd 两段式 + request_id 闭环）、墓碑与孤儿处理。
package runtime

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"alarmengine/internal/natsclient"
	"alarmengine/internal/store"

	"github.com/google/uuid"
)

const (
	// definitionsRetryDelay 定义同步失败后的重试间隔。
	definitionsRetryDelay = 30 * time.Second
	// presumedSuccessWait 推定成功等待窗：契约 cmdAck 超时 30s + 余量。
	// 受理后该窗口内未收到 thingsmodel.cmd.result 失败终态即推定成功（契约成功静默）。
	presumedSuccessWait = 35 * time.Second
	// definitionsConcurrency bootstrap 逐设备定义拉取的有界并发（防止大量设备时
	// 并发 REQ 风暴压垮物模型，同时避免串行拉取耗时随设备数线性增长）。
	definitionsConcurrency = 4
)

// DeviceDefinition 单个设备的定义缓存：清单概要 + 告警定义 + 服务定义。
type DeviceDefinition struct {
	Device    natsclient.DeviceSummary      `json:"device"`
	Events    []natsclient.EventDefinition  `json:"events"`
	Methods   []natsclient.MethodDefinition `json:"methods"`
	FetchedAt time.Time                     `json:"fetchedAt"`
}

// DefinitionsSnapshot 定义缓存快照（规则编辑器下拉数据源）。
type DefinitionsSnapshot struct {
	SyncedAt  time.Time          `json:"syncedAt"`
	Syncing   bool               `json:"syncing"`
	LastError string             `json:"lastError,omitempty"`
	Devices   []DeviceDefinition `json:"devices"`
}

// definitions 并发安全的定义缓存：单一互斥锁保护快照与同步状态（bootstrapping/retrying）。
// 不变式：Syncing 只由 beginBootstrap / endBootstrap / markError 在锁内写；
// setAll 只在 endBootstrap 之后调用，不会再被并发写入覆盖。
type definitions struct {
	mu            sync.RWMutex
	snapshot      DefinitionsSnapshot
	bootstrapping bool
	retrying      bool
}

// beginBootstrap singleflight：返回 false 表示已有一次全量同步在跑。
func (d *definitions) beginBootstrap() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.bootstrapping {
		return false
	}
	d.bootstrapping = true
	d.snapshot.Syncing = true
	return true
}

func (d *definitions) endBootstrap() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bootstrapping = false
	d.snapshot.Syncing = false
}

func (d *definitions) markError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snapshot.Syncing = false
	d.snapshot.LastError = err.Error()
}

// beginRetry 抢占重试调度标志（同一时刻至多一个重试任务）。
func (d *definitions) beginRetry() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.retrying {
		return false
	}
	d.retrying = true
	return true
}

func (d *definitions) endRetry() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.retrying = false
}

// setAll 全量落库（P1 时序合并）：bootstrap 拉取期间若 model.changed 增量已刷新某设备
// （其 FetchedAt 晚于本次拉取），保留更新的增量条目；本次拉取期间新增、但不在全量结果中的
// 设备（FetchedAt 晚于 startedAt）同样保留，避免全量覆盖把刚建好的设备丢掉。
func (d *definitions) setAll(devices []DeviceDefinition, startedAt time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	merged := make([]DeviceDefinition, 0, len(devices))
	seen := make(map[string]struct{}, len(devices))
	for _, dd := range devices {
		kept := dd
		for _, old := range d.snapshot.Devices {
			if old.Device.DeviceID == dd.Device.DeviceID && old.FetchedAt.After(dd.FetchedAt) {
				kept = old
				break
			}
		}
		merged = append(merged, kept)
		seen[dd.Device.DeviceID] = struct{}{}
	}
	// 保留拉取窗口内新增的增量设备（全量结果里没有，但比本次拉取更新）。
	for _, old := range d.snapshot.Devices {
		if _, ok := seen[old.Device.DeviceID]; ok {
			continue
		}
		if old.FetchedAt.After(startedAt) {
			merged = append(merged, old)
		}
	}
	d.snapshot = DefinitionsSnapshot{SyncedAt: time.Now().UTC(), Devices: merged}
}

func (d *definitions) snapshotView() DefinitionsSnapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return DefinitionsSnapshot{
		SyncedAt: d.snapshot.SyncedAt, Syncing: d.snapshot.Syncing,
		LastError: d.snapshot.LastError, Devices: append([]DeviceDefinition(nil), d.snapshot.Devices...),
	}
}

// upsertDevice 增量更新单设备定义（model.changed created/updated 后重查）；概要缺失字段沿用旧值。
func (d *definitions) upsertDevice(dd DeviceDefinition) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.snapshot.Devices {
		if d.snapshot.Devices[i].Device.DeviceID != dd.Device.DeviceID {
			continue
		}
		if dd.Device.DeviceName == "" {
			dd.Device = d.snapshot.Devices[i].Device
		}
		d.snapshot.Devices[i] = dd
		return
	}
	d.snapshot.Devices = append(d.snapshot.Devices, dd)
}

func (d *definitions) removeDevice(deviceID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	kept := d.snapshot.Devices[:0]
	for _, dd := range d.snapshot.Devices {
		if dd.Device.DeviceID != deviceID {
			kept = append(kept, dd)
		}
	}
	d.snapshot.Devices = kept
}

func (d *definitions) clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snapshot = DefinitionsSnapshot{}
}

// bootstrapDefinitions 启动/重载时拉取全部启用设备定义：设备清单 → 每设备事件 + 服务定义。
// singleflight（并发触发只跑一个）+ 有界并发拉取；失败自动重试（30s 间隔），
// 前端可经 POST /api/definitions/refresh 手动触发。
// 客户端在拉取期间被热重载替换时丢弃本轮结果（旧连接已 Drain，拉到的只会是空定义）。
func (m *Manager) bootstrapDefinitions() {
	client := m.currentClient()
	if client == nil {
		return
	}
	if !m.defs.beginBootstrap() {
		return
	}
	defer m.defs.endBootstrap()
	startedAt := time.Now().UTC()
	devices, err := client.QueryDevices()
	if err != nil {
		m.defs.markError(err)
		m.log.Warn("拉取物模型设备清单失败", "error", err)
		m.scheduleDefinitionsRetry()
		return
	}
	enabled := make([]natsclient.DeviceSummary, 0, len(devices.Devices))
	for _, summary := range devices.Devices {
		if summary.Enabled {
			enabled = append(enabled, summary) // 契约：未启用设备不对外
		}
	}
	results := make([]DeviceDefinition, len(enabled))
	failures := make([]string, 0)
	var failMu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, definitionsConcurrency)
	for i, summary := range enabled {
		wg.Add(1)
		m.goTracked(func() {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			dd, failed := m.fetchDeviceDefinitions(client, summary)
			results[i] = dd
			if failed {
				failMu.Lock()
				failures = append(failures, summary.DeviceID)
				failMu.Unlock()
			}
		})
	}
	wg.Wait()
	if m.currentClient() != client {
		// 拉取期间客户端已换代：旧连接上的查询结果不可信，丢弃并等新一代重试。
		m.log.Warn("定义拉取期间 NATS 客户端已切换，丢弃本轮结果")
		return
	}
	m.defs.setAll(results, startedAt)
	if len(failures) > 0 {
		// 如实上报：局部失败时不能显示"已同步"却给出空事件/服务（运维会以为配置丢了）。
		m.defs.markError(fmt.Errorf("部分设备定义拉取失败（%d 台），缓存不完整", len(failures)))
		m.log.Warn("物模型定义部分失败", "failed_devices", failures)
	} else {
		m.log.Info("物模型定义已同步", "devices", len(results))
	}
	// 全量同步同样做一次孤儿对账：否则启动/手动"同步定义"后，定义已移除的告警仍显示孤儿。
	for _, def := range results {
		if err := m.reconcileDeviceAlarms(def.Device.DeviceID, def.Events); err != nil {
			m.log.Warn("同步后对账设备活动告警失败", "device", def.Device.DeviceID, "error", err)
		}
	}
}

// fetchDeviceDefinitions 拉取单设备的事件与服务定义；返回是否发生局部失败
// （失败时保留空定义，由调用方如实上报"部分失败"，不再显示"已同步"）。
func (m *Manager) fetchDeviceDefinitions(client *natsclient.Client, summary natsclient.DeviceSummary) (DeviceDefinition, bool) {
	dd := DeviceDefinition{Device: summary, FetchedAt: time.Now().UTC()}
	failed := false
	if events, err := client.QueryEvents(summary.DeviceID); err != nil {
		m.log.Warn("拉取告警定义失败", "device", summary.DeviceID, "error", err)
		failed = true
	} else {
		dd.Events = events.Events
	}
	if methods, err := client.QueryMethods(summary.DeviceID); err != nil {
		m.log.Warn("拉取服务定义失败", "device", summary.DeviceID, "error", err)
		failed = true
	} else {
		dd.Methods = methods.Methods
	}
	return dd, failed
}

// scheduleDefinitionsRetry 同步失败后定时重试（同一时刻至多一个重试任务）。
func (m *Manager) scheduleDefinitionsRetry() {
	if !m.defs.beginRetry() {
		return
	}
	m.goTracked(func() {
		defer m.defs.endRetry()
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(definitionsRetryDelay):
		}
		if m.currentClient() != nil {
			m.bootstrapDefinitions()
		}
	})
}

// enabledRules 读取启用规则（带内存缓存）；缓存未命中时全表加载并缓存。
func (m *Manager) enabledRules() ([]store.AlarmRule, error) {
	if rules, ok := m.ruleCache.snapshot(); ok {
		return rules, nil
	}
	var rules []store.AlarmRule
	if err := m.db.Where("enabled = ?", true).Find(&rules).Error; err != nil {
		return nil, err
	}
	m.ruleCache.set(rules)
	return rules, nil
}

// InvalidateRules 使规则缓存失效（规则保存后调用；不等下一次全量加载）。
func (m *Manager) InvalidateRules() { m.ruleCache.invalidate() }

// DefinitionsSnapshot 返回定义缓存快照（API 视图）。
func (m *Manager) DefinitionsSnapshot() DefinitionsSnapshot { return m.defs.snapshotView() }

// sweepStaleActions 启动清扫器（P0：跨进程恢复两段式协议）：
// 推定窗口之外的 pending/accepted 记录，其终态推进依赖的进程内定时器已随上次退出丢失，
// 批量条件更新为 unknown（终态未知），避免动作列表出现永久中间态。
func (m *Manager) sweepStaleActions() {
	cutoff := time.Now().Add(-presumedSuccessWait - time.Minute)
	result := m.db.Model(&store.ActionRecord{}).
		Where("status IN ? AND dispatched_at < ?", []string{store.ActionPending, store.ActionAccepted}, cutoff).
		Updates(map[string]any{
			"status":       store.ActionUnknown,
			"message":      "引擎重启前未推进终态，状态未知",
			"finalized_at": time.Now().UTC(),
		})
	if result.Error != nil {
		m.log.Warn("清扫滞留动作记录失败", "error", result.Error)
		return
	}
	if result.RowsAffected > 0 {
		m.log.Info("已清扫滞留动作记录", "count", result.RowsAffected)
	}
}

// sweepStaleLoop 周期清扫滞留中间态动作（每 60s）：仅靠启动清扫会漏掉
// "上次退出前 95 秒内产生"的 pending/accepted（定时器随进程消失，此后永不推进）。
func (m *Manager) sweepStaleLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.sweepStaleActions()
		}
	}
}

// ResetRuleState 清理规则的内存态（边沿/冷却），规则删除后调用，避免同 ID 重建时残留。
func (m *Manager) ResetRuleState(ruleID string) {
	m.ruleCache.invalidate()
	m.ruleHoldMu.Lock()
	delete(m.ruleHoldState, ruleID)
	m.ruleHoldMu.Unlock()
	m.cooldownMu.Lock()
	delete(m.lastDispatch, ruleID)
	m.cooldownMu.Unlock()
}

// RefreshDefinitions 手动触发定义同步（异步执行，前端轮询快照观察结果）。
func (m *Manager) RefreshDefinitions() error {
	if m.currentClient() == nil {
		return fmt.Errorf("NATS 客户端未启用")
	}
	m.goTracked(m.bootstrapDefinitions)
	return nil
}

// handleInternalSignal 处理引擎内部消费的软件事件；返回 true 表示仅作内部信号、不上告警看板。
func (m *Manager) handleInternalSignal(msg natsclient.AlarmMessage) bool {
	if msg.Category != natsclient.CategorySoftware || msg.Software == nil {
		return false
	}
	switch msg.Software.Code {
	case natsclient.EventCodeCmdResult:
		m.closeActionLoop(msg)
		return false // 控制失败终态上板：保护动作失败是重要运维信息
	case natsclient.EventCodeModelChanged:
		m.handleModelChanged(msg)
		return true // 定义变更提示不上板，只做缓存维护信号
	case natsclient.EventCodeFrequentFail:
		// 高频控制失败聚合告警：按普通软件事件上板（聚合计数由发布方携带），引擎无额外处理。
		return false
	}
	return false
}

// closeActionLoop 以 request_id 对账控制失败终态（契约 §4.2：cmd.result 每请求一条、不聚合）。
func (m *Manager) closeActionLoop(msg natsclient.AlarmMessage) {
	requestID, _ := msg.Software.Context["request_id"].(string)
	if requestID == "" {
		return
	}
	message, _ := msg.Software.Context["message"].(string)
	if message == "" {
		message = msg.Software.Message
	}
	var record store.ActionRecord
	// 包含 success：推定成功只是"未收到失败"的推定，迟到失败应能覆盖（宁可错报失败，不可错报成功）。
	if err := m.db.Where("request_id = ? AND status IN ?", requestID,
		[]string{store.ActionPending, store.ActionAccepted, store.ActionSuccess}).
		First(&record).Error; err != nil {
		return // 非引擎发起的控制（无匹配动作记录），仅按告警落库
	}
	now := time.Now().UTC()
	// 条件更新（P0）：仅当仍处于未终态或已被推定成功时写入——失败终态允许覆盖推定成功
	//（宁可错报失败，不可错报成功；cmd.result 每请求一条，无重复覆盖风险）。
	result := m.db.Model(&store.ActionRecord{}).
		Where("id = ? AND status IN ?", record.ID, []string{store.ActionPending, store.ActionAccepted, store.ActionSuccess}).
		Updates(map[string]any{
			"status":       store.ActionFailure,
			"message":      orDefault(message, "控制执行失败"),
			"finalized_at": now,
		})
	if result.Error != nil {
		m.log.Warn("回填动作失败终态失败", "id", record.ID, "error", result.Error)
		return
	}
	if result.RowsAffected == 0 {
		return // 已是 failure 终态（重复对账），保持原状
	}
	m.log.Warn("保护动作执行失败", "rule", record.RuleName, "request_id", requestID, "message", orDefault(message, "控制执行失败"))
}

// handleModelChanged 处理模型定义变更（契约 §4.3）：
// created/updated → 重查该设备定义并对账孤儿；deleted/disabled → 墓碑：活动告警自动恢复并移除缓存。
func (m *Manager) handleModelChanged(msg natsclient.AlarmMessage) {
	deviceID, _ := msg.Software.Context["device_id"].(string)
	action, _ := msg.Software.Context["action"].(string)
	if deviceID == "" {
		return
	}
	switch action {
	case "created", "updated":
		m.goTracked(func() { m.refreshDeviceDefinitions(deviceID) })
	case "deleted", "disabled":
		m.defs.removeDevice(deviceID)
		m.tombstoneDevice(deviceID, action)
		// 墓碑清除活动告警后重算边沿：只刷新状态、不下发（本次并非真实告警沿，避免空 AlarmID 动作）。
		m.evaluateRulesQuiet(natsclient.AlarmMessage{})
	default:
		m.log.Warn("未识别的模型变更动作", "action", action, "device", deviceID)
	}
}

// refreshDeviceDefinitions 重查单设备定义并对账活动告警孤儿状态。
func (m *Manager) refreshDeviceDefinitions(deviceID string) {
	client := m.currentClient()
	if client == nil {
		return
	}
	dd := DeviceDefinition{Device: natsclient.DeviceSummary{DeviceID: deviceID, Enabled: true}, FetchedAt: time.Now().UTC()}
	events, err := client.QueryEvents(deviceID)
	if err != nil {
		m.log.Warn("重查告警定义失败", "device", deviceID, "error", err)
		return
	}
	dd.Events = events.Events
	if methods, err := client.QueryMethods(deviceID); err == nil {
		dd.Methods = methods.Methods
	}
	m.defs.upsertDevice(dd)
	if err := m.reconcileDeviceAlarms(deviceID, dd.Events); err != nil {
		m.log.Warn("对账设备活动告警失败", "device", deviceID, "error", err)
	}
}

// reconcileDeviceAlarms 定义对账：活动告警的 event_key 已不在最新定义中 → 置孤儿（源失联）；
// 定义重新包含该 key → 解除孤儿。
func (m *Manager) reconcileDeviceAlarms(deviceID string, events []natsclient.EventDefinition) error {
	keys := make(map[string]bool, len(events))
	for _, event := range events {
		keys[event.Key] = true
	}
	var alarms []store.Alarm
	if err := m.db.Where("device_id = ? AND state = ?", deviceID, store.AlarmStateActive).Find(&alarms).Error; err != nil {
		return err
	}
	for _, alarm := range alarms {
		orphan := !keys[alarm.EventKey]
		if alarm.Orphan == orphan {
			continue
		}
		if err := m.db.Model(&store.Alarm{}).Where("id = ?", alarm.ID).Update("orphan", orphan).Error; err != nil {
			return err
		}
		if orphan {
			m.log.Warn("告警事件定义已移除，标记孤儿", "id", alarm.ID, "event_key", alarm.EventKey, "device", deviceID)
		} else {
			m.log.Info("告警事件定义已恢复，解除孤儿", "id", alarm.ID, "event_key", alarm.EventKey)
		}
	}
	return nil
}

// tombstoneDevice 设备删除/停用墓碑：recover 永远不会来，活动告警自动恢复并备注原因，
// 之后允许人工结案进历史（避免"孤儿不可关闭"与"永不恢复"形成死结）。
func (m *Manager) tombstoneDevice(deviceID, action string) {
	var alarms []store.Alarm
	if err := m.db.Where("device_id = ? AND state = ?", deviceID, store.AlarmStateActive).Find(&alarms).Error; err != nil {
		m.log.Warn("墓碑查询活动告警失败", "device", deviceID, "error", err)
		return
	}
	noun := "删除"
	if action == "disabled" {
		noun = "停用"
	}
	note := "触发源已" + noun + "（model.changed 墓碑），自动恢复"
	now := time.Now().UTC()
	for _, alarm := range alarms {
		if strings.Contains(alarm.Message, note) {
			continue
		}
		duration := now.Sub(alarm.OccurredAt).Milliseconds()
		if duration < 0 {
			duration = 0
		}
		message := alarm.Message
		if message != "" {
			message += "；"
		}
		message += note
		// 条件列更新（P0）：仅活动行可被墓碑恢复，避免覆盖并发的人工确认写入。
		result := m.db.Model(&store.Alarm{}).
			Where("id = ? AND state = ?", alarm.ID, store.AlarmStateActive).
			Updates(map[string]any{
				"state":        store.AlarmStateCleared,
				"orphan":       false,
				"recovered_at": now,
				"duration_ms":  duration,
				"message":      message,
			})
		if result.Error != nil {
			m.log.Warn("墓碑自动恢复失败", "id", alarm.ID, "error", result.Error)
			continue
		}
		if result.RowsAffected == 0 {
			continue // 并发窗口内已被恢复/结案
		}
		m.log.Info("设备墓碑，活动告警自动恢复", "id", alarm.ID, "device", deviceID, "action", action)
	}
}

// evaluateRules 告警状态变化后评估全部启用规则（多监测源模型）：
//
//	event 源：该设备/事件的活动告警存在（状态判定，非"当前消息匹配"）——恢复后边沿下降，
//	          下次触发重新武装；无关告警消息不会改变边沿状态；
//	level 源：级别集合内 active 告警计数 > 0（仅状态型 trigger 且非孤儿，"出现严重告警即统一动作"）；
//	按 MatchMode（anyone 任一成立 / all 全部成立）聚合，上升沿（false→true）且冷却通过时
//	执行全部控制源。评估点：trigger/event 落库后、recover 后、墓碑清除后。
//	重启时由 seedRuleHoldState 预置边沿，首个状态变化不会把存量条件误判为上升沿。
func (m *Manager) evaluateRules(msg natsclient.AlarmMessage) {
	m.evaluateRulesInternal(msg, true)
}

// evaluateRulesQuiet 只刷新规则边沿状态、不下发控制源（墓碑/定义变更等非告警沿场景）。
func (m *Manager) evaluateRulesQuiet(msg natsclient.AlarmMessage) {
	m.evaluateRulesInternal(msg, false)
}

func (m *Manager) evaluateRulesInternal(msg natsclient.AlarmMessage, fire bool) {
	rules, err := m.enabledRules()
	if err != nil {
		m.log.Warn("查询启用规则失败", "error", err)
		return
	}
	for _, rule := range rules {
		if len(rule.Monitors) == 0 || len(rule.Controls) == 0 {
			continue
		}
		holds := m.ruleHolds(rule, msg)
		m.ruleHoldMu.Lock()
		previous := m.ruleHoldState[rule.ID]
		m.ruleHoldState[rule.ID] = holds
		m.ruleHoldMu.Unlock()
		if !holds || previous || !fire {
			continue
		}
		if !m.cooldownAllows(rule.ID, rule.CooldownMs) {
			m.log.Info("规则冷却中，跳过下发", "rule", rule.Name, "cooldown_ms", rule.CooldownMs)
			continue
		}
		m.log.Info("规则条件成立（上升沿），执行控制源", "rule", rule.Name, "match_mode", rule.MatchMode)
		m.fireControls(rule, msg)
	}
}

// seedRuleHoldState 启动播种：按当前存量告警计算一次边沿状态（只写状态、不下发）。
// 避免重启后首条消息把"重启前已成立"的条件误判为上升沿而重复执行保护动作。
// 必须在告警订阅启动前调用（Manager 构造期），避免与 evaluateRules 并发写状态。
func (m *Manager) seedRuleHoldState() {
	rules, err := m.enabledRules()
	if err != nil {
		m.log.Warn("播种规则边沿状态失败", "error", err)
		return
	}
	states := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if len(rule.Monitors) == 0 || len(rule.Controls) == 0 {
			continue
		}
		states[rule.ID] = m.ruleHolds(rule, natsclient.AlarmMessage{})
	}
	m.ruleHoldMu.Lock()
	for id, holds := range states {
		m.ruleHoldState[id] = holds
	}
	m.ruleHoldMu.Unlock()
	m.log.Info("规则边沿状态已播种", "rules", len(states))
}

// ruleHolds 单规则聚合判断：anyone 任一源成立 / all 全部源成立。
func (m *Manager) ruleHolds(rule store.AlarmRule, msg natsclient.AlarmMessage) bool {
	mode := rule.MatchMode
	if mode == "" {
		mode = store.RuleMatchAny
	}
	for _, monitor := range rule.Monitors {
		holds := m.monitorHolds(monitor, msg)
		if mode == store.RuleMatchAny && holds {
			return true
		}
		if mode == store.RuleMatchAll && !holds {
			return false
		}
	}
	return mode == store.RuleMatchAll
}

// monitorHolds 单监测源成立判断。
// 两类监测源都按"源告警当前是否仍处于活动"判定（DB 活性），而不是只看"当前消息是否匹配"：
// 只看消息的话，任何不匹配的无关消息都会把边沿状态打回 false，导致条件从未解除却被判为
// 新的上升沿、重复下发保护动作（无关消息夹在两次触发之间即可复现）。
func (m *Manager) monitorHolds(monitor store.RuleMonitor, msg natsclient.AlarmMessage) bool {
	switch monitor.Kind {
	case "event":
		// 监测类型：v1 仅设备（软件/系统预留；保存时已拒绝，存量空值按设备兜底）。
		category := monitor.Type
		if category == "" {
			category = natsclient.CategoryDevice
		}
		var count int64
		if err := m.db.Model(&store.Alarm{}).
			Where("state = ? AND type = ? AND orphan = ? AND device_id = ? AND event_key = ?",
				store.AlarmStateActive, category, false, monitor.DeviceID, monitor.EventKey).
			Count(&count).Error; err != nil {
			m.log.Warn("统计事件源活动告警失败", "device", monitor.DeviceID, "event_key", monitor.EventKey, "error", err)
			return false
		}
		return count > 0
	case "level":
		// 级别监测按状态型告警计数：只统计仍有效的 trigger 行（kind=trigger 且非孤儿）。
		// kind=event 无恢复沿（只能人工结案）、孤儿源失联（永不恢复），若计入会让计数永久 >0、
		// 边沿永不下降，导致该规则此后不再动作。
		query := m.db.Model(&store.Alarm{}).
			Where("state = ? AND kind = ? AND orphan = ?", store.AlarmStateActive, natsclient.KindTrigger, false)
		if len(monitor.Levels) > 0 {
			query = query.Where("severity IN ?", monitor.Levels)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			m.log.Warn("统计活动告警失败", "error", err)
			return false
		}
		return count > 0
	default:
		return false
	}
}

// fireControls 执行规则的全部控制源：device 型并发各自下发，独立 ActionRecord/闭环，互不影响。
func (m *Manager) fireControls(rule store.AlarmRule, msg natsclient.AlarmMessage) {
	for _, control := range rule.Controls {
		if control.Kind != "device" {
			continue // sms/email 预留
		}
		control := control
		m.goTracked(func() { m.dispatchControl(rule.ID, rule.Name, control, msg) })
	}
}

// cooldownAllows 同规则重复下发冷却（内存态；重启后重置，属可接受损失）。
// 注意：冷却在判定时即消耗，且下发失败同样消耗——这同时起防风暴作用
// （失败后若源侧重触发沿到达，需等冷却窗结束才允许再次下发）。
func (m *Manager) cooldownAllows(ruleID string, cooldownMs int) bool {
	if cooldownMs <= 0 {
		return true
	}
	now := time.Now()
	m.cooldownMu.Lock()
	defer m.cooldownMu.Unlock()
	if last, ok := m.lastDispatch[ruleID]; ok && now.Sub(last) < time.Duration(cooldownMs)*time.Millisecond {
		return false
	}
	m.lastDispatch[ruleID] = now
	return true
}

// dispatchControl 下发单个控制源（契约 §6 两段式）：
// 预生成 request_id 并先落动作记录 → .cmd REQ → REP 受理/同步失败；执行终态：失败经 cmd.result 回填，成功推定（35s 窗）。
// request_id 先落库是为了堵住 REQ/REP 窗口内到达的 cmd.result 因查不到记录而被丢弃（否则失败会被误报为推定成功）。
// 同一设备的控制按到达顺序串行执行，避免并发规则命中时冲突指令乱序。
func (m *Manager) dispatchControl(ruleID, ruleName string, control store.RuleControl, msg natsclient.AlarmMessage) {
	unlock := m.lockDevice(control.DeviceID)
	defer unlock()

	channel := m.currentControl()
	if channel == nil {
		m.log.Warn("控制通道未启用，保护动作未下发", "rule", ruleName)
		return
	}
	requestID := uuid.NewString() // 预生成，与 .cmd 请求 PP-Message-ID 一致
	record := store.ActionRecord{
		ID: uuid.NewString(), RuleID: ruleID, RuleName: ruleName, AlarmID: msg.AlarmID,
		DeviceID: control.DeviceID, ServiceKey: control.ServiceKey, Value: control.Value,
		RequestID: requestID, Status: store.ActionPending, DispatchedAt: time.Now().UTC(),
	}
	if err := m.db.Create(&record).Error; err != nil {
		m.log.Warn("落动作记录失败", "error", err)
		return
	}
	// 下发前按定义缓存校验服务存在且已绑定（契约 §6：控制前必须先查服务定义）。
	if reason := m.validateControl(control); reason != "" {
		m.finalizeAction(record.ID, store.ActionFailure, reason, time.Now().UTC())
		m.log.Warn("保护动作未下发（定义校验失败）", "rule", ruleName, "reason", reason)
		return
	}
	ack, _, err := channel(natsclient.ControlRequest{
		RequestID: requestID,
		DeviceID:  control.DeviceID, ServiceKey: control.ServiceKey, Value: control.Value,
	})
	now := time.Now().UTC()
	switch {
	case err != nil:
		// 传输失败/超时不重试：契约规定重复请求不去重，必须 at-most-once。
		m.finalizeAction(record.ID, store.ActionFailure, "下发失败（不重试）: "+err.Error(), now)
		m.log.Warn("保护动作下发失败", "rule", ruleName, "message", err.Error())
	case ack.Status == "failure":
		m.finalizeAction(record.ID, store.ActionFailure, orDefault(ack.Message, "受理失败"), now)
		m.log.Warn("保护动作下发失败", "rule", ruleName, "message", orDefault(ack.Message, "受理失败"))
	default:
		// 条件更新：仅 pending 可升级为 accepted——REQ/REP 窗口内 cmd.result 可能已把记录置为 failure。
		result := m.db.Model(&store.ActionRecord{}).
			Where("id = ? AND status = ?", record.ID, store.ActionPending).
			Updates(map[string]any{"status": store.ActionAccepted})
		if result.Error != nil {
			m.log.Warn("更新动作记录失败", "id", record.ID, "error", result.Error)
			return
		}
		if result.RowsAffected == 0 {
			return // 已被失败终态闭环覆盖：不再推定成功
		}
		time.AfterFunc(presumedSuccessWait, func() { m.presumeSuccess(record.ID) })
		m.log.Info("保护动作已受理", "rule", ruleName, "request_id", requestID, "target", control.DeviceID+"."+control.ServiceKey)
	}
}

// validateControl 下发前按定义缓存校验服务存在且已绑定（契约 §6：控制前必须先查服务定义）。
// 定义缓存为空（尚未同步）时跳过校验，避免启动窗口把正常动作误判为失败。
func (m *Manager) validateControl(control store.RuleControl) string {
	snapshot := m.defs.snapshotView()
	if len(snapshot.Devices) == 0 {
		return ""
	}
	for _, def := range snapshot.Devices {
		if def.Device.DeviceID != control.DeviceID {
			continue
		}
		for _, method := range def.Methods {
			if method.Key != control.ServiceKey {
				continue
			}
			if !method.Bound {
				return "服务未绑定目标点位，不可控制"
			}
			return ""
		}
		return "服务未在定义缓存中: " + control.ServiceKey
	}
	return "设备未在定义缓存中: " + control.DeviceID
}

// finalizeAction 条件更新动作终态：仅未终态（pending/accepted）可写入，避免覆盖并发写入。
func (m *Manager) finalizeAction(id, status, message string, at time.Time) {
	result := m.db.Model(&store.ActionRecord{}).
		Where("id = ? AND status IN ?", id, []string{store.ActionPending, store.ActionAccepted}).
		Updates(map[string]any{
			"status":       status,
			"message":      message,
			"finalized_at": at,
		})
	if result.Error != nil {
		m.log.Warn("回填动作终态失败", "id", id, "error", result.Error)
		return
	}
	if result.RowsAffected == 0 {
		m.log.Info("动作记录已进入终态，跳过回填", "id", id, "status", status)
	}
}

// presumeSuccess 推定成功：受理后窗口内未收到失败终态。契约成功回执为 .data 值变化，
// 本引擎不订阅 .data（告警沿驱动设计），故采用推定语义。
// 条件更新（P0）：仅 accepted 可升级为 success，与 closeActionLoop 的失败写入天然互斥，
// 杜绝"读后写"竞态把失败终态覆盖为成功。
func (m *Manager) presumeSuccess(id string) {
	if m.closed.Load() {
		return // 引擎已停机（定时器未受 WaitGroup 跟踪）：不再访问可能已关闭的数据库
	}
	result := m.db.Model(&store.ActionRecord{}).
		Where("id = ? AND status = ?", id, store.ActionAccepted).
		Updates(map[string]any{
			"status":       store.ActionSuccess,
			"message":      "推定成功（受理后未收到失败终态）",
			"finalized_at": time.Now().UTC(),
		})
	if result.Error != nil {
		m.log.Warn("推定成功回填失败", "id", id, "error", result.Error)
	}
}
