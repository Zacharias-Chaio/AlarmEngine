package store

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// 告警级别（与平台告警标准 level 一致：0 提示 / 1 一般 / 2 严重 / 3 紧急）由 API 层与前端常量维护，
// 存储层不再重复定义（避免三处副本不同步）。

// 告警状态（自动，告警消息驱动）：触发沿已到持续中 / 已恢复。
const (
	AlarmStateActive  = "active"  // 触发沿已到，告警持续中
	AlarmStateCleared = "cleared" // 已恢复（自动恢复沿到达，未人工结案）
)

// 告警类型（与平台告警标准 category 一致）。
// 注意：系统告警不由本模块检测产生，由其他模块经标准接口发布。
const (
	AlarmTypeDevice   = "device"   // 设备告警
	AlarmTypeSystem   = "system"   // 系统告警（外部模块发布）
	AlarmTypeSoftware = "software" // 软件告警（外部模块发布）
)

// 告警生命周期种类（与平台告警标准 kind 一致）。
const (
	AlarmKindTrigger = "trigger" // 状态型告警（trigger/recover 配对）
	AlarmKindEvent   = "event"   // 一次性事件（无恢复沿）
)

// Alarm 告警记录。
// 状态分两条正交的线：State+Orphan（自动，告警消息驱动）+ Acknowledged/Handled（人为操作）。
// 展示状态三态（前端派生）：orphan → 孤儿（触发源失联）；cleared → 已恢复；active → 活动中（含未确认/已确认）。
type Alarm struct {
	ID             string     `gorm:"primaryKey;size:128" json:"id"`
	Name           string     `gorm:"not null" json:"name"`              // 告警名称
	Type           string     `gorm:"index" json:"type"`                 // 告警类型：device|software|system
	Kind           string     `gorm:"index;default:trigger" json:"kind"` // trigger | event
	Source         string     `gorm:"index" json:"source"`               // 展示来源：系统/软件名/设备名
	Module         string     `gorm:"index" json:"module"`               // source.module（标准来源模块）
	Instance       string     `json:"instance"`                          // source.instance（标准来源实例）
	Point          string     `gorm:"index" json:"point"`                // 告警点位
	DeviceID       string     `gorm:"index" json:"deviceId"`
	DeviceName     string     `json:"deviceName"`
	SoftwareID     string     `gorm:"index" json:"softwareId"`                                                                                                   // 软件实例标识（source.instance）
	EventID        string     `gorm:"index" json:"eventId"`                                                                                                      // 设备告警条目 ID（定位键，优先）
	EventKey       string     `gorm:"index" json:"eventKey"`                                                                                                     // 设备告警规则 key（定位键）
	Code           string     `gorm:"index" json:"code"`                                                                                                         // 软件/系统事件码（定位键）
	Component      string     `json:"component"`                                                                                                                 // 软件/系统组件（定位键）
	Severity       int        `gorm:"index" json:"severity"`                                                                                                     // 告警等级
	State          string     `gorm:"index" json:"state"`                                                                                                        // active | cleared（自动）
	Orphan         bool       `gorm:"index;default:false" json:"orphan"`                                                                                         // 孤儿标志：触发源失联（配置变更/设备离线等），叠加于 active
	Acknowledged   bool       `gorm:"index;default:false" json:"acknowledged"`                                                                                   // 是否人工确认（进入处理阶段）
	AcknowledgedAt *time.Time `json:"acknowledgedAt"`                                                                                                            // 确认时间（MTTA 统计依据）
	Handled        bool       `gorm:"index;default:false;index:idx_alarm_handled_occurred,priority:1;index:idx_alarm_handled_cleared,priority:1" json:"handled"` // 是否人工处理（结案）
	Message        string     `json:"message"`
	Value          float64    `json:"value"`                             // 当前值
	ValueInvalid   bool       `gorm:"default:false" json:"valueInvalid"` // 触发值无法解析（true 时前端显示"—"；零值即正常，避开 GORM 默认值覆盖陷阱）
	Threshold      float64    `json:"threshold"`                         // 告警阈值
	Count          int        `gorm:"default:1" json:"count"`            // 节流聚合计数（标准 count 累加）
	DurationMs     int64      `gorm:"index" json:"durationMs"`           // 持续时间(ms)
	OccurredAt     time.Time  `gorm:"index;index:idx_alarm_handled_occurred,priority:2" json:"occurredAt"`
	RecoveredAt    *time.Time `json:"recoveredAt"`                                                 // 自动恢复时间
	ClearedAt      *time.Time `gorm:"index:idx_alarm_handled_cleared,priority:2" json:"clearedAt"` // 人工处理时间
	CreatedAt      time.Time  `json:"-"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// 动作记录状态（.cmd 两段式闭环）。
const (
	ActionPending  = "pending"  // 已入队待受理（REQ 发出）
	ActionAccepted = "accepted" // REP 受理成功，等待终态
	ActionFailure  = "failure"  // 同步 failure / REQ 失败 / thingsmodel.cmd.result 失败终态
	ActionSuccess  = "success"  // 推定成功（受理后 35s 内未收到失败终态；契约成功静默）
	ActionUnknown  = "unknown"  // 终态未知：引擎重启前未推进终态（启动清扫器写入）
)

// ActionRecord 保护动作下发记录：request_id 与 thingsmodel.cmd.result 闭环对账。
type ActionRecord struct {
	ID           string     `gorm:"primaryKey;size:128" json:"id"`
	RequestID    string     `gorm:"index;size:128" json:"requestId"` // .cmd 请求 PP-Message-ID（闭环键）
	RuleID       string     `gorm:"index" json:"ruleId"`             // 命中的规则
	RuleName     string     `json:"ruleName"`
	AlarmID      string     `gorm:"index" json:"alarmId"`  // 触发源告警 alarm_id
	DeviceID     string     `gorm:"index" json:"deviceId"` // 目标设备
	ServiceKey   string     `json:"serviceKey"`
	Value        float64    `json:"value"`
	Status       string     `gorm:"index" json:"status"` // pending | accepted | failure | success
	Message      string     `json:"message"`             // 失败原因 / 推定说明
	DispatchedAt time.Time  `gorm:"index" json:"dispatchedAt"`
	FinalizedAt  *time.Time `json:"finalizedAt"`
	CreatedAt    time.Time  `json:"-"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// 规则监测源聚合模式。
const (
	RuleMatchAny = "anyone" // 任一源成立 → 规则成立
	RuleMatchAll = "all"    // 全部源成立 → 规则成立
)

// RuleMonitor 规则监测源（多源，按 MatchMode 聚合）。
//
//	kind=level：级别计数——active 告警数量 > 0 即成立（不区分类型，
//	用于"出现严重告警即统一动作"）；
//	kind=event ：监测类型 + 设备/事件定点——沿命中即成立（v1 仅设备类型，软件/系统预留）。
type RuleMonitor struct {
	Kind      string `json:"kind"`                // level | event
	Type      string `json:"type,omitempty"`      // kind=event：监测类型 device | software | system（v1 仅 device，空按 device 兜底）
	Levels    []int  `json:"levels,omitempty"`    // kind=level：级别集合 0-3
	DeviceID  string `json:"deviceId,omitempty"`  // kind=event：触发设备
	EventKey  string `json:"eventKey,omitempty"`  // kind=event：告警事件 key
	EventName string `json:"eventName,omitempty"` // 冗余展示名
}

// RuleControl 规则控制源（多源，规则成立时全部执行）。
//
//	kind=device：经 powerpulse.thingsmodel.cmd 下发服务控制（v1）；
//	kind=sms/email：通知渠道（预留，暂不支持）。
//
// 注意：Value 不能带 omitempty——0 是合法工程值（归零/停机），省略会破坏回读链路。
type RuleControl struct {
	Kind       string  `json:"kind"` // device | sms | email（预留）
	DeviceID   string  `json:"deviceId,omitempty"`
	ServiceKey string  `json:"serviceKey,omitempty"`
	Value      float64 `json:"value"`
}

// RuleMonitors / RuleControls 以 JSON 文本落库（设计指南：JSON Blob 灵活 schema）。
// 实现 Valuer/Scanner，保证 Create/Save/Updates(map) 全路径一致序列化。
type RuleMonitors []RuleMonitor
type RuleControls []RuleControl

func (r RuleMonitors) Value() (driver.Value, error) {
	if r == nil {
		return "[]", nil
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("序列化监测源失败: %w", err)
	}
	return string(raw), nil
}

func (r *RuleMonitors) Scan(src any) error {
	return scanJSONList(src, (*[]RuleMonitor)(r))
}

func (r RuleControls) Value() (driver.Value, error) {
	if r == nil {
		return "[]", nil
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("序列化控制源失败: %w", err)
	}
	return string(raw), nil
}

func (r *RuleControls) Scan(src any) error {
	return scanJSONList(src, (*[]RuleControl)(r))
}

func scanJSONList(src any, dst any) error {
	switch value := src.(type) {
	case nil:
		return nil
	case []byte:
		if len(value) == 0 {
			return nil
		}
		return tolerantUnmarshal(value, dst)
	case string:
		if value == "" {
			return nil
		}
		return tolerantUnmarshal([]byte(value), dst)
	default:
		return fmt.Errorf("无法反序列化规则源列表: %T", src)
	}
}

// tolerantUnmarshal 解析规则 JSON：单行损坏时降级为空列表 + Warn，而不是让整张规则表
// 加载失败（那会导致所有保护动作静默停摆）。该行规则因无监测源/控制源而不参与评估。
func tolerantUnmarshal(raw []byte, dst any) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		slog.Warn("规则 JSON 解析失败，按空列表降级处理", "error", err)
		return nil
	}
	return nil
}

// AlarmRule 告警引擎保护动作规则：多监测源（anyone/all 聚合）→ 多控制源（成立时全部执行）。
// 规则判定本身已在物模型完成（阈值/持续时间见事件定义），引擎不做属性条件计算。
type AlarmRule struct {
	ID          string `gorm:"primaryKey;size:128" json:"id"`
	Name        string `gorm:"not null" json:"name"`
	Description string `json:"description"`
	Version     int    `json:"version"` // 乐观锁版本号：保存时条件校验递增，防止并发覆盖
	Enabled     bool   `gorm:"default:true" json:"enabled"`
	MatchMode   string `gorm:"default:anyone" json:"matchMode"` // 监测源聚合：anyone（任一成立）| all（全部成立）

	// 核心模型：多监测源 + 多控制源（JSON blob）。
	Monitors   RuleMonitors `gorm:"type:text" json:"monitors"`
	Controls   RuleControls `gorm:"type:text" json:"controls"`
	CooldownMs int          `json:"cooldownMs"` // 规则级重复下发最小间隔（0=不限；默认值由前端/API 提供）

	// ↓ 兼容列（多源模型前的单源字段；启动迁移后只读，保存走 Monitors/Controls）
	Severity       int     `json:"severity"`
	DeviceID       string  `gorm:"index" json:"deviceId"`
	EventKey       string  `gorm:"index" json:"eventKey"`
	EventName      string  `json:"eventName"`
	ActionType     string  `gorm:"default:protection" json:"actionType"`
	TargetDeviceID string  `gorm:"index" json:"targetDeviceId"`
	ServiceKey     string  `json:"serviceKey"`
	CommandValue   float64 `json:"commandValue"`

	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"updatedAt"`
}
