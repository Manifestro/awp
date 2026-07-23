// Package wake tracks, per (provider, session_id), which delivered events have
// already been processed and what the session is currently doing. The daemon
// consults this store before invoking a runtime adapter so a provider that
// resends an event it already acknowledged does not wake the agent again.
package wake

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Manifestro/awp/internal/config"
)

const Version = "0.1"

// maxEventHistory bounds how many event records are kept per session so a
// long-lived binding does not grow the store file without limit.
const maxEventHistory = 500

const (
	StatusIdle            = "idle"
	StatusRunning         = "running"
	StatusCompleted       = "completed"
	StatusFailed          = "failed"
	StatusPaused          = "paused"
	StatusWaitingApproval = "waiting_approval"
	StatusCrashed         = "crashed"
)

const (
	EventStatusPending   = "pending"
	EventStatusCompleted = "completed"
	EventStatusFailed    = "failed"
)

type ApprovalDetails struct {
	What      string `json:"what"`
	Target    string `json:"target,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type Lifecycle struct {
	Status          string           `json:"status"`
	UpdatedAt       string           `json:"updated_at"`
	Reason          string           `json:"reason,omitempty"`
	ContextHint     string           `json:"context_hint,omitempty"`
	ApprovalDetails *ApprovalDetails `json:"approval_details,omitempty"`
}

type EventRecord struct {
	EventID    string          `json:"event_id"`
	DeliveryID string          `json:"delivery_id"`
	Status     string          `json:"status"`
	ReceivedAt string          `json:"received_at"`
	UpdatedAt  string          `json:"updated_at"`
	Event      json.RawMessage `json:"event,omitempty"`
}

type SessionState struct {
	Provider    string        `json:"provider"`
	SessionID   string        `json:"session_id"`
	CreatedAt   string        `json:"created_at"`
	Lifecycle   Lifecycle     `json:"lifecycle"`
	Events      []EventRecord `json:"events"`
	Permissions []string      `json:"permissions,omitempty"`
}

type Store struct {
	Version  string         `json:"version"`
	Sessions []SessionState `json:"sessions"`
}

func NewStore() Store {
	return Store{Version: Version, Sessions: []SessionState{}}
}

func Path(configPath, explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if fromEnv := os.Getenv("AWP_EVENTS"); fromEnv != "" {
		return filepath.Abs(fromEnv)
	}
	resolvedConfig, err := config.Path(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(resolvedConfig), "events.json"), nil
}

func Load(path string) (Store, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewStore(), nil
	}
	if err != nil {
		return Store{}, err
	}
	defer file.Close()

	store := NewStore()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store); err != nil {
		return Store{}, fmt.Errorf("decode event store: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Store{}, errors.New("decode event store: multiple JSON values")
		}
		return Store{}, fmt.Errorf("decode event store: %w", err)
	}
	if store.Version != Version {
		return Store{}, fmt.Errorf("unsupported event store version %q", store.Version)
	}
	if store.Sessions == nil {
		store.Sessions = []SessionState{}
	}
	return store, nil
}

func Save(path string, store Store) error {
	if store.Version != Version {
		return fmt.Errorf("unsupported event store version %q", store.Version)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create event store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".events-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary event store: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(store); err != nil {
		temporary.Close()
		return fmt.Errorf("encode event store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace event store: %w", err)
	}
	return nil
}

func find(sessions []SessionState, provider, sessionID string) int {
	for index := range sessions {
		if sessions[index].Provider == provider && sessions[index].SessionID == sessionID {
			return index
		}
	}
	return -1
}

func Get(store Store, provider, sessionID string) (SessionState, bool) {
	index := find(store.Sessions, provider, sessionID)
	if index == -1 {
		return SessionState{}, false
	}
	return store.Sessions[index], true
}

// Seen reports whether eventID has already been recorded for (provider, sessionID).
func Seen(store Store, provider, sessionID, eventID string) bool {
	state, found := Get(store, provider, sessionID)
	if !found {
		return false
	}
	for _, event := range state.Events {
		if event.EventID == eventID {
			return true
		}
	}
	return false
}

func ensureSession(store *Store, provider, sessionID, now string) *SessionState {
	index := find(store.Sessions, provider, sessionID)
	if index == -1 {
		store.Sessions = append(store.Sessions, SessionState{
			Provider:  provider,
			SessionID: sessionID,
			CreatedAt: now,
			Lifecycle: Lifecycle{Status: StatusIdle, UpdatedAt: now},
			Events:    []EventRecord{},
		})
		index = len(store.Sessions) - 1
	}
	return &store.Sessions[index]
}

// RecordDelivery registers an incoming delivery for (provider, sessionID),
// keeping the raw event payload so a resumed agent can inspect it without
// re-fetching it from the provider. It reports duplicate=true only when
// eventID already reached EventStatusCompleted, so the caller can skip waking
// the runtime adapter for work it already finished. A redelivered event that
// previously failed, or was left pending by a daemon crash, is not a
// duplicate: it is reset to pending so it gets retried, matching the
// at-least-once delivery semantics the provider relies on.
//
// If the session is currently paused or crashed (see Gate), the event is
// still recorded so it shows up as pending once the session resumes, but the
// lifecycle status itself is left untouched: the caller is expected to check
// Gate on the returned state and hold the delivery instead of running the
// adapter, and RecordDelivery must not overwrite the deliberate pause/crash
// with "running" in the process.
func RecordDelivery(store *Store, provider, sessionID, eventID, deliveryID string, event json.RawMessage) (SessionState, bool, error) {
	provider = strings.TrimSpace(provider)
	sessionID = strings.TrimSpace(sessionID)
	eventID = strings.TrimSpace(eventID)
	if provider == "" || sessionID == "" || eventID == "" {
		return SessionState{}, false, errors.New("provider, session_id, and event_id are required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	payload := append(json.RawMessage(nil), event...)
	current := ensureSession(store, provider, sessionID, now)
	gated, _ := Gate(*current)

	reason := fmt.Sprintf("processing event %s", eventID)
	for index := range current.Events {
		if current.Events[index].EventID != eventID {
			continue
		}
		if current.Events[index].Status == EventStatusCompleted {
			return *current, true, nil
		}
		current.Events[index].Status = EventStatusPending
		current.Events[index].DeliveryID = strings.TrimSpace(deliveryID)
		current.Events[index].UpdatedAt = now
		current.Events[index].Event = payload
		if !gated {
			current.Lifecycle = Lifecycle{Status: StatusRunning, UpdatedAt: now, Reason: fmt.Sprintf("retrying event %s", eventID)}
		}
		return *current, false, nil
	}
	current.Events = append(current.Events, EventRecord{
		EventID:    eventID,
		DeliveryID: strings.TrimSpace(deliveryID),
		Status:     EventStatusPending,
		ReceivedAt: now,
		UpdatedAt:  now,
		Event:      payload,
	})
	if len(current.Events) > maxEventHistory {
		current.Events = append([]EventRecord(nil), current.Events[len(current.Events)-maxEventHistory:]...)
	}
	if !gated {
		current.Lifecycle = Lifecycle{Status: StatusRunning, UpdatedAt: now, Reason: reason}
	}
	return *current, false, nil
}

// CompleteEvent marks eventID as finished and updates the session lifecycle.
// status must be EventStatusCompleted or EventStatusFailed. The event payload
// is dropped once it reaches a terminal status: only pending (unprocessed)
// events need to carry their full body for MCP introspection, and this keeps
// the store from growing without bound over a long-lived binding.
func CompleteEvent(store *Store, provider, sessionID, eventID, status string) (SessionState, error) {
	if status != EventStatusCompleted && status != EventStatusFailed {
		return SessionState{}, fmt.Errorf("unsupported event status %q", status)
	}
	index := find(store.Sessions, provider, sessionID)
	if index == -1 {
		return SessionState{}, fmt.Errorf("no event state for %s/%s", provider, sessionID)
	}
	current := &store.Sessions[index]
	found := false
	for eventIndex := range current.Events {
		if current.Events[eventIndex].EventID == eventID {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			current.Events[eventIndex].Status = status
			current.Events[eventIndex].UpdatedAt = now
			current.Events[eventIndex].Event = nil
			found = true
			break
		}
	}
	if !found {
		return SessionState{}, fmt.Errorf("event %s was not recorded for %s/%s", eventID, provider, sessionID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	lifecycle := Lifecycle{Status: StatusIdle, UpdatedAt: now}
	if status == EventStatusFailed {
		lifecycle = Lifecycle{Status: StatusFailed, UpdatedAt: now, Reason: fmt.Sprintf("event %s failed", eventID)}
	}
	current.Lifecycle = lifecycle
	return *current, nil
}

// Pause deliberately stops (provider, sessionID) from being woken by future
// deliveries until Resume is called. Callable by a human (CLI) or by the
// agent itself (MCP tool) when it wants to keep working uninterrupted.
func Pause(store *Store, provider, sessionID, reason string) SessionState {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	current := ensureSession(store, provider, sessionID, now)
	current.Lifecycle = Lifecycle{Status: StatusPaused, UpdatedAt: now, Reason: strings.TrimSpace(reason)}
	return *current
}

// MarkCrashed records that (provider, sessionID)'s binding is structurally
// broken (missing runtime binary, missing workspace, and similar) rather than
// this one event having failed. A crashed session is gated the same way a
// paused one is: retrying automatically would just fail again and burn
// tokens, so it stays held until a human fixes the binding and calls Resume.
func MarkCrashed(store *Store, provider, sessionID, reason string) SessionState {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	current := ensureSession(store, provider, sessionID, now)
	current.Lifecycle = Lifecycle{Status: StatusCrashed, UpdatedAt: now, Reason: strings.TrimSpace(reason)}
	return *current
}

// Resume clears a paused or crashed session back to idle so future
// deliveries wake the runtime adapter again.
func Resume(store *Store, provider, sessionID string) (SessionState, error) {
	index := find(store.Sessions, provider, sessionID)
	if index == -1 {
		return SessionState{}, fmt.Errorf("no event state for %s/%s", provider, sessionID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	store.Sessions[index].Lifecycle = Lifecycle{Status: StatusIdle, UpdatedAt: now}
	return store.Sessions[index], nil
}

// Gate reports whether the daemon should hold a new delivery instead of
// waking the runtime adapter, and why:
//   - StatusPaused: a human or the agent itself deliberately stopped this
//     session from being woken.
//   - StatusCrashed: the binding itself is broken, not just one event; waking
//     it again would just fail the same way.
//
// A session that is merely StatusRunning is not gated here: the daemon's
// per-session lock already serializes concurrent deliveries for the same
// session, so a fresh delivery simply waits its turn instead of being
// skipped.
func Gate(state SessionState) (blocked bool, reason string) {
	switch state.Lifecycle.Status {
	case StatusPaused:
		return true, "session is paused: " + state.Lifecycle.Reason
	case StatusCrashed:
		return true, "session is crashed: " + state.Lifecycle.Reason
	default:
		return false, ""
	}
}

// SetPermissions records the permission IDs currently granted for (provider,
// sessionID), so a workspace projection can show the agent what it is allowed
// to do without it having to read the separate permissions store.
func SetPermissions(store *Store, provider, sessionID string, permissions []string) SessionState {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	current := ensureSession(store, provider, sessionID, now)
	current.Permissions = append([]string(nil), permissions...)
	return *current
}

// Pending returns event records that have not reached a terminal status,
// oldest first. This is the set that should be surfaced to a resumed agent.
func Pending(state SessionState) []EventRecord {
	pending := make([]EventRecord, 0, len(state.Events))
	for _, event := range state.Events {
		if event.Status == EventStatusPending {
			pending = append(pending, event)
		}
	}
	return pending
}

func List(store Store, provider string) []SessionState {
	values := []SessionState{}
	for _, state := range store.Sessions {
		if provider == "" || state.Provider == provider {
			values = append(values, state)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Provider != values[j].Provider {
			return values[i].Provider < values[j].Provider
		}
		return values[i].SessionID < values[j].SessionID
	})
	return values
}
