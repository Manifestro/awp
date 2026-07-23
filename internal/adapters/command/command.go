// Package command implements a generic runtime adapter: instead of Go code
// hardcoding how to build one CLI's resume invocation (as codex does), it
// executes whatever argv template the binding was configured with via
// set_awp. This lets any CLI runtime register itself with AWP without a new
// adapter package being written and shipped for it.
package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	execrunner "github.com/Manifestro/awp/internal/adapters/exec"
	"github.com/Manifestro/awp/internal/events"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

// ErrBindingUnusable re-exports the shared sentinel from internal/adapters/exec.
var ErrBindingUnusable = execrunner.ErrBindingUnusable

type Adapter struct {
	Output io.Writer
	Runner execrunner.Runner
}

func New(output io.Writer) *Adapter {
	if output == nil {
		output = io.Discard
	}
	return &Adapter{Output: output, Runner: execrunner.CommandRunner{}}
}

// Run substitutes the binding's ResumeCommand placeholders and executes it.
// Only AWP-controlled values are substitutable ({runtime_session_id},
// {workspace}, {mcp_server}, {mcp_tools_json}, {mcp_tools_prefixed_csv},
// {prompt}); the provider event itself is never interpolated into the
// template. Go's exec never invokes a shell to run this argv, so there is no
// shell-metacharacter injection risk regardless of what a placeholder's
// value contains; {prompt} still carries untrusted provider data, exactly as
// it already does when piped over stdin, and the receiving runtime is
// responsible for treating it as untrusted input rather than as
// instructions (see events.FormatPrompt).
func (adapter *Adapter) Run(ctx context.Context, binding sessions.Binding, delivery protocol.DeliveryData, authorization permissions.Authorization, mcpServer string) error {
	if len(binding.ResumeCommand) == 0 {
		return fmt.Errorf("resume command is not configured: %w", ErrBindingUnusable)
	}
	if binding.Workspace != "" {
		info, err := os.Stat(binding.Workspace)
		if err != nil {
			return fmt.Errorf("open workspace: %w: %w", err, ErrBindingUnusable)
		}
		if !info.IsDir() {
			return fmt.Errorf("workspace is not a directory: %s: %w", binding.Workspace, ErrBindingUnusable)
		}
	}
	prompt, err := events.FormatPrompt(delivery)
	if err != nil {
		return err
	}
	if !hasPermission(authorization.Permissions, permissions.RuntimeWake) {
		return fmt.Errorf("runtime.wake is not granted")
	}
	toolsJSON, err := json.Marshal(authorization.MCPTools)
	if err != nil {
		return fmt.Errorf("encode MCP tool allowlist: %w", err)
	}

	substitutions := map[string]string{
		"{runtime_session_id}":     binding.RuntimeSessionID,
		"{workspace}":              binding.Workspace,
		"{mcp_server}":             mcpServer,
		"{mcp_tools_json}":         string(toolsJSON),
		"{mcp_tools_prefixed_csv}": prefixedToolCSV(mcpServer, authorization.MCPTools),
		"{prompt}":                 string(prompt),
	}
	args := make([]string, len(binding.ResumeCommand))
	for index, token := range binding.ResumeCommand {
		args[index] = substitute(token, substitutions)
	}

	commandName, commandArgs := args[0], args[1:]
	if err := adapter.Runner.Run(ctx, commandName, commandArgs, binding.Workspace, prompt, adapter.Output); err != nil {
		return fmt.Errorf("run configured resume command: %w", err)
	}
	return nil
}

// prefixedToolCSV formats granted MCP tool names as
// "mcp__<mcpServer>__<tool>,..." — the naming convention Claude Code (and
// other MCP-hosting CLIs using the same scheme) expects for --allowedTools.
// Recomputed at every wake from the current authorization, so it always
// reflects whatever is actually granted right now, the same way
// {mcp_tools_json} does for Codex's -c overrides.
func prefixedToolCSV(mcpServer string, tools []string) string {
	prefixed := make([]string, len(tools))
	for index, tool := range tools {
		prefixed[index] = "mcp__" + mcpServer + "__" + tool
	}
	return strings.Join(prefixed, ",")
}

func substitute(token string, values map[string]string) string {
	for placeholder, value := range values {
		token = strings.ReplaceAll(token, placeholder, value)
	}
	return token
}

func hasPermission(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
