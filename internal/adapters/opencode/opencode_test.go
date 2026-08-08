package opencode

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

func TestIDName(t *testing.T) {
	a, _ := newAdapter(t)
	if a.ID() != "opencode" || a.Name() != "OpenCode" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
}

// TestDetect proves the binary-based contract: a leftover global config dir is
// NOT an installed signal; only a resolvable "opencode" binary is.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed with empty home")
	}
	if err := os.MkdirAll(filepath.Dir(a.configPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, _ := a.Detect(); installed {
		t.Fatal("config dir present + binary absent must be NOT installed")
	}

	a.lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	if installed, path := a.Detect(); !installed || path != r.ConfigJoin("opencode", "opencode.json") {
		t.Fatalf("expected installed via PATH binary, got %v %q", installed, path)
	}
}

func TestApplyNewFileAndStatus(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("expected NotInstalled, got %v", st)
	}
	// Binary resolvable from here so Status reaches the config-reading branch.
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, res.ChangedPath)
	if root["model"] != "mintrouter/gpt-mint" {
		t.Fatalf("model = %v", root["model"])
	}
	prov := root["provider"].(map[string]any)[providerID].(map[string]any)
	if prov["npm"] != npmPackage || prov["name"] != providerName {
		t.Fatalf("provider meta wrong: %v", prov)
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

// TestApplyWritesModalities pins the vision fix: every model entry MintSwitch
// writes must declare modalities so OpenCode enables image/video input.
// Without it, OpenCode's transform strips image parts (capabilities.input.image
// = false for custom providers with no models.dev fallback).
func TestApplyWritesModalities(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, res.ChangedPath)
	prov := root["provider"].(map[string]any)[providerID].(map[string]any)
	models := prov["models"].(map[string]any)
	entry, ok := models[p.Model].(map[string]any)
	if !ok {
		t.Fatalf("model entry missing: %v", models)
	}
	mod, ok := entry["modalities"].(map[string]any)
	if !ok {
		t.Fatalf("modalities missing from model entry: %v", entry)
	}
	input, ok := mod["input"].([]any)
	if !ok {
		t.Fatalf("modalities.input not a list: %v", mod)
	}
	got := map[string]bool{}
	for _, v := range input {
		got[v.(string)] = true
	}
	for _, want := range []string{"text", "image", "video"} {
		if !got[want] {
			t.Fatalf("modalities.input missing %q: %v", want, mod["input"])
		}
	}
	output, ok := mod["output"].([]any)
	if !ok || len(output) != 1 || output[0].(string) != "text" {
		t.Fatalf("modalities.output must be [\"text\"]: %v", mod["output"])
	}
}

func TestApplyPreservesExistingKeys(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{"$schema":"https://opencode.ai/config.json","autoupdate":false,` +
		`"provider":{"anthropic":{"options":{"baseURL":"https://api.anthropic.com"}}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, path)
	if root["$schema"] != "https://opencode.ai/config.json" || root["autoupdate"] != false {
		t.Fatalf("unrelated top-level keys not preserved: %v", root)
	}
	prov := root["provider"].(map[string]any)
	if _, ok := prov["anthropic"]; !ok {
		t.Fatalf("existing provider not preserved: %v", prov)
	}
	if _, ok := prov[providerID]; !ok {
		t.Fatalf("mintrouter provider missing: %v", prov)
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
	original := []byte(`{"autoupdate":true}` + "\n")
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
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply1: %v", err)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply2: %v", err)
	}
	root := readJSON(t, a.configPath())
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
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.WriteFile(a.configPath(), []byte(`{"theme":"opencode"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default when managed provider block is gone, got %v", st)
	}
}

// writeLegacyConfig writes an opencode.json carrying the legacy in-file marker
// for profile p plus user keys (provider/model/mcp), mimicking the real broken
// config a pre-store MintSwitch apply leaves behind.
func writeLegacyConfig(t *testing.T, a *Adapter, p core.Profile) string {
	t.Helper()
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			providerID: map[string]any{
				"npm":     npmPackage,
				"name":    providerName,
				"options": map[string]any{"baseURL": p.BaseURL, "apiKey": p.APIKey},
				"models":  map[string]any{p.Model: map[string]any{"name": p.Model}},
			},
		},
		"model": providerID + "/" + p.Model,
		"mcp": map[string]any{
			"mintrouter-context": map[string]any{"type": "remote", "url": "https://mcp.example.com"},
		},
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

// TestApplyStripsLegacyMarker proves an Apply over a legacy-marker file removes
// the key in the same write and records the fresh marker in the store, without
// snapshotting the managed file (backup gate honors the legacy marker).
func TestApplyStripsLegacyMarker(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	p := sampleProfile()
	path := writeLegacyConfig(t, a, p)

	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("legacy-managed file must not be backed up, got %q", res.BackupPath)
	}
	root := readJSON(t, path)
	if _, ok := root[core.MarkerKey]; ok {
		t.Fatalf("legacy marker not stripped on Apply: %v", root)
	}
	if _, ok := root["mcp"]; !ok {
		t.Fatalf("user mcp key lost: %v", root)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want AppliedByMintSwitch, got %v", st)
	}
}

// TestStripLegacyMarkerMigrates is the startup-sweep case: an opencode.json
// broken by the legacy marker gets the key removed — keeping provider, model
// and mcp intact — and the marker migrated into the store, without any Apply.
func TestStripLegacyMarkerMigrates(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	path := writeLegacyConfig(t, a, p)

	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip: %v", err)
	}
	root := readJSON(t, path)
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
	if _, ok := root["mcp"]; !ok {
		t.Fatalf("mcp key lost: %v", root)
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
	writeLegacyConfig(t, a, sampleProfile())

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
	if root := readJSON(t, a.configPath()); root[core.MarkerKey] != nil {
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
	if _, err := os.Stat(a.configPath()); !os.IsNotExist(err) {
		t.Fatalf("strip must not create opencode.json, stat err=%v", err)
	}

	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	clean := []byte(`{"theme": "opencode"}`)
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
// must be ignored — Restore reverts to the oldest, pristine entry and prunes
// every backup so the contaminated one can never resurface.
func TestRestoreIgnoresContaminatedNewerBackup(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
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
// strip the MintSwitch provider and default model while preserving the user's
// own settings.
func TestRestoreNoBackupStripsManagedProvider(t *testing.T) {
	a, r := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
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
	root, err := core.ReadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := root["provider"]; present {
		t.Fatalf("provider must be stripped: %v", root)
	}
	if _, present := root["model"]; present {
		t.Fatalf("model must be stripped: %v", root)
	}
	if root["theme"] != "dark" {
		t.Fatalf("user settings must be preserved: %v", root)
	}
	want := "No backup found; removed the MintSwitch provider from OpenCode config."
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
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"$schema":"https://opencode.ai/config.json","theme":"dark"}` + "\n"
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
// gone AND backups gone, but the file still carries the MintSwitch provider —
// Restore must strip it and the managed model while preserving the user's own
// settings, and Status must offer Restore beforehand.
func TestOrphanRestoreNoBackupStrips(t *testing.T) {
	a, r := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark","mcp":{"own":{"type":"remote"}}}`), 0o600); err != nil {
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
	want := "No backup found; removed the MintSwitch provider from OpenCode config."
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	root := readJSON(t, path)
	if _, present := root["provider"]; present {
		t.Fatalf("provider must be stripped: %v", root)
	}
	if _, present := root["model"]; present {
		t.Fatalf("model must be stripped: %v", root)
	}
	if root["theme"] != "dark" {
		t.Fatalf("user settings must be preserved: %v", root)
	}
	if _, present := root["mcp"]; !present {
		t.Fatalf("user mcp key must be preserved: %v", root)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after strip = %v, want Default", st)
	}
}

// TestPureUserConfigNeverOrphan proves the no-false-positive contract: a
// hand-written config that never saw Apply (no mintrouter provider) stays
// Default (no Restore button) and Restore leaves it byte-for-byte untouched.
func TestPureUserConfigNeverOrphan(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	user := `{"theme":"dark","provider":{"anthropic":{"options":{"baseURL":"https://api.anthropic.com"}}},"model":"anthropic/claude-3"}`
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
