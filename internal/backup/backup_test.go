package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	e := NewEngine(root)

	src := filepath.Join(work, "config.json")
	original := []byte("{\n  \"a\": 1,\n  \"b\": [2,3]\n}\n\x00\xff")
	if err := os.WriteFile(src, original, 0o644); err != nil {
		t.Fatal(err)
	}

	bpath, err := e.Backup(src)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if bpath == "" {
		t.Fatal("Backup returned empty path")
	}

	if err := os.WriteFile(src, []byte("MODIFIED"), 0o644); err != nil {
		t.Fatal(err)
	}

	restored, used, err := e.RestoreLatest(src)
	if err != nil {
		t.Fatalf("RestoreLatest: %v", err)
	}
	if !restored || used == "" {
		t.Fatalf("expected restore, got restored=%v used=%q", restored, used)
	}
	got, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("round-trip not byte-for-byte: got %q want %q", got, original)
	}
}

func TestBackupAbsentThenRestoreDeletes(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	e := NewEngine(root)

	src := filepath.Join(work, "created.json")
	bpath, err := e.Backup(src) // source missing -> absent marker
	if err != nil {
		t.Fatalf("Backup absent: %v", err)
	}
	if filepath.Ext(bpath) != ".absent" {
		t.Fatalf("expected .absent entry, got %q", bpath)
	}

	// Adapter then creates the file.
	if err := os.WriteFile(src, []byte("created by adapter"), 0o644); err != nil {
		t.Fatal(err)
	}

	restored, _, err := e.RestoreLatest(src)
	if err != nil {
		t.Fatalf("RestoreLatest: %v", err)
	}
	if !restored {
		t.Fatal("expected restored=true for absent marker")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, stat err = %v", err)
	}
}

func TestRestoreNoBackupIsNoOp(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	e := NewEngine(root)
	src := filepath.Join(work, "never-backed-up.json")

	has, err := e.HasBackup(src)
	if err != nil || has {
		t.Fatalf("HasBackup = %v, %v", has, err)
	}

	restored, used, err := e.RestoreLatest(src)
	if err != nil {
		t.Fatalf("RestoreLatest: %v", err)
	}
	if restored || used != "" {
		t.Fatalf("expected no-op, got restored=%v used=%q", restored, used)
	}
}

func TestLatestPicksMostRecent(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	e := NewEngine(root)
	src := filepath.Join(work, "c.json")

	for i, content := range []string{"v1", "v2", "v3"} {
		if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Backup(src); err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
	}
	if err := os.WriteFile(src, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.RestoreLatest(src); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(src)
	if string(got) != "v3" {
		t.Fatalf("expected latest v3, got %q", got)
	}
}
