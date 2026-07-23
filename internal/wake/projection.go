package wake

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Projection is the read-only snapshot written to <workspace>/.awp/data.json.
// It carries only what a resumed agent needs to answer "why was I woken and
// what is left to do": the current lifecycle and the events that are not yet
// processed. Already-processed events stay in the central Store only, so this
// file does not grow with a session's full history.
type Projection struct {
	SessionID   string        `json:"session_id"`
	Provider    string        `json:"provider"`
	CreatedAt   string        `json:"created_at"`
	Lifecycle   Lifecycle     `json:"lifecycle"`
	Pending     []EventRecord `json:"pending_events"`
	Permissions []string      `json:"permissions,omitempty"`
}

// WriteProjection persists state as <workspace>/.awp/data.json. It is a no-op
// when workspace is empty, since not every binding is tied to a directory.
func WriteProjection(workspace string, state SessionState) error {
	if workspace == "" {
		return nil
	}
	directory := filepath.Join(workspace, ".awp")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create workspace projection directory: %w", err)
	}
	path := filepath.Join(directory, "data.json")

	projection := Projection{
		SessionID:   state.SessionID,
		Provider:    state.Provider,
		CreatedAt:   state.CreatedAt,
		Lifecycle:   state.Lifecycle,
		Pending:     Pending(state),
		Permissions: state.Permissions,
	}

	temporary, err := os.CreateTemp(directory, ".data-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary workspace projection: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(projection); err != nil {
		temporary.Close()
		return fmt.Errorf("encode workspace projection: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace workspace projection: %w", err)
	}
	return nil
}
