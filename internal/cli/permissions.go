package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Manifestro/awp/internal/client"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

func runPermissions(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "permissions requires a subcommand: request, pending, grant, list, revoke, or audit")
		return 2
	}
	switch args[0] {
	case "request":
		return runPermissionsRequest(args[1:], stdout, stderr)
	case "pending":
		return runPermissionsPending(args[1:], stdout, stderr)
	case "grant":
		return runPermissionsGrant(args[1:], stdout, stderr)
	case "list":
		return runPermissionsList(args[1:], stdout, stderr)
	case "revoke":
		return runPermissionsRevoke(args[1:], stdout, stderr)
	case "audit":
		return runPermissionsAudit(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown permissions subcommand %q\n", args[0])
		return 2
	}
}

func runPermissionsRequest(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("permissions request", flag.ContinueOnError)
	flags.SetOutput(stderr)
	providerName := flags.String("provider", "", "AWP provider name")
	sessionID := flags.String("session-id", "", "bound AWP session identifier")
	configPath := flags.String("config", "", "config file path")
	sessionStore := flags.String("store", "", "session registry file path")
	permissionStore := flags.String("permissions-store", "", "permission state file path")
	tokenFile := flags.String("token-file", "", "read provider token from a protected file")
	timeout := flags.Duration("timeout", 30*time.Second, "maximum time to wait for provider request")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *providerName == "" || *sessionID == "" {
		return commandError("permissions.request", "target_required", fmt.Errorf("--provider and --session-id are required"), *jsonOutput, stdout, stderr)
	}
	resolvedConfig, err := config.Path(*configPath)
	if err != nil {
		return commandError("permissions.request", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg, err := config.Load(resolvedConfig)
	if err != nil {
		return commandError("permissions.request", "config_read", err, *jsonOutput, stdout, stderr)
	}
	provider, found := cfg.Providers[*providerName]
	if !found {
		return commandError("permissions.request", "provider_not_found", fmt.Errorf("provider %q is not configured", *providerName), *jsonOutput, stdout, stderr)
	}
	registryPath, err := sessions.Path(resolvedConfig, *sessionStore)
	if err != nil {
		return commandError("permissions.request", "registry_path", err, *jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(registryPath)
	if err != nil {
		return commandError("permissions.request", "registry_read", err, *jsonOutput, stdout, stderr)
	}
	binding, found := sessions.Get(registry, *providerName, *sessionID)
	if !found {
		return commandError("permissions.request", "session_not_bound", fmt.Errorf("AWP session %s/%s is not bound locally", *providerName, *sessionID), *jsonOutput, stdout, stderr)
	}
	token, err := loadToken(provider.TokenEnv, *tokenFile)
	if err != nil {
		return commandError("permissions.request", "token_missing", err, *jsonOutput, stdout, stderr)
	}
	path, err := permissions.Path(resolvedConfig, *permissionStore)
	if err != nil {
		return commandError("permissions.request", "permissions_path", err, *jsonOutput, stdout, stderr)
	}
	store, err := permissions.Load(path)
	if err != nil {
		return commandError("permissions.request", "permissions_read", err, *jsonOutput, stdout, stderr)
	}
	var captured permissions.Request
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	err = client.Run(ctx, client.Options{ServiceURL: provider.ServiceURL, DeviceID: cfg.DeviceID, Token: token, Version: Version, Sessions: []client.SessionRegistration{{SessionID: binding.SessionID, Adapter: binding.Adapter, Metadata: binding.Metadata}}, StopAfterPermissionRequest: true, Receive: func(message protocol.Message) error {
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
		captured, decodeErr = permissions.RecordRequest(&store, permissionRequestFromProtocol(*providerName, data))
		return decodeErr
	}})
	if err != nil {
		return commandError("permissions.request", "permission_request_failed", err, *jsonOutput, stdout, stderr)
	}
	if captured.RequestID == "" {
		return commandError("permissions.request", "permission_request_missing", fmt.Errorf("provider did not send permission.request"), *jsonOutput, stdout, stderr)
	}
	if err := permissions.Save(path, store); err != nil {
		return commandError("permissions.request", "permissions_write", err, *jsonOutput, stdout, stderr)
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "permissions.request", Data: map[string]any{"path": path, "request": captured}})
	}
	fmt.Fprintf(stdout, "Provider %s requests permissions for %s:\n", *providerName, *sessionID)
	for _, permission := range captured.Permissions {
		tools := "none"
		if len(permission.MCPTools) > 0 {
			tools = strings.Join(permission.MCPTools, ",")
		}
		fmt.Fprintf(stdout, "  %-32s risk=%-9s delegation=%-16s tools=%s\n    %s\n", permission.ID, permission.Risk, permission.Delegation, tools, permission.Title)
	}
	return 0
}

type permissionStoreFlags struct {
	configPath, permissionPath *string
	jsonOutput                 *bool
}

func addPermissionStoreFlags(flags *flag.FlagSet) permissionStoreFlags {
	return permissionStoreFlags{
		configPath:     flags.String("config", "", "config file path used to locate permission state"),
		permissionPath: flags.String("permissions-store", "", "permission state file path"),
		jsonOutput:     flags.Bool("json", false, "print machine-readable JSON"),
	}
}

func loadPermissionStore(common permissionStoreFlags) (string, permissions.Store, error) {
	path, err := permissions.Path(*common.configPath, *common.permissionPath)
	if err != nil {
		return "", permissions.Store{}, err
	}
	store, err := permissions.Load(path)
	return path, store, err
}

func runPermissionsPending(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("permissions pending", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "optional provider filter")
	common := addPermissionStoreFlags(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	path, store, err := loadPermissionStore(common)
	if err != nil {
		return commandError("permissions.pending", "permissions_read", err, *common.jsonOutput, stdout, stderr)
	}
	requests := permissions.ListRequests(store, *provider)
	if *common.jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "permissions.pending", Data: map[string]any{"path": path, "requests": requests}})
	}
	if len(requests) == 0 {
		fmt.Fprintln(stdout, "No provider permission requests.")
		return 0
	}
	for _, request := range requests {
		fmt.Fprintf(stdout, "%s / %s (%s)\n", request.Provider, request.SessionID, request.RequestID)
		for _, permission := range request.Permissions {
			tools := "none"
			if len(permission.MCPTools) > 0 {
				tools = strings.Join(permission.MCPTools, ",")
			}
			fmt.Fprintf(stdout, "  %-32s risk=%-9s delegation=%-16s tools=%s\n    %s\n", permission.ID, permission.Risk, permission.Delegation, tools, permission.Title)
		}
	}
	return 0
}

func runPermissionsGrant(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("permissions grant", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "AWP provider name")
	sessionID := flags.String("session-id", "", "AWP session identifier whose request is being approved")
	allow := flags.String("allow", "", "comma-separated permission IDs. If the provider has not sent a permission.request (most do not implement it), these are granted directly with no provider round-trip")
	mcpTools := flags.String("mcp-tools", "", "comma-separated provider MCP tool names to enable; only used when there is no existing provider request")
	scope := flags.String("scope", permissions.ScopeBinding, "grant scope: once, binding, or provider")
	sessionStore := flags.String("store", "", "session registry file path")
	common := addPermissionStoreFlags(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *provider == "" || *sessionID == "" || *allow == "" {
		return commandError("permissions.grant", "grant_required", fmt.Errorf("--provider, --session-id, and --allow are required"), *common.jsonOutput, stdout, stderr)
	}
	registryPath, err := sessions.Path(*common.configPath, *sessionStore)
	if err != nil {
		return commandError("permissions.grant", "registry_path", err, *common.jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(registryPath)
	if err != nil {
		return commandError("permissions.grant", "registry_read", err, *common.jsonOutput, stdout, stderr)
	}
	if _, found := sessions.Get(registry, *provider, *sessionID); !found {
		return commandError("permissions.grant", "session_not_bound", fmt.Errorf("AWP session %s/%s is not bound locally", *provider, *sessionID), *common.jsonOutput, stdout, stderr)
	}
	path, store, err := loadPermissionStore(common)
	if err != nil {
		return commandError("permissions.grant", "permissions_read", err, *common.jsonOutput, stdout, stderr)
	}
	allowedIDs := splitCSV(*allow)
	if _, found := permissions.GetRequest(store, *provider, *sessionID); !found {
		if _, err := permissions.RequestLocal(&store, *provider, *sessionID, allowedIDs, splitCSV(*mcpTools)); err != nil {
			return commandError("permissions.grant", "local_request_invalid", err, *common.jsonOutput, stdout, stderr)
		}
	}
	grant, err := permissions.GrantPermissions(&store, *provider, *sessionID, *scope, allowedIDs)
	if err != nil {
		return commandError("permissions.grant", "grant_invalid", err, *common.jsonOutput, stdout, stderr)
	}
	if err := permissions.Save(path, store); err != nil {
		return commandError("permissions.grant", "permissions_write", err, *common.jsonOutput, stdout, stderr)
	}
	if *common.jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "permissions.grant", Data: map[string]any{"path": path, "grant": grant}})
	}
	fmt.Fprintf(stdout, "Granted %s permissions to %s/%s with scope=%s\n", strings.Join(grant.Permissions, ","), *provider, *sessionID, grant.Scope)
	return 0
}

func runPermissionsList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("permissions list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "optional provider filter")
	common := addPermissionStoreFlags(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	path, store, err := loadPermissionStore(common)
	if err != nil {
		return commandError("permissions.list", "permissions_read", err, *common.jsonOutput, stdout, stderr)
	}
	grants := permissions.ListGrants(store, *provider)
	if *common.jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "permissions.list", Data: map[string]any{"path": path, "grants": grants}})
	}
	if len(grants) == 0 {
		fmt.Fprintln(stdout, "No local permission grants.")
		return 0
	}
	for _, grant := range grants {
		target := grant.SessionID
		if target == "" {
			target = "*"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", grant.Provider, target, grant.Scope, strings.Join(grant.Permissions, ","))
	}
	return 0
}

func runPermissionsRevoke(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("permissions revoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "AWP provider name")
	sessionID := flags.String("session-id", "", "AWP session identifier")
	permissionIDs := flags.String("permissions", "", "optional comma-separated permissions; omit to remove matching grants")
	scope := flags.String("scope", "", "optional scope filter")
	common := addPermissionStoreFlags(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *provider == "" || *sessionID == "" {
		return commandError("permissions.revoke", "target_required", fmt.Errorf("--provider and --session-id are required"), *common.jsonOutput, stdout, stderr)
	}
	path, store, err := loadPermissionStore(common)
	if err != nil {
		return commandError("permissions.revoke", "permissions_read", err, *common.jsonOutput, stdout, stderr)
	}
	changed, err := permissions.Revoke(&store, *provider, *sessionID, *scope, splitCSV(*permissionIDs))
	if err != nil {
		return commandError("permissions.revoke", "revoke_failed", err, *common.jsonOutput, stdout, stderr)
	}
	if changed {
		if err := permissions.Save(path, store); err != nil {
			return commandError("permissions.revoke", "permissions_write", err, *common.jsonOutput, stdout, stderr)
		}
	}
	if *common.jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "permissions.revoke", Data: map[string]any{"path": path, "revoked": changed}})
	}
	fmt.Fprintf(stdout, "Permissions revoked=%t\n", changed)
	return 0
}

func runPermissionsAudit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("permissions audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common := addPermissionStoreFlags(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	path, store, err := loadPermissionStore(common)
	if err != nil {
		return commandError("permissions.audit", "permissions_read", err, *common.jsonOutput, stdout, stderr)
	}
	if *common.jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "permissions.audit", Data: map[string]any{"path": path, "entries": store.Audit}})
	}
	for _, entry := range store.Audit {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", entry.Timestamp, entry.Action, entry.Provider, entry.SessionID, strings.Join(entry.Permissions, ","))
	}
	return 0
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func permissionRequestFromProtocol(provider string, data protocol.PermissionRequestData) permissions.Request {
	items := make([]permissions.RequestedPermission, 0, len(data.Permissions))
	for _, item := range data.Permissions {
		items = append(items, permissions.RequestedPermission{
			ID: item.ID, Title: item.Title, Description: item.Description,
			Risk: item.Risk, Delegation: item.Delegation, MCPTools: item.MCPTools,
		})
	}
	return permissions.Request{Provider: provider, SessionID: data.SessionID, RequestID: data.RequestID, Permissions: items}
}
