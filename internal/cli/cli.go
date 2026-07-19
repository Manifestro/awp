package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Manifestro/awp/internal/adapters"
	"github.com/Manifestro/awp/internal/client"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

var Version = "0.1.0-dev"

type result struct {
	OK      bool        `json:"ok"`
	Command string      `json:"command"`
	Data    any         `json:"data,omitempty"`
	Error   *errorValue `json:"error,omitempty"`
}

type errorValue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "connect":
		return runConnect(args[1:], stdout, stderr)
	case "sessions":
		return runSessions(args[1:], stdout, stderr)
	case "permissions":
		return runPermissions(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr)
	case "autostart":
		return runAutostart(args[1:], stdout, stderr)
	case "daemon":
		return runDaemon(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runConnect(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	providerName := flags.String("provider", "", "AWP provider name")
	jsonOutput := flags.Bool("json", false, "print received messages as JSON Lines")
	once := flags.Bool("once", false, "exit after acknowledging one event.deliver message")
	timeout := flags.Duration("timeout", 0, "optional connection timeout, for example 30s")
	sessionID := flags.String("session-id", "", "AWP session binding to register after connecting")
	storePath := flags.String("store", "", "session registry file path")
	permissionPathFlag := flags.String("permissions-store", "", "permission state file path")
	tokenFile := flags.String("token-file", "", "read bearer token from a protected file")
	reconnect := flags.Bool("reconnect", false, "reconnect forever with exponential backoff")
	reconnectInitial := flags.Duration("reconnect-initial", time.Second, "initial reconnect delay")
	reconnectMax := flags.Duration("reconnect-max", 30*time.Second, "maximum reconnect delay")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, err := config.Path(*configPath)
	if err != nil {
		return commandError("connect", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return commandError("connect", "config_read", err, *jsonOutput, stdout, stderr)
	}
	if err := config.Validate(cfg); err != nil {
		return commandError("connect", "invalid_config", err, *jsonOutput, stdout, stderr)
	}
	if *providerName == "" {
		return commandError("connect", "provider_required", errors.New("--provider is required"), *jsonOutput, stdout, stderr)
	}
	provider, found := cfg.Providers[*providerName]
	if !found {
		return commandError("connect", "provider_not_found", fmt.Errorf("provider %q is not configured", *providerName), *jsonOutput, stdout, stderr)
	}
	token, err := loadToken(provider.TokenEnv, *tokenFile)
	if err != nil {
		return commandError("connect", "token_missing", err, *jsonOutput, stdout, stderr)
	}

	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	permissionPath := ""
	permissionState := permissions.NewStore()
	var permissionLock sync.Mutex
	if *sessionID != "" {
		permissionPath, err = permissions.Path(path, *permissionPathFlag)
		if err != nil {
			return commandError("connect", "permissions_path", err, *jsonOutput, stdout, stderr)
		}
		permissionState, err = permissions.Load(permissionPath)
		if err != nil {
			return commandError("connect", "permissions_read", err, *jsonOutput, stdout, stderr)
		}
	}
	receive := func(message protocol.Message) error {
		if message.Action == protocol.ActionPermissionRequest && *sessionID != "" {
			data, decodeErr := protocol.DecodeData[protocol.PermissionRequestData](message)
			if decodeErr != nil {
				return decodeErr
			}
			if data.SessionID != *sessionID {
				return fmt.Errorf("provider requested permissions for unexpected session %q", data.SessionID)
			}
			permissionLock.Lock()
			_, recordErr := permissions.RecordRequest(&permissionState, permissionRequestFromProtocol(*providerName, data))
			if recordErr == nil {
				recordErr = permissions.Save(permissionPath, permissionState)
			}
			permissionLock.Unlock()
			if recordErr != nil {
				return recordErr
			}
		}
		if *jsonOutput {
			return json.NewEncoder(stdout).Encode(message)
		}
		fmt.Fprintf(stdout, "received %-18s id=%s\n", message.Action, message.ID)
		return nil
	}
	var adapterName string
	var clientSessions []client.SessionRegistration
	var handle func(context.Context, protocol.DeliveryData) error
	if *sessionID != "" {
		registryPath, err := sessions.Path(*configPath, *storePath)
		if err != nil {
			return commandError("connect", "registry_path", err, *jsonOutput, stdout, stderr)
		}
		registry, err := sessions.Load(registryPath)
		if err != nil {
			return commandError("connect", "registry_read", err, *jsonOutput, stdout, stderr)
		}
		binding, found := sessions.Get(registry, *providerName, *sessionID)
		if !found {
			return commandError("connect", "session_not_bound", fmt.Errorf("AWP session %s is not bound locally", *sessionID), *jsonOutput, stdout, stderr)
		}
		resolved, err := adapters.Resolve(binding, stderr)
		if err != nil {
			return commandError("connect", "adapter_unavailable", err, *jsonOutput, stdout, stderr)
		}
		adapterName = binding.Adapter
		clientSessions = []client.SessionRegistration{{SessionID: binding.SessionID, Adapter: binding.Adapter, Metadata: binding.Metadata}}
		handle = func(ctx context.Context, delivery protocol.DeliveryData) error {
			permissionLock.Lock()
			authorization, authorizationErr := permissions.Authorize(&permissionState, *providerName, *sessionID, true)
			if authorizationErr == nil {
				authorizationErr = permissions.Save(permissionPath, permissionState)
			}
			permissionLock.Unlock()
			if authorizationErr != nil {
				return authorizationErr
			}
			mcpServer := provider.MCPServer
			if mcpServer == "" {
				mcpServer = *providerName
			}
			return resolved.Run(ctx, binding, delivery, authorization, mcpServer)
		}
	}
	clientOptions := client.Options{
		ServiceURL: provider.ServiceURL,
		DeviceID:   cfg.DeviceID,
		Token:      token,
		Version:    Version,
		SessionID:  *sessionID,
		Adapter:    adapterName,
		Sessions:   clientSessions,
		Once:       *once,
		Receive:    receive,
		Handle:     handle,
	}
	if *reconnect {
		err = client.RunWithReconnect(ctx, clientOptions, client.ReconnectPolicy{
			InitialDelay: *reconnectInitial,
			MaxDelay:     *reconnectMax,
		}, stderr)
	} else {
		err = client.Run(ctx, clientOptions)
	}
	if err != nil {
		code := "connection_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "timeout"
		}
		return commandError("connect", code, err, *jsonOutput, stdout, stderr)
	}
	return 0
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "version", Data: map[string]string{"version": Version}})
	}
	fmt.Fprintln(stdout, Version)
	return 0
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "config requires a subcommand: set, show, or remove")
		return 2
	}
	switch args[0] {
	case "set":
		return runConfigSet(args[1:], stdout, stderr)
	case "show":
		return runConfigShow(args[1:], stdout, stderr)
	case "remove":
		return runConfigRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown config subcommand %q\n", args[0])
		return 2
	}
}

func runConfigSet(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("config set", flag.ContinueOnError)
	flags.SetOutput(stderr)
	serviceURL := flags.String("service-url", "", "provider-owned AWP WebSocket URL")
	providerName := flags.String("provider", "", "provider name, for example sinores")
	deviceID := flags.String("device-id", "", "opaque AWP device identifier; required for the first provider")
	tokenEnv := flags.String("token-env", "", "environment variable containing this provider's bearer token")
	mcpServer := flags.String("mcp-server", "", "Codex MCP server name; defaults to provider name; use none when no MCP exists")
	configPath := flags.String("config", "", "config file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, err := config.Path(*configPath)
	if err != nil {
		return commandError("config.set", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg := config.Default()
	if existing, loadErr := config.Load(path); loadErr == nil {
		cfg = existing
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return commandError("config.set", "config_read", loadErr, *jsonOutput, stdout, stderr)
	}
	if *deviceID != "" {
		cfg.DeviceID = *deviceID
	}
	if err := config.SetProvider(&cfg, *providerName, config.Provider{ServiceURL: *serviceURL, TokenEnv: *tokenEnv, MCPServer: *mcpServer}); err != nil {
		return commandError("config.set", "invalid_provider", err, *jsonOutput, stdout, stderr)
	}
	if err := config.Save(path, cfg); err != nil {
		return commandError("config.set", "invalid_config", err, *jsonOutput, stdout, stderr)
	}

	data := map[string]any{"path": path, "provider": *providerName, "config": cfg}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "config.set", Data: data})
	}
	fmt.Fprintf(stdout, "Provider %s saved to %s\n", *providerName, path)
	return 0
}

func runConfigShow(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("config show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, err := config.Path(*configPath)
	if err != nil {
		return commandError("config.show", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return commandError("config.show", "config_read", err, *jsonOutput, stdout, stderr)
	}

	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "config.show", Data: map[string]any{"path": path, "config": cfg}})
	}
	fmt.Fprintf(stdout, "Config: %s\nDevice: %s\n", path, cfg.DeviceID)
	for name, provider := range cfg.Providers {
		mcpServer := provider.MCPServer
		if mcpServer == "" {
			mcpServer = name
		}
		fmt.Fprintf(stdout, "Provider: %s\n  Service: %s\n  Token environment: %s\n  MCP server: %s\n", name, provider.ServiceURL, provider.TokenEnv, mcpServer)
	}
	return 0
}

func runConfigRemove(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("config remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	providerName := flags.String("provider", "", "provider name")
	configPath := flags.String("config", "", "config file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *providerName == "" {
		return commandError("config.remove", "provider_required", errors.New("--provider is required"), *jsonOutput, stdout, stderr)
	}
	path, err := config.Path(*configPath)
	if err != nil {
		return commandError("config.remove", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return commandError("config.remove", "config_read", err, *jsonOutput, stdout, stderr)
	}
	_, removed := cfg.Providers[*providerName]
	delete(cfg.Providers, *providerName)
	if removed {
		if len(cfg.Providers) == 0 {
			return commandError("config.remove", "last_provider", errors.New("cannot remove the last provider; add another provider first"), *jsonOutput, stdout, stderr)
		}
		if err := config.Save(path, cfg); err != nil {
			return commandError("config.remove", "config_write", err, *jsonOutput, stdout, stderr)
		}
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "config.remove", Data: map[string]any{"path": path, "provider": *providerName, "removed": removed}})
	}
	fmt.Fprintf(stdout, "Provider %s removed=%t\n", *providerName, removed)
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	storePath := flags.String("store", "", "session registry file path")
	permissionPathFlag := flags.String("permissions-store", "", "permission state file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, pathErr := config.Path(*configPath)
	checks := make([]check, 0, 8)
	var cfg config.Config
	if pathErr != nil {
		checks = append(checks, check{Name: "config", OK: false, Message: pathErr.Error()})
	} else {
		loaded, err := config.Load(path)
		if err != nil {
			checks = append(checks, check{Name: "config", OK: false, Message: err.Error()})
		} else if err := config.Validate(loaded); err != nil {
			checks = append(checks, check{Name: "config", OK: false, Message: err.Error()})
		} else {
			cfg = loaded
			checks = append(checks, check{Name: "config", OK: true, Message: path})
		}
	}

	providerChecks := 0
	for name, provider := range cfg.Providers {
		providerChecks++
		if os.Getenv(provider.TokenEnv) == "" {
			checks = append(checks, check{Name: "provider:" + name, OK: false, Message: provider.TokenEnv + " is not set"})
		} else {
			checks = append(checks, check{Name: "provider:" + name, OK: true, Message: provider.ServiceURL + " using " + provider.TokenEnv})
		}
	}
	if pathErr == nil && cfg.Version != "" {
		registryPath, registryPathErr := sessions.Path(path, *storePath)
		permissionPath, permissionPathErr := permissions.Path(path, *permissionPathFlag)
		if registryPathErr != nil || permissionPathErr != nil {
			message := "resolve local state"
			if registryPathErr != nil {
				message = registryPathErr.Error()
			} else if permissionPathErr != nil {
				message = permissionPathErr.Error()
			}
			checks = append(checks, check{Name: "permissions", OK: false, Message: message})
		} else {
			registry, registryErr := sessions.Load(registryPath)
			permissionState, permissionErr := permissions.Load(permissionPath)
			if registryErr != nil || permissionErr != nil {
				message := "read local state"
				if registryErr != nil {
					message = registryErr.Error()
				} else if permissionErr != nil {
					message = permissionErr.Error()
				}
				checks = append(checks, check{Name: "permissions", OK: false, Message: message})
			} else {
				for _, binding := range sessions.List(registry, "") {
					_, grantErr := permissions.Authorize(&permissionState, binding.Provider, binding.SessionID, false)
					checks = append(checks, check{Name: "permission:" + binding.Provider + "/" + binding.SessionID, OK: grantErr == nil, Message: func() string {
						if grantErr != nil {
							return grantErr.Error()
						}
						return "runtime.wake granted"
					}()})
				}
			}
		}
	}

	codexCheck := executableCheck("codex")
	claudeCheck := executableCheck("claude")
	checks = append(checks, codexCheck, claudeCheck)
	ok := true
	for _, item := range checks[:len(checks)-2] {
		if !item.OK {
			ok = false
		}
	}
	if providerChecks == 0 {
		ok = false
	}
	if !codexCheck.OK && !claudeCheck.OK {
		ok = false
	}

	if *jsonOutput {
		code := writeJSON(stdout, result{OK: ok, Command: "doctor", Data: map[string]any{"checks": checks}})
		if code != 0 || !ok {
			return 1
		}
		return 0
	}
	for _, item := range checks {
		state := "ok"
		if !item.OK {
			state = "fail"
		}
		fmt.Fprintf(stdout, "%-4s %-8s %s\n", state, item.Name, item.Message)
	}
	if !ok {
		return 1
	}
	return 0
}

func executableCheck(name string) check {
	path, err := exec.LookPath(name)
	if err != nil {
		return check{Name: name, OK: false, Message: "not found in PATH"}
	}
	return check{Name: name, OK: true, Message: path}
}

func commandError(command, code string, err error, jsonOutput bool, stdout, stderr io.Writer) int {
	if jsonOutput {
		if writeJSON(stdout, result{OK: false, Command: command, Error: &errorValue{Code: code, Message: err.Error()}}) != 0 {
			return 1
		}
		return 1
	}
	fmt.Fprintln(stderr, err)
	return 1
}

func writeJSON(writer io.Writer, value any) int {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 1
	}
	return 0
}

func printUsage(writer io.Writer) {
	fmt.Fprint(writer, strings.TrimSpace(`Agent Wake Protocol client

Usage:
  awp version [--json]
  awp config set --provider <name> --service-url <wss://.../awp> --token-env <ENV> [--mcp-server <name>] [--device-id <id>] [--config <path>] [--json]
  awp config show [--config <path>] [--json]
  awp config remove --provider <name> [--config <path>] [--json]
  awp doctor [--config <path>] [--store <path>] [--permissions-store <path>] [--json]
  awp sessions bind --provider <name> --session-id <id> --adapter codex --runtime-session-id <id> [--workspace <path>] [--metadata-json <object>] [--json]
  awp sessions list [--provider <name>] [--json]
  awp sessions remove --provider <name> --session-id <id> [--json]
  awp permissions request --provider <name> --session-id <id> [--timeout 30s] [--json]
  awp permissions pending [--provider <name>] [--json]
  awp permissions grant --provider <name> --session-id <id> --allow <ids> [--scope once|binding|provider] [--json]
  awp permissions list [--provider <name>] [--json]
  awp permissions revoke --provider <name> --session-id <id> [--permissions <ids>] [--scope <scope>] [--json]
  awp permissions audit [--json]
  awp update check [--json]
  awp update install [--json]
  awp update auto enable [--interval-hours 24] [--json]
  awp update auto disable [--json]
  awp update auto status [--json]
  awp daemon [--config <path>] [--store <path>] [--permissions-store <path>] [--token-dir <path>] [--once] [--timeout 30s] [--json]
  awp autostart enable [--start-now] [--json]
  awp autostart status [--json]
  awp autostart disable [--json]
  awp connect --provider <name> [--config <path>] [--session-id <id>] [--store <path>] [--permissions-store <path>] [--token-file <path>] [--reconnect] [--once] [--timeout 30s] [--json]

Environment:
  AWP_CONFIG  Override the default configuration path.
  AWP_SESSIONS Override the default session registry path.
  AWP_PERMISSIONS Override the default permission state path.
  Provider token environment variables are configured per provider.
`)+"\n")
}
