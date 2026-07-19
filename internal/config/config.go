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

const Version = "0.2"

type Config struct {
	Version   string              `json:"version"`
	DeviceID  string              `json:"device_id"`
	Providers map[string]Provider `json:"providers"`
}

type Provider struct {
	ServiceURL string `json:"service_url"`
	TokenEnv   string `json:"token_env"`
}

func Default() Config {
	return Config{Version: Version, Providers: map[string]Provider{}}
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
	if cfg.Version != Version {
		return fmt.Errorf("unsupported config version %q", cfg.Version)
	}
	if strings.TrimSpace(cfg.DeviceID) == "" {
		return errors.New("device_id is required")
	}
	if len(cfg.Providers) == 0 {
		return errors.New("at least one provider is required")
	}
	for name, provider := range cfg.Providers {
		if err := ValidateProvider(name, provider); err != nil {
			return err
		}
	}
	return nil
}

func ValidateProvider(name string, provider Provider) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " /\\\t\r\n") {
		return errors.New("provider name must be a non-empty identifier without whitespace or slashes")
	}
	if strings.TrimSpace(provider.TokenEnv) == "" {
		return fmt.Errorf("provider %q token_env is required", name)
	}
	if strings.ContainsAny(provider.TokenEnv, "= \t\r\n") {
		return fmt.Errorf("provider %q token_env must be an environment variable name", name)
	}
	parsed, err := url.Parse(provider.ServiceURL)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("provider %q service_url must be an absolute WebSocket URL", name)
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return fmt.Errorf("provider %q service_url scheme must be wss or ws", name)
	}
	if parsed.Scheme == "ws" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
		return fmt.Errorf("provider %q insecure ws is allowed only for localhost", name)
	}
	return nil
}

func SetProvider(cfg *Config, name string, provider Provider) error {
	if err := ValidateProvider(name, provider); err != nil {
		return err
	}
	if cfg.Version == "" {
		cfg.Version = Version
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	cfg.Providers[name] = provider
	return nil
}
