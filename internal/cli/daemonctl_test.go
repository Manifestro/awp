package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/daemonctl"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

var (
	builtAWPBinaryOnce sync.Once
	builtAWPBinaryPath string
	builtAWPBinaryErr  error
)

// buildAWPBinary compiles the real cmd/awp binary once per test run, so
// `daemon start` tests can re-exec it exactly the way a real installation
// would, instead of re-execing the go test binary (which is not the awp CLI
// at all).
func buildAWPBinary(t *testing.T) string {
	t.Helper()
	builtAWPBinaryOnce.Do(func() {
		directory := t.TempDir()
		// t.TempDir() from the first caller only; safe because builds don't
		// need per-test cleanup and the whole test binary process exits together.
		path := filepath.Join(directory, "awp-test-binary")
		command := exec.Command("go", "build", "-o", path, "github.com/Manifestro/awp/cmd/awp")
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			builtAWPBinaryErr = err
			return
		}
		builtAWPBinaryPath = path
	})
	if builtAWPBinaryErr != nil {
		t.Fatalf("build awp binary: %v", builtAWPBinaryErr)
	}
	return builtAWPBinaryPath
}

func TestDaemonStartStatusStopLifecycle(t *testing.T) {
	binary := buildAWPBinary(t)
	original := daemonctl.ExecutablePath
	daemonctl.ExecutablePath = func() (string, error) { return binary, nil }
	defer func() { daemonctl.ExecutablePath = original }()

	connected := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		var hello protocol.Message
		if err := wsjson.Read(request.Context(), connection, &hello); err != nil {
			return
		}
		welcome, err := protocol.New(protocol.ActionServerWelcome, map[string]any{"device_id": "dev_lifecycle"})
		if err != nil {
			return
		}
		if err := wsjson.Write(request.Context(), connection, welcome); err != nil {
			return
		}
		select {
		case connected <- struct{}{}:
		default:
		}
		<-request.Context().Done()
	}))
	defer server.Close()

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	storePath := filepath.Join(directory, "sessions.json")
	pidPath := filepath.Join(directory, "daemon.pid")

	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_lifecycle", Providers: map[string]config.Provider{
		"lifecycle": {ServiceURL: "ws" + strings.TrimPrefix(server.URL, "http"), TokenEnv: "LIFECYCLE_TOKEN"},
	}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "lifecycle", SessionID: "ses_lifecycle", Adapter: "codex", RuntimeSessionID: "runtime_lifecycle"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(storePath, registry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIFECYCLE_TOKEN", "secret")

	var startOut, startErr bytes.Buffer
	code := Run([]string{
		"daemon", "start",
		"--config", configPath, "--store", storePath, "--pid-file", pidPath,
		"--json",
	}, &startOut, &startErr)
	if code != 0 {
		t.Fatalf("daemon start code=%d stdout=%s stderr=%s", code, startOut.String(), startErr.String())
	}

	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("background daemon never connected to the fake provider")
	}

	var statusOut, statusErr bytes.Buffer
	if code := Run([]string{"daemon", "status", "--pid-file", pidPath, "--json"}, &statusOut, &statusErr); code != 0 {
		t.Fatalf("daemon status code=%d stdout=%s stderr=%s", code, statusOut.String(), statusErr.String())
	}
	if !strings.Contains(statusOut.String(), `"running":true`) {
		t.Fatalf("status = %s, want running", statusOut.String())
	}

	var stopOut, stopErr bytes.Buffer
	if code := Run([]string{"daemon", "stop", "--pid-file", pidPath, "--json"}, &stopOut, &stopErr); code != 0 {
		t.Fatalf("daemon stop code=%d stdout=%s stderr=%s", code, stopOut.String(), stopErr.String())
	}
	if !strings.Contains(stopOut.String(), `"was_running":true`) {
		t.Fatalf("stop = %s, want was_running true", stopOut.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, alive, err := daemonctl.Status(pidPath); err == nil && !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background daemon did not stop after SIGTERM")
		}
		time.Sleep(50 * time.Millisecond)
	}

	var finalStatusOut bytes.Buffer
	if code := Run([]string{"daemon", "status", "--pid-file", pidPath, "--json"}, &finalStatusOut, &bytes.Buffer{}); code != 0 {
		t.Fatalf("final daemon status code=%d", code)
	}
	if !strings.Contains(finalStatusOut.String(), `"running":false`) {
		t.Fatalf("final status = %s, want not running", finalStatusOut.String())
	}
}

func TestDaemonStartRefusesWhenAlreadyRunning(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"daemon", "start", "--pid-file", pidPath, "--json"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("daemon start succeeded while a daemon was already running")
	}
	if !strings.Contains(stdout.String(), "already_running") {
		t.Fatalf("stdout = %s, want already_running error", stdout.String())
	}
}

func TestDaemonStopWithoutRunningDaemonIsNotAnError(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"daemon", "stop", "--pid-file", pidPath, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"was_running":false`) {
		t.Fatalf("stdout = %s, want was_running false", stdout.String())
	}
}
