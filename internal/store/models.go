package store

import "time"

// 告警级别（与平台告警标准 level 一致）。
const (
	AlarmLevelInfo     = 0 // 提示
	AlarmLevelMinor    = 1 // 一般
	AlarmLevelMajor    = 2 // 严重
	AlarmLevelCritical = 3 // 紧急
)

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
	ID           string     `gorm:"primaryKey;size:128" json:"id"`
	Name         string     `gorm:"not null" json:"name"`              // 告警名称
	Type         string     `gorm:"index" json:"type"`                 // 告警类型：device|software|system
	Kind         string     `gorm:"index;default:trigger" json:"kind"` // trigger | event
	Source       string     `gorm:"index" json:"source"`               // 展示来源：系统/软件名/设备名
	Module       string     `gorm:"index" json:"module"`               // source.module（标准来源模块）
	Instance     string     `json:"instance"`                          // source.instance（标准来源实例）
	Point        string     `gorm:"index" json:"point"`                // 告警点位
	DeviceID     string     `gorm:"index" json:"deviceId"`
	DeviceName   string     `json:"deviceName"`
	SoftwareID   string     `gorm:"index" json:"softwareId"`                 // 软件实例标识（source.instance）
	EventID      string     `gorm:"index" json:"eventId"`                    // 设备告警条目 ID（定位键，优先）
	EventKey     string     `gorm:"index" json:"eventKey"`                   // 设备告警规则 key（定位键）
	Code         string     `gorm:"index" json:"code"`                       // 软件/系统事件码（定位键）
	Component    string     `json:"component"`                               // 软件/系统组件（定位键）
	Severity     int        `gorm:"index" json:"severity"`                   // 告警等级
	State        string     `gorm:"index" json:"state"`                      // active | cleared（自动）
	Orphan       bool       `gorm:"index;default:false" json:"orphan"`       // 孤儿标志：触发源失联（配置变更/设备离线等），叠加于 active
	Acknowledged bool       `gorm:"index;default:false" json:"acknowledged"` // 是否人工确认（进入处理阶段）
	AcknowledgedAt *time.Time `json:"acknowledgedAt"`                        // 确认时间（MTTA 统计依据）
	Handled      bool       `gorm:"index;default:false" json:"handled"`      // 是否人工处理（结案）
	Message      string     `json:"message"`
	Value        float64    `json:"value"`                   // 当前值
	Threshold    float64    `json:"threshold"`               // 告警阈值
	Unit         string     `json:"unit"`                    // 展示单位（标准消息无此字段，仅演示/旧数据携带）
	Count        int        `gorm:"default:1" json:"count"`  // 节流聚合计数（标准 count 累加）
	DurationMs   int64      `gorm:"index" json:"durationMs"` // 持续时间(ms)
	OccurredAt   time.Time  `gorm:"index" json:"occurredAt"`
	RecoveredAt  *time.Time `json:"recoveredAt"` // 自动恢复时间
	ClearedAt    *time.Time `json:"clearedAt"`   // 人工处理时间
	CreatedAt    time.Time  `json:"-"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// AlarmRule 告警引擎保护动作规则。
type AlarmRule struct {
	ID          string `gorm:"primaryKey;size:128" json:"id"`
	Name        string `gorm:"not null" json:"name"`
	Description string `json:"description"`
	Enabled     bool   `gorm:"default:true" json:"enabled"`
	Severity    int    `json:"severity"`

	// 触发条件
	DeviceID    string  `json:"deviceId"`
	PropertyKey string  `json:"propertyKey"`
	Operator    string  `json:"operator"` // gt | gte | lt | lte | eq
	Threshold   float64 `json:"threshold"`
	DurationMs  int     `json:"durationMs"`

	// 保护动作
	ActionType     string  `json:"actionType"` // protection | notify
	TargetDeviceID string  `json:"targetDeviceId"`
	Command        string  `json:"command"`
	CommandValue   float64 `json:"commandValue"`

	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"updatedAt"`
}
