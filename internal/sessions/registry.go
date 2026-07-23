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

const Version = "0.2"

type Binding struct {
	Provider         string         `json:"provider"`
	SessionID        string         `json:"session_id"`
	Adapter          string         `json:"adapter"`
	RuntimeSessionID string         `json:"runtime_session_id"`
	Workspace        string         `json:"workspace,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	// ResumeCommand is an argv template for the generic "command" adapter
	// (see internal/adapters/command): when set, it is executed instead of
	// the hardcoded Codex invocation, so any CLI runtime can register itself
	// via the local MCP server's set_awp tool without a new adapter package.
	ResumeCommand []string `json:"resume_command,omitempty"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

type Registry struct {
	Version  string    `json:"version"`
	Bindings []Binding `json:"bindings"`
}

func NewRegistry() Registry {
	return Registry{Version: Version, Bindings: []Binding{}}
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
	if registry.Bindings == nil {
		registry.Bindings = []Binding{}
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
	if strings.TrimSpace(binding.Provider) == "" {
		return Binding{}, errors.New("provider is required")
	}
	if strings.TrimSpace(binding.SessionID) == "" {
		return Binding{}, errors.New("session_id is required")
	}
	if strings.TrimSpace(binding.Adapter) == "" {
		return Binding{}, errors.New("adapter is required")
	}
	// A binding with no resume_command uses the hardcoded, built-in Codex
	// invocation, so its adapter name must actually be "codex". A binding
	// with a resume_command runs through the generic command adapter
	// instead; its adapter name is just a descriptive label at that point.
	if len(binding.ResumeCommand) == 0 && binding.Adapter != "codex" {
		return Binding{}, fmt.Errorf("unsupported adapter %q (set resume_command to register a custom runtime)", binding.Adapter)
	}
	for _, token := range binding.ResumeCommand {
		if strings.ContainsAny(token, "\r\n") {
			return Binding{}, errors.New("resume_command tokens must not contain newlines")
		}
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
	for index, existing := range registry.Bindings {
		if existing.Provider == binding.Provider && existing.SessionID == binding.SessionID {
			binding.CreatedAt = existing.CreatedAt
			binding.UpdatedAt = now
			registry.Bindings[index] = binding
			return binding, nil
		}
	}
	binding.CreatedAt = now
	binding.UpdatedAt = now
	registry.Bindings = append(registry.Bindings, binding)
	return binding, nil
}

func Get(registry Registry, provider, sessionID string) (Binding, bool) {
	for _, binding := range registry.Bindings {
		if binding.Provider == provider && binding.SessionID == sessionID {
			return binding, true
		}
	}
	return Binding{}, false
}

func Remove(registry *Registry, provider, sessionID string) bool {
	for index, binding := range registry.Bindings {
		if binding.Provider == provider && binding.SessionID == sessionID {
			registry.Bindings = append(registry.Bindings[:index], registry.Bindings[index+1:]...)
			return true
		}
	}
	return false
}

func List(registry Registry, provider string) []Binding {
	bindings := make([]Binding, 0, len(registry.Bindings))
	for _, binding := range registry.Bindings {
		if provider == "" || binding.Provider == provider {
			bindings = append(bindings, binding)
		}
	}
	sort.Slice(bindings, func(left, right int) bool {
		if bindings[left].Provider != bindings[right].Provider {
			return bindings[left].Provider < bindings[right].Provider
		}
		return bindings[left].SessionID < bindings[right].SessionID
	})
	return bindings
}
