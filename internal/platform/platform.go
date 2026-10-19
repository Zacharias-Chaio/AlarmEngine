// Package platform 汇集平台级常量（NATS 主题命名空间），避免在多个包里各写一份字面量。
package platform

// AlarmSubjectDefault 默认告警订阅主题（标准命名空间通配）。
const AlarmSubjectDefault = "powerpulse.alarm.>"

// AlarmNamespace 告警标准命名空间前缀（用于从主题提取模块名）。
const AlarmNamespace = "powerpulse.alarm."

// ThingsmodelPrefixDefault 物模型查询/控制主题前缀默认值。
const ThingsmodelPrefixDefault = "powerpulse.thingsmodel"
