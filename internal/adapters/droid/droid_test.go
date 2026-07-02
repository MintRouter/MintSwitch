package droid

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	data := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: data}
	a := New(r, backup.NewEngine(r.BackupsDir()))
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return a, r
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:   "work",
		APIKey:  "sk-secret-123",
		BaseURL: "https://router.example.com/v1",
		Model:   "gpt-mint",
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// ourEntry returns the MintSwitch-owned customModels entry from settings.json.
func ourEntry(t *testing.T, path string) map[string]any {
	t.Helper()
	root := readJSON(t, path)
	models, ok := root[customModelsKey].([]any)
	if !ok {
		t.Fatalf("customModels missing: %+v", root)
	}
	for _, v := range models {
		if obj, ok := v.(map[string]any); ok && obj["displayName"] == entryDisplayName {
			return obj
		}
	}
	t.Fatalf("MintSwitch entry missing: %+v", models)
	return nil
}

func TestIDName(t *testing.T) {
	a, _ := newAdapter(t)
	if a.ID() != "droid" || a.Name() != "Factory Droid" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
}

// TestDetect proves the contract: a resolvable "droid" binary OR an existing
// ~/.factory directory is an installed signal.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed with empty home")
	}
	if err := os.MkdirAll(r.Join(".factory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, path := a.Detect(); !installed || path != r.Join(".factory", "settings.json") {
		t.Fatalf("expected installed via ~/.factory dir, got %v %q", installed, path)
	}
	if err := os.RemoveAll(r.Join(".factory")); err != nil {
		t.Fatal(err)
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	if installed, _ := a.Detect(); !installed {
		t.Fatal("expected installed via PATH binary")
	}
}

func TestApplyNewFileAndStatus(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("expected NotInstalled, got %v", st)
	}
	// Binary resolvable from here so Status reaches the config-reading branch.
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, res.ChangedPath)
	if root["model"] != p.Model {
		t.Fatalf("model = %v", root["model"])
	}
	e := ourEntry(t, res.ChangedPath)
	if e["model"] != p.Model || e["baseUrl"] != p.BaseURL || e["apiKey"] != p.APIKey {
		t.Fatalf("entry wrong: %+v", e)
	}
	if e["provider"] != providerType {
		t.Fatalf("provider = %v", e["provider"])
	}
	if tok, _ := e["maxOutputTokens"].(float64); int(tok) != defaultMaxOutputTokens {
		t.Fatalf("maxOutputTokens = %v", e["maxOutputTokens"])
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch, got %v", st)
	}
	other := sampleProfile()
	other.Model = "different"
	if st, _, _ := a.Status(other); st != core.StatusModifiedExternally {
		t.Fatalf("expected ModifiedExternally, got %v", st)
	}
}

func TestApplyPreservesExistingKeys(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{"theme":"dark","customModels":[{"model":"llama","displayName":"Mine",` +
		`"baseUrl":"https://x/v1","apiKey":"k","provider":"generic-chat-completion-api","maxOutputTokens":8192}]}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, path)
	if root["theme"] != "dark" {
		t.Fatalf("unrelated top-level keys not preserved: %v", root)
	}
	models := root[customModelsKey].([]any)
	if len(models) != 2 {
		t.Fatalf("expected user entry + ours, got %v", models)
	}
	if first := models[0].(map[string]any); first["displayName"] != "Mine" {
		t.Fatalf("existing entry not preserved: %v", first)
	}
}

func TestRestoreDeletesCreatedFile(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file created: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, got err=%v", err)
	}
}

func TestRestoreRevertsExisting(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"model":"factory-default"}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("not byte-for-byte restored: %q", got)
	}
}

func TestReApplyIdempotent(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply1: %v", err)
	}
	p2 := p
	p2.Model = "gpt-mint-2"
	if _, err := a.Apply(p2); err != nil {
		t.Fatalf("apply2: %v", err)
	}
	root := readJSON(t, a.configPath())
	models := root[customModelsKey].([]any)
	if len(models) != 1 {
		t.Fatalf("expected single entry after re-apply, got %v", models)
	}
	if e := ourEntry(t, a.configPath()); e["model"] != "gpt-mint-2" {
		t.Fatalf("entry not updated: %+v", e)
	}
	if root["model"] != "gpt-mint-2" {
		t.Fatalf("top-level model not updated: %v", root["model"])
	}
	if st, _, _ := a.Status(p2); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch after re-apply, got %v", st)
	}
}

func TestRestoreNoBackupNoOp(t *testing.T) {
	a, _ := newAdapter(t)
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected no backup path, got %q", res.BackupPath)
	}
}

// TestApplyErrorNeverContainsKey proves validation errors are safe to log: the
// secret API key never appears in the error message.
func TestApplyErrorNeverContainsKey(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	p.Model = ""
	_, err := a.Apply(p)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if strings.Contains(err.Error(), p.APIKey) {
		t.Fatalf("error leaks API key: %v", err)
	}
}
