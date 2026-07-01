package antigravity

import (
	"os"
	"path/filepath"
	"testing"

	"mintswitch/internal/core"
)

func TestApplyPreservesExistingKeys(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{"editor.fontSize":14,"antigravity.telemetry":false}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	root := readJSON(t, path)
	if root["editor.fontSize"] != float64(14) || root["antigravity.telemetry"] != false {
		t.Fatalf("unrelated keys not preserved: %v", root)
	}
	if root["antigravity.ai.provider"] != "openai-compatible" {
		t.Fatalf("provider key missing: %v", root)
	}
}

func TestRestoreDeletesCreatedFile(t *testing.T) {
	a, _ := newAdapter(t)
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

func TestRestoreRevertsExisting(t *testing.T) {
	a, _ := newAdapter(t)
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"editor.fontSize":14}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
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

func TestReApplyIdempotentSingleBackup(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply1: %v", err)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply2: %v", err)
	}
	// After two applies over an initially-absent file, Restore must remove the
	// file (backup taken only on the first, pristine apply).
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(a.configPath()); !os.IsNotExist(err) {
		t.Fatalf("expected file removed after restore, got err=%v", err)
	}
}

func TestRestoreNoBackupNoOp(t *testing.T) {
	a, _ := newAdapter(t)
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected no backup path, got %q", res.BackupPath)
	}
	_ = core.MarkerKey
}
