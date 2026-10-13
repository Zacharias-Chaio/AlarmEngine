package store

import (
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// SeedIfEmpty 在告警/规则表为空时写入演示数据，便于首次启动即可查看 Web 效果。
// 演示数据模拟按平台告警标准（powerpulse-alarm-standard.md）发布的消息落库结果：
// 覆盖活动中（未确认/已确认）/已恢复/孤儿/一次性事件、今日与昨日同期分布（环比演示）。
func SeedIfEmpty(db *gorm.DB) error {
	var alarmCount int64
	if err := db.Model(&Alarm{}).Count(&alarmCount).Error; err != nil {
		return err
	}
	if alarmCount > 0 {
		return nil
	}

	now := time.Now().UTC()
	// ago/agoPtr 统一传正数毫秒，表示"多久之前"，避免正负号混用。
	ago := func(ms int64) time.Time { return now.Add(time.Duration(-ms) * time.Millisecond) }
	agoPtr := func(ms int64) *time.Time { t := ago(ms); return &t }
	const minute, hour = int64(60 * 1000), int64(60 * 60 * 1000)

	alarms := []Alarm{
		// ===== 活动中（未确认）=====
		{ID: "al-101", Name: "电池温度过高", Kind: AlarmKindTrigger, Type: AlarmTypeDevice, Source: "1号电池簇",
			Module: "thingsmodel", Instance: "thingsmodel-001", Point: "temperature",
			DeviceID: "bms-001", DeviceName: "1号电池簇", EventID: "event_1", EventKey: "temp_high",
			Severity: AlarmLevelCritical, State: AlarmStateActive,
			Message: "电池温度(temperature) 当前值 62.5，阈值 60（upper）",
			Value:   62.5, Threshold: 60, Unit: "℃", Count: 1,
			OccurredAt: ago(8 * minute), DurationMs: 8 * minute},
		{ID: "al-102", Name: "单体电压偏低", Kind: AlarmKindTrigger, Type: AlarmTypeDevice, Source: "1号电池簇",
			Module: "thingsmodel", Instance: "thingsmodel-001", Point: "voltage",
			DeviceID: "bms-001", DeviceName: "1号电池簇", EventID: "event_2", EventKey: "voltage_low",
			Severity: AlarmLevelMajor, State: AlarmStateActive,
			Message: "单体电压(voltage) 当前值 2.8，阈值 3（lower）",
			Value:   2.8, Threshold: 3.0, Unit: "V", Count: 1,
			OccurredAt: ago(35 * minute), DurationMs: 35 * minute},
		// 系统告警演示：外部模块经标准接口发布（本模块不检测产生）
		{ID: "al-104", Name: "powerpulse.node.offline", Kind: AlarmKindTrigger, Type: AlarmTypeSystem, Source: "系统",
			Module: "gateway", Instance: "gateway-001", Code: "powerpulse.node.offline", Component: "core",
			Severity: AlarmLevelMajor, State: AlarmStateActive,
			Message: "边缘节点 node-03 心跳丢失超过 60 秒",
			Count:   1, OccurredAt: ago(2 * hour), DurationMs: 2 * hour},
		// ===== 活动中（已确认，处理流程中）=====
		{ID: "al-103", Name: "thingsmodel.cmd.result", Kind: AlarmKindEvent, Type: AlarmTypeSoftware, Source: "thingsmodel",
			Module: "thingsmodel", Instance: "thingsmodel-001", SoftwareID: "thingsmodel-001",
			Code: "thingsmodel.cmd.result", Component: "runtime",
			Severity: AlarmLevelMajor, State: AlarmStateActive, Acknowledged: true,
			Message: "服务控制执行失败：设备 bms-001 热管理指令超时（request_id=req-7721）",
			Count:   3, OccurredAt: ago(50 * minute), DurationMs: 50 * minute},
		{ID: "al-106", Name: "环境温度偏高", Kind: AlarmKindTrigger, Type: AlarmTypeDevice, Source: "电表1",
			Module: "thingsmodel", Instance: "thingsmodel-001", Point: "envTemperature",
			DeviceID: "meter-001", DeviceName: "电表1", EventID: "event_5", EventKey: "env_temp_high",
			Severity: AlarmLevelMinor, State: AlarmStateActive, Acknowledged: true,
			Message: "环境温度(envTemperature) 当前值 37.5，阈值 35（upper）",
			Value:   37.5, Threshold: 35, Unit: "℃", Count: 1,
			OccurredAt: ago(5 * hour), DurationMs: 5 * hour},
		// ===== 孤儿（触发源失联，预留状态演示）=====
		{ID: "al-105", Name: "通讯中断", Kind: AlarmKindTrigger, Type: AlarmTypeDevice, Source: "电表1",
			Module: "thingsmodel", Instance: "thingsmodel-001", Point: "communication",
			DeviceID: "meter-001", DeviceName: "电表1", EventID: "event_3", EventKey: "comm_lost",
			Severity: AlarmLevelMinor, State: AlarmStateActive, Orphan: true,
			Message: "与网关通讯中断，触发源失联（设备离线）",
			Count:   1, OccurredAt: ago(3 * hour), DurationMs: 3 * hour},
		// ===== 已恢复（自动恢复沿到达，未结案）=====
		{ID: "al-107", Name: "环境温度提示", Kind: AlarmKindTrigger, Type: AlarmTypeDevice, Source: "电表1",
			Module: "thingsmodel", Instance: "thingsmodel-001", Point: "envTemperature",
			DeviceID: "meter-001", DeviceName: "电表1", EventID: "event_4", EventKey: "env_temp_high",
			Severity: AlarmLevelInfo, State: AlarmStateCleared, Acknowledged: true,
			Message: "环境温度略高，已自动恢复",
			Value:   36, Threshold: 35, Unit: "℃", Count: 1,
			OccurredAt: ago(6*hour + 30*minute), RecoveredAt: agoPtr(6 * hour), DurationMs: 30 * minute},
		// ===== 昨日同期（环比演示）=====
		{ID: "al-108", Name: "母线电压异常", Kind: AlarmKindTrigger, Type: AlarmTypeDevice, Source: "2号电池簇",
			Module: "thingsmodel", Instance: "thingsmodel-001", Point: "busVoltage",
			DeviceID: "bms-002", DeviceName: "2号电池簇", EventID: "event_6", EventKey: "bus_voltage_abnormal",
			Severity: AlarmLevelMajor, State: AlarmStateCleared,
			Message: "母线电压(busVoltage) 当前值 415，阈值 420（lower）",
			Value:   415, Threshold: 420, Unit: "V", Count: 1,
			OccurredAt: ago(24*hour + 10*minute), RecoveredAt: agoPtr(24 * hour), DurationMs: 10 * minute},
		{ID: "al-109", Name: "thingsmodel.cmd.frequent_failure", Kind: AlarmKindEvent, Type: AlarmTypeSoftware, Source: "thingsmodel",
			Module: "thingsmodel", Instance: "thingsmodel-001", SoftwareID: "thingsmodel-001",
			Code: "thingsmodel.cmd.frequent_failure", Component: "south",
			Severity: AlarmLevelMajor, State: AlarmStateActive, Acknowledged: true,
			Message: "高频控制失败聚合告警（窗口 60s 内 5 次）",
			Count:   5, OccurredAt: ago(24*hour + 40*minute), DurationMs: 24*hour + 40*minute},
		// ===== 更早的历史（趋势/历史页演示）=====
		{ID: "al-110", Name: "SOC 越限", Kind: AlarmKindTrigger, Type: AlarmTypeDevice, Source: "1号电池簇",
			Module: "thingsmodel", Instance: "thingsmodel-001", Point: "soc",
			DeviceID: "bms-001", DeviceName: "1号电池簇", EventID: "event_7", EventKey: "soc_over",
			Severity: AlarmLevelMajor, State: AlarmStateCleared, Handled: true,
			Message: "SOC(soc) 当前值 95，阈值 90（upper）",
			Value:   95, Threshold: 90, Unit: "%", Count: 1,
			OccurredAt: ago(26 * hour), ClearedAt: agoPtr(25 * hour), DurationMs: hour},
		{ID: "al-111", Name: "任务执行失败", Kind: AlarmKindEvent, Type: AlarmTypeSoftware, Source: "thingsmodel",
			Module: "thingsmodel", Instance: "thingsmodel-001", SoftwareID: "thingsmodel-001",
			Code: "thingsmodel.cmd.result", Component: "store",
			Severity: AlarmLevelMinor, State: AlarmStateCleared, Handled: true,
			Message: "设备订阅任务执行失败（已处理结案）",
			Count:   1, OccurredAt: ago(5 * 24 * hour), ClearedAt: agoPtr((5*24 - 2) * hour), DurationMs: 2 * hour},
	}
	if err := db.Create(&alarms).Error; err != nil {
		return err
	}

	rules := []AlarmRule{
		{ID: "rule-001", Name: "电池温度保护", Description: "电池温度超过阈值时触发紧急告警并下发热保护", Enabled: true, Severity: AlarmLevelCritical, DeviceID: "bms-001", PropertyKey: "temperature", Operator: "gt", Threshold: 60, DurationMs: 0, ActionType: "protection", TargetDeviceID: "bms-001", Command: "thermalCutoff", CommandValue: 1},
		{ID: "rule-002", Name: "单体电压保护", Description: "单体电压低于下限时触发严重告警", Enabled: true, Severity: AlarmLevelMajor, DeviceID: "bms-001", PropertyKey: "voltage", Operator: "lt", Threshold: 3.0, DurationMs: 0, ActionType: "notify", TargetDeviceID: "", Command: "", CommandValue: 0},
		{ID: "rule-003", Name: "通讯保护", Description: "通讯中断时触发一般告警并通知", Enabled: true, Severity: AlarmLevelMinor, DeviceID: "pcs-001", PropertyKey: "communication", Operator: "eq", Threshold: 1, DurationMs: 0, ActionType: "notify", TargetDeviceID: "", Command: "", CommandValue: 0},
		{ID: "rule-004", Name: "SOC 上限保护", Description: "SOC 超过上限时触发严重告警", Enabled: false, Severity: AlarmLevelMajor, DeviceID: "bms-001", PropertyKey: "soc", Operator: "gt", Threshold: 90, DurationMs: 0, ActionType: "notify", TargetDeviceID: "", Command: "", CommandValue: 0},
	}
	if err := db.Create(&rules).Error; err != nil {
		return err
	}
	slog.Info("已写入演示告警与规则数据", "alarms", len(alarms), "rules", len(rules))
	return nil
}
