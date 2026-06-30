package pi

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

func newAdapter(t *testing.T) (*Adapter, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	a := New(r, backup.NewEngine(r.BackupsDir()))
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return a, r
}

// TestDetectViaPATHBinary proves a fresh "npm install -g" is detected via the
// "pi" binary on PATH even before ~/.pi exists.
func TestDetectViaPATHBinary(t *testing.T) {
	a, _ := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed with empty home and no binary")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/pi", nil }
	installed, path := a.Detect()
	if !installed {
		t.Fatal("expected installed via pi binary on PATH")
	}
	if filepath.Base(path) != "models.json" {
		t.Fatalf("Detect() path = %q, want models.json", path)
	}
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:          "work",
		APIKey:         "sk-secret-token",
		BaseURL:        "https://gateway.example.com/v1",
		Model:          "anthropic/claude-opus-4-8",
		SmallFastModel: "anthropic/claude-haiku-4-5",
	}
}

func readModels(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal models.json: %v", err)
	}
	return m
}

func providerOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	providers, ok := m[providersKey].(map[string]any)
	if !ok {
		t.Fatalf("providers object missing: %+v", m)
	}
	prov, ok := providers[providerKey].(map[string]any)
	if !ok {
		t.Fatalf("mintswitch provider missing: %+v", providers)
	}
	return prov
}

func TestIDAndName(t *testing.T) {
	a, _ := newAdapter(t)
	if a.ID() != "pi" || a.Name() != "Pi (earendil-works)" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
	if got := a.ConfigPaths(); len(got) != 1 {
		t.Fatalf("expected 1 config path, got %v", got)
	}
}

// TestDetect proves the binary-based contract: a leftover ~/.pi dir is NOT an
// installed signal; only a resolvable "pi" binary is.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed on empty home")
	}
	if err := os.MkdirAll(r.Join(".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if installed, _ := a.Detect(); installed {
		t.Fatal("~/.pi present + binary absent must be NOT installed")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/pi", nil }
	if installed, _ := a.Detect(); !installed {
		t.Fatal("expected installed once pi binary is resolvable")
	}
}

func TestStatusTransitions(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()

	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}
	// Binary resolvable from here so Status reaches the config-reading branch.
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/pi", nil }
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want AppliedByMintSwitch, got %v", st)
	}
	p2 := p
	p2.Model = "other-model"
	if st, _, _ := a.Status(p2); st != core.StatusModifiedExternally {
		t.Fatalf("want ModifiedExternally, got %v", st)
	}
}

func TestStatusDefaultWhenNoMarker(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/pi", nil }
	path := a.modelsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("want Default, got %v", st)
	}
}

func TestApplyNewFile(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	prov := providerOf(t, readModels(t, res.ChangedPath))
	if prov[keyBaseURL] != p.BaseURL || prov[keyAPIKey] != p.APIKey ||
		prov[keyAPI] != apiType || prov[keyAuthHeader] != true {
		t.Fatalf("provider not configured correctly: %+v", prov)
	}
	models, ok := prov[keyModels].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("expected one model entry, got %+v", prov[keyModels])
	}
	first, _ := models[0].(map[string]any)
	if first[keyID] != p.Model {
		t.Fatalf("model id not set: %+v", first)
	}
}

func TestApplyPreservesExistingKeys(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.modelsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"theme":"dark","providers":{"ollama":{"baseUrl":"http://localhost:11434/v1","api":"openai-completions","models":[{"id":"llama3.1:8b"}]}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	m := readModels(t, path)
	if m["theme"] != "dark" {
		t.Fatalf("unrelated top-level key lost: %+v", m)
	}
	providers, _ := m[providersKey].(map[string]any)
	if _, ok := providers["ollama"]; !ok {
		t.Fatalf("existing provider lost: %+v", providers)
	}
	prov := providerOf(t, m)
	if prov[keyBaseURL] != sampleProfile().BaseURL {
		t.Fatalf("base url not injected: %+v", prov)
	}
}

func TestRestoreRevertsExistingFile(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.modelsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"providers":{"ollama":{"baseUrl":"http://localhost:11434/v1"}}}` + "\n")
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
		t.Fatalf("read after restore: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("file not restored byte-for-byte: %q", got)
	}
}

func TestRestoreDeletesCreatedFile(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.modelsPath()
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file created by apply: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected created file removed, stat err=%v", err)
	}
}

func TestRestoreNoBackupIsNoOp(t *testing.T) {
	a, _ := newAdapter(t)
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore no-op: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected empty backup path, got %q", res.BackupPath)
	}
}

func TestApplyIdempotent(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/pi", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	st, _, err := a.Status(p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want AppliedByMintSwitch after re-apply, got %v", st)
	}
	prov := providerOf(t, readModels(t, a.modelsPath()))
	models, _ := prov[keyModels].([]any)
	if len(models) != 1 {
		t.Fatalf("model entry duplicated after re-apply: %+v", models)
	}
}

func TestApplyInvalidProfile(t *testing.T) {
	a, _ := newAdapter(t)
	if _, err := a.Apply(core.Profile{}); err == nil {
		t.Fatal("expected validation error for empty profile")
	}
}
