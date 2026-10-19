// message.go - 消息契约：信封与标准告警消息/保护动作载体定义。
// 告警输入契约遵循 docs/powerpulse-alarm-standard.md（平台通用告警/事件消息标准）。
package natsclient

import (
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// 信封 Header 契约（与网关侧一致）。
const (
	HeaderMessageID        = "PP-Message-ID"
	HeaderMessageType      = "PP-Message-Type"
	HeaderMessageVersion   = "PP-Message-Version"
	HeaderMessageTimestamp = "PP-Message-Timestamp"
	MessageVersion         = "v1.0"
)

// 信封消息类型。
const (
	MessageTypeAlarm   = "alarm"   // 告警事件输入（标准）
	MessageTypeQuery   = "query"   // 物模型定义查询（REQ/REP）
	MessageTypeControl = "control" // 服务控制下发（REQ/REP）
)

// 物模型软件事件码（thingsmodel 契约第 4/6 节登记）。
const (
	EventCodeCmdResult    = "thingsmodel.cmd.result"           // 服务控制失败终态（成功静默）
	EventCodeFrequentFail = "thingsmodel.cmd.frequent_failure" // 高频控制失败聚合
	EventCodeModelChanged = "thingsmodel.model.changed"        // 模型定义变更提示（墓碑信号）
)

// 告警类别（标准 category）。
const (
	CategoryDevice   = "device"
	CategorySoftware = "software"
	CategorySystem   = "system"
)

// 告警生命周期（标准 kind）。
const (
	KindTrigger = "trigger"
	KindRecover = "recover"
	KindEvent   = "event"
)

// Envelope 是 NATS 消息信封：元数据映射到 msg.Header，业务负载映射到 msg.Data。
type Envelope struct {
	ID        string
	Type      string
	Version   string
	Timestamp time.Time
	Payload   []byte
}

func NewEnvelope(messageType string, payload []byte) Envelope {
	return Envelope{ID: uuid.NewString(), Type: messageType, Version: MessageVersion, Timestamp: time.Now().UTC(), Payload: payload}
}

func envelopeFromMsg(msg *nats.Msg) (Envelope, error) {
	if msg.Header == nil {
		return Envelope{}, fmt.Errorf("缺少消息头")
	}
	timestamp, err := strconv.ParseInt(msg.Header.Get(HeaderMessageTimestamp), 10, 64)
	if err != nil {
		return Envelope{}, fmt.Errorf("无效消息时间戳: %w", err)
	}
	parsed := Envelope{
		ID: msg.Header.Get(HeaderMessageID), Type: msg.Header.Get(HeaderMessageType),
		Version: msg.Header.Get(HeaderMessageVersion), Timestamp: time.UnixMilli(timestamp), Payload: msg.Data,
	}
	if parsed.ID == "" || parsed.Type == "" || parsed.Version == "" {
		return Envelope{}, fmt.Errorf("消息头缺少必填字段")
	}
	return parsed, nil
}

func (e Envelope) toMsg(subject string) *nats.Msg {
	message := nats.NewMsg(subject)
	message.Header.Set(HeaderMessageID, e.ID)
	message.Header.Set(HeaderMessageType, e.Type)
	message.Header.Set(HeaderMessageVersion, e.Version)
	message.Header.Set(HeaderMessageTimestamp, strconv.FormatInt(e.Timestamp.UnixMilli(), 10))
	message.Data = e.Payload
	return message
}

// AlarmMessage 平台通用告警/事件消息（标准 §4）。
type AlarmMessage struct {
	AlarmID   string      `json:"alarm_id"`        // UUIDv4，消费方幂等键
	Source    AlarmSource `json:"source"`          // 发布方来源
	Category  string      `json:"category"`        // device | software | system
	Kind      string      `json:"kind"`            // trigger | recover | event
	Level     int         `json:"level"`           // 0 提示 / 1 一般 / 2 严重 / 3 紧急
	Timestamp int64       `json:"timestamp"`       // Unix 毫秒
	Count     int         `json:"count,omitempty"` // 节流聚合计数（聚合消息 >1）

	Device   *AlarmDevice   `json:"device,omitempty"`   // category=device 时必填
	Software *AlarmSoftware `json:"software,omitempty"` // category=software 时必填
	System   *AlarmSystem   `json:"system,omitempty"`   // category=system 时必填
}

// AlarmSource 发布方来源（标准）。
type AlarmSource struct {
	Module   string `json:"module"`   // 模块名，与主题二级命名空间一致
	Instance string `json:"instance"` // 模块内实例标识；单例模块与 module 相同
}

// AlarmDevice 设备告警明细（标准）。
type AlarmDevice struct {
	DeviceID   string        `json:"device_id"` // 设备实例 ID
	DeviceName string        `json:"device_name"`
	EventID    string        `json:"event_id,omitempty"` // 告警条目 ID（发布方模块内稳定）
	EventKey   string        `json:"event_key"`          // 告警规则 key
	EventName  string        `json:"event_name"`
	Point      string        `json:"point"`      // 监测属性 key
	PointName  string        `json:"point_name"` // 监测属性名称
	Value      any           `json:"value"`      // 触发值
	Rule       AlarmRuleSpec `json:"rule"`       // 判定依据
}

// AlarmRuleSpec 告警判定规则（标准 AlarmRule，更名避免与存储模型混淆）。
type AlarmRuleSpec struct {
	Type      string  `json:"type"`      // equal | upper | lower
	Threshold float64 `json:"threshold"` // 阈值
	Time      int     `json:"time"`      // 持续时间（毫秒），0 表示立即触发
}

// AlarmSoftware 软件告警明细（标准）。
type AlarmSoftware struct {
	Code      string         `json:"code"`              // 标准化事件码
	Component string         `json:"component"`         // 模块内组件
	Message   string         `json:"message,omitempty"` // 人读信息
	Context   map[string]any `json:"context,omitempty"` // 按事件码约定的上下文
}

// AlarmSystem 系统告警明细（标准）。
type AlarmSystem struct {
	Code      string         `json:"code"`
	Component string         `json:"component"`
	Message   string         `json:"message,omitempty"`
	Context   map[string]any `json:"context,omitempty"`
}

// DevicesQueryResponse 设备清单查询响应（契约 §5.1，GET powerpulse.thingsmodel.query.devices）。
type DevicesQueryResponse struct {
	OK      bool            `json:"ok"`
	Message string          `json:"message,omitempty"`
	Devices []DeviceSummary `json:"devices"`
}

// DeviceSummary 设备实例概要。
type DeviceSummary struct {
	DeviceID        string `json:"device_id"`
	DeviceName      string `json:"device_name"`
	TemplateCode    string `json:"template_code"`
	TemplateVersion string `json:"template_version,omitempty"`
	Description     string `json:"description,omitempty"`
	Enabled         bool   `json:"enabled"`
	DataStatus      string `json:"data_status,omitempty"` // available / degraded / unavailable
	PropertyCount   int    `json:"property_count"`
	MethodCount     int    `json:"method_count"`
	EventCount      int    `json:"event_count"`
}

// EventsQueryRequest 告警定义查询请求（契约 §5.4）。
type EventsQueryRequest struct {
	DeviceID string `json:"device_id"`
}

// EventsQueryResponse 告警定义查询响应。
type EventsQueryResponse struct {
	OK       bool              `json:"ok"`
	Message  string            `json:"message,omitempty"`
	DeviceID string            `json:"device_id"`
	Events   []EventDefinition `json:"events"`
}

// EventDefinition 单条告警（规则）定义：引擎预知"未来可能产生哪些告警"的依据。
type EventDefinition struct {
	ID                  string  `json:"id"`  // 实例条目 ID（event_N）
	Key                 string  `json:"key"` // 规则语义标识，跨配置版本稳定（规则匹配主键）
	Name                string  `json:"name"`
	Description         string  `json:"description,omitempty"`
	Level               int     `json:"level"` // 0 提示 / 1 一般 / 2 严重 / 3 紧急
	Type                string  `json:"type"`  // equal | upper | lower
	Threshold           float64 `json:"threshold"`
	Time                int     `json:"time"`             // 持续时间（毫秒）
	BindingProperty     string  `json:"binding_property"` // 监测属性 key（空=未绑定）
	BindingPropertyName string  `json:"binding_property_name,omitempty"`
}

// MethodsQueryRequest 服务定义查询请求（契约 §5.3）。
type MethodsQueryRequest struct {
	DeviceID string `json:"device_id"`
}

// MethodsQueryResponse 服务定义查询响应。
type MethodsQueryResponse struct {
	OK       bool               `json:"ok"`
	Message  string             `json:"message,omitempty"`
	DeviceID string             `json:"device_id"`
	Methods  []MethodDefinition `json:"methods"`
}

// MethodDefinition 单个服务（控制能力）定义：构造 .cmd 请求前必须先查。
type MethodDefinition struct {
	ID           string       `json:"id"`
	Key          string       `json:"key"` // service_key（控制调用标识）
	Name         string       `json:"name"`
	Type         string       `json:"type"`  // status | number
	Bound        bool         `json:"bound"` // 是否已绑定目标点位（false 不可控制）
	TargetMode   string       `json:"target_mode,omitempty"`
	Validation   *Validation  `json:"validation,omitempty"`   // type=number：工程值范围
	Descriptions []StatusDesc `json:"descriptions,omitempty"` // type=status：合法工程值枚举
}

// Validation 数值型服务的工程值范围。
type Validation struct {
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
}

// StatusDesc 状态型服务的合法工程值枚举项。
type StatusDesc struct {
	Key   string `json:"key,omitempty"`
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// ControlRequest 服务控制下发请求（契约 §6，powerpulse.thingsmodel.cmd）。
type ControlRequest struct {
	// RequestID 为调用方预生成的 PP-Message-ID（不随负载上线，仅用于请求头）；
	// 引擎需先以该 ID 落动作记录再发布，才能接住 REQ/REP 窗口内到达的 cmd.result。
	RequestID  string  `json:"-"`
	DeviceID   string  `json:"device_id"`   // 目标设备实例 ID
	ServiceKey string  `json:"service_key"` // 服务标识符（来自 query.methods）
	Value      float64 `json:"value"`       // 工程值（number 为数值；status 为状态工程值）
}

// ControlAck 服务控制受理应答（REQ/REP 同步返回，只表达受理不表达终态）。
type ControlAck struct {
	RequestID  string `json:"request_id"` // 原请求 PP-Message-ID
	DeviceID   string `json:"device_id"`
	ServiceKey string `json:"service_key"`
	Status     string `json:"status"` // accepted / failure
	Message    string `json:"message,omitempty"`
}
