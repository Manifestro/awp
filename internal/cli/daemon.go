package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Manifestro/awp/internal/adapters"
	"github.com/Manifestro/awp/internal/client"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

func runDaemon(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("daemon", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	storePath := flags.String("store", "", "session registry file path")
	tokenFile := flags.String("token-file", "", "read bearer token from a protected file")
	jsonOutput := flags.Bool("json", false, "print received messages as JSON Lines")
	once := flags.Bool("once", false, "exit after processing one delivered event")
	timeout := flags.Duration("timeout", 0, "optional daemon timeout")
	reconnectInitial := flags.Duration("reconnect-initial", time.Second, "initial reconnect delay")
	reconnectMax := flags.Duration("reconnect-max", 30*time.Second, "maximum reconnect delay")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	resolvedConfig, err := config.Path(*configPath)
	if err != nil {
		return commandError("daemon", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg, err := config.Load(resolvedConfig)
	if err != nil {
		return commandError("daemon", "config_read", err, *jsonOutput, stdout, stderr)
	}
	if err := config.Validate(cfg); err != nil {
		return commandError("daemon", "invalid_config", err, *jsonOutput, stdout, stderr)
	}
	token, err := loadToken(cfg.TokenEnv, *tokenFile)
	if err != nil {
		return commandError("daemon", "token_missing", err, *jsonOutput, stdout, stderr)
	}
	registryPath, err := sessions.Path(resolvedConfig, *storePath)
	if err != nil {
		return commandError("daemon", "registry_path", err, *jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(registryPath)
	if err != nil {
		return commandError("daemon", "registry_read", err, *jsonOutput, stdout, stderr)
	}
	bindings := sessions.List(registry)
	if len(bindings) == 0 {
		return commandError("daemon", "sessions_empty", errors.New("no local AWP sessions are bound"), *jsonOutput, stdout, stderr)
	}

	registrations := make([]client.SessionRegistration, 0, len(bindings))
	handlers := make(map[string]struct {
		binding sessions.Binding
		adapter adapters.Adapter
	}, len(bindings))
	locks := make(map[string]*sync.Mutex, len(bindings))
	for _, binding := range bindings {
		resolved, resolveErr := adapters.Resolve(binding, stderr)
		if resolveErr != nil {
			return commandError("daemon", "adapter_unavailable", resolveErr, *jsonOutput, stdout, stderr)
		}
		registrations = append(registrations, client.SessionRegistration{
			SessionID: binding.SessionID,
			Adapter:   binding.Adapter,
			Metadata:  map[string]any{},
		})
		handlers[binding.SessionID] = struct {
			binding sessions.Binding
			adapter adapters.Adapter
		}{binding: binding, adapter: resolved}
		locks[binding.SessionID] = &sync.Mutex{}
	}

	receive := func(message protocol.Message) error {
		if *jsonOutput {
			return json.NewEncoder(stdout).Encode(message)
		}
		fmt.Fprintf(stdout, "received %-18s id=%s\n", message.Action, message.ID)
		return nil
	}
	handle := func(ctx context.Context, delivery protocol.DeliveryData) error {
		var target protocol.TargetData
		if decodeErr := json.Unmarshal(delivery.Target, &target); decodeErr != nil {
			return fmt.Errorf("decode delivery target: %w", decodeErr)
		}
		handler, found := handlers[target.SessionID]
		if !found {
			return fmt.Errorf("no local binding for AWP session %q", target.SessionID)
		}
		lock := locks[target.SessionID]
		lock.Lock()
		defer lock.Unlock()
		return handler.adapter.Run(ctx, handler.binding, delivery)
	}

	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	err = client.RunWithReconnect(ctx, client.Options{
		Config: cfg, Token: token, Version: Version, Sessions: registrations,
		Once: *once, Concurrent: true, Receive: receive, Handle: handle,
	}, client.ReconnectPolicy{InitialDelay: *reconnectInitial, MaxDelay: *reconnectMax}, stderr)
	if err != nil {
		code := "daemon_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "timeout"
		}
		return commandError("daemon", code, err, *jsonOutput, stdout, stderr)
	}
	return 0
}

func loadToken(tokenEnvironment, tokenFile string) (string, error) {
	token := strings.TrimSpace(os.Getenv(tokenEnvironment))
	if tokenFile != "" {
		contents, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", err
		}
		token = strings.TrimSpace(string(contents))
	}
	if token == "" {
		if tokenFile != "" {
			return "", fmt.Errorf("%s is empty", tokenFile)
		}
		return "", fmt.Errorf("%s is not set", tokenEnvironment)
	}
	return token, nil
}
