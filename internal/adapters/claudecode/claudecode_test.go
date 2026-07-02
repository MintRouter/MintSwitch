package claudecode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	a := New(r, backup.NewEngine(r.BackupsDir()), markers.NewStore(r.MarkersPath()))
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return a, r
}

// TestDetectViaPATHBinary proves a fresh "npm install -g" is detected via the
// "claude" binary on PATH even before ~/.claude or settings.json exist.
func TestDetectViaPATHBinary(t *testing.T) {
	a, _ := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed with empty home and no binary")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	installed, path := a.Detect()
	if !installed {
		t.Fatal("expected installed via claude binary on PATH")
	}
	if filepath.Base(path) != "settings.json" {
		t.Fatalf("Detect() path = %q, want settings.json", path)
	}
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

// TestConfigPathsHonorClaudeConfigDir proves the adapter follows the
// documented CLAUDE_CONFIG_DIR override (wired into the resolver by
// paths.NewResolver).
func TestConfigPathsHonorClaudeConfigDir(t *testing.T) {
	a, r := newAdapter(t)
	override := t.TempDir()
	r.ClaudeConfigDir = override
	want := filepath.Join(override, "settings.json")
	if got := a.ConfigPaths(); len(got) != 1 || got[0] != want {
		t.Fatalf("ConfigPaths = %v, want [%q]", got, want)
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
	if a.ID() != "claude-code" || a.Name() != "Claude Code (CLI + IDE)" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
	if got := a.ConfigPaths(); len(got) != 1 {
		t.Fatalf("expected 1 config path, got %v", got)
	}
}

// TestDetect proves the binary-based contract: a leftover ~/.claude dir is NOT
// an installed signal; only a resolvable "claude" binary is.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed on empty home")
	}
	if err := os.MkdirAll(r.Join(".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if installed, _ := a.Detect(); installed {
		t.Fatal("config dir present + binary absent must be NOT installed")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	if installed, _ := a.Detect(); !installed {
		t.Fatal("expected installed once claude binary is resolvable")
	}
}

func TestStatusTransitions(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()

	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}
	// Binary resolvable from here so Status reaches the config-reading branch.
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
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
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
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
	if _, ok := m[core.MarkerKey]; ok {
		t.Fatal("legacy marker must not be written into settings.json")
	}
	marker, ok, err := a.m.Get(id)
	if err != nil || !ok || !marker.Managed {
		t.Fatalf("store marker = %+v ok=%v err=%v, want managed entry", marker, ok, err)
	}
	if marker.Fingerprint != core.Fingerprint(p) {
		t.Fatalf("store fingerprint mismatch: %q", marker.Fingerprint)
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
	if _, ok, _ := a.m.Get(id); ok {
		t.Fatal("store entry must be deleted on Restore")
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
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
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

// writeLegacySettings writes a settings.json carrying the legacy in-file
// marker for profile p plus a user key, mimicking a pre-store MintSwitch apply.
func writeLegacySettings(t *testing.T, a *Adapter, p core.Profile) string {
	t.Helper()
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{
		"theme":        "dark",
		envKey:         map[string]any{envBaseURL: p.BaseURL, envAuthToken: p.APIKey, envModel: p.Model},
		core.MarkerKey: core.NewMarker(p, p.Label),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStatusDefaultWhenEnvRemoved proves a store entry alone does not report
// Applied: when the managed env block was removed from the file (e.g. Claude
// wiped an invalid settings.json), Status falls back to Default.
func TestStatusDefaultWhenEnvRemoved(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.WriteFile(a.settingsPath(), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default when managed env block is gone, got %v", st)
	}
}

// TestApplyStripsLegacyMarker proves an Apply over a legacy-marker file removes
// the key in the same write and records the fresh marker in the store, without
// snapshotting the managed file (backup gate honors the legacy marker).
func TestApplyStripsLegacyMarker(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	p := sampleProfile()
	path := writeLegacySettings(t, a, p)

	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("legacy-managed file must not be backed up, got %q", res.BackupPath)
	}
	m := readSettings(t, path)
	if _, ok := m[core.MarkerKey]; ok {
		t.Fatalf("legacy marker not stripped on Apply: %+v", m)
	}
	if m["theme"] != "dark" {
		t.Fatalf("user key lost: %+v", m)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want AppliedByMintSwitch, got %v", st)
	}
}

// TestStripLegacyMarkerMigrates is the startup-sweep case: a file carrying the
// legacy marker gets it removed and migrated into the store even though the
// user never pressed Apply.
func TestStripLegacyMarkerMigrates(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	path := writeLegacySettings(t, a, p)

	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip: %v", err)
	}
	m := readSettings(t, path)
	if _, ok := m[core.MarkerKey]; ok {
		t.Fatalf("legacy marker still in file: %+v", m)
	}
	if m["theme"] != "dark" {
		t.Fatalf("user key lost: %+v", m)
	}
	env := envOf(t, m)
	if env[envBaseURL] != p.BaseURL {
		t.Fatalf("env block lost: %+v", env)
	}
	marker, ok, err := a.m.Get(id)
	if err != nil || !ok {
		t.Fatalf("store entry after migrate = ok=%v err=%v", ok, err)
	}
	if marker.Fingerprint != core.Fingerprint(p) {
		t.Fatalf("migrated fingerprint mismatch: %q", marker.Fingerprint)
	}
}

// TestStripLegacyMarkerKeepsExistingStoreEntry proves the sweep never
// overwrites a marker already recorded in the store (the store is newer truth).
func TestStripLegacyMarkerKeepsExistingStoreEntry(t *testing.T) {
	a, _ := newAdapter(t)
	stored := core.NewMarker(secondProfile(), "personal")
	if err := a.m.Put(id, stored); err != nil {
		t.Fatal(err)
	}
	writeLegacySettings(t, a, sampleProfile())

	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip: %v", err)
	}
	marker, ok, err := a.m.Get(id)
	if err != nil || !ok {
		t.Fatalf("store entry = ok=%v err=%v", ok, err)
	}
	if marker.Fingerprint != stored.Fingerprint {
		t.Fatalf("store entry overwritten by legacy marker: %+v", marker)
	}
	if m := readSettings(t, a.settingsPath()); m[core.MarkerKey] != nil {
		t.Fatal("legacy marker still in file")
	}
}

// TestStripLegacyMarkerNoOp proves the sweep neither creates a missing file nor
// rewrites a clean one.
func TestStripLegacyMarkerNoOp(t *testing.T) {
	a, _ := newAdapter(t)
	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip on missing file: %v", err)
	}
	if _, err := os.Stat(a.settingsPath()); !os.IsNotExist(err) {
		t.Fatalf("strip must not create settings.json, stat err=%v", err)
	}

	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := []byte(`{"theme": "dark"}`)
	if err := os.WriteFile(path, clean, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip on clean file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(clean) {
		t.Fatalf("clean file rewritten: %q", got)
	}
}
