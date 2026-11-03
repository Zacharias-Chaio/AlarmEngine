package natsclient

import (
	"strconv"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func validEnvelopeMsg() *nats.Msg {
	message := nats.NewMsg("powerpulse.alarm.thingsmodel")
	message.Header.Set(HeaderMessageID, "id-1")
	message.Header.Set(HeaderMessageType, MessageTypeAlarm)
	message.Header.Set(HeaderMessageVersion, MessageVersion)
	message.Header.Set(HeaderMessageTimestamp, strconv.FormatInt(time.Now().UnixMilli(), 10))
	message.Data = []byte("{}")
	return message
}

func TestEnvelopeFromMsg(t *testing.T) {
	envelope, err := envelopeFromMsg(validEnvelopeMsg())
	if err != nil {
		t.Fatalf("合法信封不应报错: %v", err)
	}
	if envelope.ID != "id-1" || envelope.Type != MessageTypeAlarm || envelope.Version != MessageVersion {
		t.Fatalf("信封字段解析错误: %+v", envelope)
	}
	if envelope.Payload == nil {
		t.Fatalf("负载应透传")
	}
}

func TestEnvelopeFromMsgInvalid(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*nats.Msg)
	}{
		{"nil 消息头", func(m *nats.Msg) { m.Header = nil }},
		{"缺少消息 ID", func(m *nats.Msg) { m.Header.Del(HeaderMessageID) }},
		{"缺少消息类型", func(m *nats.Msg) { m.Header.Del(HeaderMessageType) }},
		{"缺少版本号", func(m *nats.Msg) { m.Header.Del(HeaderMessageVersion) }},
		{"非数字时间戳", func(m *nats.Msg) { m.Header.Set(HeaderMessageTimestamp, "not-a-number") }},
	}
	for _, testCase := range cases {
		message := validEnvelopeMsg()
		testCase.mutate(message)
		if _, err := envelopeFromMsg(message); err == nil {
			t.Fatalf("%s 应报错", testCase.name)
		}
	}
}

func TestEnvelopeToMsgRoundTrip(t *testing.T) {
	original := NewEnvelope(MessageTypeControl, []byte(`{"device_id":"d1"}`))
	message := original.toMsg("powerpulse.thingsmodel.cmd")
	if message.Subject != "powerpulse.thingsmodel.cmd" {
		t.Fatalf("主题错误: %s", message.Subject)
	}
	if message.Header.Get(HeaderMessageID) != original.ID {
		t.Fatalf("信封 ID 应写入 Header")
	}
	parsed, err := envelopeFromMsg(message)
	if err != nil {
		t.Fatalf("回环解析失败: %v", err)
	}
	if parsed.ID != original.ID || parsed.Type != MessageTypeControl || string(parsed.Payload) != string(original.Payload) {
		t.Fatalf("回环字段不一致: %+v", parsed)
	}
}
