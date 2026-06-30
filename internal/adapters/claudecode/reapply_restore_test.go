package claudecode

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
	p.BaseURL = "https://second.example.com"
	p.Model = "anthropic/claude-sonnet-4-5"
	return p
}

// TestReApplyThenRestoreReturnsToPristine is the regression for the re-Apply
// backup blocker: a second Apply must not snapshot the already-managed file, so
// Restore returns the config byte-for-byte to its pristine pre-MintSwitch state.
func TestReApplyThenRestoreReturnsToPristine(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	pristine := []byte("{\n  \"theme\": \"dark\",\n  \"env\": {\n    \"EXISTING\": \"keep\"\n  }\n}\n")
	if err := os.WriteFile(path, pristine, 0o600); err != nil {
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

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(got) != string(pristine) {
		t.Fatalf("not restored to pristine bytes:\n got: %q\nwant: %q", got, pristine)
	}
	if strings.Contains(string(got), p1.APIKey) || strings.Contains(string(got), p2.APIKey) {
		t.Fatalf("api key leaked into restored config: %q", got)
	}
	if strings.Contains(string(got), core.MarkerKey) {
		t.Fatalf("marker leaked into restored config: %q", got)
	}
	if st, _, err := a.Status(p2); err != nil || st != core.StatusDefault {
		t.Fatalf("status = %v (err %v), want default", st, err)
	}
}

// TestReApplyThenRestoreAbsentFile covers the originally-absent case: after two
// Applies, Restore must remove the adapter-created file.
func TestReApplyThenRestoreAbsentFile(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.settingsPath()

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(secondProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file removed after restore, got %v", err)
	}
}
