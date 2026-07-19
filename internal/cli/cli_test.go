package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Manifestro/awp/internal/autostart"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/sessions"
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

func TestAutostartEnableIsOptInAndEditable(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	if err := config.Save(configPath, config.Config{Version: "0.1", ServiceURL: "wss://awp.example.com/ws", DeviceID: "dev_test", TokenEnv: "AWP_TEST_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{SessionID: "ses_test", Adapter: "codex", RuntimeSessionID: "runtime_test"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(storePath, registry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWP_TEST_TOKEN", "secret-test-token")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"autostart", "enable", "--session-id", "ses_test", "--config", configPath, "--store", storePath, "--directory", directory, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("enable code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	manifest := autostart.Filename(directory, "ses_test")
	contents, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "--reconnect") {
		t.Fatalf("manifest does not enable reconnect: %s", contents)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"autostart", "disable", "--session-id", "ses_test", "--config", configPath, "--store", storePath, "--directory", directory, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("disable code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(manifest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest still exists: %v", err)
	}
}
