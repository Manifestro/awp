package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/sessions"
)

func runSessions(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "sessions requires a subcommand: bind, list, or remove")
		return 2
	}
	switch args[0] {
	case "bind":
		return runSessionsBind(args[1:], stdout, stderr)
	case "list":
		return runSessionsList(args[1:], stdout, stderr)
	case "remove":
		return runSessionsRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown sessions subcommand %q\n", args[0])
		return 2
	}
}

func runSessionsBind(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sessions bind", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sessionID := flags.String("session-id", "", "opaque AWP session identifier")
	provider := flags.String("provider", "", "configured AWP provider name")
	adapter := flags.String("adapter", "", "runtime adapter name")
	runtimeSessionID := flags.String("runtime-session-id", "", "local vendor runtime session identifier")
	workspace := flags.String("workspace", "", "working directory used when resuming the runtime")
	metadataJSON := flags.String("metadata-json", "{}", "provider-defined session metadata JSON object")
	configPath := flags.String("config", "", "config file path used to locate the default registry")
	storePath := flags.String("store", "", "session registry file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *provider == "" {
		return commandError("sessions.bind", "provider_required", fmt.Errorf("--provider is required"), *jsonOutput, stdout, stderr)
	}
	resolvedConfig, err := config.Path(*configPath)
	if err != nil {
		return commandError("sessions.bind", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg, err := config.Load(resolvedConfig)
	if err != nil {
		return commandError("sessions.bind", "config_read", err, *jsonOutput, stdout, stderr)
	}
	if _, found := cfg.Providers[*provider]; !found {
		return commandError("sessions.bind", "provider_not_found", fmt.Errorf("provider %q is not configured", *provider), *jsonOutput, stdout, stderr)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(*metadataJSON), &metadata); err != nil || metadata == nil {
		return commandError("sessions.bind", "invalid_metadata", fmt.Errorf("--metadata-json must be a JSON object"), *jsonOutput, stdout, stderr)
	}

	path, err := sessions.Path(resolvedConfig, *storePath)
	if err != nil {
		return commandError("sessions.bind", "registry_path", err, *jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(path)
	if err != nil {
		return commandError("sessions.bind", "registry_read", err, *jsonOutput, stdout, stderr)
	}
	binding, err := sessions.Bind(&registry, sessions.Binding{
		Provider:         *provider,
		SessionID:        *sessionID,
		Adapter:          *adapter,
		RuntimeSessionID: *runtimeSessionID,
		Workspace:        *workspace,
		Metadata:         metadata,
	})
	if err != nil {
		return commandError("sessions.bind", "invalid_binding", err, *jsonOutput, stdout, stderr)
	}
	if err := sessions.Save(path, registry); err != nil {
		return commandError("sessions.bind", "registry_write", err, *jsonOutput, stdout, stderr)
	}

	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "sessions.bind", Data: map[string]any{"path": path, "binding": binding}})
	}
	fmt.Fprintf(stdout, "Bound %s AWP session %s to %s session %s\n", binding.Provider, binding.SessionID, binding.Adapter, binding.RuntimeSessionID)
	return 0
}

func runSessionsList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sessions list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path used to locate the default registry")
	storePath := flags.String("store", "", "session registry file path")
	provider := flags.String("provider", "", "optional provider filter")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, err := sessions.Path(*configPath, *storePath)
	if err != nil {
		return commandError("sessions.list", "registry_path", err, *jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(path)
	if err != nil {
		return commandError("sessions.list", "registry_read", err, *jsonOutput, stdout, stderr)
	}
	bindings := sessions.List(registry, *provider)
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "sessions.list", Data: map[string]any{"path": path, "sessions": bindings}})
	}
	if len(bindings) == 0 {
		fmt.Fprintln(stdout, "No session bindings configured.")
		return 0
	}
	for _, binding := range bindings {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", binding.Provider, binding.SessionID, binding.Adapter, binding.RuntimeSessionID, binding.Workspace)
	}
	return 0
}

func runSessionsRemove(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sessions remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sessionID := flags.String("session-id", "", "opaque AWP session identifier")
	provider := flags.String("provider", "", "configured AWP provider name")
	configPath := flags.String("config", "", "config file path used to locate the default registry")
	storePath := flags.String("store", "", "session registry file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *provider == "" || *sessionID == "" {
		return commandError("sessions.remove", "binding_required", fmt.Errorf("--provider and --session-id are required"), *jsonOutput, stdout, stderr)
	}

	path, err := sessions.Path(*configPath, *storePath)
	if err != nil {
		return commandError("sessions.remove", "registry_path", err, *jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(path)
	if err != nil {
		return commandError("sessions.remove", "registry_read", err, *jsonOutput, stdout, stderr)
	}
	removed := sessions.Remove(&registry, *provider, *sessionID)
	if removed {
		if err := sessions.Save(path, registry); err != nil {
			return commandError("sessions.remove", "registry_write", err, *jsonOutput, stdout, stderr)
		}
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "sessions.remove", Data: map[string]any{"path": path, "provider": *provider, "session_id": *sessionID, "removed": removed}})
	}
	fmt.Fprintf(stdout, "Provider %s session %s removed=%t\n", *provider, *sessionID, removed)
	return 0
}
