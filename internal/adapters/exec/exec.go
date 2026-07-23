// Package exec provides the process-execution primitive shared by every
// runtime adapter: run a binary with argv, a working directory, and stdin,
// and stream its output. Nothing here is specific to any one CLI.
package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
)

// ErrBindingUnusable marks a failure caused by the binding itself (missing
// runtime binary, missing or non-directory workspace, no resume command
// configured) rather than by one event. Every adapter wraps this same value
// so callers can check errors.Is(err, exec.ErrBindingUnusable) regardless of
// which concrete adapter produced the failure.
var ErrBindingUnusable = errors.New("runtime adapter binding is unusable")

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
