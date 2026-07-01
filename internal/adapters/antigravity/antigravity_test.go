package antigravity

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	data := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: data}
	a := New(r, backup.NewEngine(r.BackupsDir()))
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
	if a.ID() != "antigravity" || a.Name() != "Antigravity" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
	if paths := a.ConfigPaths(); len(paths) != 1 ||
		paths[0] != a.r.Join(".antigravity", "settings.json") {
		t.Fatalf("unexpected config paths: %v", paths)
	}
}

// TestDetect proves install detection via binary and via either config dir.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed with empty home")
	}
	if err := os.MkdirAll(r.Join(".antigravity"), 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, path := a.Detect(); !installed || path != a.configPath() {
		t.Fatalf("expected installed via ~/.antigravity dir, got %v %q", installed, path)
	}

	a2, r2 := newAdapter(t)
	if err := os.MkdirAll(r2.Join(".gemini", "antigravity"), 0o700); err != nil {
		t.Fatal(err)
	}
	if installed, _ := a2.Detect(); !installed {
		t.Fatal("expected installed via ~/.gemini/antigravity dir")
	}

	a3, _ := newAdapter(t)
	a3.lookPath = func(string) (string, error) { return "/usr/local/bin/agy", nil }
	if installed, _ := a3.Detect(); !installed {
		t.Fatal("expected installed via agy binary")
	}
}

func TestApplyStatusAndSecrecy(t *testing.T) {
	a, r := newAdapter(t)
	p := sampleProfile()
	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("expected NotInstalled, got %v", st)
	}
	if err := os.MkdirAll(r.Join(".antigravity"), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, res.ChangedPath)
	checks := map[string]string{
		"antigravity.ai.provider":                      "openai-compatible",
		"antigravity.openai-compatible.endpoint":       p.BaseURL,
		"antigravity.openai-compatible.apiKey":         p.APIKey,
		"antigravity.ai.model":                         p.Model,
		"antigravity.agent.provider":                   "openai-compatible",
		"antigravity.agent.openai-compatible.endpoint": p.BaseURL,
		"antigravity.agent.openai-compatible.apiKey":   p.APIKey,
		"antigravity.agent.model":                      p.Model,
	}
	for k, want := range checks {
		if root[k] != want {
			t.Fatalf("key %q = %v, want %v", k, root[k], want)
		}
	}
	fi, err := os.Stat(res.ChangedPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perms, got %v", fi.Mode().Perm())
	}
	if st, detail, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch, got %v", st)
	} else if strings.Contains(detail, p.APIKey) {
		t.Fatal("API key leaked into status detail")
	}
	other := sampleProfile()
	other.Model = "different"
	if st, _, _ := a.Status(other); st != core.StatusModifiedExternally {
		t.Fatalf("expected ModifiedExternally, got %v", st)
	}
}
