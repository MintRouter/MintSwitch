package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	a := New(r, backup.NewEngine(r.BackupsDir()))
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
	if fp, ok := markerFingerprint(cfg); !ok || fp != core.Fingerprint(p) {
		t.Fatalf("marker fingerprint mismatch: %q ok=%v", fp, ok)
	}

	auth, err := readJSON(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if auth[authKeyName] != p.APIKey {
		t.Fatalf("auth key not written: %+v", auth)
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
}
