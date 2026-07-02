package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"mintswitch/internal/core"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nope", "settings.json"))
	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if st == nil || st.ActiveProfile != nil {
		t.Fatalf("expected empty state, got %+v", st)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	s := NewStore(path)

	in := &State{
		ActiveProfile: &core.Profile{
			Label: "work", APIKey: "sk-secret", BaseURL: "https://h", Model: "m", SmallFastModel: "sf",
		},
	}

	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.ActiveProfile == nil || !reflect.DeepEqual(*out.ActiveProfile, *in.ActiveProfile) {
		t.Fatalf("profile round-trip mismatch: %+v vs %+v", out.ActiveProfile, in.ActiveProfile)
	}
}

func TestMCPFieldsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	in := &State{MCPKey: "sk-mcp-secret", MCPEndpoint: "https://custom.example/mcp"}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.MCPKey != in.MCPKey || out.MCPEndpoint != in.MCPEndpoint {
		t.Fatalf("mcp fields round-trip mismatch: %+v", out)
	}
}

func TestSaveAtomicAndPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	s := NewStore(path)

	if err := s.Save(&State{}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("perms = %o, want 600", perm)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "settings.json" {
			t.Fatalf("leftover temp file: %q", e.Name())
		}
	}
}

// TestLoadIgnoresLegacyFields proves a settings file written by an older
// version that still carried the removed custom_tools field loads without
// error and keeps every remaining field intact (unknown JSON keys are ignored).
func TestLoadIgnoresLegacyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := `{
  "active_profile": {"label": "work", "api_key": "sk-secret", "base_url": "https://h", "model": "m"},
  "custom_tools": [
    {"id": "acme-cli", "name": "Acme CLI", "config_path": "~/.config/acme/config.json", "template": "{}"}
  ]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy settings: %v", err)
	}
	out, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load legacy settings: %v", err)
	}
	want := core.Profile{Label: "work", APIKey: "sk-secret", BaseURL: "https://h", Model: "m"}
	if out.ActiveProfile == nil || !reflect.DeepEqual(*out.ActiveProfile, want) {
		t.Fatalf("active profile not preserved from legacy file: %+v", out.ActiveProfile)
	}
}

// TestSaveLoadToolModelsRoundTrip proves the per-tool model selections persist
// alongside the active profile and survive a save+reload unchanged.
func TestSaveLoadToolModelsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	s := NewStore(path)

	in := &State{
		ActiveProfile: &core.Profile{
			Label: "work", APIKey: "sk-secret", BaseURL: "https://h", Model: "m",
			Models: []string{"m", "m2"},
		},
		ToolModels: map[string]string{
			"claude-code": "m2",
			"codex":       "m",
		},
	}

	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(out.ToolModels, in.ToolModels) {
		t.Fatalf("tool models round-trip mismatch:\n got %+v\nwant %+v", out.ToolModels, in.ToolModels)
	}
}

func TestSaveNilState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewStore(path)
	if err := s.Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveProfile != nil {
		t.Fatal("expected empty state from nil save")
	}
}
