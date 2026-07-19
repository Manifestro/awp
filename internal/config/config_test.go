package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{
		Version:    "0.1",
		ServiceURL: "wss://awp.example.com/ws",
		DeviceID:   "dev_test",
		TokenEnv:   "AWP_TEST_TOKEN",
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestValidateRejectsRemotePlaintextWebSocket(t *testing.T) {
	cfg := Config{Version: "0.1", ServiceURL: "ws://example.com/ws", DeviceID: "dev_test", TokenEnv: "AWP_TOKEN"}
	if err := Validate(cfg); err == nil {
		t.Fatal("Validate() accepted remote plaintext WebSocket")
	}
}

func TestValidateAllowsLocalPlaintextWebSocket(t *testing.T) {
	cfg := Config{Version: "0.1", ServiceURL: "ws://localhost:8000/ws", DeviceID: "dev_test", TokenEnv: "AWP_TOKEN"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
