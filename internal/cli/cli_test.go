package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Manifestro/awp/internal/autostart"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/protocol"
	"github.com/Manifestro/awp/internal/sessions"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestConfigSetJSONErrorReturnsFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "config.json")

	exitCode := Run([]string{
		"config", "set",
		"--provider", "example",
		"--service-url", "ws://example.com/awp",
		"--device-id", "dev_test",
		"--token-env", "EXAMPLE_TOKEN",
		"--config", path,
		"--json",
	}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("Run() exit code = %d, want 1", exitCode)
	}
	var response struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if response.OK {
		t.Fatal("JSON output reported success for invalid configuration")
	}
}

func TestDaemonConnectsEveryProviderIndependently(t *testing.T) {
	newProvider := func() (*httptest.Server, <-chan string) {
		bound := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			connection, err := websocket.Accept(writer, request, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer connection.CloseNow()
			var hello protocol.Message
			if err := wsjson.Read(request.Context(), connection, &hello); err != nil {
				return
			}
			welcome, err := protocol.New(protocol.ActionServerWelcome, map[string]any{"device_id": "dev_multi_provider"})
			if err != nil {
				t.Error(err)
				return
			}
			if err := wsjson.Write(request.Context(), connection, welcome); err != nil {
				return
			}
			var binding protocol.Message
			if err := wsjson.Read(request.Context(), connection, &binding); err != nil {
				return
			}
			data, err := protocol.DecodeData[protocol.SessionBindData](binding)
			if err != nil {
				t.Error(err)
				return
			}
			bound <- data.SessionID
			<-request.Context().Done()
		}))
		return server, bound
	}
	firstServer, firstBound := newProvider()
	defer firstServer.Close()
	secondServer, secondBound := newProvider()
	defer secondServer.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	cfg := config.Config{Version: config.Version, DeviceID: "dev_multi_provider", Providers: map[string]config.Provider{
		"first":  {ServiceURL: "ws" + strings.TrimPrefix(firstServer.URL, "http"), TokenEnv: "FIRST_TOKEN"},
		"second": {ServiceURL: "ws" + strings.TrimPrefix(secondServer.URL, "http"), TokenEnv: "SECOND_TOKEN"},
	}}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	for _, binding := range []sessions.Binding{
		{Provider: "first", SessionID: "ses_first", Adapter: "codex", RuntimeSessionID: "runtime_first"},
		{Provider: "second", SessionID: "ses_second", Adapter: "codex", RuntimeSessionID: "runtime_second"},
	} {
		if _, err := sessions.Bind(&registry, binding); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.Save(storePath, registry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIRST_TOKEN", "first-secret")
	t.Setenv("SECOND_TOKEN", "second-secret")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"daemon", "--config", configPath, "--store", storePath, "--timeout", "250ms", "--json"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), `"code":"timeout"`) {
		t.Fatalf("daemon code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for name, channel := range map[string]<-chan string{"first": firstBound, "second": secondBound} {
		select {
		case sessionID := <-channel:
			if sessionID != "ses_"+name {
				t.Fatalf("provider %s bound session %s", name, sessionID)
			}
		case <-time.After(time.Second):
			t.Fatalf("provider %s did not receive its independent binding", name)
		}
	}
}

func TestSessionsBindListAndRemove(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	store := filepath.Join(t.TempDir(), "sessions.json")
	workspace := t.TempDir()
	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_test", Providers: map[string]config.Provider{"example": {ServiceURL: "wss://example.com/awp", TokenEnv: "EXAMPLE_TOKEN"}}}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "bind",
			args: []string{"sessions", "bind", "--provider", "example", "--session-id", "ses_test", "--adapter", "codex", "--runtime-session-id", "runtime_test", "--workspace", workspace, "--config", configPath, "--store", store, "--json"},
		},
		{
			name: "list",
			args: []string{"sessions", "list", "--provider", "example", "--store", store, "--json"},
		},
		{
			name: "remove",
			args: []string{"sessions", "remove", "--provider", "example", "--session-id", "ses_test", "--store", store, "--json"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := Run(test.args, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("Run() exit code = %d, stderr = %s", exitCode, stderr.String())
			}
			var response struct {
				OK bool `json:"ok"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("invalid JSON output: %v", err)
			}
			if !response.OK {
				t.Fatalf("command reported failure: %s", stdout.String())
			}
		})
	}
}

func TestAutostartEnableIsOptInAndEditable(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	if err := config.Save(configPath, config.Config{Version: config.Version, DeviceID: "dev_test", Providers: map[string]config.Provider{"example": {ServiceURL: "wss://example.com/awp", TokenEnv: "AWP_TEST_TOKEN"}}}); err != nil {
		t.Fatal(err)
	}
	registry := sessions.NewRegistry()
	if _, err := sessions.Bind(&registry, sessions.Binding{Provider: "example", SessionID: "ses_test", Adapter: "codex", RuntimeSessionID: "runtime_test"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(storePath, registry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWP_TEST_TOKEN", "secret-test-token")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"autostart", "enable", "--config", configPath, "--store", storePath, "--directory", directory, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("enable code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	manifest := autostart.Filename(directory)
	contents, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "<string>daemon</string>") || strings.Contains(string(contents), "--session-id") {
		t.Fatalf("manifest does not run one multi-session daemon: %s", contents)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"autostart", "disable", "--config", configPath, "--store", storePath, "--directory", directory, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("disable code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(manifest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest still exists: %v", err)
	}
}
