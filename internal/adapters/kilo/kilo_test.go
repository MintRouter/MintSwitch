package kilo

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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIDName(t *testing.T) {
	a, _ := newAdapter(t)
	if a.ID() != "kilo" || a.Name() != "Kilo Code" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
}

// TestDetect proves the binary-based contract: a leftover global config dir is
// NOT an installed signal; only a resolvable "kilo" binary is.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed with empty home")
	}
	if err := os.MkdirAll(filepath.Dir(a.jsonPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, _ := a.Detect(); installed {
		t.Fatal("config dir present + binary absent must be NOT installed")
	}

	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	if installed, path := a.Detect(); !installed || path != r.ConfigJoin("kilo", "kilo.json") {
		t.Fatalf("expected installed via PATH binary, got %v %q", installed, path)
	}
}

// TestConfigPathResolution proves the active-file contract: kilo.jsonc wins
// when present (it overrides kilo.json in Kilo's merge order), kilo.json is
// used when it is the only file, and kilo.json is the default when neither
// exists.
func TestConfigPathResolution(t *testing.T) {
	a, _ := newAdapter(t)
	if got := a.configPath(); got != a.jsonPath() {
		t.Fatalf("neither file: configPath = %q, want %q", got, a.jsonPath())
	}
	writeFile(t, a.jsonPath(), `{}`)
	if got := a.configPath(); got != a.jsonPath() {
		t.Fatalf("only kilo.json: configPath = %q, want %q", got, a.jsonPath())
	}
	writeFile(t, a.jsoncPath(), `{}`)
	if got := a.configPath(); got != a.jsoncPath() {
		t.Fatalf("both files: configPath = %q, want %q", got, a.jsoncPath())
	}
	if err := os.Remove(a.jsonPath()); err != nil {
		t.Fatal(err)
	}
	if got := a.configPath(); got != a.jsoncPath() {
		t.Fatalf("only kilo.jsonc: configPath = %q, want %q", got, a.jsoncPath())
	}
	if got := a.ConfigPaths(); len(got) != 2 {
		t.Fatalf("ConfigPaths = %v, want both candidates", got)
	}
}

func TestApplyNewFileAndStatus(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("expected NotInstalled, got %v", st)
	}
	// Binary resolvable from here so Status reaches the config-reading branch.
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ChangedPath != a.jsonPath() {
		t.Fatalf("expected kilo.json created, got %q", res.ChangedPath)
	}
	root := readJSON(t, res.ChangedPath)
	if root["model"] != "openai-compatible/gpt-mint" {
		t.Fatalf("model = %v", root["model"])
	}
	prov := root["provider"].(map[string]any)[providerID].(map[string]any)
	if _, ok := prov["npm"]; ok {
		t.Fatalf("built-in provider must not carry npm key: %v", prov)
	}
	opts := prov["options"].(map[string]any)
	if opts["baseURL"] != p.BaseURL || opts["apiKey"] != p.APIKey {
		t.Fatalf("options wrong: %v", opts)
	}
	models := prov["models"].(map[string]any)
	if _, ok := models[p.Model]; !ok {
		t.Fatalf("model entry missing: %v", models)
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
	path := a.jsonPath()
	existing := `{"$schema":"https://app.kilo.ai/config.json","autoupdate":false,` +
		`"provider":{"anthropic":{"options":{"baseURL":"https://api.anthropic.com"}}}}`
	writeFile(t, path, existing)
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, path)
	if root["$schema"] != "https://app.kilo.ai/config.json" || root["autoupdate"] != false {
		t.Fatalf("unrelated top-level keys not preserved: %v", root)
	}
	prov := root["provider"].(map[string]any)
	if _, ok := prov["anthropic"]; !ok {
		t.Fatalf("existing provider not preserved: %v", prov)
	}
	if _, ok := prov[providerID]; !ok {
		t.Fatalf("openai-compatible provider missing: %v", prov)
	}
}

func TestRestoreDeletesCreatedFile(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.jsonPath()
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
	path := a.jsonPath()
	original := `{"autoupdate":true}` + "\n"
	writeFile(t, path, original)
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
	if string(got) != original {
		t.Fatalf("not byte-for-byte restored: %q", got)
	}
}

func TestReApplyIdempotent(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply1: %v", err)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply2: %v", err)
	}
	root := readJSON(t, a.jsonPath())
	prov := root["provider"].(map[string]any)
	if len(prov) != 1 {
		t.Fatalf("expected single provider after re-apply, got %v", prov)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
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
