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
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

// SecretStore stores the active profile's API key outside the settings file
// (in the OS keychain). Get returns ("", false, nil) when no key is stored;
// any other error means the backing keychain is unavailable, in which case
// the Store falls back to keeping the key in the settings file as before.
type SecretStore interface {
	Get() (value string, found bool, err error)
	Set(value string) error
}

// Store loads and saves a [State] from a single JSON file.
type Store struct {
	// Path is the settings file location (e.g. DataDir/settings.json).
	Path string
	// Secrets, when non-nil, holds the active profile's API key in the OS
	// keychain: Save moves a non-empty key there (clearing it from the file
	// only after the keychain write succeeded) and Load re-populates it into
	// the returned State. When nil (the default, and in tests), the key is
	// kept in the file exactly as before.
	Secrets SecretStore

	// secMu guards the key cache below. The cache avoids re-reading the
	// keychain (a subprocess on macOS) on every Load; only successful reads
	// and writes are cached.
	secMu     sync.Mutex
	secKey    string
	secCached bool
}

// NewStore returns a Store backed by the given file path.
func NewStore(path string) *Store { return &Store{Path: path} }

// Load reads the persisted state. A missing file is not an error: an empty
// State is returned so first-run callers need no special handling.
//
// When Secrets is set and the file carries no api_key, the active profile's
// key is filled in from the keychain, so callers always see a complete State.
// A key still present in the file (keychain unavailable, or not yet migrated)
// is used as-is.
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
	if s.Secrets != nil && st.ActiveProfile != nil && strings.TrimSpace(st.ActiveProfile.APIKey) == "" {
		if v, ok, err := s.getSecret(); err == nil && ok {
			st.ActiveProfile.APIKey = v
		}
	}
	return &st, nil
}

// MigrateAPIKey moves an api_key found in the settings file into the keychain
// and rewrites the file without it. It is idempotent: once the file carries no
// key it is a no-op, so it is safe to call on every startup. When the keychain
// write fails the file is left carrying the key exactly as before (Save's
// fallback), so the key is never lost. It is a no-op when Secrets is nil.
func (s *Store) MigrateAPIKey() error {
	if s.Secrets == nil {
		return nil
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	if st.ActiveProfile == nil || strings.TrimSpace(st.ActiveProfile.APIKey) == "" {
		return nil
	}
	return s.Save(&st)
}

// getSecret reads the API key from the keychain, serving repeat calls from
// the in-memory cache. Only successful, found reads are cached.
func (s *Store) getSecret() (string, bool, error) {
	s.secMu.Lock()
	defer s.secMu.Unlock()
	if s.secCached {
		return s.secKey, true, nil
	}
	v, ok, err := s.Secrets.Get()
	if err != nil || !ok {
		return "", false, err
	}
	s.secKey, s.secCached = v, true
	return v, true, nil
}

// setSecret writes the API key to the keychain and updates the cache on
// success. The key value itself must never be logged by callers.
func (s *Store) setSecret(v string) error {
	s.secMu.Lock()
	defer s.secMu.Unlock()
	if err := s.Secrets.Set(v); err != nil {
		return err
	}
	s.secKey, s.secCached = v, true
	return nil
}

// Save writes the state atomically: it marshals to JSON, writes a sibling temp
// file with 0600 permissions, then renames it over the target path.
//
// When Secrets is set and st carries a non-empty profile API key, the key is
// first written to the keychain; only after that write succeeds is the file
// persisted with an empty api_key (the caller's st is never mutated). If the
// keychain write fails, a warning is logged (never the value) and the key is
// kept in the file as before, so it is never lost.
func (s *Store) Save(st *State) error {
	if st == nil {
		st = &State{}
	}
	if s.Secrets != nil && st.ActiveProfile != nil && strings.TrimSpace(st.ActiveProfile.APIKey) != "" {
		if err := s.setSecret(st.ActiveProfile.APIKey); err != nil {
			log.Printf("settings: OS keychain unavailable, keeping api_key in settings file: %v", err)
		} else {
			cp := *st
			p := *st.ActiveProfile
			p.APIKey = ""
			cp.ActiveProfile = &p
			st = &cp
		}
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
