// Package config owns the editable application settings persisted in the platform database.
package config

import (
	"fmt"
	"strings"

	"alarmengine/internal/logging"
)

// App 平台设置：软件标识、日志、单实例 NATS 客户端。
// 注意：系统告警不由本模块检测产生，由其他模块经平台告警标准（powerpulse.alarm.>）发布。
type App struct {
	Software Software             `json:"software"`
	Logger   logging.LoggerConfig `json:"logger"`
	Nats     Nats                 `json:"nats"`
}

type Software struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Nats 单实例 NATS 客户端：同一连接同时订阅标准告警输入与发布保护动作输出。
type Nats struct {
	Enabled              bool   `json:"enabled"`
	URL                  string `json:"url"`
	Name                 string `json:"name"`
	AlarmSubject         string `json:"alarmSubject"` // 订阅主题，默认 powerpulse.alarm.>
	QueueSize            int    `json:"queueSize"`
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
			AlarmSubject: "powerpulse.alarm.>",
			QueueSize:    4096, ConnectTimeout: 2000, ReconnectWait: 2000, MaxReconnects: -1,
			RetryOnFailedConnect: true, ReconnectBufSize: 8388608, PingInterval: 20000, MaxPingsOut: 3,
		},
	}
}

func Validate(app App) error {
	if !validSubjectToken(app.Software.ID) {
		return fmt.Errorf("software.id 不能为空，且只能包含字母、数字、下划线和连字符")
	}
	if strings.TrimSpace(app.Software.Name) == "" {
		return fmt.Errorf("software.name 不能为空")
	}
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
	}
	if nats.QueueSize < 1 || nats.ConnectTimeout < 0 || nats.ReconnectWait < 0 || nats.ReconnectBufSize < 0 || nats.PingInterval < 0 || nats.MaxPingsOut < 0 || nats.MaxReconnects < -1 {
		return fmt.Errorf("NATS 数值配置无效")
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
