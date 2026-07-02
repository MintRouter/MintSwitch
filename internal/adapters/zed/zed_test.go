package zed

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	data := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: data}
	a := New(r, backup.NewEngine(r.BackupsDir()), markers.NewStore(r.MarkersPath()))
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	a.appBundles = []string{r.Join("Applications", "Zed.app")}
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

func TestIDName(t *testing.T) {
	a, _ := newAdapter(t)
	if a.ID() != "zed" || a.Name() != "Zed" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
}

// TestConfigPathUsesResolverZedDir proves the adapter derives settings.json
// from the resolver's per-OS ZedConfigDir (%APPDATA%\Zed on Windows,
// ~/.config/zed elsewhere; the per-GOOS branches are covered in
// internal/paths).
func TestConfigPathUsesResolverZedDir(t *testing.T) {
	a, r := newAdapter(t)
	want := filepath.Join(r.ZedConfigDir(), "settings.json")
	if got := a.ConfigPaths(); len(got) != 1 || got[0] != want {
		t.Fatalf("ConfigPaths = %v, want [%q]", got, want)
	}
}

// TestDetect proves the contract: a leftover settings dir is NOT an installed
// signal; only a resolvable "zed" binary or a Zed.app bundle is.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed with empty home")
	}
	if err := os.MkdirAll(filepath.Dir(a.configPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, _ := a.Detect(); installed {
		t.Fatal("settings dir present + binary/bundle absent must be NOT installed")
	}

	a.lookPath = func(string) (string, error) { return "/usr/local/bin/zed", nil }
	if installed, path := a.Detect(); !installed || path != r.ConfigJoin("zed", "settings.json") {
		t.Fatalf("expected installed via PATH binary, got %v %q", installed, path)
	}
}

func TestDetectAppBundle(t *testing.T) {
	a, r := newAdapter(t)
	if err := os.MkdirAll(r.Join("Applications", "Zed.app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, _ := a.Detect(); !installed {
		t.Fatal("expected installed via app bundle")
	}
}

func TestApplyNewFileAndStatus(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("expected NotInstalled, got %v", st)
	}
	// Binary resolvable from here so Status reaches the settings-reading branch.
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/zed", nil }
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(res.Message, "MINTROUTER_API_KEY") {
		t.Fatalf("apply message must mention MINTROUTER_API_KEY: %q", res.Message)
	}
	root := readJSON(t, res.ChangedPath)
	lm := root["language_models"].(map[string]any)
	prov := lm["openai_compatible"].(map[string]any)[providerID].(map[string]any)
	if prov["api_url"] != p.BaseURL {
		t.Fatalf("api_url = %v", prov["api_url"])
	}
	models := prov["available_models"].([]any)
	if len(models) != 1 {
		t.Fatalf("expected one model, got %v", models)
	}
	m := models[0].(map[string]any)
	if m["name"] != p.Model || m["display_name"] != p.Model || m["max_tokens"] != float64(modelMaxTokens) {
		t.Fatalf("model entry wrong: %v", m)
	}
	dm := root["agent"].(map[string]any)["default_model"].(map[string]any)
	if dm["provider"] != providerID || dm["model"] != p.Model {
		t.Fatalf("default_model wrong: %v", dm)
	}
	if st, detail, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch, got %v", st)
	} else if !strings.Contains(detail, "MINTROUTER_API_KEY") {
		t.Fatalf("applied detail must mention MINTROUTER_API_KEY: %q", detail)
	}
	other := sampleProfile()
	other.Model = "different"
	if st, _, _ := a.Status(other); st != core.StatusModifiedExternally {
		t.Fatalf("expected ModifiedExternally, got %v", st)
	}
}

// TestApplyNeverWritesAPIKey is the Zed-specific safety contract: the profile
// API key must never appear anywhere in settings.json.
func TestApplyNeverWritesAPIKey(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	data, err := os.ReadFile(res.ChangedPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), p.APIKey) {
		t.Fatal("API key must not be written to settings.json")
	}
}

// TestApplyPreservesExistingKeys covers Zed's JSONC dialect: comments and
// trailing commas must parse, and unrelated settings must survive Apply.
func TestApplyPreservesExistingKeys(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
  // Zed settings with a comment
  "theme": "One Dark", /* block comment */
  "vim_mode": true,
  "language_models": {
    "anthropic": {"api_url": "https://api.anthropic.com"},
  },
  "agent": {"always_allow_tool_actions": false,},
}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, path)
	if root["theme"] != "One Dark" || root["vim_mode"] != true {
		t.Fatalf("unrelated top-level keys not preserved: %v", root)
	}
	lm := root["language_models"].(map[string]any)
	if _, ok := lm["anthropic"]; !ok {
		t.Fatalf("existing provider not preserved: %v", lm)
	}
	if _, ok := lm["openai_compatible"].(map[string]any)[providerID]; !ok {
		t.Fatalf("mintrouter provider missing: %v", lm)
	}
	agent := root["agent"].(map[string]any)
	if agent["always_allow_tool_actions"] != false {
		t.Fatalf("existing agent keys not preserved: %v", agent)
	}
	if _, ok := agent["default_model"]; !ok {
		t.Fatalf("default_model missing: %v", agent)
	}
}

// TestStatusIgnoresAPIKey proves the fingerprint excludes the API key: Zed
// never writes it to settings.json (it lives in MINTROUTER_API_KEY), so
// rotating the key must not flip Status to ModifiedExternally, while changes
// to managed fields (Model, BaseURL) must still be detected.
func TestStatusIgnoresAPIKey(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/zed", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	rotated := sampleProfile()
	rotated.APIKey = "sk-rotated-456"
	if st, _, _ := a.Status(rotated); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("API key change must not affect status, got %v", st)
	}

	otherModel := sampleProfile()
	otherModel.Model = "different-model"
	if st, _, _ := a.Status(otherModel); st != core.StatusModifiedExternally {
		t.Fatalf("model change must be detected, got %v", st)
	}

	otherURL := sampleProfile()
	otherURL.BaseURL = "https://other.example.com/v1"
	if st, _, _ := a.Status(otherURL); st != core.StatusModifiedExternally {
		t.Fatalf("base URL change must be detected, got %v", st)
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

// TestRestoreRevertsExisting proves comments survive the round trip via the
// backup, even though the applied file is rewritten as plain JSON.
func TestRestoreRevertsExisting(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  // keep me\n  \"vim_mode\": true,\n}\n")
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
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/zed", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply1: %v", err)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply2: %v", err)
	}
	root := readJSON(t, a.configPath())
	compatible := root["language_models"].(map[string]any)["openai_compatible"].(map[string]any)
	if len(compatible) != 1 {
		t.Fatalf("expected single openai_compatible provider after re-apply, got %v", compatible)
	}
	models := compatible[providerID].(map[string]any)["available_models"].([]any)
	if len(models) != 1 {
		t.Fatalf("expected single model after re-apply, got %v", models)
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

func TestStripJSONC(t *testing.T) {
	in := `{
  // line comment
  "a": "with // not a comment and \" escape", /* block
  spanning lines */
  "b": [1, 2,],
  "c": {"d": true,},
}`
	var m map[string]any
	if err := json.Unmarshal(stripJSONC([]byte(in)), &m); err != nil {
		t.Fatalf("unmarshal stripped: %v", err)
	}
	if m["a"] != `with // not a comment and " escape` {
		t.Fatalf("string mangled: %v", m["a"])
	}
	if len(m["b"].([]any)) != 2 || m["c"].(map[string]any)["d"] != true {
		t.Fatalf("structure wrong: %v", m)
	}
}

// writeLegacySettings writes a settings.json carrying the legacy in-file
// marker for profile p plus a user key, mimicking a pre-store MintSwitch
// apply.
func writeLegacySettings(t *testing.T, a *Adapter, p core.Profile) string {
	t.Helper()
	path := a.configPath()
	root := map[string]any{
		"theme": "One Dark",
		"language_models": map[string]any{
			"openai_compatible": map[string]any{
				providerID: map[string]any{"api_url": p.BaseURL},
			},
		},
		core.MarkerKey: core.NewMarker(fingerprintProfile(p), p.Label),
	}
	if err := writeConfig(path, root); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStatusDefaultWhenProviderRemoved proves a store entry alone does not
// report Applied: when the managed provider block was removed from the file
// (e.g. an external restore/wipe), Status falls back to Default.
func TestStatusDefaultWhenProviderRemoved(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/zed", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.WriteFile(a.configPath(), []byte(`{"theme":"One Dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default when managed provider block is gone, got %v", st)
	}
}

// TestApplyStripsLegacyMarker proves an Apply over a legacy-marker file
// removes the key in the same write and records the fresh marker in the
// store, without snapshotting the managed file (backup gate honors the legacy
// marker).
func TestApplyStripsLegacyMarker(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/zed", nil }
	p := sampleProfile()
	path := writeLegacySettings(t, a, p)

	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("legacy-managed file must not be backed up, got %q", res.BackupPath)
	}
	root := readJSON(t, path)
	if _, ok := root[core.MarkerKey]; ok {
		t.Fatalf("legacy marker not stripped on Apply: %+v", root)
	}
	if root["theme"] != "One Dark" {
		t.Fatalf("user key lost: %+v", root)
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
	root := readJSON(t, path)
	if _, ok := root[core.MarkerKey]; ok {
		t.Fatalf("legacy marker still in file: %+v", root)
	}
	if root["theme"] != "One Dark" {
		t.Fatalf("user key lost: %+v", root)
	}
	if !hasManagedProvider(root) {
		t.Fatalf("provider block lost: %+v", root)
	}
	marker, ok, err := a.m.Get(a.ID())
	if err != nil || !ok {
		t.Fatalf("store entry after migrate = ok=%v err=%v", ok, err)
	}
	if marker.Fingerprint != core.Fingerprint(fingerprintProfile(p)) {
		t.Fatalf("migrated fingerprint mismatch: %q", marker.Fingerprint)
	}
}

// TestStripLegacyMarkerKeepsExistingStoreEntry proves the sweep never
// overwrites a marker already recorded in the store (the store is newer truth).
func TestStripLegacyMarkerKeepsExistingStoreEntry(t *testing.T) {
	a, _ := newAdapter(t)
	p2 := sampleProfile()
	p2.BaseURL = "https://other.example.com/v1"
	stored := core.NewMarker(fingerprintProfile(p2), "personal")
	if err := a.m.Put(a.ID(), stored); err != nil {
		t.Fatal(err)
	}
	writeLegacySettings(t, a, sampleProfile())

	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip: %v", err)
	}
	marker, ok, err := a.m.Get(a.ID())
	if err != nil || !ok {
		t.Fatalf("store entry = ok=%v err=%v", ok, err)
	}
	if marker.Fingerprint != stored.Fingerprint {
		t.Fatalf("store entry overwritten by legacy marker: %+v", marker)
	}
	if root := readJSON(t, a.configPath()); root[core.MarkerKey] != nil {
		t.Fatal("legacy marker still in file")
	}
}

// TestStripLegacyMarkerNoOp proves the sweep neither creates a missing file
// nor rewrites a clean one.
func TestStripLegacyMarkerNoOp(t *testing.T) {
	a, _ := newAdapter(t)
	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip on missing file: %v", err)
	}
	if _, err := os.Stat(a.configPath()); !os.IsNotExist(err) {
		t.Fatalf("strip must not create settings.json, stat err=%v", err)
	}

	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := []byte(`{"theme": "One Dark"}`)
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
