package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	a := New(r, backup.NewEngine(r.BackupsDir()), markers.NewStore(r.MarkersPath()))
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return a, home
}

// TestDetectViaPATHBinary proves a fresh "npm install -g" is detected via the
// "codex" binary on PATH even before ~/.codex exists.
func TestDetectViaPATHBinary(t *testing.T) {
	a, _ := newAdapter(t)
	if ok, _ := a.Detect(); ok {
		t.Fatal("expected not detected with empty home and no binary")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	ok, active := a.Detect()
	if !ok {
		t.Fatal("expected detected via codex binary on PATH")
	}
	if !strings.HasSuffix(active, filepath.Join(".codex", "config.toml")) {
		t.Fatalf("unexpected active path %q", active)
	}
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:   "work",
		APIKey:  "sk-test-123",
		BaseURL: "https://proxy.example.com/v1",
		Model:   "gpt-5.5",
	}
}

// TestConfigPathsHonorCodexHome proves the adapter follows the documented
// CODEX_HOME override (wired into the resolver by paths.NewResolver).
func TestConfigPathsHonorCodexHome(t *testing.T) {
	a, _ := newAdapter(t)
	override := t.TempDir()
	a.r.CodexHome = override
	want := []string{
		filepath.Join(override, "config.toml"),
		filepath.Join(override, "auth.json"),
	}
	got := a.ConfigPaths()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ConfigPaths = %v, want %v", got, want)
	}
}

// TestDetect proves the binary-based contract: a leftover ~/.codex dir is NOT
// an installed signal; only a resolvable "codex" binary is.
func TestDetect(t *testing.T) {
	a, home := newAdapter(t)
	if ok, _ := a.Detect(); ok {
		t.Fatal("expected not detected before codex binary is resolvable")
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Detect(); ok {
		t.Fatal("~/.codex present + binary absent must be NOT detected")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	ok, active := a.Detect()
	if !ok {
		t.Fatal("expected detected once codex binary is resolvable")
	}
	if !strings.HasSuffix(active, filepath.Join(".codex", "config.toml")) {
		t.Fatalf("unexpected active path %q", active)
	}
}

func TestStatusTransitions(t *testing.T) {
	a, home := newAdapter(t)
	p := sampleProfile()

	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}

	// Binary resolvable from here so Status reaches the config-reading branch.
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default, got %v", st)
	}

	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want Applied, got %v", st)
	}

	other := p
	other.Model = "gpt-4o"
	if st, _, _ := a.Status(other); st != core.StatusModifiedExternally {
		t.Fatalf("want ModifiedExternally, got %v", st)
	}
}

func TestApplyNewFiles(t *testing.T) {
	a, home := newAdapter(t)
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}

	cfg, err := readTOML(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg["openai_base_url"] != p.BaseURL || cfg["model"] != p.Model {
		t.Fatalf("config not written: %+v", cfg)
	}
	if _, present := cfg[core.MarkerKey]; present {
		t.Fatalf("legacy marker written to config.toml: %+v", cfg)
	}
	marker, ok, err := a.m.Get(a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || marker.Fingerprint != core.Fingerprint(p) {
		t.Fatalf("store marker fingerprint mismatch: %+v ok=%v", marker, ok)
	}

	auth, err := readJSON(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if auth[authKeyName] != p.APIKey {
		t.Fatalf("auth key not written: %+v", auth)
	}
	if auth[authModeKey] != authModeAPIKey {
		t.Fatalf("auth_mode not set to apikey: %+v", auth)
	}
}

func TestApplyPreservesExistingKeys(t *testing.T) {
	a, home := newAdapter(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(codexDir, "config.toml")
	authPath := filepath.Join(codexDir, "auth.json")
	existingCfg := "model_provider = \"openai\"\napproval_policy = \"on-request\"\n\n[mcp_servers.context7]\nenabled = true\n"
	if err := os.WriteFile(cfgPath, []byte(existingCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{\"tokens\":{\"id_token\":\"keep-me\"}}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}

	cfg, err := readTOML(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg["model_provider"] != "openai" || cfg["approval_policy"] != "on-request" {
		t.Fatalf("unrelated top-level keys lost: %+v", cfg)
	}
	mcp, ok := cfg["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_servers table lost: %+v", cfg)
	}
	if _, ok := mcp["context7"]; !ok {
		t.Fatalf("nested mcp server lost: %+v", mcp)
	}
	roundTrip, _ := toml.Marshal(cfg)
	if !strings.Contains(string(roundTrip), "openai_base_url") {
		t.Fatalf("expected openai_base_url in output:\n%s", roundTrip)
	}

	auth, err := readJSON(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := auth["tokens"]; !ok {
		t.Fatalf("auth.json tokens lost: %+v", auth)
	}
	if auth[authModeKey] != authModeAPIKey {
		t.Fatalf("auth_mode not set to apikey: %+v", auth)
	}
}

// writeLegacyConfig writes a config.toml carrying the legacy in-file marker
// for profile p plus a user key, mimicking a pre-store MintSwitch apply.
func writeLegacyConfig(t *testing.T, a *Adapter, p core.Profile) string {
	t.Helper()
	path := a.configPath()
	marker := core.NewMarker(p, p.Label)
	cfg := map[string]any{
		"sandbox_mode":    "read-only",
		"openai_base_url": p.BaseURL,
		"model":           p.Model,
		core.MarkerKey: map[string]any{
			"managed":      marker.Managed,
			"profileLabel": marker.ProfileLabel,
			"fingerprint":  marker.Fingerprint,
			"appliedAt":    marker.AppliedAt.Format(time.RFC3339),
			"version":      marker.Version,
		},
	}
	if err := writeTOML(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStatusDefaultWhenBaseURLRemoved proves a store entry alone does not
// report Applied: when the managed openai_base_url was removed from the file
// (e.g. an external restore/wipe), Status falls back to Default.
func TestStatusDefaultWhenBaseURLRemoved(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.WriteFile(a.configPath(), []byte("model = \"gpt-5.5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default when openai_base_url is gone, got %v", st)
	}
}

// TestApplyStripsLegacyMarker proves an Apply over a legacy-marker config
// removes the table in the same write and records the fresh marker in the
// store, without snapshotting the managed file (backup gate honors the legacy
// marker).
func TestApplyStripsLegacyMarker(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	p := sampleProfile()
	path := writeLegacyConfig(t, a, p)

	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("legacy-managed file must not be backed up, got %q", res.BackupPath)
	}
	cfg, err := readTOML(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg[core.MarkerKey]; ok {
		t.Fatalf("legacy marker not stripped on Apply: %+v", cfg)
	}
	if cfg["sandbox_mode"] != "read-only" {
		t.Fatalf("user key lost: %+v", cfg)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want AppliedByMintSwitch, got %v", st)
	}
}

// TestStripLegacyMarkerMigrates is the startup-sweep case: a config carrying
// the legacy marker gets it removed and migrated into the store even though
// the user never pressed Apply.
func TestStripLegacyMarkerMigrates(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	path := writeLegacyConfig(t, a, p)

	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip: %v", err)
	}
	cfg, err := readTOML(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg[core.MarkerKey]; ok {
		t.Fatalf("legacy marker still in file: %+v", cfg)
	}
	if cfg["sandbox_mode"] != "read-only" || cfg["openai_base_url"] != p.BaseURL {
		t.Fatalf("existing keys lost: %+v", cfg)
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
	stored := core.NewMarker(secondProfile(), "personal")
	if err := a.m.Put(a.ID(), stored); err != nil {
		t.Fatal(err)
	}
	writeLegacyConfig(t, a, sampleProfile())

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
	cfg, err := readTOML(a.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cfg[core.MarkerKey]; present {
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
		t.Fatalf("strip must not create config.toml, stat err=%v", err)
	}

	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := []byte("sandbox_mode = \"read-only\"\n")
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

// TestOrphanStatusAndRestoreWithBackup is the regression for the lost-marker
// gap: with the sidecar marker gone but the pristine backups intact, Status
// must report ModifiedExternally (so the UI offers Restore instead of treating
// the tool as never applied) and Restore must still revert byte-for-byte.
func TestOrphanStatusAndRestoreWithBackup(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	cfgPath, authPath := a.configPath(), a.authPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	originalCfg := "approval_policy = \"never\"\n"
	originalAuth := `{"auth_mode":"chatgpt"}` + "\n"
	if err := os.WriteFile(cfgPath, []byte(originalCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(originalAuth), 0o600); err != nil {
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
	for path, want := range map[string]string{cfgPath: originalCfg, authPath: originalAuth} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s not byte-for-byte restored: %q", path, got)
		}
	}
}

// TestOrphanRestoreNoBackupStrips covers the orphan-no-backup branch: marker
// gone AND backups gone, but the files still carry the full MintSwitch
// signature — Restore must strip the managed keys from both files while
// preserving the user's own settings, and Status must offer Restore
// beforehand.
func TestOrphanRestoreNoBackupStrips(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	cfgPath, authPath := a.configPath(), a.authPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"id_token":"tok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.RemoveAll(a.r.BackupsDir()); err != nil {
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
	want := "No backup found; removed the MintSwitch-managed keys from the Codex config files."
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	cfg, err := readTOML(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cfg["openai_base_url"]; present {
		t.Fatalf("openai_base_url must be stripped: %v", cfg)
	}
	if _, present := cfg["model"]; present {
		t.Fatalf("model must be stripped: %v", cfg)
	}
	if cfg["approval_policy"] != "never" {
		t.Fatalf("user config keys must be preserved: %v", cfg)
	}
	auth, err := readJSON(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := auth[authKeyName]; present {
		t.Fatalf("OPENAI_API_KEY must be stripped: %v", auth)
	}
	if _, present := auth[authModeKey]; present {
		t.Fatalf("auth_mode must be stripped: %v", auth)
	}
	if _, present := auth["tokens"]; !present {
		t.Fatalf("user auth keys must be preserved: %v", auth)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after strip = %v, want Default", st)
	}
}

// TestPureUserConfigNeverOrphan proves the no-false-positive contract: a
// hand-written config that never saw Apply — even one carrying a proxy
// openai_base_url + model with an API-key login under a ChatGPT auth_mode —
// stays Default (no Restore button) and Restore leaves both files
// byte-for-byte untouched.
func TestPureUserConfigNeverOrphan(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	cfgPath, authPath := a.configPath(), a.authPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	userCfg := "model = \"gpt-5.5\"\nopenai_base_url = \"https://my-proxy.example.com/v1\"\n"
	userAuth := `{"OPENAI_API_KEY":"sk-user","auth_mode":"chatgpt"}`
	if err := os.WriteFile(cfgPath, []byte(userCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(userAuth), 0o600); err != nil {
		t.Fatal(err)
	}

	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("pure user config status = %v, want Default", st)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for path, want := range map[string]string{cfgPath: userCfg, authPath: userAuth} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("pure user file %s rewritten: %q", path, got)
		}
	}
}
