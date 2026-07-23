package wake

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteProjectionOnlyIncludesPendingEvents(t *testing.T) {
	store := NewStore()
	if _, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_2", "del_2", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteEvent(&store, "sinores", "ses_test", "evt_1", EventStatusCompleted); err != nil {
		t.Fatal(err)
	}
	SetPermissions(&store, "sinores", "ses_test", []string{"messages.read_new"})
	state, _ := Get(store, "sinores", "ses_test")

	workspace := t.TempDir()
	if err := WriteProjection(workspace, state); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(workspace, ".awp", "data.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var projection Projection
	if err := json.Unmarshal(raw, &projection); err != nil {
		t.Fatal(err)
	}
	if len(projection.Pending) != 1 || projection.Pending[0].EventID != "evt_2" {
		t.Fatalf("projection.Pending = %#v", projection.Pending)
	}
	if len(projection.Permissions) != 1 || projection.Permissions[0] != "messages.read_new" {
		t.Fatalf("projection.Permissions = %#v", projection.Permissions)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteProjectionSkipsEmptyWorkspace(t *testing.T) {
	if err := WriteProjection("", SessionState{}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteProjectionOverwritesPreviousSnapshot(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore()
	if _, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	state, _ := Get(store, "sinores", "ses_test")
	if err := WriteProjection(workspace, state); err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteEvent(&store, "sinores", "ses_test", "evt_1", EventStatusCompleted); err != nil {
		t.Fatal(err)
	}
	state, _ = Get(store, "sinores", "ses_test")
	if err := WriteProjection(workspace, state); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(workspace, ".awp", "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var projection Projection
	if err := json.Unmarshal(raw, &projection); err != nil {
		t.Fatal(err)
	}
	if len(projection.Pending) != 0 {
		t.Fatalf("stale pending event survived overwrite: %#v", projection.Pending)
	}
}
