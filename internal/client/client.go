package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"

	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const maxMessageBytes = 64 * 1024

type Options struct {
	Config    config.Config
	Token     string
	Version   string
	SessionID string
	Adapter   string
	Once      bool
	Receive   func(protocol.Message) error
	Handle    func(context.Context, protocol.DeliveryData) error
}

func Run(ctx context.Context, options Options) error {
	headers := http.Header{}
	if options.Token != "" {
		headers.Set("Authorization", "Bearer "+options.Token)
	}

	connection, response, err := websocket.Dial(ctx, options.Config.ServiceURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		if response != nil {
			return fmt.Errorf("connect to AWP Service: HTTP %d: %w", response.StatusCode, err)
		}
		return fmt.Errorf("connect to AWP Service: %w", err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "client stopped")
	connection.SetReadLimit(maxMessageBytes)

	hello, err := protocol.New(protocol.ActionClientHello, protocol.ClientHelloData{
		DeviceID: options.Config.DeviceID,
		Client: protocol.ClientInfo{
			Name:    "awp-go",
			Version: options.Version,
		},
		Capabilities: protocol.Capabilities{
			Adapters: installedAdapters(),
			Resume:   options.Handle != nil,
		},
	})
	if err != nil {
		return err
	}
	if err := wsjson.Write(ctx, connection, hello); err != nil {
		return fmt.Errorf("send client.hello: %w", err)
	}

	welcomeReceived := false
	for {
		var message protocol.Message
		if err := wsjson.Read(ctx, connection, &message); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return fmt.Errorf("read AWP message: %w", err)
		}
		if err := message.Validate(); err != nil {
			return fmt.Errorf("invalid AWP message: %w", err)
		}

		switch message.Action {
		case protocol.ActionServerWelcome:
			welcome, err := protocol.DecodeData[protocol.ServerWelcomeData](message)
			if err != nil {
				return err
			}
			if welcome.DeviceID != options.Config.DeviceID {
				return fmt.Errorf("server.welcome device_id %q does not match configured device %q", welcome.DeviceID, options.Config.DeviceID)
			}
			welcomeReceived = true
			if err := receive(options.Receive, message); err != nil {
				return err
			}
			if options.SessionID != "" {
				if err := bindSession(ctx, connection, options); err != nil {
					return err
				}
			}
		case protocol.ActionEventDeliver:
			if !welcomeReceived {
				return errors.New("received event.deliver before server.welcome")
			}
			if err := receive(options.Receive, message); err != nil {
				return err
			}
			delivery, err := protocol.DecodeData[protocol.DeliveryData](message)
			if err != nil {
				return err
			}
			if delivery.DeliveryID == "" || delivery.EventID == "" {
				return errors.New("event.deliver requires delivery_id and event_id")
			}
			if err := validateTarget(options, delivery); err != nil {
				return err
			}
			if options.Handle == nil {
				if err := acknowledge(ctx, connection, delivery, "accepted", nil); err != nil {
					return err
				}
			} else {
				handleErr := options.Handle(ctx, delivery)
				status := "completed"
				var result map[string]any
				if handleErr != nil {
					status = "failed"
					result = map[string]any{"error": handleErr.Error()}
				}
				if err := acknowledge(ctx, connection, delivery, status, result); err != nil {
					return err
				}
				if handleErr != nil {
					return fmt.Errorf("handle AWP event %s: %w", delivery.EventID, handleErr)
				}
			}
			if options.Once {
				return nil
			}
		case protocol.ActionHeartbeatPing:
			if err := pong(ctx, connection, message.ID); err != nil {
				return err
			}
		case protocol.ActionSessionBound, protocol.ActionError:
			if err := receive(options.Receive, message); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected server action %q", message.Action)
		}
	}
}

func bindSession(ctx context.Context, connection *websocket.Conn, options Options) error {
	message, err := protocol.New(protocol.ActionSessionBind, protocol.SessionBindData{
		SessionID: options.SessionID,
		Adapter:   options.Adapter,
		Metadata:  map[string]any{},
	})
	if err != nil {
		return err
	}
	if err := wsjson.Write(ctx, connection, message); err != nil {
		return fmt.Errorf("send session.bind: %w", err)
	}
	return nil
}

func acknowledge(
	ctx context.Context,
	connection *websocket.Conn,
	delivery protocol.DeliveryData,
	status string,
	result map[string]any,
) error {
	ack, err := protocol.New(protocol.ActionEventAck, protocol.AckData{
		DeliveryID: delivery.DeliveryID,
		EventID:    delivery.EventID,
		Status:     status,
		Result:     result,
	})
	if err != nil {
		return err
	}
	if err := wsjson.Write(ctx, connection, ack); err != nil {
		return fmt.Errorf("send event.ack: %w", err)
	}
	return nil
}

func validateTarget(options Options, delivery protocol.DeliveryData) error {
	if options.SessionID == "" {
		return nil
	}
	var target protocol.TargetData
	if err := json.Unmarshal(delivery.Target, &target); err != nil {
		return fmt.Errorf("decode event.deliver target: %w", err)
	}
	if target.DeviceID != options.Config.DeviceID {
		return fmt.Errorf("delivery targets device %q, expected %q", target.DeviceID, options.Config.DeviceID)
	}
	if target.SessionID != options.SessionID {
		return fmt.Errorf("delivery targets session %q, expected %q", target.SessionID, options.SessionID)
	}
	return nil
}

func pong(ctx context.Context, connection *websocket.Conn, replyTo string) error {
	message, err := protocol.New(protocol.ActionHeartbeatPong, protocol.PongData{ReplyTo: replyTo})
	if err != nil {
		return err
	}
	if err := wsjson.Write(ctx, connection, message); err != nil {
		return fmt.Errorf("send heartbeat.pong: %w", err)
	}
	return nil
}

func receive(callback func(protocol.Message) error, message protocol.Message) error {
	if callback == nil {
		return nil
	}
	return callback(message)
}

func installedAdapters() []string {
	adapters := make([]string, 0, 2)
	if _, err := exec.LookPath("codex"); err == nil {
		adapters = append(adapters, "codex")
	}
	if _, err := exec.LookPath("claude"); err == nil {
		adapters = append(adapters, "claude-code")
	}
	return adapters
}
