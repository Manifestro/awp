package command

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	execrunner "github.com/Manifestro/awp/internal/adapters/exec"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

type recordingRunner struct {
	command   string
	args      []string
	directory string
	stdin     string
}

func (runner *recordingRunner) Run(
	_ context.Context,
	command string,
	args []string,
	directory string,
	stdin []byte,
	_ io.Writer,
) error {
	runner.command = command
	runner.args = append([]string(nil), args...)
	runner.directory = directory
	runner.stdin = string(stdin)
	return nil
}

func TestAdapterSubstitutesPlaceholdersAndRunsConfiguredCommand(t *testing.T) {
	runner := &recordingRunner{}
	adapter := &Adapter{Output: io.Discard, Runner: runner}
	workspace := t.TempDir()
	binding := sessions.Binding{
		Adapter:          "claude-code",
		RuntimeSessionID: "runtime_test",
		Workspace:        workspace,
		ResumeCommand:    []string{"claude", "-r", "{runtime_session_id}", "-p", "{prompt}"},
	}
	delivery := protocol.DeliveryData{
		DeliveryID: "dlv_test",
		EventID:    "evt_test",
		Event:      json.RawMessage(`{"source":"monitoring","name":"alert.created","data":{"service":"api"}}`),
	}
	authorization := permissions.Authorization{Permissions: []string{permissions.RuntimeWake, "messages.read_new"}, MCPTools: []string{"get_new_messages"}}

	if err := adapter.Run(context.Background(), binding, delivery, authorization, "sinores"); err != nil {
		t.Fatal(err)
	}
	if runner.command != "claude" {
		t.Fatalf("command = %q, want claude", runner.command)
	}
	if len(runner.args) != 4 || runner.args[0] != "-r" || runner.args[1] != "runtime_test" || runner.args[2] != "-p" {
		t.Fatalf("args = %#v", runner.args)
	}
	if !strings.Contains(runner.args[3], `"source": "monitoring"`) {
		t.Fatalf("{prompt} placeholder was not substituted with the formatted event: %s", runner.args[3])
	}
	if !strings.Contains(runner.stdin, `"source": "monitoring"`) {
		t.Fatalf("prompt was not also piped over stdin: %s", runner.stdin)
	}
	if runner.directory != workspace {
		t.Fatalf("directory = %q, want %q", runner.directory, workspace)
	}
}

func TestAdapterFormatsPrefixedToolCSVForClaudeCodeAllowedTools(t *testing.T) {
	runner := &recordingRunner{}
	adapter := &Adapter{Output: io.Discard, Runner: runner}
	binding := sessions.Binding{
		Adapter:          "claude-code",
		RuntimeSessionID: "runtime_test",
		Workspace:        t.TempDir(),
		ResumeCommand:    []string{"claude", "-r", "{runtime_session_id}", "-p", "{prompt}", "--allowedTools", "{mcp_tools_prefixed_csv}"},
	}
	delivery := protocol.DeliveryData{EventID: "evt_test", DeliveryID: "dlv_test", Event: json.RawMessage(`{"source":"sinores","name":"message.received"}`)}
	authorization := permissions.Authorization{Permissions: []string{permissions.RuntimeWake}, MCPTools: []string{"get_new_messages", "send_message"}}

	if err := adapter.Run(context.Background(), binding, delivery, authorization, "sinores"); err != nil {
		t.Fatal(err)
	}
	want := "mcp__sinores__get_new_messages,mcp__sinores__send_message"
	if runner.args[len(runner.args)-1] != want {
		t.Fatalf("--allowedTools value = %q, want %q", runner.args[len(runner.args)-1], want)
	}
}

func TestAdapterNeverSubstitutesEventDataIntoNonPromptArgs(t *testing.T) {
	runner := &recordingRunner{}
	adapter := &Adapter{Output: io.Discard, Runner: runner}
	binding := sessions.Binding{
		Adapter:          "claude-code",
		RuntimeSessionID: "runtime_test",
		Workspace:        t.TempDir(),
		ResumeCommand:    []string{"claude", "-r", "{runtime_session_id}", "--mcp-tools", "{mcp_tools_json}"},
	}
	delivery := protocol.DeliveryData{
		DeliveryID: "dlv_test",
		EventID:    "evt_test",
		Event:      json.RawMessage(`{"source":"test","name":"test.event","data":{"malicious":"--dangerous-flag"}}`),
	}
	authorization := permissions.Authorization{Permissions: []string{permissions.RuntimeWake}, MCPTools: []string{"tool_a"}}

	if err := adapter.Run(context.Background(), binding, delivery, authorization, "sinores"); err != nil {
		t.Fatal(err)
	}
	if runner.args[3] != `["tool_a"]` {
		t.Fatalf("mcp_tools_json = %q, want the authorization's own tool list, unrelated to event content", runner.args[3])
	}
	for _, arg := range runner.args {
		if strings.Contains(arg, "malicious") {
			t.Fatalf("event payload leaked into argv: %#v", runner.args)
		}
	}
}

func TestAdapterRejectsMissingResumeCommand(t *testing.T) {
	adapter := &Adapter{Output: io.Discard, Runner: &recordingRunner{}}
	binding := sessions.Binding{Adapter: "claude-code", RuntimeSessionID: "runtime_test", Workspace: t.TempDir()}
	authorization := permissions.Authorization{Permissions: []string{permissions.RuntimeWake}}
	err := adapter.Run(context.Background(), binding, protocol.DeliveryData{EventID: "evt", Event: json.RawMessage(`{}`)}, authorization, "sinores")
	if err == nil || !errors.Is(err, execrunner.ErrBindingUnusable) {
		t.Fatalf("err = %v, want ErrBindingUnusable", err)
	}
}

func TestAdapterRejectsMissingWorkspace(t *testing.T) {
	adapter := &Adapter{Output: io.Discard, Runner: &recordingRunner{}}
	binding := sessions.Binding{
		Adapter: "claude-code", RuntimeSessionID: "runtime_test",
		Workspace:     "/definitely/does/not/exist/anywhere",
		ResumeCommand: []string{"claude", "-r", "{runtime_session_id}"},
	}
	authorization := permissions.Authorization{Permissions: []string{permissions.RuntimeWake}}
	err := adapter.Run(context.Background(), binding, protocol.DeliveryData{EventID: "evt", Event: json.RawMessage(`{}`)}, authorization, "sinores")
	if err == nil || !errors.Is(err, execrunner.ErrBindingUnusable) {
		t.Fatalf("err = %v, want ErrBindingUnusable", err)
	}
}

func TestAdapterRejectsMissingRuntimeWakePermission(t *testing.T) {
	adapter := &Adapter{Output: io.Discard, Runner: &recordingRunner{}}
	binding := sessions.Binding{
		Adapter: "claude-code", RuntimeSessionID: "runtime_test", Workspace: t.TempDir(),
		ResumeCommand: []string{"claude", "-r", "{runtime_session_id}"},
	}
	err := adapter.Run(context.Background(), binding, protocol.DeliveryData{EventID: "evt", Event: json.RawMessage(`{}`)}, permissions.Authorization{}, "sinores")
	if err == nil {
		t.Fatal("Run() accepted a delivery without runtime.wake granted")
	}
}
