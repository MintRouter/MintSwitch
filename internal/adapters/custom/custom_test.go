package custom

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

func newAdapter(t *testing.T, def core.CustomToolDef) (*Adapter, *paths.Resolver) {
	t.Helper()
	r := &paths.Resolver{Home: t.TempDir(), DataDir: t.TempDir()}
	a := New(def, r, backup.NewEngine(r.BackupsDir()))
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return a, r
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:   "work",
		APIKey:  `sk-"quoted"\back`,
		BaseURL: "https://router.example.com/v1",
		Model:   "gpt-mint",
	}
}

func nestedDef(home string) core.CustomToolDef {
	return core.CustomToolDef{
		ID:         "acme",
		Name:       "Acme CLI",
		ConfigPath: "~/.config/acme/config.json",
		Template: `{
			"env": {"KEY": "${API_KEY}", "URL": "${BASE_URL}"},
			"models": ["${MODEL}", "static-model"],
			"nested": {"deep": [{"m": "${MODEL}"}]},
			"literal": "API_KEY"
		}`,
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

func TestIDNameAndPathExpansion(t *testing.T) {
	a, r := newAdapter(t, nestedDef(""))
	if a.ID() != "acme" || a.Name() != "Acme CLI" {
		t.Fatalf("id/name: %q %q", a.ID(), a.Name())
	}
	want := filepath.Join(r.Home, ".config", "acme", "config.json")
	if got := a.configPath(); got != want {
		t.Fatalf("configPath = %q, want %q", got, want)
	}
}

// TestDetectBinaryOptional: empty BinaryName => always installed; a set
// BinaryName => resolved like a built-in.
func TestDetectBinaryOptional(t *testing.T) {
	a, _ := newAdapter(t, nestedDef(""))
	if installed, _ := a.Detect(); !installed {
		t.Fatal("config-only tool must always be installed")
	}

	def := nestedDef("")
	def.BinaryName = "acme"
	b, _ := newAdapter(t, def)
	if installed, _ := b.Detect(); installed {
		t.Fatal("binary-based tool with absent binary must be NOT installed")
	}
	b.lookPath = func(string) (string, error) { return "/usr/local/bin/acme", nil }
	if installed, _ := b.Detect(); !installed {
		t.Fatal("binary-based tool with resolvable binary must be installed")
	}
}

func TestApplyDeepWalkAndStatus(t *testing.T) {
	a, _ := newAdapter(t, nestedDef(""))
	p := sampleProfile()
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("missing file => StatusDefault, got %v", st)
	}
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, res.ChangedPath)
	env := root["env"].(map[string]any)
	if env["KEY"] != p.APIKey {
		t.Fatalf("API_KEY not substituted (quotes/backslashes): %v", env["KEY"])
	}
	if env["URL"] != p.BaseURL {
		t.Fatalf("BASE_URL not substituted: %v", env["URL"])
	}
	models := root["models"].([]any)
	if models[0] != p.Model || models[1] != "static-model" {
		t.Fatalf("nested-array MODEL not substituted: %v", models)
	}
	deep := root["nested"].(map[string]any)["deep"].([]any)[0].(map[string]any)
	if deep["m"] != p.Model {
		t.Fatalf("deeply nested MODEL not substituted: %v", deep)
	}
	if root["literal"] != "API_KEY" {
		t.Fatalf("non-exact placeholder must be untouched: %v", root["literal"])
	}
	if _, ok := root[core.MarkerKey]; !ok {
		t.Fatal("marker not injected at root")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(res.ChangedPath)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("perms = %o, want 600", info.Mode().Perm())
		}
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
