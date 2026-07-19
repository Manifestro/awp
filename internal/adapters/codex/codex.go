package codex

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/Manifestro/awp/internal/events"
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

func (adapter *Adapter) Run(ctx context.Context, binding sessions.Binding, delivery protocol.DeliveryData) error {
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
	args := []string{"exec", "resume", "--json", binding.RuntimeSessionID, "-"}
	if err := adapter.Runner.Run(ctx, adapter.Binary, args, binding.Workspace, prompt, adapter.Output); err != nil {
		return fmt.Errorf("resume Codex session %s: %w", binding.RuntimeSessionID, err)
	}
	return nil
}
