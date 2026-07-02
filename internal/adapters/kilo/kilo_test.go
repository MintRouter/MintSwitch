package kilo

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
	data := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: data}
	a := New(r, backup.NewEngine(r.BackupsDir()), markers.NewStore(r.MarkersPath()))
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

// TestStatusDefaultWhenProviderRemoved proves a store entry alone does not
// report Applied: when the managed provider block was removed from the file
// (e.g. an external restore/wipe), Status falls back to Default.
func TestStatusDefaultWhenProviderRemoved(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	writeFile(t, a.jsonPath(), `{"theme":"kilo"}`)
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default when managed provider block is gone, got %v", st)
	}
}

// writeLegacyConfig writes a kilo config at path carrying the legacy in-file
// marker for profile p plus user keys, mimicking a pre-store MintSwitch apply.
func writeLegacyConfig(t *testing.T, path string, p core.Profile) {
	t.Helper()
	m := map[string]any{
		"$schema": "https://app.kilo.ai/config.json",
		"provider": map[string]any{
			providerID: map[string]any{
				"options": map[string]any{"baseURL": p.BaseURL, "apiKey": p.APIKey},
				"models":  map[string]any{p.Model: map[string]any{"name": p.Model}},
			},
		},
		"model":        providerID + "/" + p.Model,
		core.MarkerKey: core.NewMarker(p, p.Label),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
}

// TestApplyStripsLegacyMarker proves an Apply over a legacy-marker file removes
// the key in the same write and records the fresh marker in the store, without
// snapshotting the managed file (backup gate honors the legacy marker).
func TestApplyStripsLegacyMarker(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	p := sampleProfile()
	writeLegacyConfig(t, a.jsonPath(), p)

	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("legacy-managed file must not be backed up, got %q", res.BackupPath)
	}
	root := readJSON(t, a.jsonPath())
	if _, ok := root[core.MarkerKey]; ok {
		t.Fatalf("legacy marker not stripped on Apply: %v", root)
	}
	if root["$schema"] != "https://app.kilo.ai/config.json" {
		t.Fatalf("user key lost: %v", root)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want AppliedByMintSwitch, got %v", st)
	}
}

// TestStripLegacyMarkerMigrates is the startup-sweep case: a kilo.json broken
// by the legacy marker gets the key removed — keeping provider and model
// intact — and the marker migrated into the store, without any Apply.
func TestStripLegacyMarkerMigrates(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	writeLegacyConfig(t, a.jsonPath(), p)

	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip: %v", err)
	}
	root := readJSON(t, a.jsonPath())
	if _, ok := root[core.MarkerKey]; ok {
		t.Fatalf("legacy marker still in file: %v", root)
	}
	prov := root["provider"].(map[string]any)
	if _, ok := prov[providerID]; !ok {
		t.Fatalf("provider block lost: %v", root)
	}
	if root["model"] != providerID+"/"+p.Model {
		t.Fatalf("model lost: %v", root["model"])
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
	writeLegacyConfig(t, a.jsonPath(), sampleProfile())

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
	if root := readJSON(t, a.jsonPath()); root[core.MarkerKey] != nil {
		t.Fatal("legacy marker still in file")
	}
}

// TestStripLegacyMarkerNoOp proves the sweep neither creates missing files nor
// rewrites a clean one.
func TestStripLegacyMarkerNoOp(t *testing.T) {
	a, _ := newAdapter(t)
	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip on missing files: %v", err)
	}
	if _, err := os.Stat(a.jsonPath()); !os.IsNotExist(err) {
		t.Fatalf("strip must not create kilo.json, stat err=%v", err)
	}
	if _, err := os.Stat(a.jsoncPath()); !os.IsNotExist(err) {
		t.Fatalf("strip must not create kilo.jsonc, stat err=%v", err)
	}

	clean := `{"theme": "kilo"}`
	writeFile(t, a.jsonPath(), clean)
	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip on clean file: %v", err)
	}
	got, err := os.ReadFile(a.jsonPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != clean {
		t.Fatalf("clean file rewritten: %q", got)
	}
}

// TestStripLegacyMarkerSkipsCommentedJSONC proves the sweep never rewrites a
// kilo.jsonc carrying JSONC-only syntax, even when it holds a legacy marker —
// rewriting would destroy the user's comments.
func TestStripLegacyMarkerSkipsCommentedJSONC(t *testing.T) {
	a, _ := newAdapter(t)
	jsonc := "{\n  // user comment\n  \"" + core.MarkerKey + "\": {\"managed\": true},\n  \"theme\": \"kilo\"\n}"
	writeFile(t, a.jsoncPath(), jsonc)

	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip on commented jsonc: %v", err)
	}
	got, err := os.ReadFile(a.jsoncPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != jsonc {
		t.Fatalf("commented jsonc rewritten: %q", got)
	}
	if _, ok, err := a.m.Get(id); err != nil || ok {
		t.Fatalf("no store entry expected for skipped jsonc, ok=%v err=%v", ok, err)
	}
}

// TestRestoreIgnoresContaminatedNewerBackup is the regression for dirty
// backups: a snapshot taken AFTER the file was already MintSwitch-managed
// must be ignored — Restore reverts to the oldest, pristine entry and prunes
// every backup so the contaminated one can never resurface.
func TestRestoreIgnoresContaminatedNewerBackup(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.jsonPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := []byte("{\n  \"theme\": \"dark\"\n}\n")
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

// TestRestoreNoBackupStripsManagedProvider is the regression for the missing
// backup fallback: with the backups dir deleted, Restore must surgically
// strip the MintSwitch-managed provider and default model while preserving
// the user's own settings.
func TestRestoreNoBackupStripsManagedProvider(t *testing.T) {
	a, r := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	path := a.jsonPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark","provider":{"other":{}}}`), 0o600); err != nil {
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
	root := readJSON(t, path)
	provider, _ := root["provider"].(map[string]any)
	if _, present := provider[providerID]; present {
		t.Fatalf("managed provider must be stripped: %v", root)
	}
	if _, present := provider["other"]; !present {
		t.Fatalf("user provider must be preserved: %v", root)
	}
	if _, present := root["model"]; present {
		t.Fatalf("model must be stripped: %v", root)
	}
	if root["theme"] != "dark" {
		t.Fatalf("user settings must be preserved: %v", root)
	}
	want := "No backup found; removed the MintSwitch-managed provider from Kilo Code config."
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after strip = %v, want Default", st)
	}
}

