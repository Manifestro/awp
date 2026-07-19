package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Manifestro/awp/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestRunHandshakeDeliveryAndAck(t *testing.T) {
	acknowledgement := make(chan protocol.Message, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()

		var hello protocol.Message
		if err := wsjson.Read(request.Context(), connection, &hello); err != nil {
			t.Error(err)
			return
		}
		if hello.Action != protocol.ActionClientHello {
			t.Errorf("first action = %q, want client.hello", hello.Action)
			return
		}

		welcome := mustMessage(t, protocol.ActionServerWelcome, map[string]any{"device_id": "dev_test"})
		if err := wsjson.Write(request.Context(), connection, welcome); err != nil {
			t.Error(err)
			return
		}
		for _, sessionID := range []string{"ses_first", "ses_test"} {
			var bind protocol.Message
			if err := wsjson.Read(request.Context(), connection, &bind); err != nil {
				t.Error(err)
				return
			}
			if bind.Action != protocol.ActionSessionBind {
				t.Errorf("binding action = %q, want session.bind", bind.Action)
				return
			}
			bindData, decodeErr := protocol.DecodeData[protocol.SessionBindData](bind)
			if decodeErr != nil || bindData.SessionID != sessionID {
				t.Errorf("binding = %#v, error = %v, want session %s", bindData, decodeErr, sessionID)
				return
			}
			bound := mustMessage(t, protocol.ActionSessionBound, map[string]any{"session_id": sessionID, "status": "active"})
			if err := wsjson.Write(request.Context(), connection, bound); err != nil {
				t.Error(err)
				return
			}
		}
		ping := mustMessage(t, protocol.ActionHeartbeatPing, map[string]any{})
		if err := wsjson.Write(request.Context(), connection, ping); err != nil {
			t.Error(err)
			return
		}
		var pong protocol.Message
		if err := wsjson.Read(request.Context(), connection, &pong); err != nil {
			t.Error(err)
			return
		}
		if pong.Action != protocol.ActionHeartbeatPong {
			t.Errorf("heartbeat response = %q, want heartbeat.pong", pong.Action)
			return
		}
		pongData, err := protocol.DecodeData[protocol.PongData](pong)
		if err != nil {
			t.Error(err)
			return
		}
		if pongData.ReplyTo != ping.ID {
			t.Errorf("heartbeat reply_to = %q, want %q", pongData.ReplyTo, ping.ID)
			return
		}
		delivery := mustMessage(t, protocol.ActionEventDeliver, protocol.DeliveryData{
			DeliveryID: "dlv_test",
			EventID:    "evt_test",
			Target:     json.RawMessage(`{"device_id":"dev_test","session_id":"ses_test"}`),
			Event:      json.RawMessage(`{"source":"test","name":"test.event","data":{}}`),
			Attempt:    1,
		})
		if err := wsjson.Write(request.Context(), connection, delivery); err != nil {
			t.Error(err)
			return
		}

		var ack protocol.Message
		if err := wsjson.Read(request.Context(), connection, &ack); err != nil {
			t.Error(err)
			return
		}
		acknowledgement <- ack
	}))
	defer server.Close()

	serviceURL := "ws" + strings.TrimPrefix(server.URL, "http")
	received := make([]string, 0, 2)
	err := Run(context.Background(), Options{
		ServiceURL: serviceURL,
		DeviceID:   "dev_test",
		Token:      "test-token",
		Version:    "test",
		Sessions: []SessionRegistration{
			{SessionID: "ses_first", Adapter: "codex", Metadata: map[string]any{}},
			{SessionID: "ses_test", Adapter: "codex", Metadata: map[string]any{}},
		},
		Once: true,
		Receive: func(message protocol.Message) error {
			received = append(received, message.Action)
			return nil
		},
		Handle: func(_ context.Context, _ protocol.DeliveryData) error { return nil },
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(received) != 4 || received[0] != protocol.ActionServerWelcome || received[1] != protocol.ActionSessionBound || received[2] != protocol.ActionSessionBound || received[3] != protocol.ActionEventDeliver {
		t.Fatalf("received actions = %#v", received)
	}

	ack := <-acknowledgement
	if ack.Action != protocol.ActionEventAck {
		t.Fatalf("ack action = %q", ack.Action)
	}
	data, err := protocol.DecodeData[protocol.AckData](ack)
	if err != nil {
		t.Fatal(err)
	}
	if data.DeliveryID != "dlv_test" || data.EventID != "evt_test" || data.Status != "completed" {
		t.Fatalf("ack data = %#v", data)
	}
}

func TestConcurrentDeliveryDoesNotBlockHeartbeat(t *testing.T) {
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer close(serverDone)
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()
		var hello protocol.Message
		if err := wsjson.Read(request.Context(), connection, &hello); err != nil {
			t.Error(err)
			return
		}
		if err := wsjson.Write(request.Context(), connection, mustMessage(t, protocol.ActionServerWelcome, map[string]any{"device_id": "dev_concurrent"})); err != nil {
			t.Error(err)
			return
		}
		var bind protocol.Message
		if err := wsjson.Read(request.Context(), connection, &bind); err != nil {
			t.Error(err)
			return
		}
		delivery := mustMessage(t, protocol.ActionEventDeliver, protocol.DeliveryData{
			DeliveryID: "dlv_concurrent", EventID: "evt_concurrent",
			Target: json.RawMessage(`{"device_id":"dev_concurrent","session_id":"ses_concurrent"}`),
			Event:  json.RawMessage(`{"source":"test","name":"slow.event","data":{}}`), Attempt: 1,
		})
		if err := wsjson.Write(request.Context(), connection, delivery); err != nil {
			t.Error(err)
			return
		}
		<-handlerStarted
		ping := mustMessage(t, protocol.ActionHeartbeatPing, map[string]any{})
		if err := wsjson.Write(request.Context(), connection, ping); err != nil {
			t.Error(err)
			return
		}
		var pong protocol.Message
		if err := wsjson.Read(request.Context(), connection, &pong); err != nil {
			t.Error(err)
			return
		}
		if pong.Action != protocol.ActionHeartbeatPong {
			t.Errorf("action while handler blocked = %s, want heartbeat.pong", pong.Action)
			return
		}
		close(releaseHandler)
		var ack protocol.Message
		if err := wsjson.Read(request.Context(), connection, &ack); err != nil {
			t.Error(err)
			return
		}
		if ack.Action != protocol.ActionEventAck {
			t.Errorf("action after handler = %s, want event.ack", ack.Action)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	serviceURL := "ws" + strings.TrimPrefix(server.URL, "http")
	err := Run(ctx, Options{
		ServiceURL: serviceURL, DeviceID: "dev_concurrent",
		Token: "test", Version: "test", Concurrent: true,
		Sessions: []SessionRegistration{{SessionID: "ses_concurrent", Adapter: "codex"}},
		Handle: func(context.Context, protocol.DeliveryData) error {
			close(handlerStarted)
			<-releaseHandler
			return nil
		},
	})
	cancel()
	<-serverDone
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	if !strings.Contains(err.Error(), "read AWP message") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunStopsAfterProviderPermissionRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()
		var hello protocol.Message
		if err := wsjson.Read(request.Context(), connection, &hello); err != nil {
			t.Error(err)
			return
		}
		helloData, err := protocol.DecodeData[protocol.ClientHelloData](hello)
		if err != nil {
			t.Error(err)
			return
		}
		if !helloData.Capabilities.Permissions {
			t.Error("client did not advertise permission capability")
			return
		}
		if err := wsjson.Write(request.Context(), connection, mustMessage(t, protocol.ActionServerWelcome, map[string]any{"device_id": "dev_permissions"})); err != nil {
			t.Error(err)
			return
		}
		var bind protocol.Message
		if err := wsjson.Read(request.Context(), connection, &bind); err != nil {
			t.Error(err)
			return
		}
		if err := wsjson.Write(request.Context(), connection, mustMessage(t, protocol.ActionSessionBound, map[string]any{"session_id": "ses_permissions", "status": "active"})); err != nil {
			t.Error(err)
			return
		}
		requestData := protocol.PermissionRequestData{RequestID: "req_test", SessionID: "ses_permissions", Permissions: []protocol.PermissionRequestItem{{ID: "runtime.wake", Title: "Wake", Risk: "runtime", Delegation: "background"}}}
		if err := wsjson.Write(request.Context(), connection, mustMessage(t, protocol.ActionPermissionRequest, requestData)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	received := false
	err := Run(context.Background(), Options{ServiceURL: "ws" + strings.TrimPrefix(server.URL, "http"), DeviceID: "dev_permissions", Version: "test", Sessions: []SessionRegistration{{SessionID: "ses_permissions", Adapter: "codex"}}, StopAfterPermissionRequest: true, Receive: func(message protocol.Message) error {
		if message.Action == protocol.ActionPermissionRequest {
			received = true
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !received {
		t.Fatal("permission request was not received")
	}
}

func mustMessage(t *testing.T, action string, data any) protocol.Message {
	t.Helper()
	message, err := protocol.New(action, data)
	if err != nil {
		t.Fatal(err)
	}
	return message
}
