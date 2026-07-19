package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestConfigSetJSONErrorReturnsFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "config.json")

	exitCode := Run([]string{
		"config", "set",
		"--service-url", "ws://example.com/ws",
		"--device-id", "dev_test",
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

func TestSessionsBindListAndRemove(t *testing.T) {
	store := filepath.Join(t.TempDir(), "sessions.json")
	workspace := t.TempDir()

	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "bind",
			args: []string{"sessions", "bind", "--session-id", "ses_test", "--adapter", "codex", "--runtime-session-id", "runtime_test", "--workspace", workspace, "--store", store, "--json"},
		},
		{
			name: "list",
			args: []string{"sessions", "list", "--store", store, "--json"},
		},
		{
			name: "remove",
			args: []string{"sessions", "remove", "--session-id", "ses_test", "--store", store, "--json"},
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
