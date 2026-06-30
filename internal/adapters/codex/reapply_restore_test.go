package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mintswitch/internal/core"
)

// secondProfile returns a profile distinct from sampleProfile so a second Apply
// changes every managed value.
func secondProfile() core.Profile {
	p := sampleProfile()
	p.Label = "personal"
	p.APIKey = "sk-second-secret"
	p.BaseURL = "https://second.example.com/v1"
	p.Model = "gpt-6"
	return p
}

// TestReApplyThenRestoreReturnsToPristine is the regression for the re-Apply
// backup blocker: a second Apply must not snapshot the already-managed files, so
// Restore returns config.toml and auth.json byte-for-byte to their pristine
// pre-MintSwitch state.
func TestReApplyThenRestoreReturnsToPristine(t *testing.T) {
	a, home := newAdapter(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath, authPath := a.configPath(), a.authPath()
	origCfg := []byte("model = \"original\"\nother = \"keep\"\n")
	origAuth := []byte("{\"OTHER\":\"keep\"}")
	if err := os.WriteFile(cfgPath, origCfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, origAuth, 0o600); err != nil {
		t.Fatal(err)
	}

	p1, p2 := sampleProfile(), secondProfile()
	if _, err := a.Apply(p1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(p2); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}

	gotCfg, _ := os.ReadFile(cfgPath)
	gotAuth, _ := os.ReadFile(authPath)
	if string(gotCfg) != string(origCfg) {
		t.Fatalf("config.toml not restored to pristine: %q", gotCfg)
	}
	if string(gotAuth) != string(origAuth) {
		t.Fatalf("auth.json not restored to pristine: %q", gotAuth)
	}
	if strings.Contains(string(gotAuth), p1.APIKey) || strings.Contains(string(gotAuth), p2.APIKey) {
		t.Fatalf("api key leaked into restored auth.json: %q", gotAuth)
	}
	if strings.Contains(string(gotCfg), core.MarkerKey) {
		t.Fatalf("marker leaked into restored config.toml: %q", gotCfg)
	}
	if st, _, err := a.Status(p2); err != nil || st != core.StatusDefault {
		t.Fatalf("status = %v (err %v), want default", st, err)
	}
}

// TestReApplyThenRestoreAbsentFiles covers the originally-absent case: after two
// Applies, Restore must remove both adapter-created files.
func TestReApplyThenRestoreAbsentFiles(t *testing.T) {
	a, home := newAdapter(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath, authPath := a.configPath(), a.authPath()

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(secondProfile()); err != nil {
		t.Fatal(err)
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
