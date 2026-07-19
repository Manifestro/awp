package autostart

import (
	"strings"
	"testing"
)

func TestRenderLaunchdUsesExplicitPathsAndReconnect(t *testing.T) {
	data, err := RenderLaunchd(LaunchdOptions{
		BinaryPath: "/opt/awp & tools/awp",
		ConfigPath: "/tmp/config.json",
		StorePath:  "/tmp/sessions.json",
		TokenFile:  "/tmp/token",
		SessionID:  "session/one",
		LogPath:    "/tmp/awp.log",
		PathEnv:    "/usr/local/bin:/usr/bin:/bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"net.manifestro.awp.session-one", "/opt/awp &amp; tools/awp", "--reconnect", "--token-file"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("manifest does not contain %q:\n%s", expected, text)
		}
	}
}
