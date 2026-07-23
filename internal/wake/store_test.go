package wake

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordDeliveryDeduplicatesCompletedEvents(t *testing.T) {
	store := NewStore()

	_, duplicate, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("first delivery reported as duplicate")
	}
	if _, err := CompleteEvent(&store, "sinores", "ses_test", "evt_1", EventStatusCompleted); err != nil {
		t.Fatal(err)
	}

	state, duplicate, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_2", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate {
		t.Fatal("resent event_id for an already-completed event was not recognized as a duplicate")
	}
	if len(state.Events) != 1 {
		t.Fatalf("duplicate delivery mutated event history: %#v", state.Events)
	}

	_, duplicate, err = RecordDelivery(&store, "sinores", "ses_test", "evt_2", "del_3", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("distinct event_id reported as duplicate")
	}
}

// A provider that resends an event it never got a "completed" ack for (because
// the daemon crashed mid-run, or the previous attempt genuinely failed) must
// still be able to trigger a retry. Only work AWP already finished should be
// silently dropped.
func TestRecordDeliveryRetriesPendingOrFailedEvents(t *testing.T) {
	store := NewStore()
	if _, duplicate, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil || duplicate {
		t.Fatalf("duplicate=%v err=%v", duplicate, err)
	}

	// Redelivered while still "pending" (e.g. the daemon crashed before it
	// could mark the event complete or failed): must not be a duplicate.
	state, duplicate, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_2", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("event stuck in pending state was treated as an already-completed duplicate")
	}
	if len(Pending(state)) != 1 {
		t.Fatalf("pending = %#v", state.Events)
	}

	if _, err := CompleteEvent(&store, "sinores", "ses_test", "evt_1", EventStatusFailed); err != nil {
		t.Fatal(err)
	}

	// Redelivered after a genuine failure: must retry, not be dropped as a duplicate.
	state, duplicate, err = RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_3", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("event that previously failed was treated as an already-completed duplicate")
	}
	if len(state.Events) != 1 || state.Events[0].Status != EventStatusPending {
		t.Fatalf("retried event = %#v", state.Events)
	}
}

func TestRecordDeliveryIsolatesSessionsAndProviders(t *testing.T) {
	store := NewStore()
	if _, duplicate, err := RecordDelivery(&store, "sinores", "ses_a", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil || duplicate {
		t.Fatalf("duplicate=%v err=%v", duplicate, err)
	}
	if _, duplicate, err := RecordDelivery(&store, "sinores", "ses_b", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil || duplicate {
		t.Fatalf("same event_id under a different session was treated as duplicate: duplicate=%v err=%v", duplicate, err)
	}
	if _, duplicate, err := RecordDelivery(&store, "github", "ses_a", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil || duplicate {
		t.Fatalf("same event_id under a different provider was treated as duplicate: duplicate=%v err=%v", duplicate, err)
	}
}

func TestCompleteEventUpdatesLifecycle(t *testing.T) {
	store := NewStore()
	if _, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	state, err := CompleteEvent(&store, "sinores", "ses_test", "evt_1", EventStatusCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if state.Lifecycle.Status != StatusIdle {
		t.Fatalf("lifecycle after completion = %#v", state.Lifecycle)
	}
	if len(Pending(state)) != 0 {
		t.Fatalf("completed event still reported as pending: %#v", state.Events)
	}

	if _, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_2", "del_2", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	state, err = CompleteEvent(&store, "sinores", "ses_test", "evt_2", EventStatusFailed)
	if err != nil {
		t.Fatal(err)
	}
	if state.Lifecycle.Status != StatusFailed {
		t.Fatalf("lifecycle after failure = %#v", state.Lifecycle)
	}
}

func TestCompleteEventRejectsUnknownEvent(t *testing.T) {
	store := NewStore()
	if _, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteEvent(&store, "sinores", "ses_test", "evt_missing", EventStatusCompleted); err == nil {
		t.Fatal("CompleteEvent() accepted an event_id that was never recorded")
	}
}

func TestPendingReflectsOnlyUnprocessedEvents(t *testing.T) {
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
	state, _ := Get(store, "sinores", "ses_test")
	pending := Pending(state)
	if len(pending) != 1 || pending[0].EventID != "evt_2" {
		t.Fatalf("pending = %#v", pending)
	}
}

func TestEventHistoryIsBounded(t *testing.T) {
	store := NewStore()
	for index := 0; index < maxEventHistory+10; index++ {
		id := fmt.Sprintf("evt_%d", index)
		if _, _, err := RecordDelivery(&store, "sinores", "ses_test", id, "del", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := Get(store, "sinores", "ses_test")
	if len(state.Events) != maxEventHistory {
		t.Fatalf("event history len = %d, want %d", len(state.Events), maxEventHistory)
	}
}

func TestSaveLoadRoundTripIsPrivate(t *testing.T) {
	store := NewStore()
	if _, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nested", "events.json")
	if err := Save(path, store); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !Seen(loaded, "sinores", "ses_test", "evt_1") {
		t.Fatal("loaded store lost the recorded event")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadMissingFileReturnsEmptyStore(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Sessions) != 0 {
		t.Fatalf("store = %#v", store)
	}
}

func TestPauseGatesFutureDeliveries(t *testing.T) {
	store := NewStore()
	state := Pause(&store, "sinores", "ses_test", "human is working on this by hand")
	if state.Lifecycle.Status != StatusPaused {
		t.Fatalf("lifecycle = %#v", state.Lifecycle)
	}
	blocked, reason := Gate(state)
	if !blocked || reason == "" {
		t.Fatalf("blocked=%v reason=%q", blocked, reason)
	}

	resumed, err := Resume(&store, "sinores", "ses_test")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Lifecycle.Status != StatusIdle {
		t.Fatalf("lifecycle after resume = %#v", resumed.Lifecycle)
	}
	if blocked, _ := Gate(resumed); blocked {
		t.Fatal("resumed session is still gated")
	}
}

func TestMarkCrashedGatesFutureDeliveries(t *testing.T) {
	store := NewStore()
	if _, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	state := MarkCrashed(&store, "sinores", "ses_test", "codex binary not found")
	if state.Lifecycle.Status != StatusCrashed {
		t.Fatalf("lifecycle = %#v", state.Lifecycle)
	}
	if blocked, reason := Gate(state); !blocked || reason == "" {
		t.Fatalf("blocked=%v reason=%q", blocked, reason)
	}
}

func TestResumeRequiresExistingSession(t *testing.T) {
	store := NewStore()
	if _, err := Resume(&store, "sinores", "ses_missing"); err == nil {
		t.Fatal("Resume() accepted a session with no recorded state")
	}
}

func TestGateAllowsIdleRunningFailedAndCompleted(t *testing.T) {
	for _, status := range []string{StatusIdle, StatusRunning, StatusFailed, StatusCompleted} {
		if blocked, _ := Gate(SessionState{Lifecycle: Lifecycle{Status: status}}); blocked {
			t.Fatalf("status %q was unexpectedly gated", status)
		}
	}
}

func TestRecordDeliveryPreservesPauseAndStillRecordsTheEvent(t *testing.T) {
	store := NewStore()
	Pause(&store, "sinores", "ses_test", "working on this by hand")

	state, duplicate, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("first delivery to a paused session reported as duplicate")
	}
	if state.Lifecycle.Status != StatusPaused {
		t.Fatalf("RecordDelivery overwrote the paused lifecycle: %#v", state.Lifecycle)
	}
	if blocked, _ := Gate(state); !blocked {
		t.Fatal("paused session stopped being gated after a delivery was recorded")
	}
	pending := Pending(state)
	if len(pending) != 1 || pending[0].EventID != "evt_1" {
		t.Fatalf("event was not preserved as pending while paused: %#v", state.Events)
	}
}

func TestRecordDeliveryPreservesCrashedStatus(t *testing.T) {
	store := NewStore()
	MarkCrashed(&store, "sinores", "ses_test", "codex binary not found")

	state, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if state.Lifecycle.Status != StatusCrashed {
		t.Fatalf("RecordDelivery overwrote the crashed lifecycle: %#v", state.Lifecycle)
	}
	if len(Pending(state)) != 1 {
		t.Fatalf("event was not preserved as pending while crashed: %#v", state.Events)
	}
}

func TestRecordDeliveryStoresAndClearsEventPayload(t *testing.T) {
	store := NewStore()
	payload := json.RawMessage(`{"source":"test","name":"test.event"}`)
	state, _, err := RecordDelivery(&store, "sinores", "ses_test", "evt_1", "del_1", payload)
	if err != nil {
		t.Fatal(err)
	}
	pending := Pending(state)
	if len(pending) != 1 || string(pending[0].Event) != string(payload) {
		t.Fatalf("pending event payload = %#v", pending)
	}
	completed, err := CompleteEvent(&store, "sinores", "ses_test", "evt_1", EventStatusCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Events[0].Event != nil {
		t.Fatalf("completed event retained its payload: %#v", completed.Events[0].Event)
	}
}
