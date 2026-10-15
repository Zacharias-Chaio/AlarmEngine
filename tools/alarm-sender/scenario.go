// scenario.go - 工况定义与消息构造：按平台告警标准（docs/powerpulse-alarm-standard.md）生成测试消息。
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	"alarmengine/internal/natsclient"

	"github.com/google/uuid"
)

// frame 是一条待发送的总线消息：目标主题 + 信封头 + 负载 + 统计引用。
type frame struct {
	subject string
	payload []byte
	headers map[string]string
	msg     *natsclient.AlarmMessage // 非标准/损坏消息为 nil（不参与维度统计）
	tag     string                   // 产生该消息的工况名
}

// buildFrame 按标准组装信封：PP-Message-ID 与负载 alarm_id 一致（消费方幂等键）。
func buildFrame(subject string, msg natsclient.AlarmMessage, tag string) (frame, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return frame{}, err
	}
	return frame{
		subject: subject,
		payload: payload,
		headers: map[string]string{
			natsclient.HeaderMessageID:        msg.AlarmID,
			natsclient.HeaderMessageType:      natsclient.MessageTypeAlarm,
			natsclient.HeaderMessageVersion:   natsclient.MessageVersion,
			natsclient.HeaderMessageTimestamp: strconv.FormatInt(msg.Timestamp, 10),
		},
		msg: &msg,
		tag: tag,
	}, nil
}

// deviceEvent 描述一个可触发的设备告警条目（定位键在工具运行期间稳定）。
type deviceEvent struct {
	deviceID   string
	deviceName string
	eventID    string // event_{device}_{event}，定位键优先字段
	eventKey   string
	eventName  string
	point      string
	pointName  string
	ruleType   string // equal | upper | lower
	threshold  float64
	level      int
	valueKind  string // float | bool | string
	boolOn     bool
	strOn      string
	strOff     string
}

// triggerValue 生成触发沿取值：upper 上越限、lower 下越限、equal 离线/开启等离散值。
func (e deviceEvent) triggerValue(rng *rand.Rand) any {
	switch e.valueKind {
	case "bool":
		return e.boolOn
	case "string":
		return e.strOn
	default:
		switch e.ruleType {
		case "upper":
			return round2(e.threshold * (1.05 + rng.Float64()*0.45))
		case "lower":
			return round2(e.threshold * (0.4 + rng.Float64()*0.45))
		default:
			return e.threshold
		}
	}
}

// recoverValue 生成恢复沿取值：回到正常区间，与触发沿同一定位键。
func (e deviceEvent) recoverValue(rng *rand.Rand) any {
	switch e.valueKind {
	case "bool":
		return !e.boolOn
	case "string":
		return e.strOff
	default:
		switch e.ruleType {
		case "upper":
			return round2(e.threshold * (0.75 + rng.Float64()*0.2))
		case "lower":
			return round2(e.threshold * (1.02 + rng.Float64()*0.15))
		default:
			return e.threshold
		}
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

var deviceNamePool = []string{
	"1号风机", "2号风机", "冷却水泵", "循环水泵", "1号逆变器",
	"2号逆变器", "干式变压器", "环网柜", "UPS主机", "直流屏",
}

// eventTemplates 设备告警条目模板：覆盖 upper/lower/equal 三种规则与各级别。
var eventTemplates = []struct {
	key, name, point, pointName, rule string
	threshold                         float64
	level                             int
	valueKind, strOn, strOff          string
	boolOn                            bool
}{
	{key: "temp_high", name: "温度过高", point: "temp", pointName: "绕组温度", rule: "upper", threshold: 85, level: 2, valueKind: "float"},
	{key: "vib_high", name: "振动异常", point: "vibration", pointName: "振动速度", rule: "upper", threshold: 7.2, level: 1, valueKind: "float"},
	{key: "comm_lost", name: "通讯故障", point: "signal", pointName: "通讯状态", rule: "equal", level: 3, valueKind: "string", strOn: "offline", strOff: "online"},
	{key: "hum_high", name: "湿度越限", point: "humidity", pointName: "环境湿度", rule: "upper", threshold: 75, level: 0, valueKind: "float"},
	{key: "volt_low", name: "电压偏低", point: "voltage", pointName: "母线电压", rule: "lower", threshold: 360, level: 2, valueKind: "float"},
	{key: "curr_high", name: "过流", point: "current", pointName: "输出电流", rule: "upper", threshold: 63, level: 3, valueKind: "float"},
	{key: "freq_high", name: "频率越限", point: "freq", pointName: "电网频率", rule: "upper", threshold: 50.5, level: 1, valueKind: "float"},
	{key: "door_open", name: "柜门开启", point: "door", pointName: "柜门状态", rule: "equal", threshold: 1, level: 0, valueKind: "bool", boolOn: true},
}

// systemCatalog 系统事件样例（标准 §6 对 system 事件码预留，此处仅为模拟样例）。
var systemCatalog = []struct {
	code, component, message string
	level                    int
}{
	{"platform.health.degraded", "supervisor", "平台健康检查降级", 2},
	{"platform.node.clock_skew", "ntp", "节点时钟偏差超限", 1},
	{"platform.license.expiring", "license", "平台许可即将到期", 1},
	{"platform.backup.failed", "backup", "配置备份失败", 2},
	{"platform.db.slow_query", "store", "数据库慢查询占比升高", 0},
}

var serviceKeyPool = []string{"switch", "set_power", "set_mode", "start", "stop", "reset"}

var cmdFailMessages = []string{"设备无响应", "指令被设备拒绝", "写寄存器失败", "控制会话已失效", "设备返回异常码"}

// pendingRecover 一条已排期的恢复沿。
type pendingRecover struct {
	event    deviceEvent
	scenario string
	dueAt    time.Time
}

// recentCapacity 重复消息工况的回放缓存容量。
const recentCapacity = 128

// sim 维持仿真的世界状态：设备事件目录、待恢复队列、聚合与风暴进度。
type sim struct {
	cfg       config
	rng       *rand.Rand
	events    []deviceEvent
	pending   []pendingRecover
	recent    []frame
	aggEvent  *deviceEvent
	aggActive bool
	stormSeq  int
}

func newSim(cfg config, rng *rand.Rand) *sim {
	s := &sim{cfg: cfg, rng: rng}
	for d := 0; d < cfg.devices; d++ {
		name := deviceNamePool[d%len(deviceNamePool)]
		if d >= len(deviceNamePool) {
			name = fmt.Sprintf("%s#%d", name, d/len(deviceNamePool)+1)
		}
		for i, t := range eventTemplates {
			s.events = append(s.events, deviceEvent{
				deviceID:   fmt.Sprintf("dev-%03d", d+1),
				deviceName: name,
				eventID:    fmt.Sprintf("event_%d_%d", d+1, i+1),
				eventKey:   t.key,
				eventName:  t.name,
				point:      t.point,
				pointName:  t.pointName,
				ruleType:   t.rule,
				threshold:  t.threshold,
				level:      t.level,
				valueKind:  t.valueKind,
				strOn:      t.strOn,
				strOff:     t.strOff,
				boolOn:     t.boolOn,
			})
		}
	}
	return s
}

func (s *sim) source() natsclient.AlarmSource {
	return natsclient.AlarmSource{Module: s.cfg.module, Instance: s.cfg.instance}
}

// sourceModule 构造指定 module 的来源（mismatch 工况用）。
func (s *sim) sourceModule(module string) natsclient.AlarmSource {
	return natsclient.AlarmSource{Module: module, Instance: s.cfg.instance}
}

func (s *sim) randomEvent() deviceEvent { return s.events[s.rng.IntN(len(s.events))] }

func (s *sim) isActive(e deviceEvent) bool {
	for _, p := range s.pending {
		if p.event.eventID == e.eventID {
			return true
		}
	}
	return false
}

// pickDormantEvent 挑选一个当前未活动的设备事件，尽量保证触发/恢复配对。
func (s *sim) pickDormantEvent() (deviceEvent, bool) {
	for try := 0; try < 8; try++ {
		e := s.randomEvent()
		if !s.isActive(e) {
			return e, true
		}
	}
	return deviceEvent{}, false
}

func (s *sim) scheduleRecover(e deviceEvent, scenario string, dueAt time.Time) {
	s.pending = append(s.pending, pendingRecover{event: e, scenario: scenario, dueAt: dueAt})
}

// recoverDeadline 触发到恢复的时长：hold × factor × [0.5, 2.0) 随机抖动。
func (s *sim) recoverDeadline(now time.Time, factor float64) time.Time {
	spread := 0.5 + s.rng.Float64()*1.5
	return now.Add(time.Duration(float64(s.cfg.hold) * factor * spread))
}

// drainRecovers 取出到期的恢复沿并生成消息（触发/恢复沿配对语义）。
func (s *sim) drainRecovers(now time.Time) []frame {
	var frames []frame
	remaining := s.pending[:0]
	for _, p := range s.pending {
		if p.dueAt.After(now) {
			remaining = append(remaining, p)
			continue
		}
		f, err := buildFrame(s.cfg.subject, s.recoverMessage(p.event, now), p.scenario)
		if err != nil {
			continue
		}
		frames = append(frames, f)
	}
	s.pending = remaining
	return frames
}

func (s *sim) pendingCount() int { return len(s.pending) }

// remember 缓存近期已发送的标准消息，供 duplicate 工况原样重放。
func (s *sim) remember(f frame) {
	if f.msg == nil {
		return
	}
	s.recent = append(s.recent, f)
	if len(s.recent) > recentCapacity {
		s.recent = s.recent[len(s.recent)-recentCapacity:]
	}
}

// step 单个发送节拍：先到期恢复，再依次执行各激活工况。
func (s *sim) step(now time.Time, active []scenario) []frame {
	frames := s.drainRecovers(now)
	for _, sc := range active {
		frames = append(frames, sc.step(s, now)...)
	}
	return frames
}

func (s *sim) deviceMessage(e deviceEvent, kind string, value any, count int, source natsclient.AlarmSource, now time.Time) natsclient.AlarmMessage {
	return natsclient.AlarmMessage{
		AlarmID:   uuid.NewString(),
		Source:    source,
		Category:  natsclient.CategoryDevice,
		Kind:      kind,
		Level:     e.level,
		Timestamp: now.UnixMilli(),
		Count:     count,
		Device: &natsclient.AlarmDevice{
			DeviceID:   e.deviceID,
			DeviceName: e.deviceName,
			EventID:    e.eventID,
			EventKey:   e.eventKey,
			EventName:  e.eventName,
			Point:      e.point,
			PointName:  e.pointName,
			Value:      value,
			Rule:       natsclient.AlarmRuleSpec{Type: e.ruleType, Threshold: e.threshold},
		},
	}
}

func (s *sim) recoverMessage(e deviceEvent, now time.Time) natsclient.AlarmMessage {
	return s.deviceMessage(e, natsclient.KindRecover, e.recoverValue(s.rng), 0, s.source(), now)
}

func (s *sim) triggerFrame(e deviceEvent, tag string, now time.Time) (frame, error) {
	msg := s.deviceMessage(e, natsclient.KindTrigger, e.triggerValue(s.rng), 0, s.source(), now)
	return buildFrame(s.cfg.subject, msg, tag)
}

// deviceTrigger 设备触发拍：偶发平静期；优先休眠事件并排期恢复，全部活动时重发触发沿走合并路径。
func (s *sim) deviceTrigger(tag string, now time.Time) []frame {
	if s.rng.Float64() < 0.15 {
		return nil // 平静拍：本节拍无告警
	}
	e, dormant := s.pickDormantEvent()
	if dormant {
		s.scheduleRecover(e, tag, s.recoverDeadline(now, 1))
	} else {
		e = s.randomEvent()
	}
	f, err := s.triggerFrame(e, tag, now)
	if err != nil {
		return nil
	}
	return []frame{f}
}

// softwareFrame 软件告警：标准 §6 登记的三个事件码按权重出现。
func (s *sim) softwareFrame(tag string, now time.Time) (frame, error) {
	device := s.randomEvent()
	var msg natsclient.AlarmMessage
	switch r := s.rng.Float64(); {
	case r < 0.5: // thingsmodel.cmd.result：控制失败终态（成功静默）
		serviceKey := serviceKeyPool[s.rng.IntN(len(serviceKeyPool))]
		message := cmdFailMessages[s.rng.IntN(len(cmdFailMessages))]
		msg = natsclient.AlarmMessage{
			AlarmID:   uuid.NewString(),
			Source:    s.source(),
			Category:  natsclient.CategorySoftware,
			Kind:      natsclient.KindEvent,
			Level:     2,
			Timestamp: now.UnixMilli(),
			Software: &natsclient.AlarmSoftware{
				Code: "thingsmodel.cmd.result", Component: "runtime", Message: message,
				Context: map[string]any{
					"request_id":  uuid.NewString(),
					"device_id":   device.deviceID,
					"service_key": serviceKey,
					"service_id":  fmt.Sprintf("svc_%03d", s.rng.IntN(200)+1),
					"value":       round2(s.rng.Float64() * 100),
					"message":     message,
				},
			},
		}
	case r < 0.8: // thingsmodel.cmd.frequent_failure：高频失败聚合，携带 count
		msg = natsclient.AlarmMessage{
			AlarmID:   uuid.NewString(),
			Source:    s.source(),
			Category:  natsclient.CategorySoftware,
			Kind:      natsclient.KindEvent,
			Level:     2,
			Timestamp: now.UnixMilli(),
			Count:     3 + s.rng.IntN(48),
			Software: &natsclient.AlarmSoftware{
				Code:      "thingsmodel.cmd.frequent_failure",
				Component: "runtime",
				Message:   "窗口期内控制失败次数超阈值",
				Context: map[string]any{
					"device_id":   device.deviceID,
					"service_key": serviceKeyPool[s.rng.IntN(len(serviceKeyPool))],
					"window_ms":   60000,
				},
			},
		}
	default: // thingsmodel.model.changed：模型变更提示
		actions := []string{"created", "updated", "deleted", "disabled"}
		msg = natsclient.AlarmMessage{
			AlarmID:   uuid.NewString(),
			Source:    s.source(),
			Category:  natsclient.CategorySoftware,
			Kind:      natsclient.KindEvent,
			Level:     0,
			Timestamp: now.UnixMilli(),
			Software: &natsclient.AlarmSoftware{
				Code: "thingsmodel.model.changed", Component: "runtime", Message: "模型定义已变更",
				Context: map[string]any{
					"device_id": device.deviceID,
					"action":    actions[s.rng.IntN(len(actions))],
				},
			},
		}
	}
	return buildFrame(s.cfg.subject, msg, tag)
}

func (s *sim) systemFrame(tag string, now time.Time) (frame, error) {
	c := systemCatalog[s.rng.IntN(len(systemCatalog))]
	msg := natsclient.AlarmMessage{
		AlarmID:   uuid.NewString(),
		Source:    s.source(),
		Category:  natsclient.CategorySystem,
		Kind:      natsclient.KindEvent,
		Level:     c.level,
		Timestamp: now.UnixMilli(),
		System: &natsclient.AlarmSystem{
			Code: c.code, Component: c.component, Message: c.message,
			Context: map[string]any{
				"node":   fmt.Sprintf("node-%02d", s.rng.IntN(8)+1),
				"metric": round2(s.rng.Float64() * 100),
			},
		},
	}
	return buildFrame(s.cfg.subject, msg, tag)
}

// syntheticStormEvent 构造一次性风暴事件（全新定位键，延后恢复避免只增不减）。
func (s *sim) syntheticStormEvent(n int) deviceEvent {
	base := s.randomEvent()
	e := base
	e.eventKey = fmt.Sprintf("%s_storm_%d", base.eventKey, n)
	e.eventID = fmt.Sprintf("event_storm_%d", n)
	e.deviceID = fmt.Sprintf("dev-storm-%03d", s.rng.IntN(s.cfg.devices*3)+1)
	e.deviceName = "告警风暴模拟装置"
	return e
}

// stormBurst 突发一轮触发沿：少量全新事件 + 大量既有定位键（覆盖消费方合并路径）。
func stormBurst(s *sim, now time.Time, burst int, tag string) []frame {
	frames := make([]frame, 0, burst)
	for i := 0; i < burst; i++ {
		var e deviceEvent
		if s.rng.Float64() < 0.1 {
			s.stormSeq++
			e = s.syntheticStormEvent(s.stormSeq)
			s.scheduleRecover(e, tag, s.recoverDeadline(now, 3))
		} else {
			e = s.randomEvent()
		}
		f, err := s.triggerFrame(e, tag, now)
		if err != nil {
			continue
		}
		frames = append(frames, f)
	}
	return frames
}

// ---- 各工况生成函数 ----

// normalStep 稳态综合工况：设备为主、软件系统夹杂、含平静期。
func normalStep(s *sim, now time.Time) []frame {
	switch r := s.rng.Float64(); {
	case r < 0.7:
		return s.deviceTrigger("normal", now)
	case r < 0.9:
		f, err := s.softwareFrame("normal", now)
		if err != nil {
			return nil
		}
		return []frame{f}
	default:
		f, err := s.systemFrame("normal", now)
		if err != nil {
			return nil
		}
		return []frame{f}
	}
}

func deviceStep(s *sim, now time.Time) []frame {
	return s.deviceTrigger("device", now)
}

func softwareStep(s *sim, now time.Time) []frame {
	f, err := s.softwareFrame("software", now)
	if err != nil {
		return nil
	}
	return []frame{f}
}

func systemStep(s *sim, now time.Time) []frame {
	f, err := s.systemFrame("system", now)
	if err != nil {
		return nil
	}
	return []frame{f}
}

func stormStep(s *sim, now time.Time) []frame {
	return stormBurst(s, now, s.cfg.burst, "storm")
}

// aggregateStep 节流聚合工况：同定位键持续触发携带 count，偶发恢复闭环后再重新聚合。
func aggregateStep(s *sim, now time.Time) []frame {
	if s.aggEvent == nil {
		e := s.randomEvent()
		s.aggEvent = &e
		s.aggActive = false
	}
	e := *s.aggEvent
	if !s.aggActive { // 新一轮聚合的首条（count=1）
		s.aggActive = true
		f, err := buildFrame(s.cfg.subject, s.deviceMessage(e, natsclient.KindTrigger, e.triggerValue(s.rng), 1, s.source(), now), "aggregate")
		if err != nil {
			return nil
		}
		return []frame{f}
	}
	if s.rng.Float64() < 0.08 { // 偶发恢复，结束本轮聚合
		s.aggActive = false
		f, err := buildFrame(s.cfg.subject, s.recoverMessage(e, now), "aggregate")
		if err != nil {
			return nil
		}
		return []frame{f}
	}
	f, err := buildFrame(s.cfg.subject, s.deviceMessage(e, natsclient.KindTrigger, e.triggerValue(s.rng), 2+s.rng.IntN(9), s.source(), now), "aggregate")
	if err != nil {
		return nil
	}
	return []frame{f}
}

// duplicateStep 重复消息工况：原样重放缓存中的已发送消息（同 alarm_id，验证幂等去重）。
func duplicateStep(s *sim, now time.Time) []frame {
	if len(s.recent) == 0 {
		f, err := s.triggerFrame(s.randomEvent(), "duplicate", now)
		if err != nil {
			return nil
		}
		return []frame{f}
	}
	base := s.recent[s.rng.IntN(len(s.recent))]
	frames := make([]frame, 0, 3)
	for i := 0; i < 1+s.rng.IntN(2); i++ {
		dup := base
		dup.tag = "duplicate"
		frames = append(frames, dup)
	}
	return frames
}

// orphanStep 孤儿恢复工况：无匹配触发沿的 recover（验证消费方容错）。
func orphanStep(s *sim, now time.Time) []frame {
	n := s.rng.IntN(999) + 1
	e := deviceEvent{
		deviceID:   fmt.Sprintf("dev-ghost-%03d", n),
		deviceName: "幽灵设备(未触发)",
		eventID:    fmt.Sprintf("event_ghost_%d", n),
		eventKey:   "ghost_alarm",
		eventName:  "幽灵告警",
		point:      "status",
		pointName:  "状态",
		ruleType:   "equal",
		level:      1,
		valueKind:  "string",
		strOn:      "offline",
		strOff:     "online",
	}
	f, err := buildFrame(s.cfg.subject, s.recoverMessage(e, now), "orphan-recover")
	if err != nil {
		return nil
	}
	return []frame{f}
}

// mismatchStep 主题与 source.module 不一致工况（验证消费方以负载为准并记录告警）。
func mismatchStep(s *sim, now time.Time) []frame {
	candidates := []string{"gateway", "simulator", "edge-node"}
	index := s.rng.IntN(len(candidates))
	if candidates[index] == s.cfg.module {
		index = (index + 1) % len(candidates)
	}
	msg := s.deviceMessage(s.randomEvent(), natsclient.KindTrigger, nil, 0, s.sourceModule(candidates[index]), now)
	if msg.Device.Value == nil {
		msg.Device.Value = msg.Device.Rule.Threshold
	}
	f, err := buildFrame(s.cfg.subject, msg, "mismatch")
	if err != nil {
		return nil
	}
	return []frame{f}
}

// invalidStep 非法消息工况：信封缺字段/错类型/坏时间戳、负载坏 JSON、未知枚举、缺明细、级别越界。
func invalidStep(s *sim, now time.Time) []frame {
	e := s.randomEvent()
	msg := s.deviceMessage(e, natsclient.KindTrigger, e.triggerValue(s.rng), 0, s.source(), now)
	variant := s.rng.IntN(8)
	// 负载级缺陷（信封正常，负载违约）
	switch variant {
	case 4:
		msg.Category = "network"
	case 5:
		msg.Kind = "escalate"
	case 6:
		msg.Device = nil
	case 7:
		msg.Level = 9
	}
	base, err := buildFrame(s.cfg.subject, msg, "invalid")
	if err != nil {
		return nil
	}
	// 信封级缺陷（负载正常，信封违约）
	switch variant {
	case 0:
		delete(base.headers, natsclient.HeaderMessageID)
	case 1:
		base.headers[natsclient.HeaderMessageType] = "telemetry"
	case 2:
		base.headers[natsclient.HeaderMessageTimestamp] = "yesterday"
	case 3:
		base.payload = []byte(`{"alarm_id": "oops"`)
		base.msg = nil
	}
	return []frame{base}
}

// mixedStormStep 混合工况下的缩小版风暴（避免长期混合运行时压力失控）。
func mixedStormStep(s *sim, now time.Time) []frame {
	return stormBurst(s, now, min(10, s.cfg.burst), "mixed")
}

// mixedPool 全工况加权混合的权重表（直接引用生成函数，避免包级初始化循环）。
var mixedPool = []struct {
	step   func(s *sim, now time.Time) []frame
	weight int
}{
	{deviceStep, 25}, {softwareStep, 15}, {systemStep, 10}, {mixedStormStep, 8},
	{aggregateStep, 12}, {duplicateStep, 12}, {orphanStep, 8}, {invalidStep, 6}, {mismatchStep, 4},
}

// mixedStep 全工况加权混合：每拍随机挑一个子工况执行（风暴降规模）。
func mixedStep(s *sim, now time.Time) []frame {
	total := 0
	for _, p := range mixedPool {
		total += p.weight
	}
	pick := s.rng.IntN(total)
	for _, p := range mixedPool {
		if pick < p.weight {
			return p.step(s, now)
		}
		pick -= p.weight
	}
	return nil
}

// scenario 一个可激活的仿真工况。
type scenario struct {
	name string
	desc string
	step func(s *sim, now time.Time) []frame
}

var scenarios = []scenario{
	{name: "normal", desc: "稳态综合工况：设备触发/恢复为主，夹杂软件与系统事件，含平静期（默认）", step: normalStep},
	{name: "device", desc: "设备告警工况：trigger→保持→recover 配对，覆盖 upper/lower/equal 规则", step: deviceStep},
	{name: "software", desc: "软件告警工况：标准登记事件码 cmd.result / cmd.frequent_failure / model.changed", step: softwareStep},
	{name: "system", desc: "系统事件工况：平台级事件（事件码标准预留，此处为模拟样例）", step: systemStep},
	{name: "storm", desc: "告警风暴工况：每轮突发大量触发沿（-burst 控制规模）", step: stormStep},
	{name: "aggregate", desc: "节流聚合工况：同定位键重复触发携带 count，偶发恢复闭环", step: aggregateStep},
	{name: "duplicate", desc: "重复消息工况：原样重发已发送消息（同 alarm_id，验证幂等去重）", step: duplicateStep},
	{name: "orphan-recover", desc: "孤儿恢复工况：发送无匹配触发的 recover（验证消费方容错）", step: orphanStep},
	{name: "invalid", desc: "非法消息工况：缺信封头/错类型/坏时间戳/坏负载/未知枚举/缺明细/级别越界", step: invalidStep},
	{name: "mismatch", desc: "主题与 source.module 不一致工况（验证消费方以负载为准）", step: mismatchStep},
	{name: "mixed", desc: "全工况加权混合：上述所有工况按权重轮流出现", step: mixedStep},
}

// findScenario 按名称查找工况；运行时查找以避免包级初始化循环（scenarios ↔ mixedStep）。
func findScenario(name string) (scenario, bool) {
	for _, sc := range scenarios {
		if sc.name == name {
			return sc, true
		}
	}
	return scenario{}, false
}

// describe 生成单条消息的人读描述（-v / -dry-run 输出）。
func describe(f frame) string {
	if f.msg == nil {
		return fmt.Sprintf("%s <非标准负载 %d 字节>", f.subject, len(f.payload))
	}
	m := f.msg
	detail := "<无明细>"
	switch {
	case m.Device != nil:
		detail = fmt.Sprintf("%s %s=%v（%s 阈值 %v）", m.Device.DeviceName, m.Device.PointName, m.Device.Value, m.Device.Rule.Type, m.Device.Rule.Threshold)
	case m.Software != nil:
		detail = m.Software.Code
	case m.System != nil:
		detail = m.System.Code
	}
	if m.Count > 1 {
		detail += fmt.Sprintf(" count=%d", m.Count)
	}
	return fmt.Sprintf("%s %s/%s L%d %s %s", f.subject, m.Category, m.Kind, m.Level, shortID(m.AlarmID), detail)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
