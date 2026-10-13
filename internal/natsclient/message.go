// message.go - 消息契约：信封与标准告警消息/保护动作载体定义。
// 告警输入契约遵循 docs/powerpulse-alarm-standard.md（平台通用告警/事件消息标准）。
package natsclient

import (
	"encoding/json"
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
	MessageTypeAlarm  = "alarm"  // 告警事件输入（标准）
	MessageTypeAction = "action" // 保护动作输出
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

// ProtectionAction 保护动作（北向输出）。
type ProtectionAction struct {
	ActionID       string  `json:"action_id"`
	RuleID         string  `json:"rule_id"`
	RuleName       string  `json:"rule_name"`
	AlarmID        string  `json:"alarm_id"`
	Type           string  `json:"type"`
	TargetDeviceID string  `json:"target_device_id"`
	Command        string  `json:"command"`
	Value          float64 `json:"value"`
	Timestamp      int64   `json:"timestamp"`
}

// MarshalProtectionAction 序列化保护动作（供北向客户端复用）。
func MarshalProtectionAction(action ProtectionAction) ([]byte, error) {
	return json.Marshal(action)
}
