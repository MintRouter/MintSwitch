// Package settings persists MintSwitch's OWN state: the managed Provider
// list, the active provider selection and the per-tool override maps. This is
// distinct from the configuration files of the managed tools, which the
// adapters own.
//
// State is written to a single JSON file with an atomic temp-file + rename so a
// crash mid-write cannot corrupt it. The file may contain API keys (keychain
// fallback), so it is written with 0600 permissions and its contents must
// never be logged.
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
	// Providers is the managed list of endpoint providers.
	Providers []core.Provider `json:"providers,omitempty"`
	// ActiveProviderID selects the globally active member of Providers. It is
	// reconciled on Load: a stale or empty value falls back to the first
	// provider (empty when there are none).
	ActiveProviderID string `json:"active_provider_id,omitempty"`
	// ToolModels maps a tool ID to the model the user chose for that tool,
	// overriding the effective provider's default Model when applying. An
	// absent or stale entry falls back to the provider default.
	ToolModels map[string]string `json:"tool_models,omitempty"`
	// ToolProviders maps a tool ID to the provider (by ID) the user chose for
	// that tool, overriding the active provider when applying. An absent or
	// stale entry falls back to the active provider.
	ToolProviders map[string]string `json:"tool_providers,omitempty"`

	// LegacyProfile is the pre-provider active_profile shape (v1 single-key or
	// Wave 2 multi-key). It is read only so migrate() can convert it into
	// Providers transparently; it is cleared afterwards and never written back.
	LegacyProfile *legacyProfile `json:"active_profile,omitempty"`
	// LegacyToolKeys is the pre-provider per-tool key selection map
	// (tool_keys). Key entry IDs became provider IDs, so migrate() converts it
	// 1:1 into ToolProviders. Cleared afterwards, never written back.
	LegacyToolKeys map[string]string `json:"tool_keys,omitempty"`
}

// legacyProfile mirrors the pre-provider Profile persisted under
// active_profile, including the Wave 2 managed key list. It exists only to
// decode old settings files for migration.
type legacyProfile struct {
	Label          string            `json:"label,omitempty"`
	APIKey         string            `json:"api_key"`
	BaseURL        string            `json:"base_url"`
	Models         []string          `json:"models,omitempty"`
	ModelNames     map[string]string `json:"model_names,omitempty"`
	Model          string            `json:"model"`
	SmallFastModel string            `json:"small_fast_model,omitempty"`
	APIKeys        []legacyKeyEntry  `json:"api_keys,omitempty"`
	ActiveKeyID    string            `json:"active_key_id,omitempty"`
}

// legacyKeyEntry mirrors the Wave 2 APIKeyEntry for migration decoding only.
type legacyKeyEntry struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Key      string `json:"key,omitempty"`
}

// Provider returns the provider with the given ID and whether it exists.
func (st *State) Provider(id string) (core.Provider, bool) {
	for _, p := range st.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return core.Provider{}, false
}

// ActiveProvider returns the provider selected by ActiveProviderID and
// whether one exists.
func (st *State) ActiveProvider() (core.Provider, bool) {
	return st.Provider(st.ActiveProviderID)
}

// migrate converts a legacy (v1 or Wave 2) state into the provider shape, in
// place, reporting whether anything changed. A Wave 2 multi-key profile
// becomes one Provider per key entry — all sharing the profile's endpoint and
// model fields, keeping the entry ID and name — with the active key's
// provider active and tool_keys carried over into ToolProviders. A v1
// single-key profile becomes one active Provider named "Default". It is a
// no-op when the file already has the provider shape or providers exist.
func (st *State) migrate() (changed bool) {
	if st.LegacyProfile == nil && len(st.LegacyToolKeys) == 0 {
		return false
	}
	if lp := st.LegacyProfile; lp != nil && len(st.Providers) == 0 {
		if len(lp.APIKeys) > 0 {
			for _, e := range lp.APIKeys {
				st.Providers = append(st.Providers, core.Provider{
					ID:             e.ID,
					Name:           e.Provider,
					APIKey:         e.Key,
					BaseURL:        lp.BaseURL,
					Models:         lp.Models,
					ModelNames:     lp.ModelNames,
					Model:          lp.Model,
					SmallFastModel: lp.SmallFastModel,
				})
			}
			st.ActiveProviderID = lp.ActiveKeyID
		} else {
			st.Providers = []core.Provider{{
				ID:             core.DefaultProviderID,
				Name:           "Default",
				APIKey:         lp.APIKey,
				BaseURL:        lp.BaseURL,
				Models:         lp.Models,
				ModelNames:     lp.ModelNames,
				Model:          lp.Model,
				SmallFastModel: lp.SmallFastModel,
			}}
			st.ActiveProviderID = core.DefaultProviderID
		}
	}
	if len(st.LegacyToolKeys) > 0 && st.ToolProviders == nil {
		st.ToolProviders = st.LegacyToolKeys
	}
	st.LegacyProfile, st.LegacyToolKeys = nil, nil
	return true
}

// normalize reconciles the active selection with the provider list, in
// place: a missing or stale ActiveProviderID falls back to the first
// provider, and it is cleared when no providers exist.
func (st *State) normalize() {
	if len(st.Providers) == 0 {
		st.ActiveProviderID = ""
		return
	}
	if _, ok := st.Provider(st.ActiveProviderID); !ok {
		st.ActiveProviderID = st.Providers[0].ID
	}
}

// SecretStore stores the providers' API key material outside the settings
// file (in the OS keychain). Get returns ("", false, nil) when no value is
// stored; any other error means the backing keychain is unavailable, in which
// case the Store falls back to keeping the keys in the settings file as
// before.
type SecretStore interface {
	Get() (value string, found bool, err error)
	Set(value string) error
}

// keyBlob is the JSON envelope stored in the OS keychain carrying every
// provider's secret key value by provider ID. One blob under the existing
// service/account keeps old keychains readable: Wave 2 stored the blob by key
// entry ID (which became the provider ID), and a v1 plain key value simply
// fails to parse as a blob and is treated as the active provider's key.
type keyBlob struct {
	Keys map[string]string `json:"keys"`
}

// parseKeyBlob decodes a keychain value as a keyBlob, reporting whether it is
// one (as opposed to a legacy plain key value).
func parseKeyBlob(v string) (map[string]string, bool) {
	var b keyBlob
	if err := json.Unmarshal([]byte(v), &b); err != nil || b.Keys == nil {
		return nil, false
	}
	return b.Keys, true
}

// Store loads and saves a [State] from a single JSON file.
type Store struct {
	// Path is the settings file location (e.g. DataDir/settings.json).
	Path string
	// Secrets, when non-nil, holds the providers' API keys in the OS
	// keychain: Save moves non-empty keys there (clearing them from the file
	// only after the keychain write succeeded) and Load re-populates them into
	// the returned State. When nil (the default, and in tests), the keys are
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
// A legacy (v1 or Wave 2) file is migrated to the provider shape in memory on
// every Load, so callers only ever see Providers ([Migrate] persists the new
// shape at startup). When Secrets is set, providers with no key value in the
// file are filled in from the keychain, so callers always see a complete
// State. Keys still present in the file (keychain unavailable, or not yet
// migrated) are used as-is. The active selection is reconciled: a stale
// ActiveProviderID falls back to the first provider.
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
	st.migrate()
	if s.Secrets != nil {
		s.fillKeysFromSecrets(&st)
	}
	st.normalize()
	return &st, nil
}

// fillKeysFromSecrets fills providers' empty key values from the keychain. It
// understands both the blob format (by provider/entry ID) and a v1 plain
// single-key value, so upgrades in either store never lose a key. Keychain
// errors are ignored (fallback: whatever the file carries is used).
func (s *Store) fillKeysFromSecrets(st *State) {
	missing := false
	for _, p := range st.Providers {
		if strings.TrimSpace(p.APIKey) == "" {
			missing = true
			break
		}
	}
	if !missing {
		return
	}
	v, ok, err := s.getSecret()
	if err != nil || !ok {
		return
	}
	keys, isBlob := parseKeyBlob(v)
	for i := range st.Providers {
		p := &st.Providers[i]
		if strings.TrimSpace(p.APIKey) != "" {
			continue
		}
		if isBlob {
			p.APIKey = keys[p.ID]
		} else if p.ID == st.ActiveProviderID || len(st.Providers) == 1 {
			// v1 plain keychain value predating the blob format.
			p.APIKey = v
		}
	}
}

// Migrate rewrites a legacy settings file into the provider shape and moves
// API key material found in the file into the keychain. It is idempotent:
// once the file has the provider shape and carries no key values it is a
// no-op, so it is safe to call on every startup. When the keychain write
// fails the file keeps the keys exactly as before (Save's fallback), so they
// are never lost. It is a no-op when Secrets is nil.
func (s *Store) Migrate() error {
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
	migrated := st.migrate()
	if !migrated && !hasFileKeyMaterial(&st) {
		return nil
	}
	st.normalize()
	return s.Save(&st)
}

// hasFileKeyMaterial reports whether the state as read from the file still
// carries any secret key value that belongs in the keychain.
func hasFileKeyMaterial(st *State) bool {
	for _, p := range st.Providers {
		if strings.TrimSpace(p.APIKey) != "" {
			return true
		}
	}
	return false
}

// getSecret reads the raw keychain value (a keyBlob, or a legacy plain key),
// serving repeat calls from the in-memory cache. Only successful, found reads
// are cached.
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

// setSecret writes the raw keychain value and updates the cache on success.
// The value carries key material and must never be logged by callers.
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
// When Secrets is set and st carries provider key material, the keys are
// first written to the keychain as one JSON blob (by provider ID); only after
// that write succeeds is the file persisted with the key values blanked (the
// caller's st is never mutated). If the keychain write fails, a warning is
// logged (never a value) and the keys are kept in the file as before, so they
// are never lost.
func (s *Store) Save(st *State) error {
	if st == nil {
		st = &State{}
	}
	if s.Secrets != nil && len(st.Providers) > 0 {
		if stripped, ok := s.moveKeysToSecrets(st); ok {
			st = stripped
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

// moveKeysToSecrets writes st's provider key material to the keychain and
// returns a deep copy with the key values blanked, for persisting to the
// file. ok=false means nothing was moved — no key material, or the keychain
// write failed (logged, never a value) — so the caller persists st unchanged,
// preserving the plaintext file fallback.
func (s *Store) moveKeysToSecrets(st *State) (*State, bool) {
	blob, ok := s.buildKeyBlob(st.Providers)
	if !ok {
		return nil, false
	}
	if err := s.setSecret(blob); err != nil {
		log.Printf("settings: OS keychain unavailable, keeping api keys in settings file: %v", err)
		return nil, false
	}
	cp := *st
	cp.Providers = make([]core.Provider, len(st.Providers))
	copy(cp.Providers, st.Providers)
	for i := range cp.Providers {
		cp.Providers[i].APIKey = ""
	}
	return &cp, true
}

// buildKeyBlob serializes the providers' key values as the keychain blob. For
// a provider whose in-memory key is empty (e.g. the keychain was unavailable
// at load time), the value already stored in the keychain is preserved, so a
// partially-loaded state can never erase a stored key. ok=false means there
// is no key material to store.
func (s *Store) buildKeyBlob(providers []core.Provider) (string, bool) {
	m := make(map[string]string, len(providers))
	var existing map[string]string
	for _, p := range providers {
		if strings.TrimSpace(p.APIKey) != "" {
			m[p.ID] = p.APIKey
			continue
		}
		if existing == nil {
			existing = map[string]string{}
			if v, ok, err := s.getSecret(); err == nil && ok {
				if keys, isBlob := parseKeyBlob(v); isBlob {
					existing = keys
				}
			}
		}
		if prev := existing[p.ID]; prev != "" {
			m[p.ID] = prev
		}
	}
	if len(m) == 0 {
		return "", false
	}
	data, err := json.Marshal(keyBlob{Keys: m})
	if err != nil {
		return "", false
	}
	return string(data), true
}
