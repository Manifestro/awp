package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	execrunner "github.com/Manifestro/awp/internal/adapters/exec"
	"github.com/Manifestro/awp/internal/events"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

// ErrBindingUnusable re-exports the shared sentinel from internal/adapters/exec
// (see its doc comment) so existing references to codex.ErrBindingUnusable
// keep compiling unchanged.
var ErrBindingUnusable = execrunner.ErrBindingUnusable

// Runner and CommandRunner are aliases of the shared process-execution
// primitive in internal/adapters/exec; kept here so existing references to
// codex.Runner / codex.CommandRunner keep compiling unchanged.
type Runner = execrunner.Runner
type CommandRunner = execrunner.CommandRunner

type Adapter struct {
	Binary string
	Output io.Writer
	Runner Runner
}

func New(output io.Writer) *Adapter {
	if output == nil {
		output = io.Discard
	}
	return &Adapter{Binary: "codex", Output: output, Runner: CommandRunner{}}
}

func (adapter *Adapter) Run(ctx context.Context, binding sessions.Binding, delivery protocol.DeliveryData, authorization permissions.Authorization, mcpServer string) error {
	if binding.RuntimeSessionID == "" {
		return fmt.Errorf("Codex runtime session id is required")
	}
	if _, err := exec.LookPath(adapter.Binary); err != nil {
		return fmt.Errorf("find Codex CLI: %w: %w", err, ErrBindingUnusable)
	}
	if binding.Workspace != "" {
		info, err := os.Stat(binding.Workspace)
		if err != nil {
			return fmt.Errorf("open Codex workspace: %w: %w", err, ErrBindingUnusable)
		}
		if !info.IsDir() {
			return fmt.Errorf("Codex workspace is not a directory: %s: %w", binding.Workspace, ErrBindingUnusable)
		}
	}
	prompt, err := events.FormatPrompt(delivery)
	if err != nil {
		return err
	}
	if mcpServer == "" {
		return fmt.Errorf("Codex MCP server name is required")
	}
	if !hasPermission(authorization.Permissions, permissions.RuntimeWake) {
		return fmt.Errorf("runtime.wake is not granted")
	}
	if mcpServer == "none" {
		if len(authorization.MCPTools) != 0 {
			return fmt.Errorf("permission request maps to MCP tools but provider has mcp_server=none")
		}
		args := []string{"exec", "resume", "--json", binding.RuntimeSessionID, "-"}
		if err := adapter.Runner.Run(ctx, adapter.Binary, args, binding.Workspace, prompt, adapter.Output); err != nil {
			return fmt.Errorf("resume Codex session %s: %w", binding.RuntimeSessionID, err)
		}
		return nil
	}
	toolsJSON, err := json.Marshal(authorization.MCPTools)
	if err != nil {
		return fmt.Errorf("encode MCP tool allowlist: %w", err)
	}
	serverKey := mcpServer
	args := []string{"exec", "resume", "--json", "-c", "mcp_servers." + serverKey + ".enabled_tools=" + string(toolsJSON)}
	for _, tool := range authorization.MCPTools {
		args = append(args, "-c", "mcp_servers."+serverKey+".tools."+tomlKey(tool)+".approval_mode=\"approve\"")
	}
	args = append(args, binding.RuntimeSessionID, "-")
	if err := adapter.Runner.Run(ctx, adapter.Binary, args, binding.Workspace, prompt, adapter.Output); err != nil {
		return fmt.Errorf("resume Codex session %s: %w", binding.RuntimeSessionID, err)
	}
	return nil
}

func tomlKey(value string) string { return strconv.Quote(value) }

func hasPermission(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
