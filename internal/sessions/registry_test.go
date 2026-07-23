package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBindSaveLoadAndRemove(t *testing.T) {
	registry := NewRegistry()
	binding, err := Bind(&registry, Binding{
		Provider:         "example",
		SessionID:        "ses_test",
		Adapter:          "codex",
		RuntimeSessionID: "runtime_test",
		Workspace:        t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.CreatedAt == "" || binding.UpdatedAt == "" {
		t.Fatal("binding timestamps were not populated")
	}

	path := filepath.Join(t.TempDir(), "nested", "sessions.json")
	if err := Save(path, registry); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, found := Get(loaded, "example", "ses_test")
	if !found || got.RuntimeSessionID != "runtime_test" {
		t.Fatalf("loaded binding = %#v, found = %v", got, found)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry permissions = %o, want 600", info.Mode().Perm())
	}
	if !Remove(&loaded, "example", "ses_test") || Remove(&loaded, "example", "ses_test") {
		t.Fatal("Remove() is not idempotent")
	}
}

func TestBindRejectsUnsupportedAdapter(t *testing.T) {
	registry := NewRegistry()
	_, err := Bind(&registry, Binding{Provider: "example", SessionID: "ses", Adapter: "unknown", RuntimeSessionID: "runtime"})
	if err == nil {
		t.Fatal("Bind() accepted unsupported adapter")
	}
}

func TestBindAcceptsCustomAdapterWithResumeCommand(t *testing.T) {
	registry := NewRegistry()
	binding, err := Bind(&registry, Binding{
		Provider: "example", SessionID: "ses", Adapter: "claude-code", RuntimeSessionID: "runtime",
		ResumeCommand: []string{"claude", "-r", "{runtime_session_id}", "-p", "{prompt}"},
	})
	if err != nil {
		t.Fatalf("Bind() rejected a custom adapter with a resume_command: %v", err)
	}
	if len(binding.ResumeCommand) != 5 {
		t.Fatalf("binding.ResumeCommand = %#v", binding.ResumeCommand)
	}
}

func TestBindRejectsResumeCommandWithNewlines(t *testing.T) {
	registry := NewRegistry()
	_, err := Bind(&registry, Binding{
		Provider: "example", SessionID: "ses", Adapter: "claude-code", RuntimeSessionID: "runtime",
		ResumeCommand: []string{"claude", "-r\nrm -rf /"},
	})
	if err == nil {
		t.Fatal("Bind() accepted a resume_command token containing a newline")
	}
}
