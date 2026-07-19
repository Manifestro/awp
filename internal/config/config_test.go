package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{
		Version:  Version,
		DeviceID: "dev_test",
		Providers: map[string]Provider{
			"example": {ServiceURL: "wss://example.com/awp", TokenEnv: "EXAMPLE_AWP_TOKEN"},
		},
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != want.Version || got.DeviceID != want.DeviceID || got.Providers["example"] != want.Providers["example"] {
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
	cfg := Config{Version: Version, DeviceID: "dev_test", Providers: map[string]Provider{"example": {ServiceURL: "ws://example.com/awp", TokenEnv: "EXAMPLE_TOKEN"}}}
	if err := Validate(cfg); err == nil {
		t.Fatal("Validate() accepted remote plaintext WebSocket")
	}
}

func TestValidateAllowsLocalPlaintextWebSocket(t *testing.T) {
	cfg := Config{Version: Version, DeviceID: "dev_test", Providers: map[string]Provider{"example": {ServiceURL: "ws://localhost:8000/awp", TokenEnv: "EXAMPLE_TOKEN"}}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
