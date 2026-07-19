package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	Type    = "awp"
	Version = "0.1"
)

const (
	ActionClientHello   = "client.hello"
	ActionServerWelcome = "server.welcome"
	ActionSessionBind   = "session.bind"
	ActionSessionBound  = "session.bound"
	ActionEventPublish  = "event.publish"
	ActionEventDeliver  = "event.deliver"
	ActionEventAck      = "event.ack"
	ActionHeartbeatPing = "heartbeat.ping"
	ActionHeartbeatPong = "heartbeat.pong"
	ActionError         = "error"
)

type Message struct {
	Type      string          `json:"type"`
	Version   string          `json:"version"`
	ID        string          `json:"id"`
	Action    string          `json:"action"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type ClientHelloData struct {
	DeviceID     string       `json:"device_id"`
	Client       ClientInfo   `json:"client"`
	Capabilities Capabilities `json:"capabilities"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Capabilities struct {
	Adapters []string `json:"adapters"`
	Resume   bool     `json:"resume"`
}

type ServerWelcomeData struct {
	DeviceID                 string `json:"device_id"`
	ConnectionID             string `json:"connection_id"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
	MaxMessageBytes          int    `json:"max_message_bytes"`
}

type SessionBindData struct {
	SessionID string         `json:"session_id"`
	Adapter   string         `json:"adapter"`
	Metadata  map[string]any `json:"metadata"`
}

type DeliveryData struct {
	DeliveryID string          `json:"delivery_id"`
	EventID    string          `json:"event_id"`
	Target     json.RawMessage `json:"target"`
	Event      json.RawMessage `json:"event"`
	Attempt    int             `json:"attempt"`
}

type AckData struct {
	DeliveryID string `json:"delivery_id"`
	EventID    string `json:"event_id"`
	Status     string `json:"status"`
}

type PongData struct {
	ReplyTo string `json:"reply_to"`
}

func New(action string, data any) (Message, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Message{}, fmt.Errorf("encode %s data: %w", action, err)
	}
	return Message{
		Type:      Type,
		Version:   Version,
		ID:        NewMessageID(),
		Action:    action,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Data:      raw,
	}, nil
}

func NewMessageID() string {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("msg_%d", time.Now().UTC().UnixNano())
	}
	return "msg_" + hex.EncodeToString(bytes)
}

func (message Message) Validate() error {
	if message.Type != Type {
		return fmt.Errorf("unsupported message type %q", message.Type)
	}
	if message.Version != Version {
		return fmt.Errorf("unsupported protocol version %q", message.Version)
	}
	if message.ID == "" {
		return errors.New("message id is required")
	}
	if message.Action == "" {
		return errors.New("message action is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, message.Timestamp); err != nil {
		return errors.New("message timestamp must be RFC 3339")
	}
	if len(message.Data) == 0 {
		return errors.New("message data is required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(message.Data, &object); err != nil {
		return errors.New("message data must be a JSON object")
	}
	return nil
}

func DecodeData[T any](message Message) (T, error) {
	var value T
	if err := json.Unmarshal(message.Data, &value); err != nil {
		return value, fmt.Errorf("decode %s data: %w", message.Action, err)
	}
	return value, nil
}
