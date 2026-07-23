package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/Manifestro/awp/internal/daemonctl"
)

// runDaemonStart runs `awp daemon` with the given flags as a detached
// background process and records its PID, so a temporary AWP session (start
// when you need a provider, stop when you are done) does not require
// managing a shell job or a launchd service. While stopped, AWP is not
// connected at all: whatever a provider sends is neither received nor
// queued locally, exactly like disconnecting a client from any other
// at-least-once stream.
func runDaemonStart(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("daemon start", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	storePath := flags.String("store", "", "session registry file path")
	tokenDirectory := flags.String("token-dir", "", "read provider tokens from protected <provider>.token files")
	permissionPathFlag := flags.String("permissions-store", "", "permission state file path")
	eventPathFlag := flags.String("events-store", "", "event dedup/state store file path")
	providerFilter := flags.String("provider", "", "connect only this configured provider instead of every provider with a local session binding")
	reconnectInitial := flags.Duration("reconnect-initial", time.Second, "initial reconnect delay")
	reconnectMax := flags.Duration("reconnect-max", 30*time.Second, "maximum reconnect delay")
	pidPathFlag := flags.String("pid-file", "", "daemon PID file path")
	logPathFlag := flags.String("log-file", "", "file the background daemon's output is redirected to; defaults next to the PID file")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	pidPath, err := daemonctl.PIDPath(*configPath, *pidPathFlag)
	if err != nil {
		return commandError("daemon.start", "pid_path", err, *jsonOutput, stdout, stderr)
	}
	executable, err := daemonctl.ExecutablePath()
	if err != nil {
		return commandError("daemon.start", "executable", err, *jsonOutput, stdout, stderr)
	}
	daemonArgs := []string{"daemon"}
	for _, pair := range [][2]string{
		{"--config", *configPath}, {"--store", *storePath}, {"--token-dir", *tokenDirectory},
		{"--permissions-store", *permissionPathFlag}, {"--events-store", *eventPathFlag}, {"--provider", *providerFilter},
	} {
		if pair[1] != "" {
			daemonArgs = append(daemonArgs, pair[0], pair[1])
		}
	}
	daemonArgs = append(daemonArgs, "--reconnect-initial", reconnectInitial.String(), "--reconnect-max", reconnectMax.String())

	logPath := *logPathFlag
	if logPath == "" {
		logPath = filepath.Join(filepath.Dir(pidPath), "daemon.log")
	}

	pid, err := daemonctl.Start(daemonctl.StartOptions{Executable: executable, Args: daemonArgs, PIDPath: pidPath, LogPath: logPath})
	if errors.Is(err, daemonctl.ErrAlreadyRunning) {
		return commandError("daemon.start", "already_running", err, *jsonOutput, stdout, stderr)
	}
	if err != nil {
		return commandError("daemon.start", "spawn_failed", err, *jsonOutput, stdout, stderr)
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "daemon.start", Data: map[string]any{"pid": pid, "pid_file": pidPath, "log_file": logPath}})
	}
	fmt.Fprintf(stdout, "Daemon started in background: pid=%d log=%s\n", pid, logPath)
	return 0
}

func runDaemonStop(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("daemon stop", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	pidPathFlag := flags.String("pid-file", "", "daemon PID file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	pidPath, err := daemonctl.PIDPath(*configPath, *pidPathFlag)
	if err != nil {
		return commandError("daemon.stop", "pid_path", err, *jsonOutput, stdout, stderr)
	}
	wasRunning, pid, err := daemonctl.Stop(pidPath)
	if err != nil {
		return commandError("daemon.stop", "stop_failed", err, *jsonOutput, stdout, stderr)
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "daemon.stop", Data: map[string]any{"was_running": wasRunning, "pid": pid}})
	}
	if wasRunning {
		fmt.Fprintf(stdout, "Stopped daemon (pid=%d)\n", pid)
	} else {
		fmt.Fprintln(stdout, "Daemon was not running")
	}
	return 0
}

func runDaemonStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file path")
	pidPathFlag := flags.String("pid-file", "", "daemon PID file path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	pidPath, err := daemonctl.PIDPath(*configPath, *pidPathFlag)
	if err != nil {
		return commandError("daemon.status", "pid_path", err, *jsonOutput, stdout, stderr)
	}
	pid, alive, err := daemonctl.Status(pidPath)
	if err != nil {
		return commandError("daemon.status", "pid_read", err, *jsonOutput, stdout, stderr)
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "daemon.status", Data: map[string]any{"running": alive, "pid": pid, "pid_file": pidPath}})
	}
	if alive {
		fmt.Fprintf(stdout, "running (pid=%d)\n", pid)
	} else {
		fmt.Fprintln(stdout, "not running")
	}
	return 0
}
