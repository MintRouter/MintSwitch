package factorydroid

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

func newTestAdapter(t *testing.T) (*Adapter, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	e := backup.NewEngine(r.BackupsDir())
	a := New(r, e)
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return a, r
}

// TestDetectViaPATHBinary proves a fresh "npm install -g" is detected via the
// "droid" binary on PATH even before ~/.factory or settings.json exist.
func TestDetectViaPATHBinary(t *testing.T) {
	a, _ := newTestAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed with empty home and no binary")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	installed, path := a.Detect()
	if !installed {
		t.Fatal("expected installed via droid binary on PATH")
	}
	if filepath.Base(path) != "settings.json" {
		t.Fatalf("Detect() path = %q, want settings.json", path)
	}
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:   "work",
		APIKey:  "sk-secret-123",
		BaseURL: "https://router.example.com/v1",
		Model:   "mintrouter/gpt-5",
	}
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	return m
}

func managedEntry(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	arr, ok := m[customModelsKey].([]any)
	if !ok {
		t.Fatalf("customModels is not an array: %T", m[customModelsKey])
	}
	for _, raw := range arr {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if dn, _ := obj["displayName"].(string); dn == managedDisplayName {
			return obj
		}
	}
	t.Fatalf("managed entry %q not found in customModels", managedDisplayName)
	return nil
}

func TestIDName(t *testing.T) {
	a, _ := newTestAdapter(t)
	if a.ID() != "factory-droid" {
		t.Errorf("ID() = %q, want factory-droid", a.ID())
	}
	if a.Name() != "Factory Droid" {
		t.Errorf("Name() = %q, want Factory Droid", a.Name())
	}
	if got := a.ConfigPaths(); len(got) != 1 || filepath.Base(got[0]) != "settings.json" {
		t.Errorf("ConfigPaths() = %v, want one settings.json path", got)
	}
}

// TestDetect proves the binary-based contract: a leftover ~/.factory dir or
// settings.json is NOT an installed signal; only a resolvable "droid" binary is.
func TestDetect(t *testing.T) {
	a, r := newTestAdapter(t)

	// Config dir + settings.json present but binary absent ⇒ NOT installed.
	if err := os.MkdirAll(r.Join(".factory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.Join(".factory", "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if installed, path := a.Detect(); installed {
		t.Fatalf("config present + binary absent must be NOT installed, got installed (path %q)", path)
	}

	// Binary resolvable ⇒ installed; active path is still settings.json.
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	if installed, path := a.Detect(); !installed || filepath.Base(path) != "settings.json" {
		t.Fatalf("Detect() = %v, %q; want installed + settings.json", installed, path)
	}
}

func TestApplyNewFile(t *testing.T) {
	a, r := newTestAdapter(t)
	p := sampleProfile()

	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.ChangedPath != a.settingsPath() {
		t.Errorf("ChangedPath = %q, want %q", res.ChangedPath, a.settingsPath())
	}

	m := readSettings(t, r.Join(".factory", "settings.json"))
	entry := managedEntry(t, m)
	if entry["model"] != p.Model {
		t.Errorf("entry model = %v, want %v", entry["model"], p.Model)
	}
	if entry["baseUrl"] != p.BaseURL {
		t.Errorf("entry baseUrl = %v, want %v", entry["baseUrl"], p.BaseURL)
	}
	if entry["apiKey"] != p.APIKey {
		t.Errorf("entry apiKey not written correctly")
	}
	if entry["provider"] != providerOpenAI {
		t.Errorf("entry provider = %v, want %v", entry["provider"], providerOpenAI)
	}
	if m[defaultModelKey] != p.Model {
		t.Errorf("top-level model = %v, want %v", m[defaultModelKey], p.Model)
	}
	if _, ok := m[core.MarkerKey]; !ok {
		t.Errorf("marker key %q missing", core.MarkerKey)
	}

	// File must be created with 0600 perms (contains the API key).
	fi, err := os.Stat(r.Join(".factory", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("settings.json perm = %o, want 600", perm)
	}
}

func TestApplyPreservesExisting(t *testing.T) {
	a, r := newTestAdapter(t)
	if err := os.MkdirAll(r.Join(".factory"), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := map[string]any{
		"someSetting": "keep-me",
		"customModels": []any{
			map[string]any{
				"model":       "other/model",
				"displayName": "My Other Model",
				"baseUrl":     "https://other.example.com/v1",
				"apiKey":      "other-key",
				"provider":    "generic-chat-completion-api",
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(r.Join(".factory", "settings.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	m := readSettings(t, r.Join(".factory", "settings.json"))
	if m["someSetting"] != "keep-me" {
		t.Errorf("unrelated key not preserved: %v", m["someSetting"])
	}
	arr, ok := m[customModelsKey].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("customModels = %v, want 2 entries (existing + managed)", m[customModelsKey])
	}
	// The pre-existing unrelated entry must still be present.
	var foundOther bool
	for _, raw := range arr {
		if obj, ok := raw.(map[string]any); ok {
			if obj["displayName"] == "My Other Model" {
				foundOther = true
			}
		}
	}
	if !foundOther {
		t.Errorf("pre-existing customModels entry was lost")
	}
	managedEntry(t, m) // ensures managed entry exists
}

func TestApplyIdempotent(t *testing.T) {
	a, r := newTestAdapter(t)
	p := sampleProfile()

	if _, err := a.Apply(p); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	m := readSettings(t, r.Join(".factory", "settings.json"))
	arr, ok := m[customModelsKey].([]any)
	if !ok {
		t.Fatalf("customModels missing")
	}
	count := 0
	for _, raw := range arr {
		if obj, ok := raw.(map[string]any); ok {
			if obj["displayName"] == managedDisplayName {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("re-Apply produced %d managed entries, want 1 (no duplicates)", count)
	}
}

func TestStatus(t *testing.T) {
	p := sampleProfile()

	t.Run("not installed", func(t *testing.T) {
		a, _ := newTestAdapter(t)
		st, _, err := a.Status(p)
		if err != nil {
			t.Fatal(err)
		}
		if st != core.StatusNotInstalled {
			t.Errorf("status = %v, want NotInstalled", st)
		}
	})

	t.Run("default (no marker)", func(t *testing.T) {
		a, r := newTestAdapter(t)
		a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
		if err := os.MkdirAll(r.Join(".factory"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(r.Join(".factory", "settings.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		st, _, err := a.Status(p)
		if err != nil {
			t.Fatal(err)
		}
		if st != core.StatusDefault {
			t.Errorf("status = %v, want Default", st)
		}
	})

	t.Run("applied", func(t *testing.T) {
		a, _ := newTestAdapter(t)
		a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
		if _, err := a.Apply(p); err != nil {
			t.Fatal(err)
		}
		st, _, err := a.Status(p)
		if err != nil {
			t.Fatal(err)
		}
		if st != core.StatusAppliedByMintSwitch {
			t.Errorf("status = %v, want AppliedByMintSwitch", st)
		}
	})

	t.Run("modified externally", func(t *testing.T) {
		a, _ := newTestAdapter(t)
		a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
		if _, err := a.Apply(p); err != nil {
			t.Fatal(err)
		}
		other := p
		other.Model = "different/model"
		st, _, err := a.Status(other)
		if err != nil {
			t.Fatal(err)
		}
		if st != core.StatusModifiedExternally {
			t.Errorf("status = %v, want ModifiedExternally", st)
		}
	})
}

func TestRestoreDeletesCreatedFile(t *testing.T) {
	a, r := newTestAdapter(t)
	path := r.Join(".factory", "settings.json")

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected settings.json after Apply: %v", err)
	}

	if _, err := a.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected settings.json removed after Restore, stat err = %v", err)
	}
}

func TestRestoreRevertsExisting(t *testing.T) {
	a, r := newTestAdapter(t)
	path := r.Join(".factory", "settings.json")
	if err := os.MkdirAll(r.Join(".factory"), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"someSetting\": \"original\"\n}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("restored content = %q, want %q", got, original)
	}
}

func TestRestoreNoBackupNoOp(t *testing.T) {
	a, _ := newTestAdapter(t)
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.BackupPath != "" {
		t.Errorf("expected no backup path, got %q", res.BackupPath)
	}
}
