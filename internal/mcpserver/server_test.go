package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Manifestro/awp/internal/adapters"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/daemonctl"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
	"github.com/Manifestro/awp/internal/wake"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func mustMCPMessage(t *testing.T, action string, data any) protocol.Message {
	t.Helper()
	message, err := protocol.New(action, data)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func requestLine(t *testing.T, id int, method string, params any) string {
	t.Helper()
	encodedParams, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  json.RawMessage(encodedParams),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

type toolResult struct {
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
}

func runLines(t *testing.T, deps Dependencies, lines []string) []toolResult {
	t.Helper()
	var stdout bytes.Buffer
	if err := Run(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &stdout, deps); err != nil {
		t.Fatal(err)
	}
	results := make([]toolResult, 0, len(lines))
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		var result toolResult
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			t.Fatalf("invalid response line %q: %v", scanner.Text(), err)
		}
		results = append(results, result)
	}
	return results
}

func setup(t *testing.T) Dependencies {
	t.Helper()
	directory := t.TempDir()
	workspace := t.TempDir()
	sessionsPath := filepath.Join(directory, "sessions.json")
	eventsPath := filepath.Join(directory, "events.json")

	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{
		Provider: "sinores", SessionID: "ses_test", Adapter: "codex", RuntimeSessionID: "runtime_test", Workspace: workspace,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionsPath, registry); err != nil {
		t.Fatal(err)
	}

	store := wake.NewStore()
	if _, _, err := wake.RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{"source":"test","name":"test.event"}`)); err != nil {
		t.Fatal(err)
	}
	wake.SetPermissions(&store, "sinores", "ses_test", []string{"messages.read_new"})
	if err := wake.Save(eventsPath, store); err != nil {
		t.Fatal(err)
	}

	return Dependencies{SessionsPath: sessionsPath, EventsPath: eventsPath, Workspace: workspace}
}

func TestWakeContextAutoDetectsSessionFromWorkspace(t *testing.T) {
	deps := setup(t)
	results := runLines(t, deps, []string{
		requestLine(t, 1, "initialize", map[string]any{}),
		requestLine(t, 2, "tools/call", map[string]any{"name": "wake_context", "arguments": map[string]any{}}),
	})
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if results[1].Result.IsError {
		t.Fatalf("wake_context returned an error: %s", results[1].Result.Content[0].Text)
	}
	var context wakeContextResult
	if err := json.Unmarshal([]byte(results[1].Result.Content[0].Text), &context); err != nil {
		t.Fatal(err)
	}
	if context.Provider != "sinores" || context.SessionID != "ses_test" {
		t.Fatalf("context = %#v", context)
	}
	if len(context.Pending) != 1 || context.Pending[0].EventID != "evt_1" {
		t.Fatalf("pending = %#v", context.Pending)
	}
	if string(context.Pending[0].Event) == "" {
		t.Fatal("pending event lost its payload")
	}
	if len(context.Permissions) != 1 || context.Permissions[0] != "messages.read_new" {
		t.Fatalf("permissions = %#v", context.Permissions)
	}
}

func TestToolsListAdvertisesAllTools(t *testing.T) {
	deps := setup(t)
	var stdout bytes.Buffer
	if err := Run(context.Background(), strings.NewReader(requestLine(t, 1, "tools/list", map[string]any{})+"\n"), &stdout, deps); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range response.Result.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"wake_context", "list_pending_events", "list_sessions", "pause_session", "resume_session", "set_awp",
		"configure_provider", "request_permissions", "grant_permissions", "start_daemon", "stop_daemon", "daemon_status",
	} {
		if !names[want] {
			t.Fatalf("tools/list did not advertise %q: %#v", want, response.Result.Tools)
		}
	}
}

func TestPauseSessionGatesFutureDeliveryAndResumeClearsIt(t *testing.T) {
	deps := setup(t)
	results := runLines(t, deps, []string{
		requestLine(t, 1, "tools/call", map[string]any{"name": "pause_session", "arguments": map[string]any{"reason": "working by hand"}}),
		requestLine(t, 2, "tools/call", map[string]any{"name": "wake_context", "arguments": map[string]any{}}),
		requestLine(t, 3, "tools/call", map[string]any{"name": "resume_session", "arguments": map[string]any{}}),
		requestLine(t, 4, "tools/call", map[string]any{"name": "wake_context", "arguments": map[string]any{}}),
	})
	if len(results) != 4 {
		t.Fatalf("results = %#v", results)
	}

	var afterPause wakeContextResult
	if err := json.Unmarshal([]byte(results[1].Result.Content[0].Text), &afterPause); err != nil {
		t.Fatal(err)
	}
	if afterPause.Lifecycle.Status != wake.StatusPaused {
		t.Fatalf("lifecycle after pause = %#v", afterPause.Lifecycle)
	}
	if afterPause.Lifecycle.Reason != "working by hand" {
		t.Fatalf("pause reason = %q", afterPause.Lifecycle.Reason)
	}

	var afterResume wakeContextResult
	if err := json.Unmarshal([]byte(results[3].Result.Content[0].Text), &afterResume); err != nil {
		t.Fatal(err)
	}
	if afterResume.Lifecycle.Status != wake.StatusIdle {
		t.Fatalf("lifecycle after resume = %#v", afterResume.Lifecycle)
	}
}

func TestAmbiguousWorkspaceRequiresExplicitSession(t *testing.T) {
	directory := t.TempDir()
	workspace := t.TempDir()
	sessionsPath := filepath.Join(directory, "sessions.json")
	eventsPath := filepath.Join(directory, "events.json")

	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "sinores", SessionID: "ses_a", Adapter: "codex", RuntimeSessionID: "runtime_a", Workspace: workspace}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "sinores", SessionID: "ses_b", Adapter: "codex", RuntimeSessionID: "runtime_b", Workspace: workspace}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionsPath, registry); err != nil {
		t.Fatal(err)
	}
	if err := wake.Save(eventsPath, wake.NewStore()); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{SessionsPath: sessionsPath, EventsPath: eventsPath, Workspace: workspace}

	results := runLines(t, deps, []string{
		requestLine(t, 1, "tools/call", map[string]any{"name": "wake_context", "arguments": map[string]any{}}),
	})
	if !results[0].Result.IsError {
		t.Fatal("ambiguous workspace did not report an error")
	}
	if !strings.Contains(results[0].Result.Content[0].Text, "ses_a") || !strings.Contains(results[0].Result.Content[0].Text, "ses_b") {
		t.Fatalf("ambiguity error did not list candidates: %s", results[0].Result.Content[0].Text)
	}
}

func TestSetAWPRegistersNewBinding(t *testing.T) {
	directory := t.TempDir()
	workspace := t.TempDir()
	sessionsPath := filepath.Join(directory, "sessions.json")
	eventsPath := filepath.Join(directory, "events.json")
	if err := wake.Save(eventsPath, wake.NewStore()); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{SessionsPath: sessionsPath, EventsPath: eventsPath, Workspace: workspace}

	results := runLines(t, deps, []string{
		requestLine(t, 1, "tools/call", map[string]any{"name": "set_awp", "arguments": map[string]any{
			"provider":           "sinores",
			"session_id":         "ses_new",
			"adapter":            "claude-code",
			"runtime_session_id": "c2b56ae3-6502-4633-9f9b-0fd063c1cde5",
			"resume_command":     []string{"claude", "-r", "{runtime_session_id}", "-p", "{prompt}"},
		}}),
	})
	if len(results) != 1 || results[0].Result.IsError {
		t.Fatalf("results = %#v", results)
	}

	registry, err := sessions.Load(sessionsPath)
	if err != nil {
		t.Fatal(err)
	}
	binding, found := sessions.Get(registry, "sinores", "ses_new")
	if !found {
		t.Fatal("set_awp did not persist the binding")
	}
	if binding.RuntimeSessionID != "c2b56ae3-6502-4633-9f9b-0fd063c1cde5" {
		t.Fatalf("runtime_session_id = %q", binding.RuntimeSessionID)
	}
	if len(binding.ResumeCommand) != 5 {
		t.Fatalf("resume_command = %#v", binding.ResumeCommand)
	}
	if binding.Workspace != workspace {
		t.Fatalf("workspace = %q, want auto-detected %q", binding.Workspace, workspace)
	}
}

func TestSetAWPRequiresProviderAndSessionIDForNewBinding(t *testing.T) {
	directory := t.TempDir()
	sessionsPath := filepath.Join(directory, "sessions.json")
	eventsPath := filepath.Join(directory, "events.json")
	if err := sessions.Save(sessionsPath, sessions.NewRegistry()); err != nil {
		t.Fatal(err)
	}
	if err := wake.Save(eventsPath, wake.NewStore()); err != nil {
		t.Fatal(err)
	}
	// Nothing is bound to this workspace yet, so there is nothing to
	// auto-detect: provider/session_id must be explicit.
	deps := Dependencies{SessionsPath: sessionsPath, EventsPath: eventsPath, Workspace: t.TempDir()}

	results := runLines(t, deps, []string{
		requestLine(t, 1, "tools/call", map[string]any{"name": "set_awp", "arguments": map[string]any{
			"runtime_session_id": "runtime_x",
			"resume_command":     []string{"claude", "-r", "{runtime_session_id}"},
		}}),
	})
	if !results[0].Result.IsError {
		t.Fatal("set_awp created a binding without provider/session_id and no existing binding to update")
	}
}

func TestSetAWPBindingRunsThroughCommandAdapter(t *testing.T) {
	directory := t.TempDir()
	workspace := t.TempDir()
	sessionsPath := filepath.Join(directory, "sessions.json")
	eventsPath := filepath.Join(directory, "events.json")
	if err := wake.Save(eventsPath, wake.NewStore()); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{SessionsPath: sessionsPath, EventsPath: eventsPath, Workspace: workspace}

	results := runLines(t, deps, []string{
		requestLine(t, 1, "tools/call", map[string]any{"name": "set_awp", "arguments": map[string]any{
			"provider":           "sinores",
			"session_id":         "ses_new",
			"adapter":            "claude-code",
			"runtime_session_id": "runtime_x",
			"resume_command":     []string{"true"},
		}}),
	})
	if results[0].Result.IsError {
		t.Fatalf("set_awp failed: %s", results[0].Result.Content[0].Text)
	}

	registry, err := sessions.Load(sessionsPath)
	if err != nil {
		t.Fatal(err)
	}
	binding, found := sessions.Get(registry, "sinores", "ses_new")
	if !found {
		t.Fatal("binding not found")
	}
	resolved, err := adapters.Resolve(binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	authorization := permissions.Authorization{Permissions: []string{permissions.RuntimeWake}}
	delivery := protocol.DeliveryData{EventID: "evt_1", DeliveryID: "del_1", Event: json.RawMessage(`{"source":"test","name":"test.event"}`)}
	if err := resolved.Run(context.Background(), binding, delivery, authorization, "sinores"); err != nil {
		t.Fatalf("registered binding did not run through the command adapter: %v", err)
	}
}

func TestUnknownToolReturnsJSONRPCError(t *testing.T) {
	deps := setup(t)
	var stdout bytes.Buffer
	if err := Run(context.Background(), strings.NewReader(requestLine(t, 1, "tools/call", map[string]any{"name": "no_such_tool", "arguments": map[string]any{}})+"\n"), &stdout, deps); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil {
		t.Fatal("unknown tool did not produce a JSON-RPC error")
	}
}

func TestConfigureProviderWritesConfigAndProtectedTokenFile(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	deps := Dependencies{
		ConfigPath:   configPath,
		SessionsPath: filepath.Join(directory, "sessions.json"),
		EventsPath:   filepath.Join(directory, "events.json"),
	}
	if err := sessions.Save(deps.SessionsPath, sessions.NewRegistry()); err != nil {
		t.Fatal(err)
	}

	results := runLines(t, deps, []string{
		requestLine(t, 1, "tools/call", map[string]any{"name": "configure_provider", "arguments": map[string]any{
			"provider": "sinores", "service_url": "wss://api.sinores.net/awp", "token": "wa_test_token",
		}}),
	})
	if results[0].Result.IsError {
		t.Fatalf("configure_provider failed: %s", results[0].Result.Content[0].Text)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	provider, found := cfg.Providers["sinores"]
	if !found || provider.ServiceURL != "wss://api.sinores.net/awp" {
		t.Fatalf("provider = %#v found=%v", provider, found)
	}

	tokenPath, err := config.TokenPath(configPath, "sinores")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(contents)) != "wa_test_token" {
		t.Fatalf("token contents = %q", contents)
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %o, want 600", info.Mode().Perm())
	}
}

// grant_permissions must work even when the provider never sent a
// permission.request (most providers do not implement it): it synthesizes a
// local request covering exactly what was asked, so a session is never stuck
// unwakeable just because a provider skipped that handshake.
func TestGrantPermissionsSynthesizesLocalRequestWhenProviderNeverSentOne(t *testing.T) {
	deps := setup(t)
	permissionPath := filepath.Join(t.TempDir(), "permissions.json")
	deps.PermissionsPath = permissionPath

	results := runLines(t, deps, []string{
		requestLine(t, 1, "tools/call", map[string]any{"name": "grant_permissions", "arguments": map[string]any{
			"allow": []string{permissions.RuntimeWake, "messages.read_new"}, "mcp_tools": []string{"get_new_messages"},
		}}),
	})
	if results[0].Result.IsError {
		t.Fatalf("grant_permissions failed: %s", results[0].Result.Content[0].Text)
	}

	loaded, err := permissions.Load(permissionPath)
	if err != nil {
		t.Fatal(err)
	}
	request, found := permissions.GetRequest(loaded, "sinores", "ses_test")
	if !found || request.RequestID != permissions.LocalRequestID {
		t.Fatalf("request = %#v found=%v", request, found)
	}
	authorization, err := permissions.Authorize(&loaded, "sinores", "ses_test", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorization.MCPTools) != 1 || authorization.MCPTools[0] != "get_new_messages" {
		t.Fatalf("authorization = %#v", authorization)
	}
}

func TestGrantPermissionsGrantsRequestedPermission(t *testing.T) {
	deps := setup(t)
	permissionPath := filepath.Join(t.TempDir(), "permissions.json")
	deps.PermissionsPath = permissionPath

	store := permissions.NewStore()
	if _, err := permissions.RecordRequest(&store, permissions.Request{
		Provider: "sinores", SessionID: "ses_test", RequestID: "req_1",
		Permissions: []permissions.RequestedPermission{{ID: permissions.RuntimeWake, Title: "Wake", Risk: "runtime", Delegation: permissions.DelegationBackground}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := permissions.Save(permissionPath, store); err != nil {
		t.Fatal(err)
	}

	results := runLines(t, deps, []string{
		requestLine(t, 1, "tools/call", map[string]any{"name": "grant_permissions", "arguments": map[string]any{"allow": []string{permissions.RuntimeWake}}}),
	})
	if results[0].Result.IsError {
		t.Fatalf("grant_permissions failed: %s", results[0].Result.Content[0].Text)
	}

	loaded, err := permissions.Load(permissionPath)
	if err != nil {
		t.Fatal(err)
	}
	grants := permissions.ListGrants(loaded, "sinores")
	if len(grants) != 1 {
		t.Fatalf("grants = %#v", grants)
	}
}

func TestRequestPermissionsCapturesProviderRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		var hello protocol.Message
		if err := wsjson.Read(request.Context(), connection, &hello); err != nil {
			return
		}
		helloData, err := protocol.DecodeData[protocol.ClientHelloData](hello)
		if err != nil {
			return
		}
		if err := wsjson.Write(request.Context(), connection, mustMCPMessage(t, protocol.ActionServerWelcome, map[string]any{"device_id": helloData.DeviceID})); err != nil {
			return
		}
		var bind protocol.Message
		if err := wsjson.Read(request.Context(), connection, &bind); err != nil {
			return
		}
		data := protocol.PermissionRequestData{RequestID: "req_1", SessionID: "ses_test", Permissions: []protocol.PermissionRequestItem{
			{ID: permissions.RuntimeWake, Title: "Wake", Risk: "runtime", Delegation: permissions.DelegationBackground},
		}}
		_ = wsjson.Write(request.Context(), connection, mustMCPMessage(t, protocol.ActionPermissionRequest, data))
	}))
	defer server.Close()

	directory := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	sessionsPath := filepath.Join(directory, "sessions.json")
	permissionPath := filepath.Join(directory, "permissions.json")

	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "sinores", SessionID: "ses_test", Adapter: "codex", RuntimeSessionID: "runtime_test", Workspace: workspace}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionsPath, registry); err != nil {
		t.Fatal(err)
	}

	deps := Dependencies{ConfigPath: configPath, SessionsPath: sessionsPath, PermissionsPath: permissionPath, Workspace: workspace, Version: "test"}

	results := runLines(t, deps, []string{
		requestLine(t, 1, "tools/call", map[string]any{"name": "configure_provider", "arguments": map[string]any{
			"provider": "sinores", "service_url": "ws" + strings.TrimPrefix(server.URL, "http"), "token": "test-token",
		}}),
		requestLine(t, 2, "tools/call", map[string]any{"name": "request_permissions", "arguments": map[string]any{"timeout_seconds": float64(5)}}),
	})
	if results[0].Result.IsError {
		t.Fatalf("configure_provider failed: %s", results[0].Result.Content[0].Text)
	}
	if results[1].Result.IsError {
		t.Fatalf("request_permissions failed: %s", results[1].Result.Content[0].Text)
	}
	var captured permissions.Request
	if err := json.Unmarshal([]byte(results[1].Result.Content[0].Text), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.RequestID != "req_1" || len(captured.Permissions) != 1 {
		t.Fatalf("captured = %#v", captured)
	}
}

var (
	builtMCPTestBinaryOnce sync.Once
	builtMCPTestBinaryPath string
	builtMCPTestBinaryErr  error
)

func buildAWPBinaryForMCPTest(t *testing.T) string {
	t.Helper()
	builtMCPTestBinaryOnce.Do(func() {
		path := filepath.Join(t.TempDir(), "awp-test-binary")
		command := exec.Command("go", "build", "-o", path, "github.com/Manifestro/awp/cmd/awp")
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			builtMCPTestBinaryErr = fmt.Errorf("build awp binary: %w: %s", err, stderr.String())
			return
		}
		builtMCPTestBinaryPath = path
	})
	if builtMCPTestBinaryErr != nil {
		t.Fatal(builtMCPTestBinaryErr)
	}
	return builtMCPTestBinaryPath
}

func TestStartStopDaemonStatusViaMCP(t *testing.T) {
	binary := buildAWPBinaryForMCPTest(t)
	original := daemonctl.ExecutablePath
	daemonctl.ExecutablePath = func() (string, error) { return binary, nil }
	defer func() { daemonctl.ExecutablePath = original }()

	connected := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		var hello protocol.Message
		if err := wsjson.Read(request.Context(), connection, &hello); err != nil {
			return
		}
		if err := wsjson.Write(request.Context(), connection, mustMCPMessage(t, protocol.ActionServerWelcome, map[string]any{"device_id": "dev_test"})); err != nil {
			return
		}
		select {
		case connected <- struct{}{}:
		default:
		}
		<-request.Context().Done()
	}))
	defer server.Close()

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	sessionsPath := filepath.Join(directory, "sessions.json")

	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_test", Providers: map[string]config.Provider{
		"sinores": {ServiceURL: "ws" + strings.TrimPrefix(server.URL, "http"), TokenEnv: "SINORES_AWP_TOKEN"},
	}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "sinores", SessionID: "ses_test", Adapter: "codex", RuntimeSessionID: "runtime_test"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(sessionsPath, registry); err != nil {
		t.Fatal(err)
	}
	tokenPath, err := config.TokenPath(configPath, "sinores")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveToken(tokenPath, "secret"); err != nil {
		t.Fatal(err)
	}

	deps := Dependencies{
		ConfigPath: configPath, SessionsPath: sessionsPath,
		EventsPath: filepath.Join(directory, "events.json"), PermissionsPath: filepath.Join(directory, "permissions.json"),
	}

	startResults := runLines(t, deps, []string{requestLine(t, 1, "tools/call", map[string]any{"name": "start_daemon", "arguments": map[string]any{}})})
	if startResults[0].Result.IsError {
		t.Fatalf("start_daemon errored: %s", startResults[0].Result.Content[0].Text)
	}
	var startPayload struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(startResults[0].Result.Content[0].Text), &startPayload); err != nil {
		t.Fatal(err)
	}
	if !startPayload.OK {
		t.Fatalf("start_daemon reported failure: %s", startResults[0].Result.Content[0].Text)
	}

	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon started via MCP never connected to the fake provider")
	}

	statusResults := runLines(t, deps, []string{requestLine(t, 1, "tools/call", map[string]any{"name": "daemon_status", "arguments": map[string]any{}})})
	if !strings.Contains(statusResults[0].Result.Content[0].Text, `"running": true`) {
		t.Fatalf("daemon_status = %s, want running", statusResults[0].Result.Content[0].Text)
	}

	stopResults := runLines(t, deps, []string{requestLine(t, 1, "tools/call", map[string]any{"name": "stop_daemon", "arguments": map[string]any{}})})
	if !strings.Contains(stopResults[0].Result.Content[0].Text, `"was_running": true`) {
		t.Fatalf("stop_daemon = %s, want was_running true", stopResults[0].Result.Content[0].Text)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, alive, err := daemonctl.Status(filepath.Join(directory, "daemon.pid")); err == nil && !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not stop after stop_daemon")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
