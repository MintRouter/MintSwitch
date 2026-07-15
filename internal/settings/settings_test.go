package settings

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"mintswitch/internal/core"
)

// prov builds a valid provider for settings tests.
func prov(id, name, key string) core.Provider {
	return core.Provider{
		ID: id, Name: name, APIKey: key,
		BaseURL: "https://h", Model: "m", Models: []string{"m", "m2"},
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nope", "settings.json"))
	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if st == nil || len(st.Providers) != 0 || st.ActiveProviderID != "" {
		t.Fatalf("expected empty state, got %+v", st)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	s := NewStore(path)

	in := &State{
		Providers: []core.Provider{
			prov("p1", "OpenAI", "sk-one"),
			prov("p2", "MintRouter", "sk-two"),
		},
		ActiveProviderID: "p2",
	}
	in.Providers[0].Note = "team key"

	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(out.Providers, in.Providers) || out.ActiveProviderID != "p2" {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

// TestLoadNormalizesStaleActive proves a stale or missing ActiveProviderID
// falls back to the first provider on Load.
func TestLoadNormalizesStaleActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	if err := s.Save(&State{
		Providers:        []core.Provider{prov("p1", "A", "k")},
		ActiveProviderID: "gone",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ActiveProviderID != "p1" {
		t.Fatalf("ActiveProviderID = %q, want p1", out.ActiveProviderID)
	}
}

func TestSaveAtomicAndPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	s := NewStore(path)

	if err := s.Save(&State{}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("perms = %o, want 600", perm)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "settings.json" {
			t.Fatalf("leftover temp file: %q", e.Name())
		}
	}
}

// TestSaveLoadToolMapsRoundTrip proves the per-tool model and provider
// selections persist alongside the provider list unchanged.
func TestSaveLoadToolMapsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	s := NewStore(path)

	in := &State{
		Providers:        []core.Provider{prov("p1", "A", "k")},
		ActiveProviderID: "p1",
		ToolModels:       map[string]string{"claude-code": "m2", "codex": "m"},
		ToolProviders:    map[string]string{"claude-code": "p1"},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(out.ToolModels, in.ToolModels) {
		t.Fatalf("tool models mismatch: %+v vs %+v", out.ToolModels, in.ToolModels)
	}
	if !reflect.DeepEqual(out.ToolProviders, in.ToolProviders) {
		t.Fatalf("tool providers mismatch: %+v vs %+v", out.ToolProviders, in.ToolProviders)
	}
}

func TestSaveNilState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	if err := s.Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Providers) != 0 {
		t.Fatal("expected empty state from nil save")
	}
}

// TestLoadMigratesV1Profile proves a v1 settings file (single api_key
// active_profile) transparently becomes one active Provider named "Default".
func TestLoadMigratesV1Profile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v1 := `{
  "active_profile": {"label": "work", "api_key": "sk-v1", "base_url": "https://h",
    "model": "m", "models": ["m", "m2"], "model_names": {"m": "nice"},
    "small_fast_model": "sf"},
  "tool_models": {"codex": "m2"}
}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("write v1 settings: %v", err)
	}
	out, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load v1 settings: %v", err)
	}
	if len(out.Providers) != 1 {
		t.Fatalf("providers = %+v, want 1", out.Providers)
	}
	p := out.Providers[0]
	if p.ID != core.DefaultProviderID || p.Name != "Default" || p.APIKey != "sk-v1" ||
		p.BaseURL != "https://h" || p.Model != "m" || p.SmallFastModel != "sf" ||
		len(p.Models) != 2 || p.ModelNames["m"] != "nice" {
		t.Fatalf("migrated provider wrong: %+v", p)
	}
	if out.ActiveProviderID != core.DefaultProviderID {
		t.Fatalf("active = %q, want default", out.ActiveProviderID)
	}
	// Old per-tool model overrides stay valid.
	if out.ToolModels["codex"] != "m2" {
		t.Fatalf("tool models lost: %+v", out.ToolModels)
	}
}

// TestLoadMigratesWave2Profile proves a Wave 2 settings file (multi-key
// active_profile + tool_keys) becomes one Provider per key entry sharing the
// endpoint fields, with the active key's provider active and tool_keys
// carried into ToolProviders.
func TestLoadMigratesWave2Profile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	w2 := `{
  "active_profile": {"base_url": "https://h", "model": "m", "models": ["m", "m2"],
    "api_key": "sk-two",
    "api_keys": [
      {"id": "k1", "provider": "OpenAI", "key": "sk-one"},
      {"id": "k2", "provider": "MintRouter", "key": "sk-two"}
    ],
    "active_key_id": "k2"},
  "tool_keys": {"claude-code": "k1"},
  "tool_models": {"codex": "m2"}
}`
	if err := os.WriteFile(path, []byte(w2), 0o600); err != nil {
		t.Fatalf("write wave2 settings: %v", err)
	}
	out, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load wave2 settings: %v", err)
	}
	if len(out.Providers) != 2 {
		t.Fatalf("providers = %+v, want 2", out.Providers)
	}
	p1, p2 := out.Providers[0], out.Providers[1]
	if p1.ID != "k1" || p1.Name != "OpenAI" || p1.APIKey != "sk-one" ||
		p1.BaseURL != "https://h" || p1.Model != "m" || len(p1.Models) != 2 {
		t.Fatalf("provider 1 wrong: %+v", p1)
	}
	if p2.ID != "k2" || p2.Name != "MintRouter" || p2.APIKey != "sk-two" {
		t.Fatalf("provider 2 wrong: %+v", p2)
	}
	if out.ActiveProviderID != "k2" {
		t.Fatalf("active = %q, want k2", out.ActiveProviderID)
	}
	if out.ToolProviders["claude-code"] != "k1" {
		t.Fatalf("tool_keys not carried into ToolProviders: %+v", out.ToolProviders)
	}
	if out.ToolModels["codex"] != "m2" {
		t.Fatalf("tool models lost: %+v", out.ToolModels)
	}
}

// TestSaveDropsLegacyShape proves a migrated state saved back to disk carries
// the provider shape only (no active_profile / tool_keys reappear).
func TestSaveDropsLegacyShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v1 := `{"active_profile": {"api_key": "sk-v1", "base_url": "https://h", "model": "m"}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "active_profile") || strings.Contains(string(data), "tool_keys") {
		t.Fatalf("legacy shape written back: %s", data)
	}
	if !strings.Contains(string(data), `"providers"`) {
		t.Fatalf("provider shape missing: %s", data)
	}
}

// fakeSecrets is an in-memory SecretStore. A non-nil setErr makes Set fail,
// simulating an unavailable keychain (e.g. headless Linux).
type fakeSecrets struct {
	key       string
	has       bool
	sets      int
	deletes   int
	setErr    error
	setErrAt  map[int]error
	deleteErr error
}

func (f *fakeSecrets) Get() (string, bool, error) { return f.key, f.has, nil }
func (f *fakeSecrets) Set(v string) error {
	f.sets++
	if err := f.setErrAt[f.sets]; err != nil {
		return err
	}
	if f.setErr != nil {
		return f.setErr
	}
	f.key, f.has = v, true
	return nil
}
func (f *fakeSecrets) Delete() error {
	f.deletes++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.key, f.has = "", false
	return nil
}

// mustNotContain fails the test when the raw settings file contains the
// secret value.
func mustNotContain(t *testing.T, path, secret string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings file: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("settings file still contains the api key value")
	}
}

// TestSaveMovesKeysToSecrets proves Save routes every provider's key to the
// SecretStore as one blob, keeps all values out of the file, does not mutate
// the caller's state, and Load fills them back in.
func TestSaveMovesKeysToSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	fs := &fakeSecrets{}
	s.Secrets = fs

	in := &State{
		Providers: []core.Provider{
			prov("p1", "OpenAI", "sk-one"),
			prov("p2", "MintRouter", "sk-two"),
		},
		ActiveProviderID: "p1",
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if in.Providers[0].APIKey != "sk-one" {
		t.Fatal("Save mutated the caller's state")
	}
	mustNotContain(t, path, "sk-one")
	mustNotContain(t, path, "sk-two")
	// Provider names stay in the file (they are not secrets).
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "OpenAI") || !strings.Contains(string(data), "MintRouter") {
		t.Fatal("provider names must stay in the settings file")
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out.Providers) != 2 || out.Providers[0].APIKey != "sk-one" || out.Providers[1].APIKey != "sk-two" {
		t.Fatalf("Load did not restore keys from secrets: %+v", out.Providers)
	}
}

// TestMigrateMovesKeysAndShape proves the startup migration: a Wave 2 file
// becomes providers, key material moves into the keychain blob and the file
// is rewritten without it, and a second run is an idempotent no-op.
func TestMigrateMovesKeysAndShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	w2 := `{
  "active_profile": {"base_url": "https://h", "model": "m",
    "api_keys": [
      {"id": "k1", "provider": "OpenAI", "key": "sk-one"},
      {"id": "k2", "provider": "MintRouter", "key": "sk-two"}
    ],
    "active_key_id": "k1"},
  "tool_keys": {"claude-code": "k2"}
}`
	if err := os.WriteFile(path, []byte(w2), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStore(path)
	fs := &fakeSecrets{}
	s.Secrets = fs
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	mustNotContain(t, path, "sk-one")
	mustNotContain(t, path, "sk-two")
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "active_profile") {
		t.Fatalf("legacy shape survived migration: %s", data)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate (2nd run): %v", err)
	}
	if fs.sets != 1 {
		t.Fatalf("keychain writes = %d, want 1 (idempotent)", fs.sets)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load after migration: %v", err)
	}
	if len(out.Providers) != 2 || out.Providers[0].APIKey != "sk-one" || out.Providers[1].APIKey != "sk-two" {
		t.Fatalf("keys not readable after migration: %+v", out.Providers)
	}
	if out.ToolProviders["claude-code"] != "k2" {
		t.Fatalf("tool providers lost: %+v", out.ToolProviders)
	}
}

// TestLoadLegacyPlainKeychainValue proves a keychain written by v1 (a plain
// key value, not a blob) still hydrates the active provider's key.
func TestLoadLegacyPlainKeychainValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// Seed a provider file without key values (as Save would write it).
	p := prov(core.DefaultProviderID, "Default", "")
	if err := NewStore(path).Save(&State{
		Providers: []core.Provider{p}, ActiveProviderID: core.DefaultProviderID,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := NewStore(path)
	s.Secrets = &fakeSecrets{key: "sk-plain", has: true}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Providers[0].APIKey != "sk-plain" {
		t.Fatalf("legacy plain keychain value not hydrated: %+v", out.Providers)
	}
}

// TestSaveKeepsKeysInFileWhenKeychainFails proves the fallback: when the
// SecretStore write fails, every key stays in the 0600 file (never lost) and
// a plain round-trip still works.
func TestSaveKeepsKeysInFileWhenKeychainFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	s.Secrets = &fakeSecrets{setErr: errors.New("no secret service")}

	in := &State{Providers: []core.Provider{prov("p1", "OpenAI", "sk-one")}, ActiveProviderID: "p1"}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "sk-one") {
		t.Fatal("fallback should keep key values in the file when the keychain fails")
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Providers[0].APIKey != "sk-one" {
		t.Fatalf("key lost on keychain failure: %+v", out.Providers)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate with failing keychain: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "sk-one") {
		t.Fatal("failed migration must leave the key in the file")
	}
}

// TestSavePreservesStoredKeyForEmptyProvider proves a partially-loaded state
// (a provider with an empty in-memory value, e.g. added while the keychain
// briefly failed to read) never erases the value already stored in the blob.
func TestSavePreservesStoredKeyForEmptyProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	fs := &fakeSecrets{key: `{"keys":{"p1":"sk-one"}}`, has: true}
	s.Secrets = fs

	in := &State{
		Providers: []core.Provider{
			prov("p1", "OpenAI", ""), // value not loaded
			prov("p2", "MintRouter", "sk-two"),
		},
		ActiveProviderID: "p1",
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(fs.key, "sk-one") || !strings.Contains(fs.key, "sk-two") {
		t.Fatal("keychain blob must keep the stored value and add the new one")
	}
}

// TestWave2KeychainBlobHydratesMigratedProviders proves a Wave 2 keychain
// blob (stored by key entry ID) hydrates the providers migrated from those
// entries, since entry IDs became provider IDs.
func TestWave2KeychainBlobHydratesMigratedProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// Wave 2 file already stripped of key values (they live in the keychain).
	w2 := `{
  "active_profile": {"base_url": "https://h", "model": "m",
    "api_keys": [
      {"id": "k1", "provider": "OpenAI"},
      {"id": "k2", "provider": "MintRouter"}
    ],
    "active_key_id": "k2"}
}`
	if err := os.WriteFile(path, []byte(w2), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	s.Secrets = &fakeSecrets{key: `{"keys":{"k1":"sk-one","k2":"sk-two"}}`, has: true}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out.Providers) != 2 || out.Providers[0].APIKey != "sk-one" || out.Providers[1].APIKey != "sk-two" {
		t.Fatalf("wave2 blob not hydrated into providers: %+v", out.Providers)
	}
}

func failingRename(want error) func(string, string) error {
	return func(string, string) error { return want }
}

// TestSaveRollsBackExistingBlobOnPersistenceFailure exercises states produced
// by Add, Update, and Remove. A failed settings rename must leave the original
// keychain blob intact, matching the settings file that remains on disk.
func TestSaveRollsBackExistingBlobOnPersistenceFailure(t *testing.T) {
	oldBlob := `{"keys":{"p1":"sk-old","p2":"sk-two"}}`
	cases := []struct {
		name      string
		providers []core.Provider
	}{
		{"add", []core.Provider{prov("p1", "A", "sk-old"), prov("p2", "B", "sk-two"), prov("p3", "C", "sk-three")}},
		{"update", []core.Provider{prov("p1", "A", "sk-new"), prov("p2", "B", "sk-two")}},
		{"remove", []core.Provider{prov("p1", "A", "sk-old")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			persistErr := errors.New("injected settings persistence failure")
			fs := &fakeSecrets{key: oldBlob, has: true}
			s := NewStore(filepath.Join(t.TempDir(), "settings.json"))
			s.Secrets, s.rename = fs, failingRename(persistErr)

			err := s.Save(&State{Providers: tc.providers, ActiveProviderID: "p1"})
			if !errors.Is(err, persistErr) {
				t.Fatalf("Save error = %v, want persistence failure", err)
			}
			if fs.key != oldBlob || !fs.has {
				t.Fatal("failed persistence did not restore the previous keychain blob")
			}
			if fs.sets != 2 {
				t.Fatalf("keychain sets = %d, want write + rollback", fs.sets)
			}
		})
	}
}

// TestSaveRollsBackNewKeychainEntryOnPersistenceFailure proves an Add from an
// empty keychain deletes the newly-created entry when settings persistence
// fails.
func TestSaveRollsBackNewKeychainEntryOnPersistenceFailure(t *testing.T) {
	persistErr := errors.New("injected settings persistence failure")
	fs := &fakeSecrets{}
	s := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	s.Secrets, s.rename = fs, failingRename(persistErr)

	err := s.Save(&State{Providers: []core.Provider{prov("p1", "A", "sk-new")}, ActiveProviderID: "p1"})
	if !errors.Is(err, persistErr) {
		t.Fatalf("Save error = %v, want persistence failure", err)
	}
	if fs.has || fs.deletes != 1 {
		t.Fatal("failed persistence did not remove the newly-created keychain entry")
	}
}

// TestSaveSurfacesRollbackFailure verifies both failures are observable and
// that returned error text does not contain key material.
func TestSaveSurfacesRollbackFailure(t *testing.T) {
	persistErr := errors.New("injected settings persistence failure")
	rollbackErr := errors.New("injected keychain rollback failure")
	fs := &fakeSecrets{
		key: `{"keys":{"p1":"sk-old"}}`, has: true,
		setErrAt: map[int]error{2: rollbackErr},
	}
	s := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	s.Secrets, s.rename = fs, failingRename(persistErr)

	err := s.Save(&State{Providers: []core.Provider{prov("p1", "A", "sk-new")}, ActiveProviderID: "p1"})
	if !errors.Is(err, persistErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("Save error = %v, want persistence and rollback failures", err)
	}
	if !strings.Contains(err.Error(), "keychain rollback failed") {
		t.Fatalf("Save error lacks rollback context: %v", err)
	}
	if strings.Contains(err.Error(), "sk-old") || strings.Contains(err.Error(), "sk-new") {
		t.Fatal("Save error exposed api key material")
	}
}

// TestSaveRemoveLastRollsBackKeychainDelete proves Remove of the final
// provider restores the old blob when the settings file cannot be persisted.
func TestSaveRemoveLastRollsBackKeychainDelete(t *testing.T) {
	persistErr := errors.New("injected settings persistence failure")
	oldBlob := `{"keys":{"p1":"sk-old"}}`
	fs := &fakeSecrets{key: oldBlob, has: true}
	s := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	s.Secrets, s.rename = fs, failingRename(persistErr)

	err := s.Save(&State{})
	if !errors.Is(err, persistErr) {
		t.Fatalf("Save error = %v, want persistence failure", err)
	}
	if fs.deletes != 1 || fs.sets != 1 || !fs.has || fs.key != oldBlob {
		t.Fatal("failed final-provider removal did not restore the old keychain blob")
	}
}
