package adapters

import (
	"context"
	"fmt"
	"io"

	"github.com/Manifestro/awp/internal/adapters/codex"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

type Adapter interface {
	Run(context.Context, sessions.Binding, protocol.DeliveryData, permissions.Authorization, string) error
}

func Resolve(binding sessions.Binding, output io.Writer) (Adapter, error) {
	switch binding.Adapter {
	case "codex":
		return codex.New(output), nil
	default:
		return nil, fmt.Errorf("unsupported adapter %q", binding.Adapter)
	}
}
