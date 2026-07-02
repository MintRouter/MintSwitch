// Package settings persists MintSwitch's OWN state: the active profile and a
// per-tool applied-state map. This is distinct from the configuration files of
// the managed tools, which the adapters own.
//
// State is written to a single JSON file with an atomic temp-file + rename so a
// crash mid-write cannot corrupt it. The file may contain an API key, so it is
// written with 0600 permissions and its contents must never be logged.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mintswitch/internal/core"
)

// State is the full persisted MintSwitch state.
type State struct {
	// ActiveProfile is the profile the user has saved as active, if any.
	ActiveProfile *core.Profile `json:"active_profile,omitempty"`
	// ToolModels maps a tool ID to the model the user chose for that tool,
	// overriding the active profile's selected Model when applying. An absent or
	// stale entry falls back to the profile default.
	ToolModels map[string]string `json:"tool_models,omitempty"`
	// MCPKey is the MintRouter API key used to inject the Remote MCP server into
	// tools. It is a secret bearer token: it is written at 0600 with the rest of
	// this file, must never be logged, and is never returned raw over bindings.
	MCPKey string `json:"mcp_key,omitempty"`
	// MCPEndpoint optionally overrides the default MintRouter MCP endpoint
	// (core.DefaultMCPEndpoint). Empty means use the default.
	MCPEndpoint string `json:"mcp_endpoint,omitempty"`
	// ContextEngineDisabled is the persisted INVERSE of the Context Engine master
	// toggle. The inverse is stored (with omitempty) so that the feature defaults
	// to ENABLED when the field is absent from an old/existing settings file: an
	// unset key unmarshals to false ⇒ not disabled ⇒ enabled. Read it through
	// [State.ContextEngineEnabled] rather than accessing this field directly.
	ContextEngineDisabled bool `json:"context_engine_disabled,omitempty"`
}

// ContextEngineEnabled reports whether the Context Engine master toggle is on.
// It defaults to true when unset (see [State.ContextEngineDisabled]).
func (s *State) ContextEngineEnabled() bool { return !s.ContextEngineDisabled }

// Store loads and saves a [State] from a single JSON file.
type Store struct {
	// Path is the settings file location (e.g. DataDir/settings.json).
	Path string
}

// NewStore returns a Store backed by the given file path.
func NewStore(path string) *Store { return &Store{Path: path} }

// Load reads the persisted state. A missing file is not an error: an empty
// State is returned so first-run callers need no special handling.
func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Save writes the state atomically: it marshals to JSON, writes a sibling temp
// file with 0600 permissions, then renames it over the target path.
func (s *Store) Save(st *State) error {
	if st == nil {
		st = &State{}
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}
