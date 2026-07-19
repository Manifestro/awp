package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Manifestro/awp/internal/adapters"
	"github.com/Manifestro/awp/internal/client"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/permissions"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
)

func runDaemon(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("daemon", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	storePath := flags.String("store", "", "session registry file path")
	tokenDirectory := flags.String("token-dir", "", "read provider tokens from protected <provider>.token files")
	permissionPathFlag := flags.String("permissions-store", "", "permission state file path")
	updatePolicyPath := flags.String("update-policy", "", "automatic update policy file path")
	jsonOutput := flags.Bool("json", false, "print received messages as JSON Lines")
	once := flags.Bool("once", false, "exit each provider connection after one delivered event")
	timeout := flags.Duration("timeout", 0, "optional daemon timeout")
	reconnectInitial := flags.Duration("reconnect-initial", time.Second, "initial reconnect delay")
	reconnectMax := flags.Duration("reconnect-max", 30*time.Second, "maximum reconnect delay")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	runAutomaticUpdate(Version, *configPath, *updatePolicyPath, stderr)

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
	registryPath, err := sessions.Path(resolvedConfig, *storePath)
	if err != nil {
		return commandError("daemon", "registry_path", err, *jsonOutput, stdout, stderr)
	}
	registry, err := sessions.Load(registryPath)
	if err != nil {
		return commandError("daemon", "registry_read", err, *jsonOutput, stdout, stderr)
	}
	bindings := sessions.List(registry, "")
	if len(bindings) == 0 {
		return commandError("daemon", "sessions_empty", errors.New("no local AWP sessions are bound"), *jsonOutput, stdout, stderr)
	}
	permissionPath, err := permissions.Path(resolvedConfig, *permissionPathFlag)
	if err != nil {
		return commandError("daemon", "permissions_path", err, *jsonOutput, stdout, stderr)
	}
	permissionState, err := permissions.Load(permissionPath)
	if err != nil {
		return commandError("daemon", "permissions_read", err, *jsonOutput, stdout, stderr)
	}
	var permissionLock sync.Mutex
	for _, binding := range bindings {
		if _, found := cfg.Providers[binding.Provider]; !found {
			return commandError("daemon", "provider_not_found", fmt.Errorf("session %s references unconfigured provider %q", binding.SessionID, binding.Provider), *jsonOutput, stdout, stderr)
		}
	}

	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	var outputLock sync.Mutex
	providerOptions := make(map[string]client.Options)
	for providerName, provider := range cfg.Providers {
		providerBindings := sessions.List(registry, providerName)
		if len(providerBindings) == 0 {
			continue
		}
		tokenFile := ""
		if *tokenDirectory != "" {
			tokenFile = filepath.Join(*tokenDirectory, providerName+".token")
		}
		token, tokenErr := loadToken(provider.TokenEnv, tokenFile)
		if tokenErr != nil {
			return commandError("daemon", "token_missing", fmt.Errorf("provider %s: %w", providerName, tokenErr), *jsonOutput, stdout, stderr)
		}
		registrations := make([]client.SessionRegistration, 0, len(providerBindings))
		handlers := make(map[string]struct {
			binding sessions.Binding
			adapter adapters.Adapter
		}, len(providerBindings))
		locks := make(map[string]*sync.Mutex, len(providerBindings))
		for _, binding := range providerBindings {
			resolved, resolveErr := adapters.Resolve(binding, stderr)
			if resolveErr != nil {
				return commandError("daemon", "adapter_unavailable", fmt.Errorf("provider %s: %w", providerName, resolveErr), *jsonOutput, stdout, stderr)
			}
			registrations = append(registrations, client.SessionRegistration{SessionID: binding.SessionID, Adapter: binding.Adapter, Metadata: binding.Metadata})
			handlers[binding.SessionID] = struct {
				binding sessions.Binding
				adapter adapters.Adapter
			}{binding: binding, adapter: resolved}
			locks[binding.SessionID] = &sync.Mutex{}
		}
		name := providerName
		receive := func(message protocol.Message) error {
			if message.Action == protocol.ActionPermissionRequest {
				data, decodeErr := protocol.DecodeData[protocol.PermissionRequestData](message)
				if decodeErr != nil {
					return decodeErr
				}
				if _, found := handlers[data.SessionID]; !found {
					return fmt.Errorf("provider %s requested permissions for unbound session %q", name, data.SessionID)
				}
				request := permissionRequestFromProtocol(name, data)
				permissionLock.Lock()
				_, recordErr := permissions.RecordRequest(&permissionState, request)
				if recordErr == nil {
					recordErr = permissions.Save(permissionPath, permissionState)
				}
				permissionLock.Unlock()
				if recordErr != nil {
					return fmt.Errorf("store provider permission request: %w", recordErr)
				}
			}
			outputLock.Lock()
			defer outputLock.Unlock()
			if *jsonOutput {
				return json.NewEncoder(stdout).Encode(map[string]any{"provider": name, "message": message})
			}
			fmt.Fprintf(stdout, "provider=%s received %-18s id=%s\n", name, message.Action, message.ID)
			return nil
		}
		handle := func(ctx context.Context, delivery protocol.DeliveryData) error {
			var target protocol.TargetData
			if decodeErr := json.Unmarshal(delivery.Target, &target); decodeErr != nil {
				return fmt.Errorf("decode delivery target: %w", decodeErr)
			}
			handler, found := handlers[target.SessionID]
			if !found {
				return fmt.Errorf("no local %s binding for AWP session %q", name, target.SessionID)
			}
			lock := locks[target.SessionID]
			lock.Lock()
			defer lock.Unlock()
			permissionLock.Lock()
			authorization, authorizationErr := permissions.Authorize(&permissionState, name, target.SessionID, true)
			if authorizationErr == nil {
				authorizationErr = permissions.Save(permissionPath, permissionState)
			}
			permissionLock.Unlock()
			if authorizationErr != nil {
				return authorizationErr
			}
			mcpServer := provider.MCPServer
			if mcpServer == "" {
				mcpServer = name
			}
			return handler.adapter.Run(ctx, handler.binding, delivery, authorization, mcpServer)
		}
		providerOptions[providerName] = client.Options{
			ServiceURL: provider.ServiceURL, DeviceID: cfg.DeviceID, Token: token, Version: Version,
			Sessions: registrations, Once: *once, Concurrent: true, Receive: receive, Handle: handle,
		}
	}
	if len(providerOptions) == 0 {
		return commandError("daemon", "providers_empty", errors.New("no configured provider has a local session binding"), *jsonOutput, stdout, stderr)
	}

	errorsChannel := make(chan error, len(providerOptions))
	for providerName, options := range providerOptions {
		name, providerClient := providerName, options
		go func() {
			runErr := client.RunWithReconnect(ctx, providerClient, client.ReconnectPolicy{InitialDelay: *reconnectInitial, MaxDelay: *reconnectMax}, stderr)
			if runErr != nil {
				errorsChannel <- fmt.Errorf("provider %s: %w", name, runErr)
				return
			}
			errorsChannel <- nil
		}()
	}
	for range providerOptions {
		runErr := <-errorsChannel
		if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
			return commandError("daemon", "daemon_failed", runErr, *jsonOutput, stdout, stderr)
		}
	}
	if ctx.Err() != nil {
		code := "daemon_failed"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
		return commandError("daemon", code, ctx.Err(), *jsonOutput, stdout, stderr)
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
