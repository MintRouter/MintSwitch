package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mintswitch/internal/core"
)

func TestRestoreRevertsExistingFiles(t *testing.T) {
	a, home := newAdapter(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(codexDir, "config.toml")
	authPath := filepath.Join(codexDir, "auth.json")
	origCfg := []byte("model = \"original\"\n")
	origAuth := []byte("{\"OPENAI_API_KEY\":\"sk-original\"}")
	if err := os.WriteFile(cfgPath, origCfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, origAuth, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}

	gotCfg, _ := os.ReadFile(cfgPath)
	gotAuth, _ := os.ReadFile(authPath)
	if string(gotCfg) != string(origCfg) {
		t.Fatalf("config.toml not reverted byte-for-byte: %q", gotCfg)
	}
	if string(gotAuth) != string(origAuth) {
		t.Fatalf("auth.json not reverted byte-for-byte: %q", gotAuth)
	}
}

func TestRestoreDeletesCreatedFiles(t *testing.T) {
	a, home := newAdapter(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(codexDir, "config.toml")
	authPath := filepath.Join(codexDir, "auth.json")

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("expected config.toml created: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("expected config.toml removed, got %v", err)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("expected auth.json removed, got %v", err)
	}
}

func TestRestoreNoBackupNoOp(t *testing.T) {
	a, _ := newAdapter(t)
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore with no backup should be safe no-op: %v", err)
	}
}

// removeBackupDirs deletes every per-path backup dir under root whose name
// contains substr, simulating a missing/deleted backup for that file.
func removeBackupDirs(t *testing.T, root, substr string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, en := range entries {
		if en.IsDir() && strings.Contains(en.Name(), substr) {
			if err := os.RemoveAll(filepath.Join(root, en.Name())); err != nil {
				t.Fatal(err)
			}
			removed = true
		}
	}
	if !removed {
		t.Fatalf("no backup dir matching %q under %s", substr, root)
	}
}

func TestRestoreAuthBackupMissingWarnsInMessage(t *testing.T) {
	a, home := newAdapter(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(codexDir, "config.toml")
	authPath := filepath.Join(codexDir, "auth.json")
	origCfg := []byte("model = \"original\"\n")
	if err := os.WriteFile(cfgPath, origCfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{\"OTHER\":\"keep\"}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	removeBackupDirs(t, filepath.Join(home, "data", "backups"), "auth.json")

	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore should still succeed for config.toml: %v", err)
	}
	gotCfg, _ := os.ReadFile(cfgPath)
	if string(gotCfg) != string(origCfg) {
		t.Fatalf("config.toml not reverted: %q", gotCfg)
	}
	if !strings.Contains(res.Message, "auth.json") || !strings.Contains(res.Message, "API key") {
		t.Fatalf("message must warn that the API key may remain in auth.json, got %q", res.Message)
	}
}

func TestRestoreBestEffortWhenConfigRestoreFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based failure injection is ineffective as root")
	}
	a, home := newAdapter(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(codexDir, "config.toml")
	authPath := filepath.Join(codexDir, "auth.json")
	origAuth := []byte("{\"OPENAI_API_KEY\":\"sk-original\"}")
	if err := os.WriteFile(cfgPath, []byte("model = \"original\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, origAuth, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := a.Apply(sampleProfile())
	if err != nil {
		t.Fatal(err)
	}
	if res.BackupPath == "" {
		t.Fatal("expected a config.toml backup entry from Apply")
	}
	if err := os.Chmod(res.BackupPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(res.BackupPath, 0o600) })

	_, rerr := a.Restore()
	if rerr == nil {
		t.Fatal("expected error when config.toml backup is unreadable")
	}
	if !strings.Contains(rerr.Error(), "config.toml") {
		t.Fatalf("error must name the failing file, got %q", rerr)
	}
	gotAuth, _ := os.ReadFile(authPath)
	if string(gotAuth) != string(origAuth) {
		t.Fatalf("auth.json should still be restored best-effort: %q", gotAuth)
	}
}

func TestApplyIdempotent(t *testing.T) {
	a, home := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	first, _ := os.ReadFile(cfgPath)

	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	cfg, err := readTOML(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg["openai_base_url"] != p.BaseURL || cfg["model"] != p.Model {
		t.Fatalf("re-apply changed managed values: %+v", cfg)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want Applied after re-apply, got %v", st)
	}
	// Marker timestamp may differ; ensure no key duplication/explosion.
	second, _ := os.ReadFile(cfgPath)
	if len(second) > len(first)*2 {
		t.Fatalf("config grew unexpectedly on re-apply: %d -> %d", len(first), len(second))
	}
}

func TestApplyInvalidProfile(t *testing.T) {
	a, _ := newAdapter(t)
	if _, err := a.Apply(core.Profile{}); err == nil {
		t.Fatal("expected validation error for empty profile")
	}
}

// TestRestoreIgnoresContaminatedNewerBackup is the regression for dirty
// backups: snapshots taken AFTER the files were already MintSwitch-managed
// must be ignored — Restore reverts both files to their oldest, pristine
// entries and prunes every backup so the contaminated ones never resurface.
func TestRestoreIgnoresContaminatedNewerBackup(t *testing.T) {
	a, home := newAdapter(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath, authPath := a.configPath(), a.authPath()
	origCfg := []byte("model = \"original\"\n")
	origAuth := []byte("{\"OPENAI_API_KEY\":\"sk-original\"}")
	if err := os.WriteFile(cfgPath, origCfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, origAuth, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	// Contaminated snapshots of the now-managed files.
	if _, err := a.e.Backup(cfgPath); err != nil {
		t.Fatal(err)
	}
	if _, err := a.e.Backup(authPath); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	gotCfg, _ := os.ReadFile(cfgPath)
	gotAuth, _ := os.ReadFile(authPath)
	if string(gotCfg) != string(origCfg) {
		t.Fatalf("config.toml restore used contaminated backup: %q", gotCfg)
	}
	if string(gotAuth) != string(origAuth) {
		t.Fatalf("auth.json restore used contaminated backup: %q", gotAuth)
	}
	for _, p := range []string{cfgPath, authPath} {
		if has, err := a.e.HasBackup(p); err != nil || has {
			t.Fatalf("backups for %s must be pruned after restore, HasBackup = %v, %v", p, has, err)
		}
	}
}

// TestRestoreNoBackupStripsManagedKeys is the regression for the missing
// backup fallback: with the backups dir deleted, Restore must surgically
// strip the MintSwitch-managed keys from both files while preserving the
// user's own keys.
func TestRestoreNoBackupStripsManagedKeys(t *testing.T) {
	a, home := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath, authPath := a.configPath(), a.authPath()
	if err := os.WriteFile(cfgPath, []byte("other = \"keep\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{\"OTHER\":\"keep\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, "data", "backups")); err != nil {
		t.Fatal(err)
	}

	res, err := a.Restore()
	if err != nil {
		t.Fatal(err)
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
	if cfg["other"] != "keep" {
		t.Fatalf("user config key must be preserved: %v", cfg)
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
	if auth["OTHER"] != "keep" {
		t.Fatalf("user auth key must be preserved: %v", auth)
	}
	if !strings.Contains(res.Message, "removed") {
		t.Fatalf("message must report the strip, got %q", res.Message)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after strip = %v, want Default", st)
	}
}
