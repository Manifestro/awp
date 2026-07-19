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
