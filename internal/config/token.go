package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TokenDirectory resolves where per-provider protected token files live,
// next to config.json, the same way sessions/permissions/events stores are
// resolved relative to it.
func TokenDirectory(configPath string) (string, error) {
	resolvedConfig, err := Path(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(resolvedConfig), "tokens"), nil
}

// TokenPath resolves the protected token file for one provider.
func TokenPath(configPath, provider string) (string, error) {
	directory, err := TokenDirectory(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, provider+".token"), nil
}

// SaveToken writes a provider's bearer token to a 0600 file, atomically.
// This is the same protected-file mechanism `awp autostart`/`--token-dir`
// already read from; it exists so a token can be configured (e.g. via the
// set_awp MCP flow) without ever being written into config.json itself.
func SaveToken(path, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("token must not be empty")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary token file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(token + "\n"); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary token file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace token file: %w", err)
	}
	return nil
}
