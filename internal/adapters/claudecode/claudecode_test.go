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

// TestApplyDefaultsEmptySmallFastModel proves an empty SmallFastModel is
// written as the profile's main model: without the key, Claude Code's
// background requests fall back to its default Haiku model, which fails on
// gateways that do not serve it.
func TestApplyDefaultsEmptySmallFastModel(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	p.SmallFastModel = ""
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	env := envOf(t, readSettings(t, res.ChangedPath))
	if env[envSmallFastModel] != p.Model {
		t.Fatalf("small fast model should default to main model %q: %+v", p.Model, env)
	}
}

// TestApplyStripsV1Suffix proves exactly one trailing "/v1" path segment is
// stripped from the base URL before the write (Claude Code appends
// "/v1/messages" itself), while all other URLs are written verbatim.
func TestApplyStripsV1Suffix(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{"strips trailing /v1", "https://api.mintrouter.ai/v1", "https://api.mintrouter.ai"},
		{"strips trailing /v1 with slash", "https://api.mintrouter.ai/v1/", "https://api.mintrouter.ai"},
		{"strips only last /v1 segment", "https://host/api/v1", "https://host/api"},
		{"no suffix unchanged", "https://api.mintrouter.ai", "https://api.mintrouter.ai"},
		{"v1beta unchanged", "https://host/v1beta", "https://host/v1beta"},
		{"mid-path v1 unchanged", "https://host/v1/proxy", "https://host/v1/proxy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAdapter(t)
			p := sampleProfile()
			p.BaseURL = tc.baseURL
			res, err := a.Apply(p)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			env := envOf(t, readSettings(t, res.ChangedPath))
			if env[envBaseURL] != tc.want {
				t.Fatalf("base url = %q, want %q", env[envBaseURL], tc.want)
			}
			// Fingerprint stays derived from the original profile value.
			marker, ok, err := a.m.Get(id)
			if err != nil || !ok {
				t.Fatalf("store marker ok=%v err=%v", ok, err)
			}
			if marker.Fingerprint != core.Fingerprint(p) {
				t.Fatalf("fingerprint must come from unmodified profile: %q", marker.Fingerprint)
			}
		})
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

// TestRestoreIgnoresContaminatedNewerBackup is the regression for dirty
// backups: a snapshot taken AFTER the file was already MintSwitch-managed
// (e.g. by another component sharing the backup namespace before the split)
// must be ignored — Restore reverts to the oldest, pristine entry and prunes
// every backup so the contaminated one can never resurface.
func TestRestoreIgnoresContaminatedNewerBackup(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := []byte("{\n  \"permissions\": {\n    \"allow\": [\"Bash\"]\n  }\n}\n")
	if err := os.WriteFile(path, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	// Contaminated snapshot of the now-managed file.
	if _, err := a.e.Backup(path); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(orig) {
		t.Fatalf("restore used contaminated backup: %q", got)
	}
	has, err := a.e.HasBackup(path)
	if err != nil || has {
		t.Fatalf("backups must be pruned after restore, HasBackup = %v, %v", has, err)
	}
}

// TestRestoreNoBackupStripsManagedKeys is the regression for the missing
// backup fallback: with the backups dir deleted, Restore must surgically strip
// the MintSwitch-managed env keys while preserving the user's own settings.
func TestRestoreNoBackupStripsManagedKeys(t *testing.T) {
	a, r := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"permissions":{"allow":["Bash"]},"env":{"FOO":"bar"}}`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(r.BackupsDir()); err != nil {
		t.Fatal(err)
	}

	res, err := a.Restore()
	if err != nil {
		t.Fatal(err)
	}
	m, err := core.ReadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	env := core.AsJSONObject(m[envKey])
	for _, k := range []string{envBaseURL, envAuthToken, envModel, envSmallFastModel} {
		if _, present := env[k]; present {
			t.Fatalf("managed key %s must be stripped: %v", k, env)
		}
	}
	if env["FOO"] != "bar" {
		t.Fatalf("user env key must be preserved: %v", env)
	}
	if _, present := m["permissions"]; !present {
		t.Fatalf("user settings must be preserved: %v", m)
	}
	want := "No backup found; removed the MintSwitch-managed env keys from Claude Code settings.json."
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after strip = %v, want Default", st)
	}
}

// TestOrphanStatusAndRestoreWithBackup is the regression for the lost-marker
// gap: with the sidecar marker gone but the pristine backup intact, Status
// must report ModifiedExternally (so the UI offers Restore instead of treating
// the tool as never applied) and Restore must still revert byte-for-byte.
func TestOrphanStatusAndRestoreWithBackup(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"permissions":{"allow":["Bash(ls:*)"]},"theme":"dark"}` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Simulate the lost marker (e.g. an interrupted earlier restore).
	if err := a.m.Delete(id); err != nil {
		t.Fatal(err)
	}

	st, detail, err := a.Status(sampleProfile())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != core.StatusModifiedExternally || detail != orphanDetail {
		t.Fatalf("orphan status = %v %q, want ModifiedExternally + orphanDetail", st, detail)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("not byte-for-byte restored: %q", got)
	}
}

// TestOrphanRestoreNoBackupStrips covers the orphan-no-backup branch: marker
// gone AND backups gone, but the file still carries the full MintSwitch env
// signature — Restore must strip the managed env keys while preserving the
// user's own settings, and Status must offer Restore beforehand.
func TestOrphanRestoreNoBackupStrips(t *testing.T) {
	a, r := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.RemoveAll(r.BackupsDir()); err != nil {
		t.Fatal(err)
	}
	if err := a.m.Delete(id); err != nil {
		t.Fatal(err)
	}

	if st, _, _ := a.Status(sampleProfile()); st != core.StatusModifiedExternally {
		t.Fatalf("orphan status = %v, want ModifiedExternally", st)
	}
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	want := "No backup found; removed the MintSwitch-managed env keys from Claude Code settings.json."
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	m := readSettings(t, path)
	if _, present := m[envKey]; present {
		t.Fatalf("env must be stripped: %v", m)
	}
	if m["theme"] != "dark" {
		t.Fatalf("user settings must be preserved: %v", m)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after strip = %v, want Default", st)
	}
}

// TestPureUserConfigNeverOrphan proves the no-false-positive contract: a
// hand-written settings.json that never saw Apply — even one pointing
// ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN at the user's own gateway —
// stays Default (no Restore button) and Restore leaves it byte-for-byte
// untouched.
func TestPureUserConfigNeverOrphan(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	user := `{"env":{"ANTHROPIC_BASE_URL":"https://my-gateway.example.com","ANTHROPIC_AUTH_TOKEN":"sk-user"},"theme":"dark"}`
	if err := os.WriteFile(path, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}

	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("pure user config status = %v, want Default", st)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != user {
		t.Fatalf("pure user config rewritten: %q", got)
	}
}
