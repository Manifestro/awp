package client

import (
	"context"
	"fmt"
	"io"
	"time"
)

type ReconnectPolicy struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

func RunWithReconnect(ctx context.Context, options Options, policy ReconnectPolicy, output io.Writer) error {
	return runWithReconnect(ctx, options, policy, output, Run)
}

func runWithReconnect(
	ctx context.Context,
	options Options,
	policy ReconnectPolicy,
	output io.Writer,
	connect func(context.Context, Options) error,
) error {
	if policy.InitialDelay <= 0 {
		policy.InitialDelay = time.Second
	}
	if policy.MaxDelay < policy.InitialDelay {
		policy.MaxDelay = 30 * time.Second
	}
	delay := policy.InitialDelay
	for {
		err := connect(ctx, options)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if options.Once && err == nil {
			return nil
		}
		if output != nil {
			if err != nil {
				fmt.Fprintf(output, "AWP connection ended: %v; reconnecting in %s\n", err, delay)
			} else {
				fmt.Fprintf(output, "AWP connection closed; reconnecting in %s\n", delay)
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < policy.MaxDelay {
			delay *= 2
			if delay > policy.MaxDelay {
				delay = policy.MaxDelay
			}
		}
	}
}
