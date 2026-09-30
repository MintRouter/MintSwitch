package pi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// installed makes Detect see a resolvable "pi" binary.
func installed(a *Adapter) {
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/pi", nil }
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
	if a.ID() != "pi" || a.Name() != "Pi" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
}

// TestDetect proves the binary-based contract: a leftover ~/.pi dir is NOT an
// installed signal; only a resolvable "pi" binary is.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if inst, _ := a.Detect(); inst {
		t.Fatal("expected not installed with empty home")
	}
	if err := os.MkdirAll(filepath.Dir(a.modelsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if inst, _ := a.Detect(); inst {
		t.Fatal("config dir present + binary absent must be NOT installed")
	}
	installed(a)
	if inst, path := a.Detect(); !inst || path != r.Join(".pi", "agent", "models.json") {
		t.Fatalf("expected installed via PATH binary, got %v %q", inst, path)
	}
}

func TestApplyNewFilesAndStatus(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("expected NotInstalled, got %v", st)
	}
	installed(a)
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	models := readJSON(t, res.ChangedPath)
	prov := models["providers"].(map[string]any)[providerID].(map[string]any)
	if prov["baseUrl"] != p.BaseURL || prov["api"] != apiType || prov["apiKey"] != p.APIKey {
		t.Fatalf("provider wrong: %v", prov)
	}
	list := prov["models"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != p.Model {
		t.Fatalf("models list wrong: %v", list)
	}
	settings := readJSON(t, a.settingsPath())
	if settings["defaultProvider"] != providerID || settings["defaultModel"] != p.Model {
		t.Fatalf("settings wrong: %v", settings)
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

// TestApplyAllModels proves "All models" mode writes one models entry per
// profile model (selected first) while defaultModel stays the selected model,
// and that a mode switch is detected via the fingerprint until re-apply.
func TestApplyAllModels(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.Models = []string{"gpt-mint", "claude-mint"}
	p.ApplyAllModels = true
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	models := readJSON(t, res.ChangedPath)
	prov := models["providers"].(map[string]any)[providerID].(map[string]any)
	list := prov["models"].([]any)
	if len(list) != 2 {
		t.Fatalf("models list = %v, want 2 entries", list)
	}
	for i, id := range []string{"gpt-mint", "claude-mint"} {
		entry := list[i].(map[string]any)
		if entry["id"] != id || entry["name"] != id {
			t.Fatalf("entry %d = %v, want id/name %q", i, entry, id)
		}
		if _, ok := entry["input"]; !ok {
			t.Fatalf("entry %d = %v, want input on every entry", i, entry)
		}
		if _, ok := entry["reasoning"]; !ok {
			t.Fatalf("entry %d = %v, want reasoning on every entry", i, entry)
		}
	}
	settings := readJSON(t, a.settingsPath())
	if settings["defaultModel"] != p.Model {
		t.Fatalf("defaultModel = %v, want selected model", settings["defaultModel"])
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch, got %v", st)
	}
	one := p
	one.ApplyAllModels = false
	if st, _, _ := a.Status(one); st != core.StatusModifiedExternally {
		t.Fatalf("expected ModifiedExternally after mode switch, got %v", st)
	}
}

// TestApplyModelDisplayNames pins the display-name UX: a model with a
// ModelNames entry gets that display name as its "name" field (the "id" field
// stays the canonical ID), while a model without one keeps name = ID — in
// both single-model and "All models" mode.
func TestApplyModelDisplayNames(t *testing.T) {
	modelEntry := func(t *testing.T, path, id string) map[string]any {
		t.Helper()
		models := readJSON(t, path)
		prov := models["providers"].(map[string]any)[providerID].(map[string]any)
		for _, e := range prov["models"].([]any) {
			entry := e.(map[string]any)
			if entry["id"] == id {
				return entry
			}
		}
		t.Fatalf("model entry %q missing", id)
		return nil
	}

	t.Run("single-model with display name", func(t *testing.T) {
		a, _ := newAdapter(t)
		p := sampleProfile()
		p.ModelNames = map[string]string{"gpt-mint": "GPT Mint"}
		res, err := a.Apply(p)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if got := modelEntry(t, res.ChangedPath, "gpt-mint")["name"]; got != "GPT Mint" {
			t.Fatalf("name = %v, want display name %q", got, "GPT Mint")
		}
	})

	t.Run("single-model without display name", func(t *testing.T) {
		a, _ := newAdapter(t)
		p := sampleProfile()
		res, err := a.Apply(p)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if got := modelEntry(t, res.ChangedPath, "gpt-mint")["name"]; got != "gpt-mint" {
			t.Fatalf("name = %v, want ID fallback %q", got, "gpt-mint")
		}
	})

	t.Run("all-models mixed", func(t *testing.T) {
		a, _ := newAdapter(t)
		p := sampleProfile()
		p.Models = []string{"gpt-mint", "claude-mint"}
		p.ApplyAllModels = true
		p.ModelNames = map[string]string{"claude-mint": "Claude Mint"}
		res, err := a.Apply(p)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if got := modelEntry(t, res.ChangedPath, "gpt-mint")["name"]; got != "gpt-mint" {
			t.Fatalf("gpt-mint name = %v, want ID fallback", got)
		}
		if got := modelEntry(t, res.ChangedPath, "claude-mint")["name"]; got != "Claude Mint" {
			t.Fatalf("claude-mint name = %v, want display name %q", got, "Claude Mint")
		}
	})
}

func TestApplyPreservesExistingKeys(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	if err := os.MkdirAll(filepath.Dir(a.modelsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	existingModels := `{"providers":{"custom":{"baseUrl":"https://own.example.com","api":"anthropic-messages","apiKey":"k","models":[]}}}`
	if err := os.WriteFile(a.modelsPath(), []byte(existingModels), 0o600); err != nil {
		t.Fatal(err)
	}
	existingSettings := `{"theme":"dark","defaultProvider":"custom","defaultModel":"own-model"}`
	if err := os.WriteFile(a.settingsPath(), []byte(existingSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	models := readJSON(t, a.modelsPath())
	providers := models["providers"].(map[string]any)
	if _, ok := providers["custom"]; !ok {
		t.Fatalf("existing provider not preserved: %v", providers)
	}
	if _, ok := providers[providerID]; !ok {
		t.Fatalf("mintrouter provider missing: %v", providers)
	}
	settings := readJSON(t, a.settingsPath())
	if settings["theme"] != "dark" {
		t.Fatalf("unrelated settings key not preserved: %v", settings)
	}
	if settings["defaultProvider"] != providerID || settings["defaultModel"] != "gpt-mint" {
		t.Fatalf("defaults not switched: %v", settings)
	}
}

func TestRestoreDeletesCreatedFiles(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, p := range []string{a.modelsPath(), a.settingsPath()} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected file created: %v", err)
		}
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, p := range []string{a.modelsPath(), a.settingsPath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, got err=%v", p, err)
		}
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after restore = %v, want Default", st)
	}
}

func TestRestoreRevertsExisting(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	if err := os.MkdirAll(filepath.Dir(a.modelsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	origModels := []byte(`{"providers":{"own":{"baseUrl":"https://x","api":"openai-completions","apiKey":"k","models":[]}}}` + "\n")
	origSettings := []byte(`{"theme":"dark"}` + "\n")
	if err := os.WriteFile(a.modelsPath(), origModels, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.settingsPath(), origSettings, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	gotModels, _ := os.ReadFile(a.modelsPath())
	if string(gotModels) != string(origModels) {
		t.Fatalf("models.json not byte-for-byte restored: %q", gotModels)
	}
	gotSettings, _ := os.ReadFile(a.settingsPath())
	if string(gotSettings) != string(origSettings) {
		t.Fatalf("settings.json not byte-for-byte restored: %q", gotSettings)
	}
}

func TestReApplyIdempotent(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply1: %v", err)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply2: %v", err)
	}
	models := readJSON(t, a.modelsPath())
	providers := models["providers"].(map[string]any)
	if len(providers) != 1 {
		t.Fatalf("expected single provider after re-apply, got %v", providers)
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
	if _, err := os.Stat(a.modelsPath()); !os.IsNotExist(err) {
		t.Fatalf("restore must not create models.json, stat err=%v", err)
	}
	if _, err := os.Stat(a.settingsPath()); !os.IsNotExist(err) {
		t.Fatalf("restore must not create settings.json, stat err=%v", err)
	}
}

// TestStatusDefaultWhenProviderRemoved proves a store entry alone does not
// report Applied: when the managed provider block was removed from models.json
// (e.g. an external restore/wipe), Status falls back to Default.
func TestStatusDefaultWhenProviderRemoved(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.WriteFile(a.modelsPath(), []byte(`{"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default when managed provider block is gone, got %v", st)
	}
}

// TestStatusSettingsDrift pins the /model-picker case: models.json still
// matches the fingerprint, but settings.json was repointed at another
// provider/model — Status must report ModifiedExternally, not Applied.
func TestStatusSettingsDrift(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	settings := readJSON(t, a.settingsPath())
	settings["defaultProvider"] = "anthropic"
	settings["defaultModel"] = "claude-x"
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.settingsPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	st, detail, err := a.Status(p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != core.StatusModifiedExternally || detail != settingsDriftDetail {
		t.Fatalf("drift status = %v %q, want ModifiedExternally + settingsDriftDetail", st, detail)
	}
}

// TestStatusModelOnlyDrift pins the milder /model-picker case: settings.json
// still selects the MintSwitch provider (mintrouter), only defaultModel was
// changed — e.g. between models applied in "All models" mode. Traffic still
// flows through the endpoint, so the detail must say so (modelDriftDetail)
// rather than the misleading "bypasses the configured endpoint" wording.
func TestStatusModelOnlyDrift(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.Models = []string{p.Model, "other-mint"}
	p.ApplyAllModels = true
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	settings := readJSON(t, a.settingsPath())
	settings["defaultModel"] = "other-mint"
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.settingsPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	st, detail, err := a.Status(p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != core.StatusModifiedExternally || detail != modelDriftDetail {
		t.Fatalf("model drift status = %v %q, want ModifiedExternally + modelDriftDetail", st, detail)
	}
}

// TestRestoreNoBackupStripsManagedKeys covers the missing-backup fallback:
// with the backups dir deleted, Restore must strip providers.mintrouter and
// defaultProvider/defaultModel while preserving every other key in both files.
func TestRestoreNoBackupStripsManagedKeys(t *testing.T) {
	a, r := newAdapter(t)
	installed(a)
	if err := os.MkdirAll(filepath.Dir(a.modelsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.modelsPath(), []byte(`{"providers":{"own":{"baseUrl":"https://x","api":"openai-completions","apiKey":"k","models":[]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.settingsPath(), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.RemoveAll(r.BackupsDir()); err != nil {
		t.Fatal(err)
	}
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	want := "No backup found; removed the MintSwitch-managed keys from the Pi config files."
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	models := readJSON(t, a.modelsPath())
	providers := models["providers"].(map[string]any)
	if _, present := providers[providerID]; present {
		t.Fatalf("mintrouter must be stripped: %v", providers)
	}
	if _, ok := providers["own"]; !ok {
		t.Fatalf("user provider must be preserved: %v", providers)
	}
	settings := readJSON(t, a.settingsPath())
	if _, present := settings["defaultProvider"]; present {
		t.Fatalf("defaultProvider must be stripped: %v", settings)
	}
	if _, present := settings["defaultModel"]; present {
		t.Fatalf("defaultModel must be stripped: %v", settings)
	}
	if settings["theme"] != "dark" {
		t.Fatalf("user settings must be preserved: %v", settings)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after strip = %v, want Default", st)
	}
}

// TestOrphanStatusAndRestore covers the lost-marker gap: marker gone but
// models.json still carries the MintSwitch provider — Status must report
// ModifiedExternally (orphanDetail) and Restore must still revert both files.
func TestOrphanStatusAndRestore(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	if err := os.MkdirAll(filepath.Dir(a.modelsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	origModels := `{"providers":{"own":{"baseUrl":"https://x","api":"openai-completions","apiKey":"k","models":[]}}}` + "\n"
	if err := os.WriteFile(a.modelsPath(), []byte(origModels), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
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
	got, err := os.ReadFile(a.modelsPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != origModels {
		t.Fatalf("models.json not byte-for-byte restored: %q", got)
	}
}

// TestPureUserConfigNeverOrphan proves the no-false-positive contract: a
// hand-written models.json that never saw Apply stays Default and Restore
// leaves both files byte-for-byte untouched.
func TestPureUserConfigNeverOrphan(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	if err := os.MkdirAll(filepath.Dir(a.modelsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	userModels := `{"providers":{"own":{"baseUrl":"https://x","api":"openai-completions","apiKey":"k","models":[]}}}`
	userSettings := `{"defaultProvider":"own","defaultModel":"m1"}`
	if err := os.WriteFile(a.modelsPath(), []byte(userModels), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.settingsPath(), []byte(userSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("pure user config status = %v, want Default", st)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	gotModels, _ := os.ReadFile(a.modelsPath())
	if string(gotModels) != userModels {
		t.Fatalf("pure user models.json rewritten: %q", gotModels)
	}
	gotSettings, _ := os.ReadFile(a.settingsPath())
	if string(gotSettings) != userSettings {
		t.Fatalf("pure user settings.json rewritten: %q", gotSettings)
	}
}

// TestApplySettingsWriteFailureRollsBackModels pins the two-file atomicity
// contract: when the settings.json write fails, models.json must be rolled
// back to its pre-Apply bytes (no leaked API key) and no marker recorded.
func TestApplySettingsWriteFailureRollsBackModels(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	if err := os.MkdirAll(filepath.Dir(a.modelsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := `{"providers":{"own":{"baseUrl":"https://x","api":"openai-completions","apiKey":"k","models":[]}}}`
	if err := os.WriteFile(a.modelsPath(), []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	a.writeSettings = func(string, map[string]any) error { return errors.New("disk full") }
	if _, err := a.Apply(sampleProfile()); err == nil {
		t.Fatal("expected apply error")
	}
	got, err := os.ReadFile(a.modelsPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != orig {
		t.Fatalf("models.json not rolled back: %q", got)
	}
	if _, ok, _ := a.m.Get(id); ok {
		t.Fatal("marker must not be recorded on failed apply")
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after failed apply = %v, want Default", st)
	}
}

// TestApplySettingsWriteFailureRemovesCreatedModels is the created-file variant
// of the rollback: models.json did not exist before Apply, so a failed
// settings.json write must delete it entirely.
func TestApplySettingsWriteFailureRemovesCreatedModels(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	a.writeSettings = func(string, map[string]any) error { return errors.New("disk full") }
	if _, err := a.Apply(sampleProfile()); err == nil {
		t.Fatal("expected apply error")
	}
	if _, err := os.Stat(a.modelsPath()); !os.IsNotExist(err) {
		t.Fatalf("created models.json must be removed on rollback, stat err=%v", err)
	}
}

func TestApplyInvalidProfile(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.APIKey = ""
	if _, err := a.Apply(p); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := os.Stat(a.modelsPath()); !os.IsNotExist(err) {
		t.Fatalf("invalid apply must not create models.json, stat err=%v", err)
	}
}

// TestApplyWritesContextWindowAndMaxTokens proves a model entry carries Pi's
// contextWindow / maxTokens when the profile knows the endpoint-advertised
// values (so Pi does not assume its 128k default and compact too early), and
// omits the fields — keeping Pi's defaults — when it does not.
func TestApplyWritesContextWindowAndMaxTokens(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.Models = []string{"gpt-mint", "unknown"}
	p.ApplyAllModels = true
	p.ModelContextWindows = map[string]int{"gpt-mint": 200_000}
	p.ModelMaxOutputTokens = map[string]int{"gpt-mint": 32_768}
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	models := readJSON(t, res.ChangedPath)
	list := models["providers"].(map[string]any)[providerID].(map[string]any)["models"].([]any)
	if len(list) != 2 {
		t.Fatalf("models list = %v, want 2 entries", list)
	}
	known := list[0].(map[string]any)
	if known["id"] != "gpt-mint" || known["contextWindow"] != float64(200_000) || known["maxTokens"] != float64(32_768) {
		t.Fatalf("known entry = %v, want contextWindow 200000 maxTokens 32768", known)
	}
	unknown := list[1].(map[string]any)
	if _, ok := unknown["contextWindow"]; ok {
		t.Fatalf("contextWindow must be omitted when unknown: %v", unknown)
	}
	if _, ok := unknown["maxTokens"]; ok {
		t.Fatalf("maxTokens must be omitted when unknown: %v", unknown)
	}
	// Limits are metadata only: the fingerprint (and so Status) ignores them.
	p.ModelContextWindows, p.ModelMaxOutputTokens = nil, nil
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("limits must not affect the fingerprint; got %v", st)
	}
}

// modelEntryByID returns the models[] entry with the given id from the
// MintSwitch provider block in models.json at path.
func modelEntryByID(t *testing.T, path, id string) map[string]any {
	t.Helper()
	models := readJSON(t, path)
	prov := models["providers"].(map[string]any)[providerID].(map[string]any)
	for _, e := range prov["models"].([]any) {
		entry := e.(map[string]any)
		if entry["id"] == id {
			return entry
		}
	}
	t.Fatalf("model entry %q missing", id)
	return nil
}

// inputOf returns an entry's "input" list as strings, failing when the field
// is absent or not a string array.
func inputOf(t *testing.T, entry map[string]any) []string {
	t.Helper()
	raw, ok := entry["input"].([]any)
	if !ok {
		t.Fatalf("entry %v: input missing or not an array", entry)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("entry %v: input value %v is not a string", entry, v)
		}
		out = append(out, s)
	}
	return out
}

// TestApplyWritesInputAndReasoning pins Pi's per-model input / reasoning
// fields: input is the endpoint-advertised modalities filtered to Pi's
// accepted {text, image} (Pi's schema rejects any other value), falling back
// to ["text","image"] when nothing is known or nothing survives the filter;
// reasoning is true iff the endpoint advertised reasoning levels and is
// written explicitly as false otherwise (Pi defaults to false, and an
// explicit false lets a re-Apply turn it off). Both fields are metadata only:
// the fingerprint (and so Status) ignores them.
func TestApplyWritesInputAndReasoning(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.Models = []string{"gpt-mint", "text-only", "unknown", "audio-only", "reasoner"}
	p.ApplyAllModels = true
	p.ModelInputModalities = map[string][]string{
		"gpt-mint":   {"text", "image", "video"},
		"text-only":  {"text"},
		"audio-only": {"audio"},
		"reasoner":   {"image", "text"},
	}
	p.ModelReasoningLevels = map[string][]string{
		"reasoner": {"low", "medium", "high"},
		"gpt-mint": {},
	}
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	cases := []struct {
		id        string
		input     []string
		reasoning bool
	}{
		{"gpt-mint", []string{"text", "image"}, false},
		{"text-only", []string{"text"}, false},
		{"unknown", []string{"text", "image"}, false},
		{"audio-only", []string{"text", "image"}, false},
		{"reasoner", []string{"text", "image"}, true},
	}
	for _, tc := range cases {
		entry := modelEntryByID(t, res.ChangedPath, tc.id)
		if got := inputOf(t, entry); !slices.Equal(got, tc.input) {
			t.Errorf("%s input = %v, want %v", tc.id, got, tc.input)
		}
		got, ok := entry["reasoning"].(bool)
		if !ok {
			t.Errorf("%s reasoning = %v, want explicit bool", tc.id, entry["reasoning"])
		} else if got != tc.reasoning {
			t.Errorf("%s reasoning = %v, want %v", tc.id, got, tc.reasoning)
		}
	}
	// Modalities and levels are metadata only: the fingerprint (and so
	// Status) ignores them.
	p.ModelInputModalities, p.ModelReasoningLevels = nil, nil
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("modalities/levels must not affect the fingerprint; got %v", st)
	}
}

// TestApplySingleModelInputAndReasoning is the single-model variant: the one
// entry carries both fields, and a profile that knows neither modalities nor
// levels (a generic endpoint) gets the conservative input default and
// reasoning false.
func TestApplySingleModelInputAndReasoning(t *testing.T) {
	t.Run("known", func(t *testing.T) {
		a, _ := newAdapter(t)
		p := sampleProfile()
		p.ModelInputModalities = map[string][]string{"gpt-mint": {"text"}}
		p.ModelReasoningLevels = map[string][]string{"gpt-mint": {"high"}}
		res, err := a.Apply(p)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		entry := modelEntryByID(t, res.ChangedPath, "gpt-mint")
		if got := inputOf(t, entry); !slices.Equal(got, []string{"text"}) {
			t.Errorf("input = %v, want [text]", got)
		}
		if entry["reasoning"] != true {
			t.Errorf("reasoning = %v, want true", entry["reasoning"])
		}
	})
	t.Run("generic endpoint", func(t *testing.T) {
		a, _ := newAdapter(t)
		res, err := a.Apply(sampleProfile())
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		entry := modelEntryByID(t, res.ChangedPath, "gpt-mint")
		if got := inputOf(t, entry); !slices.Equal(got, []string{"text", "image"}) {
			t.Errorf("input = %v, want fallback [text image]", got)
		}
		if entry["reasoning"] != false {
			t.Errorf("reasoning = %v, want explicit false", entry["reasoning"])
		}
	})
}

// TestReApplyTurnsReasoningOff proves the explicit-false contract: once the
// endpoint stops advertising levels, a re-Apply flips reasoning back to false
// and narrows input to what is now advertised.
func TestReApplyTurnsReasoningOff(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.ModelInputModalities = map[string][]string{"gpt-mint": {"text", "image"}}
	p.ModelReasoningLevels = map[string][]string{"gpt-mint": {"low", "high"}}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply1: %v", err)
	}
	if entry := modelEntryByID(t, a.modelsPath(), "gpt-mint"); entry["reasoning"] != true {
		t.Fatalf("reasoning after apply1 = %v, want true", entry["reasoning"])
	}
	p.ModelInputModalities = map[string][]string{"gpt-mint": {"text"}}
	p.ModelReasoningLevels = nil
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply2: %v", err)
	}
	entry := modelEntryByID(t, a.modelsPath(), "gpt-mint")
	if entry["reasoning"] != false {
		t.Fatalf("reasoning after apply2 = %v, want false", entry["reasoning"])
	}
	if got := inputOf(t, entry); !slices.Equal(got, []string{"text"}) {
		t.Fatalf("input after apply2 = %v, want [text]", got)
	}
}

// TestRestoreAfterInputAndReasoningApply proves the new fields do not change
// the Restore contract: an Apply that wrote input/reasoning is still reverted
// to the pristine pre-MintSwitch bytes of both files.
func TestRestoreAfterInputAndReasoningApply(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	if err := os.MkdirAll(filepath.Dir(a.modelsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	origModels := []byte(`{"providers":{"own":{"baseUrl":"https://x","api":"openai-completions","apiKey":"k","models":[{"id":"m","input":["text"]}]}}}` + "\n")
	origSettings := []byte(`{"theme":"dark"}` + "\n")
	if err := os.WriteFile(a.modelsPath(), origModels, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.settingsPath(), origSettings, 0o600); err != nil {
		t.Fatal(err)
	}
	p := sampleProfile()
	p.ModelInputModalities = map[string][]string{"gpt-mint": {"text", "image"}}
	p.ModelReasoningLevels = map[string][]string{"gpt-mint": {"medium"}}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if entry := modelEntryByID(t, a.modelsPath(), "gpt-mint"); entry["reasoning"] != true {
		t.Fatalf("reasoning = %v, want true before restore", entry["reasoning"])
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	gotModels, _ := os.ReadFile(a.modelsPath())
	if string(gotModels) != string(origModels) {
		t.Fatalf("models.json not byte-for-byte restored: %q", gotModels)
	}
	gotSettings, _ := os.ReadFile(a.settingsPath())
	if string(gotSettings) != string(origSettings) {
		t.Fatalf("settings.json not byte-for-byte restored: %q", gotSettings)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("status after restore = %v, want Default", st)
	}
}
