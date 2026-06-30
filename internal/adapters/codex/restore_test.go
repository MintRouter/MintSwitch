package codex

import (
	"os"
	"path/filepath"
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
