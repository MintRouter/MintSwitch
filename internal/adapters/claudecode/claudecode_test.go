package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mintconfig/internal/backup"
	"mintconfig/internal/core"
	"mintconfig/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	return New(r, backup.NewEngine(r.BackupsDir())), r
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:          "work",
		APIKey:         "sk-secret-token",
		BaseURL:        "https://gateway.example.com",
		Model:          "anthropic/claude-opus-4-8",
		SmallFastModel: "anthropic/claude-haiku-4-5",
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

func envOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	env, ok := m[envKey].(map[string]any)
	if !ok {
		t.Fatalf("env object missing: %+v", m)
	}
	return env
}

func TestIDAndName(t *testing.T) {
	a, _ := newAdapter(t)
	if a.ID() != "claude-code" || a.Name() != "Claude Code" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
	if got := a.ConfigPaths(); len(got) != 1 {
		t.Fatalf("expected 1 config path, got %v", got)
	}
}

func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed on empty home")
	}
	if err := os.MkdirAll(r.Join(".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if installed, _ := a.Detect(); !installed {
		t.Fatal("expected installed once .claude dir exists")
	}
}

func TestStatusTransitions(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()

	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintConfig {
		t.Fatalf("want AppliedByMintConfig, got %v", st)
	}
	p2 := p
	p2.Model = "other-model"
	if st, _, _ := a.Status(p2); st != core.StatusModifiedExternally {
		t.Fatalf("want ModifiedExternally, got %v", st)
	}
}

func TestStatusDefaultWhenNoMarker(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
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
	m := readSettings(t, res.ChangedPath)
	env := envOf(t, m)
	if env[envBaseURL] != p.BaseURL || env[envAuthToken] != p.APIKey ||
		env[envModel] != p.Model || env[envSmallFastModel] != p.SmallFastModel {
		t.Fatalf("env not injected correctly: %+v", env)
	}
	if _, ok := m[core.MarkerKey]; !ok {
		t.Fatal("marker missing")
	}
}

func TestApplyOmitsEmptySmallFastModel(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	p.SmallFastModel = ""
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	env := envOf(t, readSettings(t, res.ChangedPath))
	if _, ok := env[envSmallFastModel]; ok {
		t.Fatalf("small fast model should be omitted: %+v", env)
	}
}

func TestApplyPreservesExistingKeys(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"theme":"dark","env":{"MCP_TIMEOUT":"10000"},"permissions":{"allow":["Read"]}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	m := readSettings(t, path)
	if m["theme"] != "dark" {
		t.Fatalf("unrelated top-level key lost: %+v", m)
	}
	if _, ok := m["permissions"]; !ok {
		t.Fatal("permissions section lost")
	}
	env := envOf(t, m)
	if env["MCP_TIMEOUT"] != "10000" {
		t.Fatalf("unrelated env key lost: %+v", env)
	}
	if env[envBaseURL] != sampleProfile().BaseURL {
		t.Fatalf("base url not injected: %+v", env)
	}
}

func TestRestoreRevertsExistingFile(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"theme":"light"}` + "\n")
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
	path := a.settingsPath()
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
	if st != core.StatusAppliedByMintConfig {
		t.Fatalf("want AppliedByMintConfig after re-apply, got %v", st)
	}
	env := envOf(t, readSettings(t, a.settingsPath()))
	if env[envBaseURL] != p.BaseURL || env[envModel] != p.Model {
		t.Fatalf("env drifted after re-apply: %+v", env)
	}
}

func TestApplyInvalidProfile(t *testing.T) {
	a, _ := newAdapter(t)
	if _, err := a.Apply(core.Profile{}); err == nil {
		t.Fatal("expected validation error for empty profile")
	}
}
