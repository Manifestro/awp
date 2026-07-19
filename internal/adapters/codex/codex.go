package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/Manifestro/awp/internal/events"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

type Runner interface {
	Run(ctx context.Context, command string, args []string, directory string, stdin []byte, output io.Writer) error
}

type CommandRunner struct{}

func (CommandRunner) Run(
	ctx context.Context,
	command string,
	args []string,
	directory string,
	stdin []byte,
	output io.Writer,
) error {
	process := exec.CommandContext(ctx, command, args...)
	process.Dir = directory
	process.Stdin = bytes.NewReader(stdin)
	process.Stdout = output
	process.Stderr = output
	return process.Run()
}

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
		return fmt.Errorf("find Codex CLI: %w", err)
	}
	if binding.Workspace != "" {
		info, err := os.Stat(binding.Workspace)
		if err != nil {
			return fmt.Errorf("open Codex workspace: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("Codex workspace is not a directory: %s", binding.Workspace)
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
