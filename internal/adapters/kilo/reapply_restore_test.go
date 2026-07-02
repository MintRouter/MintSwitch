package kilo

import (
	"os"
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
	p.Model = "gpt-second"
	return p
}

// TestReApplyThenRestoreReturnsToPristine is the regression for the re-Apply
// backup blocker: a second Apply must not snapshot the already-managed file, so
// Restore returns the config byte-for-byte to its pristine pre-MintSwitch state.
func TestReApplyThenRestoreReturnsToPristine(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	path := a.jsonPath()
	pristine := "{\n  \"theme\": \"kilo\",\n  \"model\": \"existing/model\"\n}\n"
	writeFile(t, path, pristine)

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
	if string(got) != pristine {
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
	path := a.jsonPath()

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

// TestRestoreRevertsJSONCTarget proves Restore finds the backup when Apply
// managed kilo.jsonc (the active file), reverting it byte-for-byte.
func TestRestoreRevertsJSONCTarget(t *testing.T) {
	a, _ := newAdapter(t)
	pristine := `{"theme":"kilo"}` + "\n"
	writeFile(t, a.jsoncPath(), pristine)
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(a.jsoncPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != pristine {
		t.Fatalf("kilo.jsonc not restored: %q", got)
	}
}
