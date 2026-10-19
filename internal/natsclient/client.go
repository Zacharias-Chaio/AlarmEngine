// client.go - 单实例 NATS 客户端：同一连接订阅标准告警输入、查询物模型定义并下发服务控制（.cmd REQ/REP）。
package natsclient

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"alarmengine/internal/config"
	"alarmengine/internal/platform"

	"github.com/nats-io/nats.go"
)

// defaultAlarmSubject 平台告警标准命名空间的通配订阅主题。
const defaultAlarmSubject = platform.AlarmSubjectDefault

// defaultThingsmodelPrefix 物模型模块固定主题前缀（契约 v2：无实例 ID）。
const defaultThingsmodelPrefix = platform.ThingsmodelPrefixDefault

const defaultRequestTimeout = 2 * time.Second

// AlarmHandler 接收一条已通过信封校验的标准告警消息（消息体 + 来源主题 + 信封时间戳）。
type AlarmHandler func(AlarmMessage, string, time.Time)

// Client 是告警引擎唯一的 NATS 客户端：单连接承担订阅输入、定义查询与控制下发。
type Client struct {
	conn           *nats.Conn
	alarmSubject   string
	tmPrefix       string
	requestTimeout time.Duration
	log            *slog.Logger
	handler        AlarmHandler
	stopOnce       sync.Once
}

// NewClient 按配置创建单个 NATS 客户端；未启用时返回 (nil, nil)。
func NewClient(app config.App, handler AlarmHandler) (*Client, error) {
	if !app.Nats.Enabled {
		return nil, nil
	}
	if err := config.Validate(app); err != nil {
		return nil, err
	}
	log := slog.Default().With("module", "nats")
	client := &Client{
		alarmSubject:   alarmSubject(app),
		tmPrefix:       thingsmodelPrefix(app),
		requestTimeout: requestTimeout(app),
		log:            log,
		handler:        handler,
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
	log.Info("NATS 客户端已启动", "url", app.Nats.URL, "alarm", client.alarmSubject, "thingsmodel", client.tmPrefix)
	return client, nil
}

func alarmSubject(app config.App) string {
	if subject := strings.TrimSpace(app.Nats.AlarmSubject); subject != "" {
		return subject
	}
	return defaultAlarmSubject
}

func thingsmodelPrefix(app config.App) string {
	if prefix := strings.TrimSpace(app.Nats.ThingsmodelPrefix); prefix != "" {
		return prefix
	}
	return defaultThingsmodelPrefix
}

func requestTimeout(app config.App) time.Duration {
	if app.Nats.RequestTimeout > 0 {
		return time.Duration(app.Nats.RequestTimeout) * time.Millisecond
	}
	return defaultRequestTimeout
}

func (c *Client) handleInbound(message *nats.Msg) {
	envelope, err := envelopeFromMsg(message)
	if err != nil {
		c.log.Warn("忽略无效告警消息", "error", err)
		return
	}
	if envelope.Type != MessageTypeAlarm {
		c.log.Warn("忽略未知消息类型", "type", envelope.Type, "subject", message.Subject)
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

// request 发起一次 REQ/REP：信封带 PP-Message-*，校验应答回填请求 ID，负载解析到 out。
func (c *Client) request(subject, messageType string, payload, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("编码请求 %s: %w", subject, err)
	}
	message := NewEnvelope(messageType, data).toMsg(subject)
	requestID := message.Header.Get(HeaderMessageID)
	reply, err := c.conn.RequestMsg(message, c.requestTimeout)
	if err != nil {
		return fmt.Errorf("请求 %s: %w", subject, err)
	}
	envelope, err := envelopeFromMsg(reply)
	if err != nil {
		return fmt.Errorf("应答 %s: %w", subject, err)
	}
	if envelope.ID != requestID {
		c.log.Warn("应答信封未回填请求 ID", "subject", subject, "expect", requestID, "got", envelope.ID)
	}
	if err := json.Unmarshal(envelope.Payload, out); err != nil {
		return fmt.Errorf("解析应答 %s: %w", subject, err)
	}
	return nil
}

// QueryDevices 查询物模型设备清单（契约 §5.1）。
func (c *Client) QueryDevices() (*DevicesQueryResponse, error) {
	var resp DevicesQueryResponse
	if err := c.request(c.tmPrefix+".query.devices", MessageTypeQuery, map[string]any{}, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("设备清单查询失败: %s", resp.Message)
	}
	return &resp, nil
}

// QueryEvents 查询指定设备的告警定义（契约 §5.4）。
func (c *Client) QueryEvents(deviceID string) (*EventsQueryResponse, error) {
	var resp EventsQueryResponse
	req := EventsQueryRequest{DeviceID: deviceID}
	if err := c.request(c.tmPrefix+".query.events", MessageTypeQuery, req, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("告警定义查询失败(%s): %s", deviceID, resp.Message)
	}
	return &resp, nil
}

// QueryMethods 查询指定设备的服务定义（契约 §5.3）。控制下发前必须先查。
func (c *Client) QueryMethods(deviceID string) (*MethodsQueryResponse, error) {
	var resp MethodsQueryResponse
	req := MethodsQueryRequest{DeviceID: deviceID}
	if err := c.request(c.tmPrefix+".query.methods", MessageTypeQuery, req, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("服务定义查询失败(%s): %s", deviceID, resp.Message)
	}
	return &resp, nil
}

// SendControl 下发服务控制（契约 §6 两段式第一步）：同步返回受理应答；
// requestID 为请求 PP-Message-ID，用于与失败终态 thingsmodel.cmd.result 闭环。
// 若调用方在 req.RequestID 预生成了 ID，则按其发布（引擎先落记录再发布，接住窗口内 cmd.result）。
// 传输失败/超时按错误返回（不自动重试：契约规定重复请求不去重，必须 at-most-once）。
func (c *Client) SendControl(req ControlRequest) (ack ControlAck, requestID string, err error) {
	data, err := json.Marshal(req)
	if err != nil {
		return ack, "", fmt.Errorf("编码控制请求: %w", err)
	}
	envelope := NewEnvelope(MessageTypeControl, data)
	if req.RequestID != "" {
		envelope.ID = req.RequestID
	}
	message := envelope.toMsg(c.tmPrefix + ".cmd")
	requestID = message.Header.Get(HeaderMessageID)
	reply, err := c.conn.RequestMsg(message, c.requestTimeout)
	if err != nil {
		return ack, requestID, fmt.Errorf("控制请求 %s.%s: %w", req.DeviceID, req.ServiceKey, err)
	}
	replyEnvelope, err := envelopeFromMsg(reply)
	if err != nil {
		return ack, requestID, fmt.Errorf("控制应答: %w", err)
	}
	if replyEnvelope.ID != requestID {
		c.log.Warn("控制应答未回填请求 ID", "expect", requestID, "got", replyEnvelope.ID)
	}
	if err := json.Unmarshal(replyEnvelope.Payload, &ack); err != nil {
		return ack, requestID, fmt.Errorf("解析控制应答: %w", err)
	}
	return ack, requestID, nil
}

func (c *Client) Close() {
	c.stopOnce.Do(func() {
		if err := c.conn.Drain(); err != nil {
			c.log.Warn("NATS 连接 Drain 失败", "error", err)
			c.conn.Close()
		}
	})
}

func milliseconds(value int, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}
