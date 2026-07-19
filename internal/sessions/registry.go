package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Manifestro/awp/internal/config"
)

const Version = "0.1"

type Binding struct {
	SessionID        string `json:"session_id"`
	Adapter          string `json:"adapter"`
	RuntimeSessionID string `json:"runtime_session_id"`
	Workspace        string `json:"workspace,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type Registry struct {
	Version  string             `json:"version"`
	Sessions map[string]Binding `json:"sessions"`
}

func NewRegistry() Registry {
	return Registry{Version: Version, Sessions: map[string]Binding{}}
}

func Path(configPath, explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if fromEnv := os.Getenv("AWP_SESSIONS"); fromEnv != "" {
		return filepath.Abs(fromEnv)
	}
	resolvedConfig, err := config.Path(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(resolvedConfig), "sessions.json"), nil
}

func Load(path string) (Registry, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewRegistry(), nil
	}
	if err != nil {
		return Registry{}, err
	}
	defer file.Close()

	registry := NewRegistry()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return Registry{}, fmt.Errorf("decode session registry: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Registry{}, errors.New("decode session registry: multiple JSON values")
		}
		return Registry{}, fmt.Errorf("decode session registry: %w", err)
	}
	if registry.Version != Version {
		return Registry{}, fmt.Errorf("unsupported session registry version %q", registry.Version)
	}
	if registry.Sessions == nil {
		registry.Sessions = map[string]Binding{}
	}
	return registry, nil
}

func Save(path string, registry Registry) error {
	if registry.Version != Version {
		return fmt.Errorf("unsupported session registry version %q", registry.Version)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session registry directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary session registry: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("secure temporary session registry: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(registry); err != nil {
		temp.Close()
		return fmt.Errorf("encode session registry: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync session registry: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close session registry: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace session registry: %w", err)
	}
	return nil
}

func Bind(registry *Registry, binding Binding) (Binding, error) {
	if strings.TrimSpace(binding.SessionID) == "" {
		return Binding{}, errors.New("session_id is required")
	}
	if strings.TrimSpace(binding.Adapter) == "" {
		return Binding{}, errors.New("adapter is required")
	}
	if binding.Adapter != "codex" {
		return Binding{}, fmt.Errorf("unsupported adapter %q", binding.Adapter)
	}
	if strings.TrimSpace(binding.RuntimeSessionID) == "" {
		return Binding{}, errors.New("runtime_session_id is required")
	}
	if binding.Workspace != "" {
		absolute, err := filepath.Abs(binding.Workspace)
		if err != nil {
			return Binding{}, fmt.Errorf("resolve workspace: %w", err)
		}
		binding.Workspace = absolute
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if existing, found := registry.Sessions[binding.SessionID]; found {
		binding.CreatedAt = existing.CreatedAt
	} else {
		binding.CreatedAt = now
	}
	binding.UpdatedAt = now
	registry.Sessions[binding.SessionID] = binding
	return binding, nil
}

func Get(registry Registry, sessionID string) (Binding, bool) {
	binding, found := registry.Sessions[sessionID]
	return binding, found
}

func Remove(registry *Registry, sessionID string) bool {
	if _, found := registry.Sessions[sessionID]; !found {
		return false
	}
	delete(registry.Sessions, sessionID)
	return true
}

func List(registry Registry) []Binding {
	bindings := make([]Binding, 0, len(registry.Sessions))
	for _, binding := range registry.Sessions {
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(left, right int) bool {
		return bindings[left].SessionID < bindings[right].SessionID
	})
	return bindings
}
