// Package config owns the editable application settings persisted in the platform database.
package config

import (
	"fmt"
	"strings"

	"alarmengine/internal/logging"
	"alarmengine/internal/platform"
)

// App 平台设置：软件标识、日志、单实例 NATS 客户端、数据保留。
// 注意：系统告警不由本模块检测产生，由其他模块经平台告警标准（powerpulse.alarm.>）发布。
type App struct {
	Software  Software             `json:"software"`
	Logger    logging.LoggerConfig `json:"logger"`
	Nats      Nats                 `json:"nats"`
	Retention Retention            `json:"retention"`
}

// Retention 历史告警保留策略：已结案告警保留 Days 天（0=永久保留，默认）。
type Retention struct {
	Days int `json:"days"`
}

type Software struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Nats 单实例 NATS 客户端：同一连接订阅标准告警输入、查询物模型定义并下发服务控制。
type Nats struct {
	Enabled              bool   `json:"enabled"`
	URL                  string `json:"url"`
	Name                 string `json:"name"`
	AlarmSubject         string `json:"alarmSubject"`      // 订阅主题，默认 powerpulse.alarm.>
	ThingsmodelPrefix    string `json:"thingsmodelPrefix"` // 物模型主题前缀（query.* 与 .cmd），默认 powerpulse.thingsmodel
	RequestTimeout       int    `json:"requestTimeout"`    // REQ/REP 请求超时（毫秒），默认 2000
	ConnectTimeout       int    `json:"connectTimeout"`
	ReconnectWait        int    `json:"reconnectWait"`
	MaxReconnects        int    `json:"maxReconnects"`
	RetryOnFailedConnect bool   `json:"retryOnFailedConnect"`
	ReconnectBufSize     int    `json:"reconnectBufSize"`
	PingInterval         int    `json:"pingInterval"`
	MaxPingsOut          int    `json:"maxPingsOut"`
}

func Default() App {
	return App{
		Software: Software{ID: "alarmengine-001", Name: "AlarmEngine"},
		Logger:   logging.DefaultConfig().Logger,
		Nats: Nats{
			Enabled: false, URL: "nats://127.0.0.1:4222", Name: "alarmengine",
			AlarmSubject:      platform.AlarmSubjectDefault,
			ThingsmodelPrefix: "powerpulse.thingsmodel",
			RequestTimeout:    2000,
			ConnectTimeout:    2000, ReconnectWait: 2000, MaxReconnects: -1,
			RetryOnFailedConnect: true, ReconnectBufSize: 8388608, PingInterval: 20000, MaxPingsOut: 3,
		},
		Retention: Retention{Days: 0},
	}
}

func Validate(app App) error {
	// 说明：software.id/software.name 仅用于展示/兼容，当前没有消费方，也不可经前端编辑，
	// 因此不做必填校验——否则用户会陷入"保存必失败且无从修复"的死角。
	if err := (logging.Config{Logger: app.Logger}).Validate(); err != nil {
		return err
	}
	nats := app.Nats
	if nats.Enabled {
		if strings.TrimSpace(nats.URL) == "" {
			return fmt.Errorf("启用 NATS 时必须填写服务地址")
		}
		if err := validAlarmSubject(nats.AlarmSubject); err != nil {
			return err
		}
		// 前缀按 NATS 主题 token 规则逐段校验（P2）：非空、字符集受限、无通配符。
		for _, token := range strings.Split(nats.ThingsmodelPrefix, ".") {
			if !validSubjectToken(token) {
				return fmt.Errorf("物模型主题前缀包含非法 token: %q", token)
			}
		}
		if nats.RequestTimeout < 0 {
			return fmt.Errorf("REQ 请求超时不能为负")
		}
	}
	if nats.ConnectTimeout < 0 || nats.ReconnectWait < 0 || nats.ReconnectBufSize < 0 || nats.PingInterval < 0 || nats.MaxPingsOut < 0 || nats.MaxReconnects < -1 {
		return fmt.Errorf("NATS 数值配置无效")
	}
	if app.Retention.Days < 0 {
		return fmt.Errorf("数据保留天数不能为负")
	}
	return nil
}

// validAlarmSubject 校验订阅主题：非空、无空白字符。
func validAlarmSubject(subject string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Errorf("启用 NATS 时必须填写告警订阅主题（默认 powerpulse.alarm.>）")
	}
	if strings.ContainsAny(subject, " \t\r\n") {
		return fmt.Errorf("告警订阅主题不能包含空白字符")
	}
	return nil
}

func validSubjectToken(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}
