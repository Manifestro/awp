package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Manifestro/awp/internal/autostart"
	"github.com/Manifestro/awp/internal/config"
	permissionstore "github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
	"github.com/Manifestro/awp/internal/wake"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestConfigSetJSONErrorReturnsFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "config.json")

	exitCode := Run([]string{
		"config", "set",
		"--provider", "example",
		"--service-url", "ws://example.com/awp",
		"--device-id", "dev_test",
		"--token-env", "EXAMPLE_TOKEN",
		"--config", path,
		"--json",
	}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("Run() exit code = %d, want 1", exitCode)
	}
	var response struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if response.OK {
		t.Fatal("JSON output reported success for invalid configuration")
	}
}

func TestDaemonConnectsEveryProviderIndependently(t *testing.T) {
	newProvider := func() (*httptest.Server, <-chan string) {
		bound := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			connection, err := websocket.Accept(writer, request, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer connection.CloseNow()
			var hello protocol.Message
			if err := wsjson.Read(request.Context(), connection, &hello); err != nil {
				return
			}
			welcome, err := protocol.New(protocol.ActionServerWelcome, map[string]any{"device_id": "dev_multi_provider"})
			if err != nil {
				t.Error(err)
				return
			}
			if err := wsjson.Write(request.Context(), connection, welcome); err != nil {
				return
			}
			var binding protocol.Message
			if err := wsjson.Read(request.Context(), connection, &binding); err != nil {
				return
			}
			data, err := protocol.DecodeData[protocol.SessionBindData](binding)
			if err != nil {
				t.Error(err)
				return
			}
			bound <- data.SessionID
			<-request.Context().Done()
		}))
		return server, bound
	}
	firstServer, firstBound := newProvider()
	defer firstServer.Close()
	secondServer, secondBound := newProvider()
	defer secondServer.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	cfg := config.Config{Version: config.Version, DeviceID: "dev_multi_provider", Providers: map[string]config.Provider{
		"first":  {ServiceURL: "ws" + strings.TrimPrefix(firstServer.URL, "http"), TokenEnv: "FIRST_TOKEN"},
		"second": {ServiceURL: "ws" + strings.TrimPrefix(secondServer.URL, "http"), TokenEnv: "SECOND_TOKEN"},
	}}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	for _, binding := range []sessions.Binding{
		{Provider: "first", SessionID: "ses_first", Adapter: "codex", RuntimeSessionID: "runtime_first"},
		{Provider: "second", SessionID: "ses_second", Adapter: "codex", RuntimeSessionID: "runtime_second"},
	} {
		if _, err := sessions.Bind(&registry, binding); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.Save(storePath, registry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIRST_TOKEN", "first-secret")
	t.Setenv("SECOND_TOKEN", "second-secret")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"daemon", "--config", configPath, "--store", storePath, "--timeout", "250ms", "--json"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), `"code":"timeout"`) {
		t.Fatalf("daemon code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for name, channel := range map[string]<-chan string{"first": firstBound, "second": secondBound} {
		select {
		case sessionID := <-channel:
			if sessionID != "ses_"+name {
				t.Fatalf("provider %s bound session %s", name, sessionID)
			}
		case <-time.After(time.Second):
			t.Fatalf("provider %s did not receive its independent binding", name)
		}
	}
}

func TestSessionsBindListAndRemove(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	store := filepath.Join(t.TempDir(), "sessions.json")
	workspace := t.TempDir()
	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_test", Providers: map[string]config.Provider{"example": {ServiceURL: "wss://example.com/awp", TokenEnv: "EXAMPLE_TOKEN"}}}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "bind",
			args: []string{"sessions", "bind", "--provider", "example", "--session-id", "ses_test", "--adapter", "codex", "--runtime-session-id", "runtime_test", "--workspace", workspace, "--config", configPath, "--store", store, "--json"},
		},
		{
			name: "list",
			args: []string{"sessions", "list", "--provider", "example", "--store", store, "--json"},
		},
		{
			name: "remove",
			args: []string{"sessions", "remove", "--provider", "example", "--session-id", "ses_test", "--store", store, "--json"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := Run(test.args, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("Run() exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			var response struct {
				OK bool `json:"ok"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("invalid JSON output: %v", err)
			}
			if !response.OK {
				t.Fatalf("command reported failure: %s", stdout.String())
			}
		})
	}
}

func TestSessionsRemoveAll(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	registry := sessions.NewRegistry()
	for _, binding := range []sessions.Binding{
		{Provider: "example", SessionID: "ses_a", Adapter: "codex", RuntimeSessionID: "runtime_a"},
		{Provider: "example", SessionID: "ses_b", Adapter: "codex", RuntimeSessionID: "runtime_b"},
		{Provider: "other", SessionID: "ses_c", Adapter: "codex", RuntimeSessionID: "runtime_c"},
	} {
		if _, err := sessions.Bind(&registry, binding); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.Save(storePath, registry); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"sessions", "remove", "--all", "--provider", "example", "--store", storePath, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var response struct {
		Data struct {
			Count int `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Count != 2 {
		t.Fatalf("removed count = %d, want 2", response.Data.Count)
	}

	loaded, err := sessions.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions.List(loaded, "")) != 1 {
		t.Fatalf("remaining bindings = %#v", sessions.List(loaded, ""))
	}
	if _, found := sessions.Get(loaded, "other", "ses_c"); !found {
		t.Fatal("--all with --provider removed a binding from a different provider")
	}
}

func TestSessionsRemoveAllRejectsSessionID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sessions", "remove", "--all", "--session-id", "ses_a", "--json"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("--all and --session-id together were accepted")
	}
}

func TestAutostartEnableIsOptInAndEditable(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_test", Providers: map[string]config.Provider{"example": {ServiceURL: "wss://example.com/awp", TokenEnv: "AWP_TEST_TOKEN"}}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "example", SessionID: "ses_test", Adapter: "codex", RuntimeSessionID: "runtime_test"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(storePath, registry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWP_TEST_TOKEN", "secret-test-token")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"autostart", "enable", "--config", configPath, "--store", storePath, "--directory", directory, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("enable code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	manifest := autostart.Filename(directory)
	contents, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "<string>daemon</string>") || strings.Contains(string(contents), "--session-id") {
		t.Fatalf("manifest does not run one multi-session daemon: %s", contents)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"autostart", "disable", "--config", configPath, "--store", storePath, "--directory", directory, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("disable code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(manifest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest still exists: %v", err)
	}
}

func TestPermissionsGrantListAndRevoke(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	sessionPath := filepath.Join(directory, "sessions.json")
	permissionPath := filepath.Join(directory, "permissions.json")
	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_test", Providers: map[string]config.Provider{"sinores": {ServiceURL: "wss://sinores.net/awp", TokenEnv: "SINORES_TOKEN"}}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "sinores", SessionID: "ses_test", Adapter: "codex", RuntimeSessionID: "runtime_test"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionPath, registry); err != nil {
		t.Fatal(err)
	}
	permissionState := permissionstore.NewStore()
	_, err := permissionstore.RecordRequest(&permissionState, permissionstore.Request{Provider: "sinores", SessionID: "ses_test", RequestID: "req_test", Permissions: []permissionstore.RequestedPermission{{ID: permissionstore.RuntimeWake, Title: "Wake", Risk: "runtime", Delegation: permissionstore.DelegationBackground}, {ID: "messages.read_new", Title: "Read", Risk: "read", Delegation: permissionstore.DelegationBackground, MCPTools: []string{"get_new_messages"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := permissionstore.Save(permissionPath, permissionState); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"permissions", "grant", "--provider", "sinores", "--session-id", "ses_test", "--allow", "runtime.wake,messages.read_new", "--config", configPath, "--store", sessionPath, "--permissions-store", permissionPath, "--json"},
		{"permissions", "list", "--provider", "sinores", "--permissions-store", permissionPath, "--json"},
		{"permissions", "revoke", "--provider", "sinores", "--session-id", "ses_test", "--permissions-store", permissionPath, "--json"},
	}
	for _, args := range commands {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestPermissionsRequestPreflight(t *testing.T) {
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
		if err := wsjson.Write(request.Context(), connection, mustCLIMessage(t, protocol.ActionServerWelcome, map[string]any{"device_id": "dev_preflight"})); err != nil {
			t.Error(err)
			return
		}
		var bind protocol.Message
		if err := wsjson.Read(request.Context(), connection, &bind); err != nil {
			t.Error(err)
			return
		}
		if err := wsjson.Write(request.Context(), connection, mustCLIMessage(t, protocol.ActionSessionBound, map[string]any{"session_id": "ses_preflight", "status": "active"})); err != nil {
			t.Error(err)
			return
		}
		data := protocol.PermissionRequestData{RequestID: "req_preflight", SessionID: "ses_preflight", Permissions: []protocol.PermissionRequestItem{{ID: permissionstore.RuntimeWake, Title: "Wake", Risk: "runtime", Delegation: permissionstore.DelegationBackground}}}
		if err := wsjson.Write(request.Context(), connection, mustCLIMessage(t, protocol.ActionPermissionRequest, data)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	sessionPath := filepath.Join(directory, "sessions.json")
	permissionPath := filepath.Join(directory, "permissions.json")
	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_preflight", Providers: map[string]config.Provider{"example": {ServiceURL: "ws" + strings.TrimPrefix(server.URL, "http"), TokenEnv: "PREFLIGHT_TOKEN", MCPServer: "none"}}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "example", SessionID: "ses_preflight", Adapter: "codex", RuntimeSessionID: "runtime"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionPath, registry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PREFLIGHT_TOKEN", "secret")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"permissions", "request", "--provider", "example", "--session-id", "ses_preflight", "--config", configPath, "--store", sessionPath, "--permissions-store", permissionPath, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	state, err := permissionstore.Load(permissionPath)
	if err != nil {
		t.Fatal(err)
	}
	request, found := permissionstore.GetRequest(state, "example", "ses_preflight")
	if !found || request.RequestID != "req_preflight" {
		t.Fatalf("request=%#v found=%t", request, found)
	}
}

func TestPermissionsGrantWithoutProviderRequestGrantsLocally(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	permissionPath := filepath.Join(t.TempDir(), "permissions.json")
	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_test", Providers: map[string]config.Provider{"example": {ServiceURL: "wss://example.com/awp", TokenEnv: "EXAMPLE_TOKEN"}}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "example", SessionID: "ses_local", Adapter: "codex", RuntimeSessionID: "runtime_local"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionPath, registry); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"permissions", "grant",
		"--provider", "example", "--session-id", "ses_local",
		"--allow", "runtime.wake,messages.read_new", "--mcp-tools", "get_new_messages",
		"--config", configPath, "--store", sessionPath, "--permissions-store", permissionPath, "--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	state, err := permissionstore.Load(permissionPath)
	if err != nil {
		t.Fatal(err)
	}
	request, found := permissionstore.GetRequest(state, "example", "ses_local")
	if !found || request.RequestID != permissionstore.LocalRequestID {
		t.Fatalf("request=%#v found=%t", request, found)
	}
	authorization, err := permissionstore.Authorize(&state, "example", "ses_local", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorization.MCPTools) != 1 || authorization.MCPTools[0] != "get_new_messages" {
		t.Fatalf("authorization = %#v", authorization)
	}
}

func TestDaemonSkipsAlreadyCompletedEventRedelivery(t *testing.T) {
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
		if err := wsjson.Write(request.Context(), connection, mustCLIMessage(t, protocol.ActionServerWelcome, map[string]any{"device_id": "dev_dedup"})); err != nil {
			t.Error(err)
			return
		}
		var bind protocol.Message
		if err := wsjson.Read(request.Context(), connection, &bind); err != nil {
			t.Error(err)
			return
		}
		// The provider resends an event it already delivered and got a
		// "completed" ack for, e.g. because its own retry logic is buggy.
		delivery := mustCLIMessage(t, protocol.ActionEventDeliver, protocol.DeliveryData{
			DeliveryID: "del_resend",
			EventID:    "evt_dup",
			Target:     json.RawMessage(`{"device_id":"dev_dedup","session_id":"ses_dedup"}`),
			Event:      json.RawMessage(`{"source":"test","name":"test.event","data":{}}`),
			Attempt:    2,
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

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	sessionPath := filepath.Join(directory, "sessions.json")
	permissionPath := filepath.Join(directory, "permissions.json")
	eventPath := filepath.Join(directory, "events.json")

	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_dedup", Providers: map[string]config.Provider{
		"dedup_test": {ServiceURL: "ws" + strings.TrimPrefix(server.URL, "http"), TokenEnv: "DEDUP_TOKEN"},
	}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "dedup_test", SessionID: "ses_dedup", Adapter: "codex", RuntimeSessionID: "runtime_dedup"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionPath, registry); err != nil {
		t.Fatal(err)
	}

	// Pre-seed the event store as if a previous daemon run already completed
	// this exact event_id.
	eventState := wake.NewStore()
	if _, _, err := wake.RecordDelivery(&eventState, "dedup_test", "ses_dedup", "evt_dup", "del_original", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := wake.CompleteEvent(&eventState, "dedup_test", "ses_dedup", "evt_dup", wake.EventStatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := wake.Save(eventPath, eventState); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DEDUP_TOKEN", "secret")
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"daemon",
		"--config", configPath,
		"--store", sessionPath,
		"--permissions-store", permissionPath,
		"--events-store", eventPath,
		"--once",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	ack := <-acknowledgement
	data, err := protocol.DecodeData[protocol.AckData](ack)
	if err != nil {
		t.Fatal(err)
	}
	if data.Status != "completed" {
		t.Fatalf("ack status = %q, want completed", data.Status)
	}
	if !strings.Contains(stderr.String(), "already processed, skipping wake") {
		t.Fatalf("stderr = %s, expected a dedup skip log line", stderr.String())
	}

	// The adapter/permission path must never have been reached for a
	// duplicate: it requires a granted runtime.wake permission that was
	// never set up, so if it ran, this daemon call would have created the
	// permissions store while attempting (and failing) to authorize.
	if _, err := os.Stat(permissionPath); !os.IsNotExist(err) {
		t.Fatalf("permissions store exists (err=%v): duplicate event reached the adapter path", err)
	}
}

func TestDaemonHoldsDeliveryForPausedSession(t *testing.T) {
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
		if err := wsjson.Write(request.Context(), connection, mustCLIMessage(t, protocol.ActionServerWelcome, map[string]any{"device_id": "dev_pause"})); err != nil {
			t.Error(err)
			return
		}
		var bind protocol.Message
		if err := wsjson.Read(request.Context(), connection, &bind); err != nil {
			t.Error(err)
			return
		}
		delivery := mustCLIMessage(t, protocol.ActionEventDeliver, protocol.DeliveryData{
			DeliveryID: "del_1",
			EventID:    "evt_1",
			Target:     json.RawMessage(`{"device_id":"dev_pause","session_id":"ses_paused"}`),
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

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	sessionPath := filepath.Join(directory, "sessions.json")
	permissionPath := filepath.Join(directory, "permissions.json")
	eventPath := filepath.Join(directory, "events.json")

	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_pause", Providers: map[string]config.Provider{
		"pause_test": {ServiceURL: "ws" + strings.TrimPrefix(server.URL, "http"), TokenEnv: "PAUSE_TOKEN"},
	}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "pause_test", SessionID: "ses_paused", Adapter: "codex", RuntimeSessionID: "runtime_paused"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionPath, registry); err != nil {
		t.Fatal(err)
	}

	// The human (or the agent itself) deliberately paused this session, e.g.
	// because they are working on it by hand right now.
	eventState := wake.NewStore()
	wake.Pause(&eventState, "pause_test", "ses_paused", "working on this by hand")
	if err := wake.Save(eventPath, eventState); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PAUSE_TOKEN", "secret")
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"daemon",
		"--config", configPath,
		"--store", sessionPath,
		"--permissions-store", permissionPath,
		"--events-store", eventPath,
		"--once",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	ack := <-acknowledgement
	data, err := protocol.DecodeData[protocol.AckData](ack)
	if err != nil {
		t.Fatal(err)
	}
	if data.Status != "failed" {
		t.Fatalf("ack status = %q, want failed (held, not processed)", data.Status)
	}
	if !strings.Contains(stderr.String(), "held: session is paused") {
		t.Fatalf("stderr = %s, expected a paused-session hold log line", stderr.String())
	}
	// A paused session must never reach the permission/adapter path.
	if _, err := os.Stat(permissionPath); !os.IsNotExist(err) {
		t.Fatalf("permissions store exists (err=%v): paused session reached the adapter path", err)
	}

	loaded, err := wake.Load(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	state, found := wake.Get(loaded, "pause_test", "ses_paused")
	if !found {
		t.Fatal("session state disappeared")
	}
	pending := wake.Pending(state)
	if len(pending) != 1 || pending[0].EventID != "evt_1" {
		t.Fatalf("held event was not preserved as pending: %#v", state.Events)
	}
}

func TestDaemonMarksSessionCrashedOnStructuralAdapterFailure(t *testing.T) {
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
		if err := wsjson.Write(request.Context(), connection, mustCLIMessage(t, protocol.ActionServerWelcome, map[string]any{"device_id": "dev_crash"})); err != nil {
			t.Error(err)
			return
		}
		var bind protocol.Message
		if err := wsjson.Read(request.Context(), connection, &bind); err != nil {
			t.Error(err)
			return
		}
		delivery := mustCLIMessage(t, protocol.ActionEventDeliver, protocol.DeliveryData{
			DeliveryID: "del_1",
			EventID:    "evt_1",
			Target:     json.RawMessage(`{"device_id":"dev_crash","session_id":"ses_crash"}`),
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

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	sessionPath := filepath.Join(directory, "sessions.json")
	permissionPath := filepath.Join(directory, "permissions.json")
	eventPath := filepath.Join(directory, "events.json")

	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_crash", Providers: map[string]config.Provider{
		"crash_test": {ServiceURL: "ws" + strings.TrimPrefix(server.URL, "http"), TokenEnv: "CRASH_TOKEN", MCPServer: "none"},
	}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "crash_test", SessionID: "ses_crash", Adapter: "codex", RuntimeSessionID: "runtime_crash", Workspace: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionPath, registry); err != nil {
		t.Fatal(err)
	}

	permissionState := permissionstore.NewStore()
	if _, err := permissionstore.RecordRequest(&permissionState, permissionstore.Request{
		Provider: "crash_test", SessionID: "ses_crash", RequestID: "req_1",
		Permissions: []permissionstore.RequestedPermission{{ID: permissionstore.RuntimeWake, Title: "Wake", Risk: "runtime", Delegation: permissionstore.DelegationBackground}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := permissionstore.GrantPermissions(&permissionState, "crash_test", "ses_crash", permissionstore.ScopeBinding, []string{permissionstore.RuntimeWake}); err != nil {
		t.Fatal(err)
	}
	if err := permissionstore.Save(permissionPath, permissionState); err != nil {
		t.Fatal(err)
	}

	// Make the codex binary unresolvable so adapter.Run fails structurally
	// (ErrBindingUnusable), regardless of what is actually installed on the
	// machine running this test.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CRASH_TOKEN", "secret")

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"daemon",
		"--config", configPath,
		"--store", sessionPath,
		"--permissions-store", permissionPath,
		"--events-store", eventPath,
		"--once",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	ack := <-acknowledgement
	data, err := protocol.DecodeData[protocol.AckData](ack)
	if err != nil {
		t.Fatal(err)
	}
	if data.Status != "failed" {
		t.Fatalf("ack status = %q, want failed", data.Status)
	}

	loaded, err := wake.Load(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	state, found := wake.Get(loaded, "crash_test", "ses_crash")
	if !found {
		t.Fatal("session state was not recorded")
	}
	if state.Lifecycle.Status != wake.StatusCrashed {
		t.Fatalf("lifecycle = %#v, want crashed", state.Lifecycle)
	}
}

func mustCLIMessage(t *testing.T, action string, data any) protocol.Message {
	t.Helper()
	message, err := protocol.New(action, data)
	if err != nil {
		t.Fatal(err)
	}
	return message
}
