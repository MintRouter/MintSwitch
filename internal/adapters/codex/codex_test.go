package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"mintconfig/internal/backup"
	"mintconfig/internal/core"
	"mintconfig/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	return New(r, backup.NewEngine(r.BackupsDir())), home
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:   "work",
		APIKey:  "sk-test-123",
		BaseURL: "https://proxy.example.com/v1",
		Model:   "gpt-5.5",
	}
}

func TestDetect(t *testing.T) {
	a, home := newAdapter(t)
	if ok, _ := a.Detect(); ok {
		t.Fatal("expected not detected before ~/.codex exists")
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	ok, active := a.Detect()
	if !ok {
		t.Fatal("expected detected after ~/.codex created")
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

	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default, got %v", st)
	}

	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintConfig {
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
