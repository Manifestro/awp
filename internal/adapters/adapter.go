package adapters

import (
	"context"
	"fmt"
	"io"

	"github.com/Manifestro/awp/internal/adapters/codex"
	"github.com/Manifestro/awp/internal/adapters/command"
	execrunner "github.com/Manifestro/awp/internal/adapters/exec"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

// ErrBindingUnusable marks a failure that comes from the binding itself being
// structurally broken (missing runtime binary, missing workspace directory,
// no resume command configured, and similar) rather than from this one
// event. The caller should treat this differently from an ordinary per-event
// failure: retrying the next event against the same binding will just fail
// the same way, so it is a session-level condition, not an event-level one.
// Both codex and command wrap this same value, so daemon.go can check
// errors.Is against it regardless of which concrete adapter ran.
var ErrBindingUnusable = execrunner.ErrBindingUnusable

type Adapter interface {
	Run(context.Context, sessions.Binding, protocol.DeliveryData, permissions.Authorization, string) error
}

func Resolve(binding sessions.Binding, output io.Writer) (Adapter, error) {
	if len(binding.ResumeCommand) > 0 {
		return command.New(output), nil
	}
	switch binding.Adapter {
	case "codex":
		return codex.New(output), nil
	default:
		return nil, fmt.Errorf("unsupported adapter %q", binding.Adapter)
	}
}
