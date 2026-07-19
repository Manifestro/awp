package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const defaultTokenEnv = "AWP_TOKEN"

type Config struct {
	Version    string `json:"version"`
	ServiceURL string `json:"service_url"`
	DeviceID   string `json:"device_id"`
	TokenEnv   string `json:"token_env"`
}

func Default() Config {
	return Config{
		Version:  "0.1",
		TokenEnv: defaultTokenEnv,
	}
}

func Path(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if fromEnv := os.Getenv("AWP_CONFIG"); fromEnv != "" {
		return filepath.Abs(fromEnv)
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(root, "awp", "config.json"), nil
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()

	var cfg Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode config: multiple JSON values")
		}
		return fmt.Errorf("decode config: %w", err)
	}
	return nil
}

func Save(path string, cfg Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}

	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cfg); err != nil {
		temp.Close()
		return fmt.Errorf("encode config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func Validate(cfg Config) error {
	if cfg.Version != "0.1" {
		return fmt.Errorf("unsupported config version %q", cfg.Version)
	}
	if strings.TrimSpace(cfg.DeviceID) == "" {
		return errors.New("device_id is required")
	}
	if strings.TrimSpace(cfg.TokenEnv) == "" {
		return errors.New("token_env is required")
	}
	if strings.ContainsAny(cfg.TokenEnv, "= \t\r\n") {
		return errors.New("token_env must be an environment variable name")
	}

	parsed, err := url.Parse(cfg.ServiceURL)
	if err != nil || parsed.Host == "" {
		return errors.New("service_url must be an absolute WebSocket URL")
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return errors.New("service_url scheme must be wss or ws")
	}
	if parsed.Scheme == "ws" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
		return errors.New("insecure ws is allowed only for localhost")
	}
	return nil
}
