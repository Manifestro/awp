package codex

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

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

func TestAdapterResumesCodexWithUniversalEventPrompt(t *testing.T) {
	runner := &recordingRunner{}
	adapter := &Adapter{Binary: "/bin/echo", Output: io.Discard, Runner: runner}
	binding := sessions.Binding{Adapter: "codex", RuntimeSessionID: "runtime_test", Workspace: t.TempDir()}
	delivery := protocol.DeliveryData{
		DeliveryID: "dlv_test",
		EventID:    "evt_test",
		Event:      json.RawMessage(`{"source":"monitoring","name":"alert.created","data":{"service":"api"}}`),
	}

	if err := adapter.Run(context.Background(), binding, delivery); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"exec", "resume", "--json", "runtime_test", "-"}
	if strings.Join(runner.args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %#v, want %#v", runner.args, wantArgs)
	}
	if runner.directory != binding.Workspace {
		t.Fatalf("directory = %q, want %q", runner.directory, binding.Workspace)
	}
	if !strings.Contains(runner.stdin, `"source": "monitoring"`) || strings.Contains(runner.stdin, "WhatsApp") {
		t.Fatalf("prompt is not source-agnostic:\n%s", runner.stdin)
	}
}
