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

func TestLoadMissingReturnsEmpty(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nope", "settings.json"))
	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if st == nil || st.ActiveProfile != nil {
		t.Fatalf("expected empty state, got %+v", st)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	s := NewStore(path)

	in := &State{
		ActiveProfile: &core.Profile{
			Label: "work", APIKey: "sk-secret", BaseURL: "https://h", Model: "m", SmallFastModel: "sf",
		},
	}

	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ActiveProfile == nil || !reflect.DeepEqual(*out.ActiveProfile, *in.ActiveProfile) {
		t.Fatalf("profile round-trip mismatch: %+v vs %+v", out.ActiveProfile, in.ActiveProfile)
	}
}

func TestMCPFieldsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	in := &State{MCPKey: "sk-mcp-secret", MCPEndpoint: "https://custom.example/mcp"}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.MCPKey != in.MCPKey || out.MCPEndpoint != in.MCPEndpoint {
		t.Fatalf("mcp fields round-trip mismatch: %+v", out)
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

// TestLoadIgnoresLegacyFields proves a settings file written by an older
// version that still carried the removed custom_tools field loads without
// error and keeps every remaining field intact (unknown JSON keys are ignored).
func TestLoadIgnoresLegacyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := `{
  "active_profile": {"label": "work", "api_key": "sk-secret", "base_url": "https://h", "model": "m"},
  "custom_tools": [
    {"id": "acme-cli", "name": "Acme CLI", "config_path": "~/.config/acme/config.json", "template": "{}"}
  ]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy settings: %v", err)
	}
	out, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load legacy settings: %v", err)
	}
	want := core.Profile{Label: "work", APIKey: "sk-secret", BaseURL: "https://h", Model: "m"}
	if out.ActiveProfile == nil || !reflect.DeepEqual(*out.ActiveProfile, want) {
		t.Fatalf("active profile not preserved from legacy file: %+v", out.ActiveProfile)
	}
}

// TestSaveLoadToolModelsRoundTrip proves the per-tool model selections persist
// alongside the active profile and survive a save+reload unchanged.
func TestSaveLoadToolModelsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	s := NewStore(path)

	in := &State{
		ActiveProfile: &core.Profile{
			Label: "work", APIKey: "sk-secret", BaseURL: "https://h", Model: "m",
			Models: []string{"m", "m2"},
		},
		ToolModels: map[string]string{
			"claude-code": "m2",
			"codex":       "m",
		},
	}

	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(out.ToolModels, in.ToolModels) {
		t.Fatalf("tool models round-trip mismatch:\n got %+v\nwant %+v", out.ToolModels, in.ToolModels)
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
	if st.ActiveProfile != nil {
		t.Fatal("expected empty state from nil save")
	}
}

// fakeSecrets is an in-memory SecretStore. A non-nil setErr makes Set fail,
// simulating an unavailable keychain (e.g. headless Linux).
type fakeSecrets struct {
	key    string
	has    bool
	sets   int
	setErr error
}

func (f *fakeSecrets) Get() (string, bool, error) { return f.key, f.has, nil }
func (f *fakeSecrets) Set(v string) error {
	f.sets++
	if f.setErr != nil {
		return f.setErr
	}
	f.key, f.has = v, true
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
		t.Fatal("settings file still contains the api_key value")
	}
}

// TestSaveMovesAPIKeyToSecrets proves Save routes the profile key to the
// SecretStore, keeps it out of the file, does not mutate the caller's state,
// and Load returns a State with the key filled back in from the keychain.
func TestSaveMovesAPIKeyToSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	fs := &fakeSecrets{}
	s.Secrets = fs

	in := &State{ActiveProfile: &core.Profile{
		Label: "work", APIKey: "sk-secret", BaseURL: "https://h", Model: "m",
	}}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if in.ActiveProfile.APIKey != "sk-secret" {
		t.Fatal("Save mutated the caller's state")
	}
	if fs.key != "sk-secret" {
		t.Fatalf("secret store key = %q, want sk-secret", fs.key)
	}
	mustNotContain(t, path, "sk-secret")

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ActiveProfile == nil || out.ActiveProfile.APIKey != "sk-secret" {
		t.Fatalf("Load did not restore key from secrets: %+v", out.ActiveProfile)
	}
}

// TestMigrateAPIKey proves the startup migration: a key sitting in the file
// moves into the SecretStore and the file is rewritten without it, and a
// second run is an idempotent no-op (no extra keychain writes).
func TestMigrateAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := NewStore(path).Save(&State{ActiveProfile: &core.Profile{
		Label: "work", APIKey: "sk-secret", BaseURL: "https://h", Model: "m",
	}}); err != nil {
		t.Fatalf("seed plaintext settings: %v", err)
	}

	s := NewStore(path)
	fs := &fakeSecrets{}
	s.Secrets = fs
	if err := s.MigrateAPIKey(); err != nil {
		t.Fatalf("MigrateAPIKey: %v", err)
	}
	if fs.key != "sk-secret" {
		t.Fatalf("secret store key = %q, want sk-secret", fs.key)
	}
	mustNotContain(t, path, "sk-secret")

	// Second run: file carries no key, so nothing is written again.
	if err := s.MigrateAPIKey(); err != nil {
		t.Fatalf("MigrateAPIKey (2nd run): %v", err)
	}
	if fs.sets != 1 {
		t.Fatalf("keychain writes = %d, want 1 (idempotent)", fs.sets)
	}
	mustNotContain(t, path, "sk-secret")

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load after migration: %v", err)
	}
	if out.ActiveProfile == nil || out.ActiveProfile.APIKey != "sk-secret" {
		t.Fatalf("key not readable after migration: %+v", out.ActiveProfile)
	}
}

// TestSaveKeepsKeyInFileWhenKeychainFails proves the fallback: when the
// SecretStore write fails, the key stays in the file exactly as before (never
// lost) and a plain round-trip still works.
func TestSaveKeepsKeyInFileWhenKeychainFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	s.Secrets = &fakeSecrets{setErr: errors.New("no secret service")}

	in := &State{ActiveProfile: &core.Profile{
		Label: "work", APIKey: "sk-secret", BaseURL: "https://h", Model: "m",
	}}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "sk-secret") {
		t.Fatal("fallback should keep the api_key in the file when the keychain fails")
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ActiveProfile == nil || out.ActiveProfile.APIKey != "sk-secret" {
		t.Fatalf("key lost on keychain failure: %+v", out.ActiveProfile)
	}
	if err := s.MigrateAPIKey(); err != nil {
		t.Fatalf("MigrateAPIKey with failing keychain: %v", err)
	}
	mustContainAfterFailedMigration(t, path)
}

// mustContainAfterFailedMigration asserts the key survived a migration whose
// keychain write failed.
func mustContainAfterFailedMigration(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "sk-secret") {
		t.Fatal("failed migration must leave the api_key in the file")
	}
}
