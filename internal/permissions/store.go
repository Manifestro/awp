package permissions

import (
	"crypto/sha256"
	"encoding/hex"
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

const (
	RuntimeWake               = "runtime.wake"
	ScopeOnce                 = "once"
	ScopeBinding              = "binding"
	ScopeProvider             = "provider"
	DelegationBackground      = "background"
	DelegationInteractiveOnly = "interactive-only"
)

type RequestedPermission struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Risk        string   `json:"risk"`
	Delegation  string   `json:"delegation"`
	MCPTools    []string `json:"mcp_tools,omitempty"`
}

type Request struct {
	Provider    string                `json:"provider"`
	SessionID   string                `json:"session_id"`
	RequestID   string                `json:"request_id"`
	Permissions []RequestedPermission `json:"permissions"`
	RequestedAt string                `json:"requested_at"`
}

type Grant struct {
	Provider         string            `json:"provider"`
	SessionID        string            `json:"session_id,omitempty"`
	SourceSessionID  string            `json:"source_session_id"`
	RequestID        string            `json:"request_id"`
	Scope            string            `json:"scope"`
	Permissions      []string          `json:"permissions"`
	PermissionHashes map[string]string `json:"permission_hashes"`
	GrantedAt        string            `json:"granted_at"`
	GrantedBy        string            `json:"granted_by"`
}

type AuditEntry struct {
	Timestamp   string   `json:"timestamp"`
	Action      string   `json:"action"`
	Provider    string   `json:"provider"`
	SessionID   string   `json:"session_id,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

type Store struct {
	Version  string       `json:"version"`
	Requests []Request    `json:"requests"`
	Grants   []Grant      `json:"grants"`
	Audit    []AuditEntry `json:"audit"`
}

type Authorization struct {
	Provider    string   `json:"provider"`
	SessionID   string   `json:"session_id"`
	Scope       string   `json:"scope"`
	Permissions []string `json:"permissions"`
	MCPTools    []string `json:"mcp_tools"`
}

func NewStore() Store {
	return Store{Version: Version, Requests: []Request{}, Grants: []Grant{}, Audit: []AuditEntry{}}
}

func Path(configPath, explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if fromEnv := os.Getenv("AWP_PERMISSIONS"); fromEnv != "" {
		return filepath.Abs(fromEnv)
	}
	resolvedConfig, err := config.Path(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(resolvedConfig), "permissions.json"), nil
}

func Load(path string) (Store, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewStore(), nil
	}
	if err != nil {
		return Store{}, err
	}
	defer file.Close()
	store := NewStore()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store); err != nil {
		return Store{}, fmt.Errorf("decode permission store: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Store{}, errors.New("decode permission store: multiple JSON values")
		}
		return Store{}, fmt.Errorf("decode permission store: %w", err)
	}
	if store.Version != Version {
		return Store{}, fmt.Errorf("unsupported permission store version %q", store.Version)
	}
	if store.Requests == nil {
		store.Requests = []Request{}
	}
	if store.Grants == nil {
		store.Grants = []Grant{}
	}
	if store.Audit == nil {
		store.Audit = []AuditEntry{}
	}
	return store, nil
}

func Save(path string, store Store) error {
	if store.Version != Version {
		return fmt.Errorf("unsupported permission store version %q", store.Version)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create permission directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".permissions-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary permission store: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(store); err != nil {
		temporary.Close()
		return fmt.Errorf("encode permission store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace permission store: %w", err)
	}
	return nil
}

func RecordRequest(store *Store, request Request) (Request, error) {
	request.Provider = strings.TrimSpace(request.Provider)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.Provider == "" || request.SessionID == "" || request.RequestID == "" {
		return Request{}, errors.New("provider, session_id, and request_id are required")
	}
	if len(request.Permissions) == 0 {
		return Request{}, errors.New("at least one permission is required")
	}
	seen := map[string]bool{}
	wakeRequested := false
	for index := range request.Permissions {
		permission := &request.Permissions[index]
		permission.ID = strings.TrimSpace(permission.ID)
		permission.Title = strings.TrimSpace(permission.Title)
		permission.Risk = strings.TrimSpace(permission.Risk)
		permission.Delegation = strings.TrimSpace(permission.Delegation)
		if permission.ID == "" || permission.Title == "" {
			return Request{}, errors.New("permission id and title are required")
		}
		if seen[permission.ID] {
			return Request{}, fmt.Errorf("duplicate permission %q", permission.ID)
		}
		seen[permission.ID] = true
		wakeRequested = wakeRequested || permission.ID == RuntimeWake
		switch permission.Risk {
		case "runtime", "read", "write", "sensitive":
		default:
			return Request{}, fmt.Errorf("permission %s has unsupported risk %q", permission.ID, permission.Risk)
		}
		switch permission.Delegation {
		case DelegationBackground, DelegationInteractiveOnly:
		default:
			return Request{}, fmt.Errorf("permission %s has unsupported delegation %q", permission.ID, permission.Delegation)
		}
		permission.MCPTools = uniqueSorted(permission.MCPTools)
		for _, tool := range permission.MCPTools {
			if tool == "" || strings.ContainsAny(tool, "\r\n") {
				return Request{}, fmt.Errorf("permission %s has invalid MCP tool", permission.ID)
			}
		}
		if permission.ID == RuntimeWake && len(permission.MCPTools) != 0 {
			return Request{}, errors.New("runtime.wake must not map to MCP tools")
		}
		if permission.ID == RuntimeWake && (permission.Risk != "runtime" || permission.Delegation != DelegationBackground) {
			return Request{}, errors.New("runtime.wake must use risk=runtime and delegation=background")
		}
	}
	if !wakeRequested {
		return Request{}, errors.New("provider permission request must include runtime.wake")
	}
	request.RequestedAt = time.Now().UTC().Format(time.RFC3339Nano)
	for index, existing := range store.Requests {
		if existing.Provider == request.Provider && existing.SessionID == request.SessionID {
			store.Requests[index] = request
			appendAudit(store, AuditEntry{Action: "request.updated", Provider: request.Provider, SessionID: request.SessionID})
			return request, nil
		}
	}
	store.Requests = append(store.Requests, request)
	appendAudit(store, AuditEntry{Action: "request.created", Provider: request.Provider, SessionID: request.SessionID})
	return request, nil
}

func GrantPermissions(store *Store, provider, sessionID, scope string, allowed []string) (Grant, error) {
	request, found := GetRequest(*store, provider, sessionID)
	if !found {
		return Grant{}, fmt.Errorf("provider %s has not requested permissions for session %s", provider, sessionID)
	}
	if scope == "" {
		scope = ScopeBinding
	}
	switch scope {
	case ScopeOnce, ScopeBinding, ScopeProvider:
	default:
		return Grant{}, fmt.Errorf("unsupported grant scope %q", scope)
	}
	allowed = uniqueSorted(allowed)
	if len(allowed) == 0 {
		return Grant{}, errors.New("at least one permission must be allowed")
	}
	requested := map[string]RequestedPermission{}
	for _, permission := range request.Permissions {
		requested[permission.ID] = permission
	}
	hashes := map[string]string{}
	for _, id := range allowed {
		permission, found := requested[id]
		if !found {
			return Grant{}, fmt.Errorf("permission %q was not requested by provider %s", id, provider)
		}
		if permission.Delegation == DelegationInteractiveOnly {
			return Grant{}, fmt.Errorf("permission %q is interactive-only and cannot be delegated to the daemon", id)
		}
		hashes[id] = permissionHash(permission)
	}
	if !contains(allowed, RuntimeWake) {
		return Grant{}, errors.New("runtime.wake must be granted for background delivery")
	}
	grant := Grant{Provider: provider, SourceSessionID: sessionID, RequestID: request.RequestID, Scope: scope, Permissions: allowed, PermissionHashes: hashes, GrantedAt: time.Now().UTC().Format(time.RFC3339Nano), GrantedBy: "interactive_cli"}
	if scope != ScopeProvider {
		grant.SessionID = sessionID
	}
	for index, existing := range store.Grants {
		if existing.Provider == grant.Provider && existing.SessionID == grant.SessionID && existing.Scope == grant.Scope {
			store.Grants[index] = grant
			appendAudit(store, AuditEntry{Action: "grant.updated", Provider: provider, SessionID: sessionID, Scope: scope, Permissions: allowed})
			return grant, nil
		}
	}
	store.Grants = append(store.Grants, grant)
	appendAudit(store, AuditEntry{Action: "grant.created", Provider: provider, SessionID: sessionID, Scope: scope, Permissions: allowed})
	return grant, nil
}

func Authorize(store *Store, provider, sessionID string, consumeOnce bool) (Authorization, error) {
	request, found := GetRequest(*store, provider, sessionID)
	if !found {
		return Authorization{}, fmt.Errorf("permission request is pending for %s/%s", provider, sessionID)
	}
	var selected *Grant
	selectedIndex := -1
	priority := map[string]int{ScopeProvider: 1, ScopeBinding: 2, ScopeOnce: 3}
	for index := range store.Grants {
		grant := &store.Grants[index]
		if grant.Provider != provider {
			continue
		}
		if grant.Scope != ScopeProvider && grant.SessionID != sessionID {
			continue
		}
		if selected == nil || priority[grant.Scope] > priority[selected.Scope] {
			selected, selectedIndex = grant, index
		}
	}
	if selected == nil {
		return Authorization{}, fmt.Errorf("runtime.wake is not granted for %s/%s", provider, sessionID)
	}
	requested := map[string]RequestedPermission{}
	for _, permission := range request.Permissions {
		requested[permission.ID] = permission
	}
	granted := []string{}
	tools := []string{}
	for _, id := range selected.Permissions {
		permission, exists := requested[id]
		if !exists || selected.PermissionHashes[id] != permissionHash(permission) {
			continue
		}
		granted = append(granted, id)
		tools = append(tools, permission.MCPTools...)
	}
	granted, tools = uniqueSorted(granted), uniqueSorted(tools)
	if !contains(granted, RuntimeWake) {
		return Authorization{}, fmt.Errorf("runtime.wake grant is missing or changed for %s/%s; review the provider request again", provider, sessionID)
	}
	authorization := Authorization{Provider: provider, SessionID: sessionID, Scope: selected.Scope, Permissions: granted, MCPTools: tools}
	if consumeOnce {
		appendAudit(store, AuditEntry{Action: "runtime.authorized", Provider: provider, SessionID: sessionID, Scope: selected.Scope, Permissions: granted})
	}
	if consumeOnce && selected.Scope == ScopeOnce {
		store.Grants = append(store.Grants[:selectedIndex], store.Grants[selectedIndex+1:]...)
		appendAudit(store, AuditEntry{Action: "grant.consumed", Provider: provider, SessionID: sessionID, Scope: ScopeOnce, Permissions: granted})
	}
	return authorization, nil
}

func GetRequest(store Store, provider, sessionID string) (Request, bool) {
	for _, request := range store.Requests {
		if request.Provider == provider && request.SessionID == sessionID {
			return request, true
		}
	}
	return Request{}, false
}

func ListRequests(store Store, provider string) []Request {
	values := []Request{}
	for _, request := range store.Requests {
		if provider == "" || request.Provider == provider {
			values = append(values, request)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Provider != values[j].Provider {
			return values[i].Provider < values[j].Provider
		}
		return values[i].SessionID < values[j].SessionID
	})
	return values
}

func ListGrants(store Store, provider string) []Grant {
	values := []Grant{}
	for _, grant := range store.Grants {
		if provider == "" || grant.Provider == provider {
			values = append(values, grant)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Provider != values[j].Provider {
			return values[i].Provider < values[j].Provider
		}
		if values[i].SessionID != values[j].SessionID {
			return values[i].SessionID < values[j].SessionID
		}
		return values[i].Scope < values[j].Scope
	})
	return values
}

func Revoke(store *Store, provider, sessionID, scope string, ids []string) (bool, error) {
	ids = uniqueSorted(ids)
	changed := false
	for index := len(store.Grants) - 1; index >= 0; index-- {
		grant := &store.Grants[index]
		if grant.Provider != provider || (scope != "" && grant.Scope != scope) {
			continue
		}
		if grant.Scope != ScopeProvider && grant.SessionID != sessionID {
			continue
		}
		if len(ids) == 0 {
			store.Grants = append(store.Grants[:index], store.Grants[index+1:]...)
			changed = true
			continue
		}
		remaining := []string{}
		for _, id := range grant.Permissions {
			if !contains(ids, id) {
				remaining = append(remaining, id)
			} else {
				delete(grant.PermissionHashes, id)
				changed = true
			}
		}
		grant.Permissions = remaining
		if len(remaining) == 0 {
			store.Grants = append(store.Grants[:index], store.Grants[index+1:]...)
		}
	}
	if changed {
		appendAudit(store, AuditEntry{Action: "grant.revoked", Provider: provider, SessionID: sessionID, Scope: scope, Permissions: ids})
	}
	return changed, nil
}

func permissionHash(permission RequestedPermission) string {
	encoded, _ := json.Marshal(permission)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func appendAudit(store *Store, entry AuditEntry) {
	entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	store.Audit = append(store.Audit, entry)
	if len(store.Audit) > 1000 {
		store.Audit = append([]AuditEntry(nil), store.Audit[len(store.Audit)-1000:]...)
	}
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
