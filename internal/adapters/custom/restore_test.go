package custom

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mintswitch/internal/core"
)

func TestApplyRejectsNonObjectTemplate(t *testing.T) {
	for _, tmpl := range []string{`["a","b"]`, `"scalar"`, `123`, `not json`} {
		def := nestedDef("")
		def.Template = tmpl
		a, _ := newAdapter(t, def)
		if _, err := a.Apply(sampleProfile()); err == nil {
			t.Fatalf("expected error for non-object template %q", tmpl)
		}
	}
}

func TestApplyValidatesProfile(t *testing.T) {
	a, _ := newAdapter(t, nestedDef(""))
	if _, err := a.Apply(core.Profile{}); err == nil {
		t.Fatal("expected validation error for empty profile")
	}
}

func TestRestoreDeletesCreatedFile(t *testing.T) {
	a, _ := newAdapter(t, nestedDef(""))
	path := a.configPath()
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file created: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, got err=%v", err)
	}
}

// TestRestoreRevertsExistingWithKeys proves the marker is injected into a root
// that already has keys and that Restore reverts the original byte-for-byte.
func TestRestoreRevertsExistingWithKeys(t *testing.T) {
	a, _ := newAdapter(t, nestedDef(""))
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"preexisting":true,"keep":"me"}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, path)
	if _, ok := root[core.MarkerKey]; !ok {
		t.Fatal("marker not injected into root with existing keys")
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("not byte-for-byte restored: %q", got)
	}
}

func TestReApplyBacksUpOnlyOnce(t *testing.T) {
	a, _ := newAdapter(t, nestedDef(""))
	p := sampleProfile()
	res1, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply1: %v", err)
	}
	if res1.BackupPath == "" {
		t.Fatal("first apply should create a backup (absent marker)")
	}
	res2, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply2: %v", err)
	}
	if res2.BackupPath != "" {
		t.Fatalf("re-apply over managed file must not back up again: %q", res2.BackupPath)
	}
	// Restore must still revert to the pristine (absent) state.
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(a.configPath()); !os.IsNotExist(err) {
		t.Fatal("expected file removed back to pristine absent state")
	}
}

func TestStatusBinaryNotInstalled(t *testing.T) {
	def := nestedDef("")
	def.BinaryName = "acme"
	a, _ := newAdapter(t, def)
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusNotInstalled {
		t.Fatalf("expected NotInstalled when binary absent, got %v", st)
	}
}

func TestRestoreNoBackupNoOp(t *testing.T) {
	a, _ := newAdapter(t, nestedDef(""))
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected no backup path, got %q", res.BackupPath)
	}
}
