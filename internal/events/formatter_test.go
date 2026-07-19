package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Manifestro/awp/internal/protocol"
)

func TestFormatPromptIsSourceAgnostic(t *testing.T) {
	delivery := protocol.DeliveryData{
		DeliveryID: "dlv_test",
		EventID:    "evt_test",
		Event:      json.RawMessage(`{"source":"github","name":"issue.created","data":{"issue":42}}`),
	}
	prompt, err := FormatPrompt(delivery)
	if err != nil {
		t.Fatal(err)
	}
	text := string(prompt)
	for _, expected := range []string{"evt_test", "dlv_test", `"source": "github"`, "untrusted external data"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("prompt does not contain %q:\n%s", expected, text)
		}
	}
}
