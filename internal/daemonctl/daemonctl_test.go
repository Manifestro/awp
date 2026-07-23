package daemonctl

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRunningPIDReportsNotAliveForMissingFile(t *testing.T) {
	pid, alive, err := RunningPID(filepath.Join(t.TempDir(), "missing.pid"))
	if err != nil {
		t.Fatal(err)
	}
	if alive || pid != 0 {
		t.Fatalf("pid=%d alive=%v", pid, alive)
	}
}

func TestRunningPIDDetectsSelfAsAlive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, alive, err := RunningPID(path)
	if err != nil {
		t.Fatal(err)
	}
	if !alive || pid != os.Getpid() {
		t.Fatalf("pid=%d alive=%v", pid, alive)
	}
}

func TestRunningPIDTreatsUnusedPIDAsNotAlive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	// A PID this test does not own should behave like "not alive" only if it
	// is actually gone. Use a PID far outside any plausible live range.
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, alive, err := RunningPID(path)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("PID 999999 reported as alive")
	}
}

func TestStopWithoutRunningDaemonIsNotAnError(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	wasRunning, pid, err := Stop(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	if wasRunning || pid != 0 {
		t.Fatalf("wasRunning=%v pid=%d", wasRunning, pid)
	}
}

func TestPIDPathDefaultsNextToConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	path, err := PIDPath(configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(configPath), "daemon.pid")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestPIDPathExplicitOverridesDefault(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "custom.pid")
	path, err := PIDPath("", explicit)
	if err != nil {
		t.Fatal(err)
	}
	if path != explicit {
		t.Fatalf("path = %q, want %q", path, explicit)
	}
}
