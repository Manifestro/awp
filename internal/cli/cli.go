package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Manifestro/awp/internal/client"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/protocol"
)

const Version = "0.1.0-dev"

type result struct {
	OK      bool        `json:"ok"`
	Command string      `json:"command"`
	Data    any         `json:"data,omitempty"`
	Error   *errorValue `json:"error,omitempty"`
}

type errorValue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "connect":
		return runConnect(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runConnect(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	jsonOutput := flags.Bool("json", false, "print received messages as JSON Lines")
	once := flags.Bool("once", false, "exit after acknowledging one event.deliver message")
	timeout := flags.Duration("timeout", 0, "optional connection timeout, for example 30s")
	sessionID := flags.String("session-id", "", "AWP session binding to register after connecting")
	adapter := flags.String("adapter", "codex", "runtime adapter for the session binding")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, err := config.Path(*configPath)
	if err != nil {
		return commandError("connect", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return commandError("connect", "config_read", err, *jsonOutput, stdout, stderr)
	}
	if err := config.Validate(cfg); err != nil {
		return commandError("connect", "invalid_config", err, *jsonOutput, stdout, stderr)
	}
	if *sessionID != "" && *adapter == "" {
		return commandError("connect", "adapter_required", errors.New("--adapter is required with --session-id"), *jsonOutput, stdout, stderr)
	}
	token := os.Getenv(cfg.TokenEnv)
	if token == "" {
		return commandError("connect", "token_missing", fmt.Errorf("%s is not set", cfg.TokenEnv), *jsonOutput, stdout, stderr)
	}

	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	receive := func(message protocol.Message) error {
		if *jsonOutput {
			return json.NewEncoder(stdout).Encode(message)
		}
		fmt.Fprintf(stdout, "received %-18s id=%s\n", message.Action, message.ID)
		return nil
	}
	err = client.Run(ctx, client.Options{
		Config:    cfg,
		Token:     token,
		Version:   Version,
		SessionID: *sessionID,
		Adapter:   *adapter,
		Once:      *once,
		Receive:   receive,
	})
	if err != nil {
		code := "connection_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "timeout"
		}
		return commandError("connect", code, err, *jsonOutput, stdout, stderr)
	}
	return 0
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "version", Data: map[string]string{"version": Version}})
	}
	fmt.Fprintln(stdout, Version)
	return 0
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "config requires a subcommand: set or show")
		return 2
	}
	switch args[0] {
	case "set":
		return runConfigSet(args[1:], stdout, stderr)
	case "show":
		return runConfigShow(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown config subcommand %q\n", args[0])
		return 2
	}
}

func runConfigSet(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("config set", flag.ContinueOnError)
	flags.SetOutput(stderr)
	serviceURL := flags.String("service-url", "", "AWP Service WebSocket URL")
	deviceID := flags.String("device-id", "", "opaque AWP device identifier")
	tokenEnv := flags.String("token-env", "AWP_TOKEN", "environment variable containing the bearer token")
	configPath := flags.String("config", "", "config file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, err := config.Path(*configPath)
	if err != nil {
		return commandError("config.set", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg := config.Config{Version: "0.1", ServiceURL: *serviceURL, DeviceID: *deviceID, TokenEnv: *tokenEnv}
	if err := config.Save(path, cfg); err != nil {
		return commandError("config.set", "invalid_config", err, *jsonOutput, stdout, stderr)
	}

	data := map[string]any{"path": path, "config": cfg}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "config.set", Data: data})
	}
	fmt.Fprintf(stdout, "Configuration saved to %s\n", path)
	return 0
}

func runConfigShow(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("config show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, err := config.Path(*configPath)
	if err != nil {
		return commandError("config.show", "config_path", err, *jsonOutput, stdout, stderr)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return commandError("config.show", "config_read", err, *jsonOutput, stdout, stderr)
	}

	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "config.show", Data: map[string]any{"path": path, "config": cfg}})
	}
	fmt.Fprintf(stdout, "Config: %s\nService: %s\nDevice: %s\nToken environment: %s\n", path, cfg.ServiceURL, cfg.DeviceID, cfg.TokenEnv)
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	path, pathErr := config.Path(*configPath)
	checks := make([]check, 0, 4)
	var cfg config.Config
	if pathErr != nil {
		checks = append(checks, check{Name: "config", OK: false, Message: pathErr.Error()})
	} else {
		loaded, err := config.Load(path)
		if err != nil {
			checks = append(checks, check{Name: "config", OK: false, Message: err.Error()})
		} else if err := config.Validate(loaded); err != nil {
			checks = append(checks, check{Name: "config", OK: false, Message: err.Error()})
		} else {
			cfg = loaded
			checks = append(checks, check{Name: "config", OK: true, Message: path})
		}
	}

	if cfg.TokenEnv == "" {
		checks = append(checks, check{Name: "token", OK: false, Message: "token environment is not configured"})
	} else if os.Getenv(cfg.TokenEnv) == "" {
		checks = append(checks, check{Name: "token", OK: false, Message: cfg.TokenEnv + " is not set"})
	} else {
		checks = append(checks, check{Name: "token", OK: true, Message: cfg.TokenEnv + " is set"})
	}

	codexCheck := executableCheck("codex")
	claudeCheck := executableCheck("claude")
	checks = append(checks, codexCheck, claudeCheck)
	ok := true
	for _, item := range checks[:len(checks)-2] {
		if !item.OK {
			ok = false
		}
	}
	if !codexCheck.OK && !claudeCheck.OK {
		ok = false
	}

	if *jsonOutput {
		code := writeJSON(stdout, result{OK: ok, Command: "doctor", Data: map[string]any{"checks": checks}})
		if code != 0 || !ok {
			return 1
		}
		return 0
	}
	for _, item := range checks {
		state := "ok"
		if !item.OK {
			state = "fail"
		}
		fmt.Fprintf(stdout, "%-4s %-8s %s\n", state, item.Name, item.Message)
	}
	if !ok {
		return 1
	}
	return 0
}

func executableCheck(name string) check {
	path, err := exec.LookPath(name)
	if err != nil {
		return check{Name: name, OK: false, Message: "not found in PATH"}
	}
	return check{Name: name, OK: true, Message: path}
}

func commandError(command, code string, err error, jsonOutput bool, stdout, stderr io.Writer) int {
	if jsonOutput {
		if writeJSON(stdout, result{OK: false, Command: command, Error: &errorValue{Code: code, Message: err.Error()}}) != 0 {
			return 1
		}
		return 1
	}
	fmt.Fprintln(stderr, err)
	return 1
}

func writeJSON(writer io.Writer, value any) int {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 1
	}
	return 0
}

func printUsage(writer io.Writer) {
	fmt.Fprint(writer, strings.TrimSpace(`Agent Wake Protocol client

Usage:
  awp version [--json]
  awp config set --service-url <wss://...> --device-id <id> [--token-env AWP_TOKEN] [--config <path>] [--json]
  awp config show [--config <path>] [--json]
  awp doctor [--config <path>] [--json]
  awp connect [--config <path>] [--session-id <id>] [--adapter codex] [--once] [--timeout 30s] [--json]

Environment:
  AWP_CONFIG  Override the default configuration path.
  AWP_TOKEN   Default bearer token environment variable.
`)+"\n")
}
