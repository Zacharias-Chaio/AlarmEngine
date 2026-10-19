// Package domain 承载可复用的业务不变量（规则校验、告警状态机判定）。
// 不依赖 HTTP 与数据库实现，便于单元测试与在消息处理器/CLI 中复用。
package domain

import (
	"fmt"
	"regexp"
	"strings"

	"alarmengine/internal/store"
)

// ruleIDPattern 规则 ID 字符集：仅字母数字与 _ -（同时避免含 "/" 的 ID 无法经路由删除）。
var ruleIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// MaxCooldownMs 冷却时间上限（24 小时）：防止超大值导致 time.Duration 溢出（冷却失效）。
const MaxCooldownMs = 24 * 60 * 60 * 1000

// ValidateRule 校验并规范化规则：trim 字段、按 kind 裁剪互斥字段、补默认值。
// 返回首个校验错误（nil 表示可落库）。规范化结果写回 rule。
func ValidateRule(rule *store.AlarmRule) error {
	rule.ID = strings.TrimSpace(rule.ID)
	rule.Name = strings.TrimSpace(rule.Name)
	for i := range rule.Monitors {
		rule.Monitors[i].DeviceID = strings.TrimSpace(rule.Monitors[i].DeviceID)
		rule.Monitors[i].EventKey = strings.TrimSpace(rule.Monitors[i].EventKey)
	}
	for i := range rule.Controls {
		rule.Controls[i].DeviceID = strings.TrimSpace(rule.Controls[i].DeviceID)
		rule.Controls[i].ServiceKey = strings.TrimSpace(rule.Controls[i].ServiceKey)
	}
	if rule.ID == "" {
		return fmt.Errorf("缺少 ID")
	}
	if !ruleIDPattern.MatchString(rule.ID) {
		return fmt.Errorf("ID 仅允许字母、数字、下划线与短横线（1-128 字符）")
	}
	if rule.Name == "" {
		return fmt.Errorf("缺少规则名称")
	}
	if rule.MatchMode == "" {
		rule.MatchMode = store.RuleMatchAny
	}
	if rule.MatchMode != store.RuleMatchAny && rule.MatchMode != store.RuleMatchAll {
		return fmt.Errorf("聚合模式仅支持 anyone / all")
	}
	if len(rule.Monitors) == 0 {
		return fmt.Errorf("至少配置一个监测源")
	}
	for i := range rule.Monitors {
		monitor := &rule.Monitors[i]
		switch monitor.Kind {
		case "level":
			// 级别监测不区分类型：事件字段一律裁剪，避免 kind 混淆的脏字段落库。
			monitor.Type = ""
			monitor.DeviceID = ""
			monitor.EventKey = ""
			monitor.EventName = ""
			if len(monitor.Levels) == 0 {
				return fmt.Errorf("级别监测源必须选择告警级别（P0-P3）")
			}
			for _, level := range monitor.Levels {
				if level < 0 || level > 3 {
					return fmt.Errorf("告警级别取值 0-3")
				}
			}
		case "event":
			// 事件监测：监测类型（v1 仅设备可用，软件/系统预留）；级别字段裁剪。
			// TODO(预留)：放开软件/系统时需同步实现 monitorHolds 的 category 匹配与前端选项。
			monitor.Levels = nil
			if monitor.Type == "" {
				monitor.Type = store.AlarmTypeDevice
			}
			if monitor.Type != store.AlarmTypeDevice {
				return fmt.Errorf("事件监测暂仅支持设备类型（软件/系统预留）")
			}
			if monitor.DeviceID == "" || monitor.EventKey == "" {
				return fmt.Errorf("事件监测源必须选择触发设备与告警事件")
			}
		default:
			return fmt.Errorf("未知监测源类型: %s", monitor.Kind)
		}
	}
	if len(rule.Controls) == 0 {
		return fmt.Errorf("至少配置一个控制源")
	}
	for _, control := range rule.Controls {
		if control.Kind != "device" {
			return fmt.Errorf("通知类型暂仅支持设备（短信/邮件预留）")
		}
		if control.DeviceID == "" || control.ServiceKey == "" {
			return fmt.Errorf("设备控制源必须选择目标设备与控制服务")
		}
	}
	if rule.CooldownMs < 0 || rule.CooldownMs > MaxCooldownMs {
		return fmt.Errorf("冷却时间范围 0 ~ 86400000 毫秒（24 小时）")
	}
	return nil
}
