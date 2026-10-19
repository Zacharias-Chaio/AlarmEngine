package runtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"alarmengine/internal/config"
	"alarmengine/internal/natsclient"
)

// clientManager 负责 NATS 客户端的生命周期：单连接、热重载、失败退避重试与设置副本。
type clientManager struct {
	ctx  context.Context
	log  *slog.Logger
	defs *definitions
	// 由组合根注入的钩子：拉取物模型定义、跟踪后台任务、消息回调。
	startBootstrap func()
	track          func(func())
	onMessage      natsclient.AlarmHandler

	clientMu sync.RWMutex
	client   *natsclient.Client
	control  func(natsclient.ControlRequest) (natsclient.ControlAck, string, error)

	restartMu  sync.Mutex
	restarting bool

	retryMu  sync.Mutex
	retrying bool

	settingsMu sync.RWMutex
	settings   config.App
}

// currentClient 读取当前 NATS 客户端（可能为 nil：未启用）。
func (c *clientManager) currentClient() *natsclient.Client {
	c.clientMu.RLock()
	defer c.clientMu.RUnlock()
	return c.client
}

// currentControl 读取当前控制通道闭包（可能为 nil）。
func (c *clientManager) currentControl() func(natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
	c.clientMu.RLock()
	defer c.clientMu.RUnlock()
	return c.control
}

// currentSettings 读取当前平台设置副本。
func (c *clientManager) currentSettings() config.App {
	c.settingsMu.RLock()
	defer c.settingsMu.RUnlock()
	return c.settings
}

// reloadClient 尝试用新设置换客户端；失败时仍保存新设置（供退避重试使用最新配置）。
func (c *clientManager) reloadClient(settings config.App) error {
	candidate, err := natsclient.NewClient(settings, c.onMessage)
	if err != nil {
		c.settingsMu.Lock()
		c.settings = settings
		c.settingsMu.Unlock()
		return err
	}
	var control func(natsclient.ControlRequest) (natsclient.ControlAck, string, error)
	if candidate != nil {
		control = func(req natsclient.ControlRequest) (natsclient.ControlAck, string, error) {
			return candidate.SendControl(req)
		}
	}
	c.clientMu.Lock()
	previous := c.client
	c.client = candidate
	c.control = control
	c.clientMu.Unlock()
	if previous != nil {
		previous.Close()
	}
	c.settingsMu.Lock()
	c.settings = settings
	c.settingsMu.Unlock()
	if candidate == nil {
		if c.defs != nil {
			c.defs.clear() // 客户端停用时清空定义缓存（诚实状态）
		}
	} else if c.track != nil && c.startBootstrap != nil {
		c.track(c.startBootstrap) // 启动/重载后拉取物模型定义
	}
	c.log.Info("NATS 客户端配置已加载", "enabled", settings.Nats.Enabled)
	return nil
}

// ReloadBus 热重载消息总线；失败时自动进入既有退避重试（配置已落库，不能让运行时停在旧连接）。
func (c *clientManager) ReloadBus(settings config.App) error {
	if err := config.Validate(settings); err != nil {
		return err
	}
	c.restartMu.Lock()
	err := c.reloadClient(settings)
	c.restartMu.Unlock()
	if err != nil {
		c.scheduleClientRetry()
	}
	return err
}

// Restart 进程内重启运行时资源（HTTP 服务保持可用）。
func (c *clientManager) Restart(settings config.App) bool {
	c.restartMu.Lock()
	defer c.restartMu.Unlock()
	if c.restarting {
		return false
	}
	c.restarting = true
	c.track(func() {
		defer func() {
			c.restartMu.Lock()
			c.restarting = false
			c.restartMu.Unlock()
		}()
		c.restartMu.Lock()
		err := c.reloadClient(settings)
		c.restartMu.Unlock()
		if err != nil {
			c.log.Error("告警运行时重启失败，将自动退避重试", "error", err)
			c.scheduleClientRetry()
			return
		}
		c.log.Info("告警运行时重启完成")
	})
	return true
}

// scheduleClientRetry 启动失败后的自动退避重试（5s 起、翻倍、封顶 60s，成功即停）。
func (c *clientManager) scheduleClientRetry() {
	c.retryMu.Lock()
	if c.retrying {
		c.retryMu.Unlock()
		return
	}
	c.retrying = true
	c.retryMu.Unlock()
	c.track(func() {
		defer func() {
			c.retryMu.Lock()
			c.retrying = false
			c.retryMu.Unlock()
		}()
		delay := 5 * time.Second
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(delay):
			}
			c.restartMu.Lock()
			err := c.reloadClient(c.currentSettings())
			c.restartMu.Unlock()
			if err == nil {
				c.log.Info("NATS 客户端自动重试连接成功")
				return
			}
			c.log.Warn("NATS 客户端自动重试失败，将继续退避重试", "error", err, "next_delay", delay)
			delay *= 2
			if delay > time.Minute {
				delay = time.Minute
			}
		}
	})
}
