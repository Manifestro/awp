package client

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunWithReconnectRetriesUntilContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	var output bytes.Buffer
	err := runWithReconnect(ctx, Options{}, ReconnectPolicy{InitialDelay: time.Nanosecond, MaxDelay: time.Nanosecond}, &output, func(context.Context, Options) error {
		attempts++
		if attempts == 2 {
			cancel()
		}
		return errors.New("offline")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if !strings.Contains(output.String(), "reconnecting") {
		t.Fatalf("missing reconnect diagnostic: %s", output.String())
	}
}
