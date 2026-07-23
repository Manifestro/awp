// Package daemonctl manages the `awp daemon` process's lifecycle: starting
// it detached in the background, stopping it, and checking whether it is
// currently running. It is shared by the CLI (`awp daemon start/stop/status`)
// and the local MCP server (`start_daemon`/`stop_daemon`/`daemon_status`), so
// both surfaces manage the exact same process the exact same way.
package daemonctl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Manifestro/awp/internal/config"
)

// ErrAlreadyRunning marks Start's refusal to launch a second daemon while
// one is already alive. Callers check errors.Is against this to report a
// distinct, expected condition instead of a generic spawn failure.
var ErrAlreadyRunning = errors.New("daemon already running")

// ExecutablePath returns the binary daemon control re-execs as `awp daemon
// <args>`. It is a var, not a direct os.Executable() call, so tests can
// point it at a real built binary instead of the go test binary.
var ExecutablePath = os.Executable

// PIDPath resolves the PID file used by daemon start/stop/status, mirroring
// how other AWP state paths are resolved relative to config.json.
func PIDPath(configPath, explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if fromEnv := os.Getenv("AWP_DAEMON_PID"); fromEnv != "" {
		return filepath.Abs(fromEnv)
	}
	resolvedConfig, err := config.Path(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(resolvedConfig), "daemon.pid"), nil
}

// RunningPID reads a PID file and reports the PID only if that process is
// still alive, treating a stale file left behind by a crash as "not running"
// without erroring.
func RunningPID(pidPath string) (int, bool, error) {
	contents, err := os.ReadFile(pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil {
		return 0, false, fmt.Errorf("invalid PID file %s: %w", pidPath, err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return 0, false, nil
	}
	// Signal 0 checks liveness without actually signaling the process.
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return 0, false, nil
	}
	return pid, true, nil
}

// StartOptions describes the background daemon process to launch.
type StartOptions struct {
	Executable string
	Args       []string
	PIDPath    string
	LogPath    string
}

// Start launches `Executable Args...` as a detached background process (its
// own session, so it survives the parent shell/MCP call exiting) and records
// its PID. It refuses to start a second instance while one is already alive,
// since two daemons sharing the same local state files could race on the
// same delivery.
func Start(options StartOptions) (int, error) {
	if pid, alive, err := RunningPID(options.PIDPath); err != nil {
		return 0, err
	} else if alive {
		return 0, fmt.Errorf("%w: pid %d (see %s)", ErrAlreadyRunning, pid, options.PIDPath)
	}
	logFile, err := os.OpenFile(options.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	command := exec.Command(options.Executable, options.Args...)
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return 0, fmt.Errorf("spawn daemon: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(options.PIDPath), 0o700); err != nil {
		return 0, fmt.Errorf("create pid directory: %w", err)
	}
	if err := os.WriteFile(options.PIDPath, fmt.Appendf(nil, "%d\n", command.Process.Pid), 0o600); err != nil {
		return 0, fmt.Errorf("write pid file: %w", err)
	}
	return command.Process.Pid, nil
}

// Stop sends SIGTERM to the running daemon and removes its PID file. It is
// not an error to stop a daemon that was not running; wasRunning reports
// which case happened.
func Stop(pidPath string) (wasRunning bool, pid int, err error) {
	pid, alive, err := RunningPID(pidPath)
	if err != nil {
		return false, 0, err
	}
	if !alive {
		_ = os.Remove(pidPath)
		return false, 0, nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, 0, err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return false, 0, fmt.Errorf("signal daemon: %w", err)
	}
	_ = os.Remove(pidPath)
	return true, pid, nil
}

// Status reports whether the daemon is currently running.
func Status(pidPath string) (pid int, running bool, err error) {
	return RunningPID(pidPath)
}
