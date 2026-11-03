package domain

import "alarmengine/internal/store"

// AcknowledgeReason 判断单条告警能否确认：返回跳过原因（空串=可执行）。
func AcknowledgeReason(alarm store.Alarm) string {
	if alarm.Handled {
		return "告警已结案，无需确认"
	}
	if alarm.Acknowledged {
		return "告警已确认"
	}
	return ""
}

// HandleReason 判断单条告警能否结案；clearState 表示结案时需同步置为已恢复。
// 规则：孤儿与活动中的设备 trigger 不可关闭（等待自动恢复沿）；
// 无自动恢复沿的告警——一次性事件（kind=event，任意类型）与软件/系统类 trigger
// （发布方可能静默失联、不发 recover）——确认后允许视为即时恢复直接结案，
// 否则活动告警将永久挂起。结案后同定位键的新 trigger 因合并仅匹配 active 行，
// 会落新记录、作为新告警显示（见 manager.upsertAlarm）。
func HandleReason(alarm store.Alarm) (reason string, clearState bool) {
	if alarm.Handled {
		return "告警已处理结案", false
	}
	if alarm.Orphan {
		return "孤儿告警（触发源失联）不可关闭，请先恢复来源监测", false
	}
	if alarm.State != store.AlarmStateActive {
		return "", false // 已恢复：直接结案
	}
	noRecoverEdge := alarm.Kind == store.AlarmKindEvent ||
		(alarm.Type != store.AlarmTypeDevice && alarm.Kind == store.AlarmKindTrigger)
	if noRecoverEdge && alarm.Acknowledged {
		return "", true
	}
	return "活动中的告警不可关闭，需等待告警自动恢复后再处理", false
}

// RereadSkipReason 条件更新零行命中后，按最新状态给出准确原因。
func RereadSkipReason(alarm store.Alarm, fallback string) string {
	switch {
	case alarm.Handled && alarm.Acknowledged:
		return "告警已结案，无需确认"
	case alarm.Handled:
		return "告警已处理结案"
	case alarm.Acknowledged:
		return "告警已确认"
	}
	return fallback
}
