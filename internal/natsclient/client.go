// client.go - 单实例 NATS 客户端：同一连接同时订阅告警事件输入与发布保护动作输出。
package natsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"alarmengine/internal/config"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// outputSubjectPrefix 是静态编码的输出主题前缀：powerpulse + 模块名称。
const outputSubjectPrefix = "powerpulse.alarm"

// defaultAlarmSubject 平台告警标准命名空间的通配订阅主题。
const defaultAlarmSubject = "powerpulse.alarm.>"

// AlarmHandler 接收一条已通过信封校验的标准告警消息（消息体 + 来源主题 + 信封时间戳）。
type AlarmHandler func(AlarmMessage, string, time.Time)

// Client 是告警引擎唯一的 NATS 客户端：单连接同时承担订阅输入与发布输出。
type Client struct {
	conn          *nats.Conn
	alarmSubject  string
	actionSubject string
	log           *slog.Logger
	handler       AlarmHandler
	events        chan []byte
	eventsMu      sync.Mutex
	eventsDone    bool
	stopOnce      sync.Once
	done          chan struct{}
}

// NewClient 按配置创建单个 NATS 客户端；未启用时返回 (nil, nil)。
func NewClient(ctx context.Context, app config.App, handler AlarmHandler) (*Client, error) {
	if !app.Nats.Enabled {
		return nil, nil
	}
	if err := config.Validate(app); err != nil {
		return nil, err
	}
	log := slog.Default().With("module", "nats")
	client := &Client{
		alarmSubject:  alarmSubject(app),
		actionSubject: outputSubjectPrefix + "." + app.Software.ID + ".action",
		log:           log,
		handler:       handler,
		events:        make(chan []byte, app.Nats.QueueSize),
		done:          make(chan struct{}),
	}
	options := []nats.Option{
		nats.Name(app.Nats.Name),
		nats.Timeout(milliseconds(app.Nats.ConnectTimeout, 2*time.Second)),
		nats.ReconnectWait(milliseconds(app.Nats.ReconnectWait, 2*time.Second)),
		nats.MaxReconnects(app.Nats.MaxReconnects),
		nats.RetryOnFailedConnect(app.Nats.RetryOnFailedConnect),
		nats.ReconnectBufSize(app.Nats.ReconnectBufSize),
		nats.PingInterval(milliseconds(app.Nats.PingInterval, 20*time.Second)),
		nats.MaxPingsOutstanding(app.Nats.MaxPingsOut),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Warn("NATS 连接已断开", "error", err) }),
		nats.ReconnectHandler(func(connection *nats.Conn) { log.Info("NATS 连接已重连", "url", connection.ConnectedUrl()) }),
		nats.ClosedHandler(func(connection *nats.Conn) { log.Warn("NATS 连接已关闭", "error", connection.LastError()) }),
	}
	connection, err := nats.Connect(app.Nats.URL, options...)
	if err != nil {
		return nil, fmt.Errorf("连接 NATS: %w", err)
	}
	client.conn = connection
	if _, err := connection.Subscribe(client.alarmSubject, client.handleInbound); err != nil {
		connection.Close()
		return nil, fmt.Errorf("订阅告警主题 %s: %w", client.alarmSubject, err)
	}
	if err := connection.Flush(); err != nil {
		connection.Close()
		return nil, fmt.Errorf("初始化 NATS 连接: %w", err)
	}
	go client.publishLoop(ctx)
	log.Info("NATS 客户端已启动", "url", app.Nats.URL, "alarm", client.alarmSubject, "action", client.actionSubject)
	return client, nil
}

// alarmSubject 订阅主题：平台告警标准命名空间，默认 powerpulse.alarm.>（收全部模块）。
func alarmSubject(app config.App) string {
	subject := strings.TrimSpace(app.Nats.AlarmSubject)
	if subject == "" {
		return defaultAlarmSubject
	}
	return subject
}

func (c *Client) handleInbound(message *nats.Msg) {
	envelope, err := envelopeFromMsg(message)
	if err != nil {
		c.log.Warn("忽略无效告警消息", "error", err)
		return
	}
	if envelope.Type != MessageTypeAlarm {
		c.log.Warn("忽略未知消息类型", "type", envelope.Type)
		return
	}
	var event AlarmMessage
	if err := json.Unmarshal(envelope.Payload, &event); err != nil {
		c.log.Warn("解析告警消息失败", "error", err)
		return
	}
	if c.handler != nil {
		c.handler(event, message.Subject, envelope.Timestamp.UTC())
	}
}

// PublishAction 排队发布一条保护动作。
func (c *Client) PublishAction(action ProtectionAction) {
	action.ActionID = uuid.NewString()
	action.Timestamp = time.Now().UnixMilli()
	payload, err := MarshalProtectionAction(action)
	if err != nil {
		c.log.Warn("编码保护动作失败", "error", err)
		return
	}
	c.enqueue(payload)
}

// enqueue 入队且不阻塞调用方；队列满时丢弃最旧消息，优先保留最新状态。
func (c *Client) enqueue(payload []byte) {
	c.eventsMu.Lock()
	defer c.eventsMu.Unlock()
	if c.eventsDone {
		return
	}
	select {
	case c.events <- payload:
		return
	default:
	}
	select {
	case <-c.events:
		c.log.Warn("NATS 发布队列已满，丢弃最旧消息")
	default:
	}
	c.events <- payload
}

func (c *Client) Close() {
	c.stopOnce.Do(func() {
		c.eventsMu.Lock()
		c.eventsDone = true
		close(c.events)
		c.eventsMu.Unlock()
		<-c.done
		if err := c.conn.Drain(); err != nil {
			c.log.Warn("NATS 连接 Drain 失败", "error", err)
			c.conn.Close()
		}
	})
}

func (c *Client) publishLoop(ctx context.Context) {
	defer close(c.done)
	for {
		select {
		case <-ctx.Done():
			return
		case payload, ok := <-c.events:
			if !ok {
				return
			}
			if err := c.conn.PublishMsg(NewEnvelope(MessageTypeAction, payload).toMsg(c.actionSubject)); err != nil {
				c.log.Warn("发布保护动作失败", "error", err)
			}
		}
	}
}

func milliseconds(value int, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}
