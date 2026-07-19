package cli

import (
	"flag"
	"fmt"
	"io"

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
	adapter := flags.String("adapter", "", "runtime adapter name")
	runtimeSessionID := flags.String("runtime-session-id", "", "local vendor runtime session identifier")
	workspace := flags.String("workspace", "", "working directory used when resuming the runtime")
	configPath := flags.String("config", "", "config file path used to locate the default registry")
	storePath := flags.String("store", "", "session registry file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, err := sessions.Path(*configPath, *storePath)
	if err != nil {
		return commandError("sessions.bind", "registry_path", err, *jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(path)
	if err != nil {
		return commandError("sessions.bind", "registry_read", err, *jsonOutput, stdout, stderr)
	}
	binding, err := sessions.Bind(&registry, sessions.Binding{
		SessionID:        *sessionID,
		Adapter:          *adapter,
		RuntimeSessionID: *runtimeSessionID,
		Workspace:        *workspace,
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
	fmt.Fprintf(stdout, "Bound AWP session %s to %s session %s\n", binding.SessionID, binding.Adapter, binding.RuntimeSessionID)
	return 0
}

func runSessionsList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sessions list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path used to locate the default registry")
	storePath := flags.String("store", "", "session registry file path")
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
	bindings := sessions.List(registry)
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "sessions.list", Data: map[string]any{"path": path, "sessions": bindings}})
	}
	if len(bindings) == 0 {
		fmt.Fprintln(stdout, "No session bindings configured.")
		return 0
	}
	for _, binding := range bindings {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", binding.SessionID, binding.Adapter, binding.RuntimeSessionID, binding.Workspace)
	}
	return 0
}

func runSessionsRemove(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sessions remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sessionID := flags.String("session-id", "", "opaque AWP session identifier")
	configPath := flags.String("config", "", "config file path used to locate the default registry")
	storePath := flags.String("store", "", "session registry file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *sessionID == "" {
		return commandError("sessions.remove", "session_id_required", fmt.Errorf("--session-id is required"), *jsonOutput, stdout, stderr)
	}

	path, err := sessions.Path(*configPath, *storePath)
	if err != nil {
		return commandError("sessions.remove", "registry_path", err, *jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(path)
	if err != nil {
		return commandError("sessions.remove", "registry_read", err, *jsonOutput, stdout, stderr)
	}
	removed := sessions.Remove(&registry, *sessionID)
	if removed {
		if err := sessions.Save(path, registry); err != nil {
			return commandError("sessions.remove", "registry_write", err, *jsonOutput, stdout, stderr)
		}
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "sessions.remove", Data: map[string]any{"path": path, "session_id": *sessionID, "removed": removed}})
	}
	fmt.Fprintf(stdout, "Session %s removed=%t\n", *sessionID, removed)
	return 0
}
