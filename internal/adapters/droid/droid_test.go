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

// TestDetect proves the contract: a leftover ~/.factory dir is NOT an
// installed signal; only a resolvable "droid" binary is.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed with empty home")
	}
	if err := os.MkdirAll(r.Join(".factory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, _ := a.Detect(); installed {
		t.Fatal("~/.factory present + binary absent must be NOT installed")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	if installed, path := a.Detect(); !installed || path != r.Join(".factory", "settings.json") {
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

// writeLegacySettings writes a settings.json carrying the legacy in-file
// marker for profile p plus a user key, mimicking a pre-store MintSwitch
// apply.
func writeLegacySettings(t *testing.T, a *Adapter, p core.Profile) string {
	t.Helper()
	path := a.configPath()
	root := map[string]any{
		"theme":         "dark",
		customModelsKey: []any{customModelEntry(p)},
		"model":         p.Model,
		core.MarkerKey:  core.NewMarker(p, p.Label),
	}
	if err := core.WriteJSONObjectAtomic(path, root); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStatusDefaultWhenEntryRemoved proves a store entry alone does not
// report Applied: when the managed customModels entry was removed from the
// file (e.g. an external restore/wipe), Status falls back to Default.
func TestStatusDefaultWhenEntryRemoved(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.WriteFile(a.configPath(), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default when managed entry is gone, got %v", st)
	}
}

// TestApplyStripsLegacyMarker proves an Apply over a legacy-marker file
// removes the key in the same write and records the fresh marker in the
// store, without snapshotting the managed file (backup gate honors the legacy
// marker).
func TestApplyStripsLegacyMarker(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
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
	if root["theme"] != "dark" {
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
	if root["theme"] != "dark" {
		t.Fatalf("user key lost: %+v", root)
	}
	if !hasManagedEntry(root) {
		t.Fatalf("customModels entry lost: %+v", root)
	}
	marker, ok, err := a.m.Get(a.ID())
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
	p2 := sampleProfile()
	p2.BaseURL = "https://other.example.com/v1"
	stored := core.NewMarker(p2, "personal")
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
// must be ignored — Restore reverts to the oldest, pristine entry and prunes
// every backup so the contaminated one can never resurface.
func TestRestoreIgnoresContaminatedNewerBackup(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := []byte("{\n  \"editor\": \"vim\"\n}\n")
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

// TestRestoreNoBackupStripsManagedEntry is the regression for the missing
// backup fallback: with the backups dir deleted, Restore must surgically
// strip the MintSwitch customModels entry while preserving the user's own
// entries and settings.
func TestRestoreNoBackupStripsManagedEntry(t *testing.T) {
	a, r := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"editor":"vim","customModels":[{"displayName":"Mine","model":"my-model","baseUrl":"https://me.example.com"}]}`
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
	root := readJSON(t, path)
	models, _ := root[customModelsKey].([]any)
	if len(models) != 1 {
		t.Fatalf("expected only the user's entry to remain: %v", root)
	}
	if hasManagedEntry(root) {
		t.Fatalf("managed entry must be stripped: %v", root)
	}
	if _, present := root["model"]; present {
		t.Fatalf("model must be stripped: %v", root)
	}
	if root["editor"] != "vim" {
		t.Fatalf("user settings must be preserved: %v", root)
	}
	want := "No backup found; removed the MintSwitch custom model from Factory Droid settings."
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
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"model":"claude-opus"}` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Simulate the lost marker (e.g. an interrupted earlier restore).
	if err := a.m.Delete(a.ID()); err != nil {
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
// gone AND backups gone, but the file still carries the MintSwitch
// customModels entry — Restore must strip it and the managed model while
// preserving the user's own entries and settings, and Status must offer
// Restore beforehand.
func TestOrphanRestoreNoBackupStrips(t *testing.T) {
	a, r := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	user := `{"customModels":[{"displayName":"My Model","model":"m1","baseUrl":"https://x.example.com","provider":"generic-chat-completion-api"}],"editor":"vim"}`
	if err := os.WriteFile(path, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.RemoveAll(r.BackupsDir()); err != nil {
		t.Fatal(err)
	}
	if err := a.m.Delete(a.ID()); err != nil {
		t.Fatal(err)
	}

	if st, _, _ := a.Status(sampleProfile()); st != core.StatusModifiedExternally {
		t.Fatalf("orphan status = %v, want ModifiedExternally", st)
	}
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	want := "No backup found; removed the MintSwitch custom model from Factory Droid settings."
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	root := readJSON(t, path)
	models, _ := root[customModelsKey].([]any)
	if len(models) != 1 {
		t.Fatalf("customModels = %v, want only the user's entry", models)
	}
	if obj, _ := models[0].(map[string]any); obj["displayName"] != "My Model" {
		t.Fatalf("user entry must be preserved: %v", models)
	}
	if _, present := root["model"]; present {
		t.Fatalf("managed model must be stripped: %v", root)
	}
	if root["editor"] != "vim" {
		t.Fatalf("user settings must be preserved: %v", root)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after strip = %v, want Default", st)
	}
}

// TestPureUserConfigNeverOrphan proves the no-false-positive contract: a
// hand-written config that never saw Apply (its own customModels entries, no
// MintSwitch displayName) stays Default (no Restore button) and Restore
// leaves it byte-for-byte untouched.
func TestPureUserConfigNeverOrphan(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	user := `{"customModels":[{"displayName":"My Model","model":"m1","baseUrl":"https://x.example.com"}],"model":"m1"}`
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
