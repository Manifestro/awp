// Package mcpserver implements a small local MCP (Model Context Protocol)
// server that a resumed agent can talk to over stdio. It answers the
// question the daemon's prompt alone cannot: why was I woken, what is still
// unprocessed, and can I tell AWP to leave me alone for a while. It only
// exposes the daemon's own local state (session registry and event store);
// it is not a substitute for a provider's own MCP server.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Manifestro/awp/internal/client"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/daemonctl"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
	"github.com/Manifestro/awp/internal/wake"
)

const protocolVersion = "2024-11-05"

// Dependencies are the local paths this server reads and writes. They are
// resolved by the caller the same way the daemon resolves them, so both
// processes agree on where state lives.
type Dependencies struct {
	ConfigPath      string
	SessionsPath    string
	EventsPath      string
	PermissionsPath string
	// Workspace scopes auto-detection: when a tool call omits provider and
	// session_id, the server resolves them to the one local binding whose
	// Workspace matches this directory. Defaults to the process's cwd.
	Workspace string
	// Version is reported to providers as this client's version during
	// request_permissions' handshake, matching what `awp daemon` reports.
	Version string
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Run reads one JSON-RPC 2.0 request per line from stdin and writes one
// response per line to stdout, until stdin is closed or ctx is cancelled.
// This is the MCP stdio transport: no Content-Length framing, no embedded
// newlines within a single message.
func Run(ctx context.Context, stdin io.Reader, stdout io.Writer, deps Dependencies) error {
	if deps.Workspace == "" {
		if cwd, err := os.Getwd(); err == nil {
			deps.Workspace = cwd
		}
	}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			writeError(stdout, nil, -32700, "parse error: "+err.Error())
			continue
		}
		handleRequest(ctx, stdout, deps, request)
	}
	return scanner.Err()
}

func handleRequest(ctx context.Context, stdout io.Writer, deps Dependencies, request rpcRequest) {
	switch request.Method {
	case "initialize":
		writeResult(stdout, request.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "awp", "version": "0.1"},
		})
	case "notifications/initialized", "notifications/cancelled":
		// Notifications never get a response.
	case "ping":
		writeResult(stdout, request.ID, map[string]any{})
	case "tools/list":
		writeResult(stdout, request.ID, map[string]any{"tools": toolDefinitions()})
	case "tools/call":
		handleToolCall(ctx, stdout, deps, request)
	default:
		if len(request.ID) > 0 {
			writeError(stdout, request.ID, -32601, fmt.Sprintf("unknown method %q", request.Method))
		}
	}
}

func toolDefinitions() []map[string]any {
	sessionParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider":   map[string]any{"type": "string", "description": "AWP provider name; omit to auto-detect from the current workspace"},
			"session_id": map[string]any{"type": "string", "description": "AWP session id; omit to auto-detect from the current workspace"},
		},
	}
	pauseParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider":   map[string]any{"type": "string"},
			"session_id": map[string]any{"type": "string"},
			"reason":     map[string]any{"type": "string", "description": "why this session should not be woken right now"},
		},
	}
	setAWPParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider":           map[string]any{"type": "string", "description": "AWP provider name; omit to update the one existing binding for this workspace"},
			"session_id":         map[string]any{"type": "string", "description": "AWP session id (opaque, assigned by the provider); omit to update the one existing binding for this workspace"},
			"adapter":            map[string]any{"type": "string", "description": "descriptive label for this runtime, e.g. \"claude-code\"; not looked up against a fixed list"},
			"runtime_session_id": map[string]any{"type": "string", "description": "this runtime's own session id, e.g. Claude Code's session UUID, to resume later"},
			"resume_command": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "argv to run when AWP needs to wake this session. Supports the placeholders {runtime_session_id}, {workspace}, {mcp_server}, {mcp_tools_json}, {prompt}. The provider event is never substituted into these args; it is only ever passed as the {prompt} text and over stdin.",
			},
			"workspace": map[string]any{"type": "string", "description": "working directory to run resume_command in; defaults to this MCP server's own working directory"},
			"metadata":  map[string]any{"type": "object", "description": "provider-defined metadata for this binding, e.g. a channel or thread id"},
		},
		"required": []string{"resume_command", "runtime_session_id"},
	}
	configureProviderParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider":    map[string]any{"type": "string", "description": "provider name, e.g. \"sinores\""},
			"service_url": map[string]any{"type": "string", "description": "the provider's AWP WebSocket endpoint, e.g. wss://api.example.com/awp"},
			"token":       map[string]any{"type": "string", "description": "bearer token for this provider. Written to a private, chmod 600 file on disk next to AWP's config; never stored inside config.json itself."},
			"mcp_server":  map[string]any{"type": "string", "description": "name of this provider's own MCP server as configured in the runtime, if any; use \"none\" if the provider has no MCP tools. Defaults to the provider name."},
		},
		"required": []string{"provider", "service_url", "token"},
	}
	requestPermissionsParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider":        map[string]any{"type": "string", "description": "AWP provider name; omit to auto-detect from the current workspace"},
			"session_id":      map[string]any{"type": "string", "description": "AWP session id; omit to auto-detect from the current workspace"},
			"timeout_seconds": map[string]any{"type": "number", "description": "how long to wait for the provider to send its permission.request before giving up; default 30"},
		},
	}
	grantPermissionsParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider":   map[string]any{"type": "string", "description": "AWP provider name; omit to auto-detect from the current workspace"},
			"session_id": map[string]any{"type": "string", "description": "AWP session id; omit to auto-detect from the current workspace"},
			"allow": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "the permission ids to grant. If request_permissions was called and the provider sent a real request, grant only ids it actually requested. If the provider has not sent one (many providers do not implement permission.request), any id listed here is granted directly by AWP itself, no provider round-trip needed; always include \"runtime.wake\" or nothing will wake this session.",
			},
			"mcp_tools": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "only used when there is no existing provider request: the provider's own MCP tool names to enable for this session's one-run wake (e.g. [\"get_new_messages\"]). Leave empty to grant runtime.wake with no provider MCP tools at all.",
			},
			"scope": map[string]any{"type": "string", "description": "once, binding, or provider; defaults to binding"},
		},
		"required": []string{"allow"},
	}
	daemonProviderParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider": map[string]any{"type": "string", "description": "connect only this provider instead of every provider with a local session binding"},
		},
	}
	return []map[string]any{
		{
			"name":        "wake_context",
			"description": "Explain why AWP resumed this session: current lifecycle status and reason, pending (unprocessed) events with their full payload, and currently granted permissions.",
			"inputSchema": sessionParams,
		},
		{
			"name":        "list_pending_events",
			"description": "List events AWP recorded for this session that have not completed yet. Already-processed events are not included.",
			"inputSchema": sessionParams,
		},
		{
			"name":        "list_sessions",
			"description": "List every AWP session bound on this machine. Use this to disambiguate when more than one binding shares this workspace.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "pause_session",
			"description": "Stop AWP from waking this session for new provider events until resume_session is called. Use this when you want to keep working on something uninterrupted.",
			"inputSchema": pauseParams,
		},
		{
			"name":        "resume_session",
			"description": "Clear a paused or crashed session so future provider events wake it again.",
			"inputSchema": sessionParams,
		},
		{
			"name":        "set_awp",
			"description": "Register or update how AWP should wake this session: the exact command to run and this runtime's own session id to resume. This is what lets any CLI runtime configure itself, instead of AWP needing built-in support for it. The registered command will later run unattended, with no human present, whenever a provider event arrives — review it carefully before approving this call.",
			"inputSchema": setAWPParams,
		},
		{
			"name":        "configure_provider",
			"description": "Configure an AWP provider: its WebSocket endpoint and bearer token. The token is written to a private file (chmod 600), never into config.json. Call this once per provider before set_awp, request_permissions, or start_daemon.",
			"inputSchema": configureProviderParams,
		},
		{
			"name":        "request_permissions",
			"description": "Connect to the provider once and wait for it to say what permissions (including which of its own MCP tools) it wants for this session. Returns the request for you to show the human before calling grant_permissions — never grant more than what they approve.",
			"inputSchema": requestPermissionsParams,
		},
		{
			"name":        "grant_permissions",
			"description": "Locally grant specific permission ids a provider requested via request_permissions. Only grant what the human has actually approved after seeing the request; granting includes runtime.wake, which is required before AWP will wake this session at all.",
			"inputSchema": grantPermissionsParams,
		},
		{
			"name":        "start_daemon",
			"description": "Start the AWP background daemon so it actually connects and waits for provider events. Safe to call again; it reports already_running instead of starting a duplicate.",
			"inputSchema": daemonProviderParams,
		},
		{
			"name":        "stop_daemon",
			"description": "Stop the AWP background daemon. While stopped, AWP is not connected to any provider at all: nothing is received or queued locally.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "daemon_status",
			"description": "Report whether the AWP background daemon is currently running.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type sessionArgs struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
}

type pauseArgs struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

type setAWPArgs struct {
	Provider         string         `json:"provider"`
	SessionID        string         `json:"session_id"`
	Adapter          string         `json:"adapter"`
	RuntimeSessionID string         `json:"runtime_session_id"`
	ResumeCommand    []string       `json:"resume_command"`
	Workspace        string         `json:"workspace"`
	Metadata         map[string]any `json:"metadata"`
}

type configureProviderArgs struct {
	Provider   string `json:"provider"`
	ServiceURL string `json:"service_url"`
	Token      string `json:"token"`
	MCPServer  string `json:"mcp_server"`
}

type requestPermissionsArgs struct {
	Provider       string  `json:"provider"`
	SessionID      string  `json:"session_id"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

type grantPermissionsArgs struct {
	Provider  string   `json:"provider"`
	SessionID string   `json:"session_id"`
	Allow     []string `json:"allow"`
	Scope     string   `json:"scope"`
	MCPTools  []string `json:"mcp_tools"`
}

type daemonProviderArgs struct {
	Provider string `json:"provider"`
}

func handleToolCall(ctx context.Context, stdout io.Writer, deps Dependencies, request rpcRequest) {
	var call toolCallParams
	if err := json.Unmarshal(request.Params, &call); err != nil {
		writeError(stdout, request.ID, -32602, "invalid params: "+err.Error())
		return
	}
	var result any
	var err error
	switch call.Name {
	case "wake_context":
		var args sessionArgs
		_ = json.Unmarshal(call.Arguments, &args)
		result, err = wakeContext(deps, args.Provider, args.SessionID)
	case "list_pending_events":
		var args sessionArgs
		_ = json.Unmarshal(call.Arguments, &args)
		result, err = listPendingEvents(deps, args.Provider, args.SessionID)
	case "list_sessions":
		result, err = listSessions(deps)
	case "pause_session":
		var args pauseArgs
		_ = json.Unmarshal(call.Arguments, &args)
		result, err = pauseSession(deps, args.Provider, args.SessionID, args.Reason)
	case "resume_session":
		var args sessionArgs
		_ = json.Unmarshal(call.Arguments, &args)
		result, err = resumeSession(deps, args.Provider, args.SessionID)
	case "set_awp":
		var args setAWPArgs
		_ = json.Unmarshal(call.Arguments, &args)
		result, err = setAWP(deps, args)
	case "configure_provider":
		var args configureProviderArgs
		_ = json.Unmarshal(call.Arguments, &args)
		result, err = configureProvider(deps, args)
	case "request_permissions":
		var args requestPermissionsArgs
		_ = json.Unmarshal(call.Arguments, &args)
		result, err = requestPermissions(ctx, deps, args)
	case "grant_permissions":
		var args grantPermissionsArgs
		_ = json.Unmarshal(call.Arguments, &args)
		result, err = grantPermissions(deps, args)
	case "start_daemon":
		var args daemonProviderArgs
		_ = json.Unmarshal(call.Arguments, &args)
		result, err = startDaemon(deps, args.Provider)
	case "stop_daemon":
		result, err = stopDaemon(deps)
	case "daemon_status":
		result, err = daemonStatus(deps)
	default:
		writeError(stdout, request.ID, -32601, fmt.Sprintf("unknown tool %q", call.Name))
		return
	}
	respondToolResult(stdout, request.ID, result, err)
}

// resolveSession fills in provider/session_id when a tool call omits them,
// by matching the one local binding whose workspace is deps.Workspace. It
// refuses to guess when zero or more than one binding matches, since silently
// picking the wrong session would let an agent pause or inspect the wrong
// binding.
func resolveSession(deps Dependencies, provider, sessionID string) (string, string, error) {
	if provider != "" && sessionID != "" {
		return provider, sessionID, nil
	}
	registry, err := sessions.Load(deps.SessionsPath)
	if err != nil {
		return "", "", fmt.Errorf("read session registry: %w", err)
	}
	candidates := make([]sessions.Binding, 0, 1)
	for _, binding := range sessions.List(registry, provider) {
		if deps.Workspace != "" && binding.Workspace != deps.Workspace {
			continue
		}
		if sessionID != "" && binding.SessionID != sessionID {
			continue
		}
		candidates = append(candidates, binding)
	}
	switch len(candidates) {
	case 0:
		return "", "", fmt.Errorf("no AWP session bound to workspace %q; pass provider and session_id explicitly", deps.Workspace)
	case 1:
		return candidates[0].Provider, candidates[0].SessionID, nil
	default:
		names := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			names = append(names, candidate.Provider+"/"+candidate.SessionID)
		}
		return "", "", fmt.Errorf("multiple AWP sessions are bound to workspace %q (%s); pass provider and session_id explicitly", deps.Workspace, strings.Join(names, ", "))
	}
}

type wakeContextResult struct {
	Provider    string             `json:"provider"`
	SessionID   string             `json:"session_id"`
	CreatedAt   string             `json:"created_at,omitempty"`
	Lifecycle   wake.Lifecycle     `json:"lifecycle"`
	Pending     []wake.EventRecord `json:"pending_events"`
	Permissions []string           `json:"permissions,omitempty"`
}

func wakeContext(deps Dependencies, provider, sessionID string) (any, error) {
	provider, sessionID, err := resolveSession(deps, provider, sessionID)
	if err != nil {
		return nil, err
	}
	store, err := wake.Load(deps.EventsPath)
	if err != nil {
		return nil, fmt.Errorf("read event store: %w", err)
	}
	state, found := wake.Get(store, provider, sessionID)
	if !found {
		state = wake.SessionState{Provider: provider, SessionID: sessionID, Lifecycle: wake.Lifecycle{Status: wake.StatusIdle}}
	}
	return wakeContextResult{
		Provider:    provider,
		SessionID:   sessionID,
		CreatedAt:   state.CreatedAt,
		Lifecycle:   state.Lifecycle,
		Pending:     wake.Pending(state),
		Permissions: state.Permissions,
	}, nil
}

func listPendingEvents(deps Dependencies, provider, sessionID string) (any, error) {
	provider, sessionID, err := resolveSession(deps, provider, sessionID)
	if err != nil {
		return nil, err
	}
	store, err := wake.Load(deps.EventsPath)
	if err != nil {
		return nil, fmt.Errorf("read event store: %w", err)
	}
	state, _ := wake.Get(store, provider, sessionID)
	return map[string]any{
		"provider":       provider,
		"session_id":     sessionID,
		"pending_events": wake.Pending(state),
	}, nil
}

func listSessions(deps Dependencies) (any, error) {
	registry, err := sessions.Load(deps.SessionsPath)
	if err != nil {
		return nil, fmt.Errorf("read session registry: %w", err)
	}
	return sessions.List(registry, ""), nil
}

func pauseSession(deps Dependencies, provider, sessionID, reason string) (any, error) {
	provider, sessionID, err := resolveSession(deps, provider, sessionID)
	if err != nil {
		return nil, err
	}
	store, err := wake.Load(deps.EventsPath)
	if err != nil {
		return nil, fmt.Errorf("read event store: %w", err)
	}
	state := wake.Pause(&store, provider, sessionID, reason)
	if err := wake.Save(deps.EventsPath, store); err != nil {
		return nil, fmt.Errorf("write event store: %w", err)
	}
	return state, nil
}

func resumeSession(deps Dependencies, provider, sessionID string) (any, error) {
	provider, sessionID, err := resolveSession(deps, provider, sessionID)
	if err != nil {
		return nil, err
	}
	store, err := wake.Load(deps.EventsPath)
	if err != nil {
		return nil, fmt.Errorf("read event store: %w", err)
	}
	state, err := wake.Resume(&store, provider, sessionID)
	if err != nil {
		return nil, err
	}
	if err := wake.Save(deps.EventsPath, store); err != nil {
		return nil, fmt.Errorf("write event store: %w", err)
	}
	return state, nil
}

// setAWP registers or updates the binding a resumed agent uses to wake
// itself: the exact argv to run and this runtime's own session id. Provider
// and session_id are required when creating a brand-new binding (AWP cannot
// invent them), but may be omitted to update the one existing binding for
// this workspace, the same way the other tools auto-detect it.
func setAWP(deps Dependencies, args setAWPArgs) (any, error) {
	if len(args.ResumeCommand) == 0 {
		return nil, errors.New("resume_command is required")
	}
	if strings.TrimSpace(args.RuntimeSessionID) == "" {
		return nil, errors.New("runtime_session_id is required")
	}
	provider, sessionID, err := resolveSession(deps, args.Provider, args.SessionID)
	if err != nil {
		return nil, fmt.Errorf("provider and session_id are required to register a new binding (%w)", err)
	}
	adapterName := strings.TrimSpace(args.Adapter)
	if adapterName == "" {
		adapterName = "mcp-configured"
	}
	workspace := args.Workspace
	if workspace == "" {
		workspace = deps.Workspace
	}
	registry, err := sessions.Load(deps.SessionsPath)
	if err != nil {
		return nil, fmt.Errorf("read session registry: %w", err)
	}
	binding, err := sessions.Bind(&registry, sessions.Binding{
		Provider:         provider,
		SessionID:        sessionID,
		Adapter:          adapterName,
		RuntimeSessionID: args.RuntimeSessionID,
		Workspace:        workspace,
		ResumeCommand:    args.ResumeCommand,
		Metadata:         args.Metadata,
	})
	if err != nil {
		return nil, err
	}
	if err := sessions.Save(deps.SessionsPath, registry); err != nil {
		return nil, fmt.Errorf("write session registry: %w", err)
	}
	return binding, nil
}

// configureProvider writes the provider's endpoint into config.json and its
// token into a private, chmod 600 file next to it — never into config.json
// itself, matching how `awp autostart`/`--token-dir` already keep tokens out
// of the main config file.
func configureProvider(deps Dependencies, args configureProviderArgs) (any, error) {
	provider := strings.TrimSpace(args.Provider)
	if provider == "" {
		return nil, errors.New("provider is required")
	}
	if strings.TrimSpace(args.ServiceURL) == "" {
		return nil, errors.New("service_url is required")
	}
	if strings.TrimSpace(args.Token) == "" {
		return nil, errors.New("token is required")
	}

	resolvedConfig, err := config.Path(deps.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	cfg, err := config.Load(resolvedConfig)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read config: %w", err)
		}
		cfg = config.Default()
	}
	if strings.TrimSpace(cfg.DeviceID) == "" {
		hostname, _ := os.Hostname()
		hostname = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(hostname), " ", "-"))
		if hostname == "" {
			hostname = "awp"
		}
		cfg.DeviceID = "dev_" + hostname
	}
	tokenEnv := strings.ToUpper(provider) + "_AWP_TOKEN"
	if err := config.SetProvider(&cfg, provider, config.Provider{ServiceURL: args.ServiceURL, TokenEnv: tokenEnv, MCPServer: args.MCPServer}); err != nil {
		return nil, err
	}
	if err := config.Save(resolvedConfig, cfg); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}

	tokenPath, err := config.TokenPath(deps.ConfigPath, provider)
	if err != nil {
		return nil, fmt.Errorf("resolve token path: %w", err)
	}
	if err := config.SaveToken(tokenPath, args.Token); err != nil {
		return nil, fmt.Errorf("save token: %w", err)
	}

	return map[string]any{"provider": provider, "config_path": resolvedConfig, "token_path": tokenPath}, nil
}

// requestPermissions makes one short-lived connection to the provider and
// waits for its permission.request, recording it locally. It never grants
// anything: grant_permissions is a separate call so the human sees the
// request first.
func requestPermissions(ctx context.Context, deps Dependencies, args requestPermissionsArgs) (any, error) {
	provider, sessionID, err := resolveSession(deps, args.Provider, args.SessionID)
	if err != nil {
		return nil, err
	}
	resolvedConfig, err := config.Path(deps.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	cfg, err := config.Load(resolvedConfig)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	providerConfig, found := cfg.Providers[provider]
	if !found {
		return nil, fmt.Errorf("provider %q is not configured; call configure_provider first", provider)
	}
	token, err := loadProviderToken(deps, provider, providerConfig)
	if err != nil {
		return nil, err
	}
	registry, err := sessions.Load(deps.SessionsPath)
	if err != nil {
		return nil, fmt.Errorf("read session registry: %w", err)
	}
	binding, found := sessions.Get(registry, provider, sessionID)
	if !found {
		return nil, fmt.Errorf("AWP session %s/%s is not bound locally; call set_awp first", provider, sessionID)
	}
	permissionPath := deps.PermissionsPath
	store, err := permissions.Load(permissionPath)
	if err != nil {
		return nil, fmt.Errorf("read permission store: %w", err)
	}

	timeout := 30 * time.Second
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds * float64(time.Second))
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var captured permissions.Request
	runErr := client.Run(requestCtx, client.Options{
		ServiceURL:                 providerConfig.ServiceURL,
		DeviceID:                   cfg.DeviceID,
		Token:                      token,
		Version:                    deps.Version,
		Sessions:                   []client.SessionRegistration{{SessionID: binding.SessionID, Adapter: binding.Adapter, Metadata: binding.Metadata}},
		StopAfterPermissionRequest: true,
		Receive: func(message protocol.Message) error {
			if message.Action != protocol.ActionPermissionRequest {
				return nil
			}
			data, decodeErr := protocol.DecodeData[protocol.PermissionRequestData](message)
			if decodeErr != nil {
				return decodeErr
			}
			if data.SessionID != binding.SessionID {
				return fmt.Errorf("provider requested permissions for unexpected session %q", data.SessionID)
			}
			captured, decodeErr = permissions.RecordRequest(&store, permissionRequestFromMCP(provider, data))
			return decodeErr
		},
	})
	if runErr != nil {
		return nil, fmt.Errorf("connect to provider: %w", runErr)
	}
	if captured.RequestID == "" {
		return nil, errors.New("provider did not send a permission.request")
	}
	if err := permissions.Save(permissionPath, store); err != nil {
		return nil, fmt.Errorf("write permission store: %w", err)
	}
	return captured, nil
}

func permissionRequestFromMCP(provider string, data protocol.PermissionRequestData) permissions.Request {
	items := make([]permissions.RequestedPermission, 0, len(data.Permissions))
	for _, item := range data.Permissions {
		items = append(items, permissions.RequestedPermission{
			ID: item.ID, Title: item.Title, Description: item.Description,
			Risk: item.Risk, Delegation: item.Delegation, MCPTools: item.MCPTools,
		})
	}
	return permissions.Request{Provider: provider, SessionID: data.SessionID, RequestID: data.RequestID, Permissions: items}
}

// loadProviderToken prefers the protected token file (written by
// configure_provider) over the provider's token_env, so a token handed to
// configure_provider works immediately without the human having to export
// an environment variable for this MCP server's own process.
func loadProviderToken(deps Dependencies, provider string, providerConfig config.Provider) (string, error) {
	if tokenPath, err := config.TokenPath(deps.ConfigPath, provider); err == nil {
		if contents, readErr := os.ReadFile(tokenPath); readErr == nil {
			if token := strings.TrimSpace(string(contents)); token != "" {
				return token, nil
			}
		}
	}
	token := strings.TrimSpace(os.Getenv(providerConfig.TokenEnv))
	if token == "" {
		return "", fmt.Errorf("no token available for provider %q: call configure_provider, or set %s", provider, providerConfig.TokenEnv)
	}
	return token, nil
}

// grantPermissions locally approves specific permission ids. It never
// decides what to grant on its own: the caller (an agent reviewing
// request_permissions' output, or a human who already knows what a provider
// needs) picks allow. When the provider has not sent its own
// permission.request (most providers do not implement it), this synthesizes
// a local one covering exactly allow/mcp_tools before granting, so a session
// can still be woken without requiring every provider to implement that
// handshake.
func grantPermissions(deps Dependencies, args grantPermissionsArgs) (any, error) {
	if len(args.Allow) == 0 {
		return nil, errors.New("allow must list at least one permission id")
	}
	provider, sessionID, err := resolveSession(deps, args.Provider, args.SessionID)
	if err != nil {
		return nil, err
	}
	scope := strings.TrimSpace(args.Scope)
	if scope == "" {
		scope = permissions.ScopeBinding
	}
	permissionPath := deps.PermissionsPath
	store, err := permissions.Load(permissionPath)
	if err != nil {
		return nil, fmt.Errorf("read permission store: %w", err)
	}
	if _, found := permissions.GetRequest(store, provider, sessionID); !found {
		if _, err := permissions.RequestLocal(&store, provider, sessionID, args.Allow, args.MCPTools); err != nil {
			return nil, fmt.Errorf("create local permission request: %w", err)
		}
	}
	grant, err := permissions.GrantPermissions(&store, provider, sessionID, scope, args.Allow)
	if err != nil {
		return nil, err
	}
	if err := permissions.Save(permissionPath, store); err != nil {
		return nil, fmt.Errorf("write permission store: %w", err)
	}
	return grant, nil
}

// startDaemon, stopDaemon, and daemonStatus call into internal/daemonctl
// directly — the same package `awp daemon start/stop/status` uses — so both
// surfaces manage the exact same background process the exact same way.
func startDaemon(deps Dependencies, provider string) (any, error) {
	pidPath, err := daemonctl.PIDPath(deps.ConfigPath, "")
	if err != nil {
		return nil, fmt.Errorf("resolve pid path: %w", err)
	}
	executable, err := daemonctl.ExecutablePath()
	if err != nil {
		return nil, fmt.Errorf("locate awp binary: %w", err)
	}
	daemonArgs := []string{"daemon"}
	if deps.ConfigPath != "" {
		daemonArgs = append(daemonArgs, "--config", deps.ConfigPath)
	}
	if tokenDirectory, tokenErr := config.TokenDirectory(deps.ConfigPath); tokenErr == nil {
		daemonArgs = append(daemonArgs, "--token-dir", tokenDirectory)
	}
	if provider != "" {
		daemonArgs = append(daemonArgs, "--provider", provider)
	}
	logPath := filepath.Join(filepath.Dir(pidPath), "daemon.log")
	pid, startErr := daemonctl.Start(daemonctl.StartOptions{Executable: executable, Args: daemonArgs, PIDPath: pidPath, LogPath: logPath})
	if startErr != nil {
		// Report as a result, not a Go error: "already running" is routine,
		// and the calling agent can reason about it in the response text
		// instead of just seeing an opaque tool failure.
		code := "spawn_failed"
		if errors.Is(startErr, daemonctl.ErrAlreadyRunning) {
			code = "already_running"
		}
		return map[string]any{"ok": false, "code": code, "error": startErr.Error()}, nil
	}
	return map[string]any{"ok": true, "pid": pid, "pid_file": pidPath, "log_file": logPath}, nil
}

func stopDaemon(deps Dependencies) (any, error) {
	pidPath, err := daemonctl.PIDPath(deps.ConfigPath, "")
	if err != nil {
		return nil, fmt.Errorf("resolve pid path: %w", err)
	}
	wasRunning, pid, err := daemonctl.Stop(pidPath)
	if err != nil {
		return nil, err
	}
	return map[string]any{"was_running": wasRunning, "pid": pid}, nil
}

func daemonStatus(deps Dependencies) (any, error) {
	pidPath, err := daemonctl.PIDPath(deps.ConfigPath, "")
	if err != nil {
		return nil, fmt.Errorf("resolve pid path: %w", err)
	}
	pid, running, err := daemonctl.Status(pidPath)
	if err != nil {
		return nil, err
	}
	return map[string]any{"running": running, "pid": pid, "pid_file": pidPath}, nil
}

func respondToolResult(stdout io.Writer, id json.RawMessage, result any, err error) {
	if err != nil {
		writeResult(stdout, id, map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		})
		return
	}
	encoded, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		writeResult(stdout, id, map[string]any{
			"content": []map[string]any{{"type": "text", "text": marshalErr.Error()}},
			"isError": true,
		})
		return
	}
	writeResult(stdout, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(encoded)}},
		"isError": false,
	})
}

func writeResult(stdout io.Writer, id json.RawMessage, result any) {
	writeMessage(stdout, rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeError(stdout io.Writer, id json.RawMessage, code int, message string) {
	writeMessage(stdout, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func writeMessage(stdout io.Writer, message rpcResponse) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return
	}
	_, _ = stdout.Write(encoded)
	_, _ = stdout.Write([]byte("\n"))
}
