package protocol

import (
	"encoding/json"
	"testing"
)

func TestNewCreatesValidMessage(t *testing.T) {
	message, err := New(ActionHeartbeatPing, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsNonObjectData(t *testing.T) {
	message, err := New(ActionHeartbeatPing, []string{})
	if err != nil {
		t.Fatal(err)
	}
	message.Data = json.RawMessage(`[]`)
	if err := message.Validate(); err == nil {
		t.Fatal("Validate() accepted array data")
	}
}
