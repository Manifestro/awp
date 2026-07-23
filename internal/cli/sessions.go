package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/sessions"
	"github.com/Manifestro/awp/internal/wake"
)

func runSessions(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "sessions requires a subcommand: bind, list, remove, pause, resume, or status")
		return 2
	}
	switch args[0] {
	case "bind":
		return runSessionsBind(args[1:], stdout, stderr)
	case "list":
		return runSessionsList(args[1:], stdout, stderr)
	case "remove":
		return runSessionsRemove(args[1:], stdout, stderr)
	case "pause":
		return runSessionsPause(args[1:], stdout, stderr)
	case "resume":
		return runSessionsResume(args[1:], stdout, stderr)
	case "status":
		return runSessionsStatus(args[1:], stdout, stderr)
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
	provider := flags.String("provider", "", "configured AWP provider name; with --all, limits removal to this provider")
	all := flags.Bool("all", false, "remove every local session binding (or every binding for --provider, if given) instead of one")
	configPath := flags.String("config", "", "config file path used to locate the default registry")
	storePath := flags.String("store", "", "session registry file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *all && *sessionID != "" {
		return commandError("sessions.remove", "conflicting_flags", fmt.Errorf("--all and --session-id are mutually exclusive"), *jsonOutput, stdout, stderr)
	}
	if !*all && (*provider == "" || *sessionID == "") {
		return commandError("sessions.remove", "binding_required", fmt.Errorf("--provider and --session-id are required, or pass --all"), *jsonOutput, stdout, stderr)
	}

	path, err := sessions.Path(*configPath, *storePath)
	if err != nil {
		return commandError("sessions.remove", "registry_path", err, *jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(path)
	if err != nil {
		return commandError("sessions.remove", "registry_read", err, *jsonOutput, stdout, stderr)
	}

	if *all {
		candidates := sessions.List(registry, *provider)
		removed := make([]map[string]string, 0, len(candidates))
		for _, binding := range candidates {
			if sessions.Remove(&registry, binding.Provider, binding.SessionID) {
				removed = append(removed, map[string]string{"provider": binding.Provider, "session_id": binding.SessionID})
			}
		}
		if len(removed) > 0 {
			if err := sessions.Save(path, registry); err != nil {
				return commandError("sessions.remove", "registry_write", err, *jsonOutput, stdout, stderr)
			}
		}
		if *jsonOutput {
			return writeJSON(stdout, result{OK: true, Command: "sessions.remove", Data: map[string]any{"path": path, "removed": removed, "count": len(removed)}})
		}
		fmt.Fprintf(stdout, "Removed %d session binding(s)\n", len(removed))
		return 0
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

func runSessionsPause(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sessions pause", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sessionID := flags.String("session-id", "", "opaque AWP session identifier")
	provider := flags.String("provider", "", "configured AWP provider name")
	reason := flags.String("reason", "", "why this session should not be woken right now")
	configPath := flags.String("config", "", "config file path used to locate the default event store")
	eventPathFlag := flags.String("events-store", "", "event dedup/state store file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *provider == "" || *sessionID == "" {
		return commandError("sessions.pause", "binding_required", fmt.Errorf("--provider and --session-id are required"), *jsonOutput, stdout, stderr)
	}
	resolvedConfig, err := config.Path(*configPath)
	if err != nil {
		return commandError("sessions.pause", "config_path", err, *jsonOutput, stdout, stderr)
	}
	path, err := wake.Path(resolvedConfig, *eventPathFlag)
	if err != nil {
		return commandError("sessions.pause", "events_path", err, *jsonOutput, stdout, stderr)
	}
	store, err := wake.Load(path)
	if err != nil {
		return commandError("sessions.pause", "events_read", err, *jsonOutput, stdout, stderr)
	}
	state := wake.Pause(&store, *provider, *sessionID, *reason)
	if err := wake.Save(path, store); err != nil {
		return commandError("sessions.pause", "events_write", err, *jsonOutput, stdout, stderr)
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "sessions.pause", Data: map[string]any{"path": path, "session": state}})
	}
	fmt.Fprintf(stdout, "Paused %s session %s: new deliveries will be held, not run, until resumed\n", *provider, *sessionID)
	return 0
}

func runSessionsResume(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sessions resume", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sessionID := flags.String("session-id", "", "opaque AWP session identifier")
	provider := flags.String("provider", "", "configured AWP provider name")
	configPath := flags.String("config", "", "config file path used to locate the default event store")
	eventPathFlag := flags.String("events-store", "", "event dedup/state store file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *provider == "" || *sessionID == "" {
		return commandError("sessions.resume", "binding_required", fmt.Errorf("--provider and --session-id are required"), *jsonOutput, stdout, stderr)
	}
	resolvedConfig, err := config.Path(*configPath)
	if err != nil {
		return commandError("sessions.resume", "config_path", err, *jsonOutput, stdout, stderr)
	}
	path, err := wake.Path(resolvedConfig, *eventPathFlag)
	if err != nil {
		return commandError("sessions.resume", "events_path", err, *jsonOutput, stdout, stderr)
	}
	store, err := wake.Load(path)
	if err != nil {
		return commandError("sessions.resume", "events_read", err, *jsonOutput, stdout, stderr)
	}
	state, err := wake.Resume(&store, *provider, *sessionID)
	if err != nil {
		return commandError("sessions.resume", "not_found", err, *jsonOutput, stdout, stderr)
	}
	if err := wake.Save(path, store); err != nil {
		return commandError("sessions.resume", "events_write", err, *jsonOutput, stdout, stderr)
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "sessions.resume", Data: map[string]any{"path": path, "session": state}})
	}
	fmt.Fprintf(stdout, "Resumed %s session %s: future deliveries will wake the runtime adapter again\n", *provider, *sessionID)
	return 0
}

func runSessionsStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sessions status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sessionID := flags.String("session-id", "", "opaque AWP session identifier")
	provider := flags.String("provider", "", "configured AWP provider name")
	configPath := flags.String("config", "", "config file path used to locate the default event store")
	eventPathFlag := flags.String("events-store", "", "event dedup/state store file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *provider == "" || *sessionID == "" {
		return commandError("sessions.status", "binding_required", fmt.Errorf("--provider and --session-id are required"), *jsonOutput, stdout, stderr)
	}
	resolvedConfig, err := config.Path(*configPath)
	if err != nil {
		return commandError("sessions.status", "config_path", err, *jsonOutput, stdout, stderr)
	}
	path, err := wake.Path(resolvedConfig, *eventPathFlag)
	if err != nil {
		return commandError("sessions.status", "events_path", err, *jsonOutput, stdout, stderr)
	}
	store, err := wake.Load(path)
	if err != nil {
		return commandError("sessions.status", "events_read", err, *jsonOutput, stdout, stderr)
	}
	state, found := wake.Get(store, *provider, *sessionID)
	if !found {
		state = wake.SessionState{Provider: *provider, SessionID: *sessionID, Lifecycle: wake.Lifecycle{Status: wake.StatusIdle}}
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "sessions.status", Data: map[string]any{"path": path, "session": state, "pending_events": wake.Pending(state)}})
	}
	fmt.Fprintf(stdout, "%s/%s: %s\n", *provider, *sessionID, state.Lifecycle.Status)
	if state.Lifecycle.Reason != "" {
		fmt.Fprintf(stdout, "  reason: %s\n", state.Lifecycle.Reason)
	}
	fmt.Fprintf(stdout, "  pending events: %d\n", len(wake.Pending(state)))
	return 0
}
