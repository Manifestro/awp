package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveTokenIsPrivateAndTrimmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sinores.token")
	if err := SaveToken(path, "  secret-token  \n"); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(contents)) != "secret-token" {
		t.Fatalf("contents = %q", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSaveTokenRejectsEmpty(t *testing.T) {
	if err := SaveToken(filepath.Join(t.TempDir(), "sinores.token"), "   "); err == nil {
		t.Fatal("SaveToken() accepted an empty token")
	}
}

func TestTokenPathIsNextToConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	path, err := TokenPath(configPath, "sinores")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(configPath), "tokens", "sinores.token")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}
