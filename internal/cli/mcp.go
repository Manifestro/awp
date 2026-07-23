package cli

import (
	"context"
	"flag"
	"io"
	"os"

	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/mcpserver"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/sessions"
	"github.com/Manifestro/awp/internal/wake"
)

// runMCP starts the local introspection MCP server on stdio. It is meant to
// be launched as a subprocess by an agent runtime's own MCP client (Codex,
// Claude Code, ...), the same way any other MCP server is configured, not
// invoked directly by a person.
func runMCP(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path used to locate the default session registry and event store")
	storePath := flags.String("store", "", "session registry file path")
	eventPathFlag := flags.String("events-store", "", "event dedup/state store file path")
	permissionPathFlag := flags.String("permissions-store", "", "permission state file path")
	workspace := flags.String("workspace", "", "workspace directory used to auto-detect the local session; defaults to the current directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	resolvedConfig, err := config.Path(*configPath)
	if err != nil {
		return commandError("mcp", "config_path", err, false, stdout, stderr)
	}
	sessionsPath, err := sessions.Path(resolvedConfig, *storePath)
	if err != nil {
		return commandError("mcp", "registry_path", err, false, stdout, stderr)
	}
	eventsPath, err := wake.Path(resolvedConfig, *eventPathFlag)
	if err != nil {
		return commandError("mcp", "events_path", err, false, stdout, stderr)
	}
	permissionsPath, err := permissions.Path(resolvedConfig, *permissionPathFlag)
	if err != nil {
		return commandError("mcp", "permissions_path", err, false, stdout, stderr)
	}

	deps := mcpserver.Dependencies{
		ConfigPath:      *configPath,
		SessionsPath:    sessionsPath,
		EventsPath:      eventsPath,
		PermissionsPath: permissionsPath,
		Workspace:       *workspace,
		Version:         Version,
	}
	if err := mcpserver.Run(context.Background(), os.Stdin, stdout, deps); err != nil {
		return commandError("mcp", "server_failed", err, false, stdout, stderr)
	}
	return 0
}
