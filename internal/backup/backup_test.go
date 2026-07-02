package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
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

func TestRestorePristinePicksOldestAndPrunes(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	e := NewEngine(root)
	src := filepath.Join(work, "c.json")

	for i, content := range []string{"pristine", "contaminated-v2", "contaminated-v3"} {
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

	restored, used, err := e.RestorePristine(src)
	if err != nil {
		t.Fatalf("RestorePristine: %v", err)
	}
	if !restored || used == "" {
		t.Fatalf("expected restore, got restored=%v used=%q", restored, used)
	}
	got, _ := os.ReadFile(src)
	if string(got) != "pristine" {
		t.Fatalf("expected oldest pristine content, got %q", got)
	}

	// All entries for src must be pruned after a successful restore.
	has, err := e.HasBackup(src)
	if err != nil || has {
		t.Fatalf("expected backups pruned, HasBackup = %v, %v", has, err)
	}
}

func TestRestorePristineAbsentMarkerDeletesAndPrunes(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	e := NewEngine(root)
	src := filepath.Join(work, "created.json")

	if _, err := e.Backup(src); err != nil { // missing -> oldest is .absent
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("managed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Backup(src); err != nil { // contaminated later snapshot
		t.Fatal(err)
	}

	restored, _, err := e.RestorePristine(src)
	if err != nil {
		t.Fatalf("RestorePristine: %v", err)
	}
	if !restored {
		t.Fatal("expected restored=true for absent marker")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, stat err = %v", err)
	}
	has, err := e.HasBackup(src)
	if err != nil || has {
		t.Fatalf("expected backups pruned, HasBackup = %v, %v", has, err)
	}
}

func TestRestorePristineNoBackupIsNoOp(t *testing.T) {
	e := NewEngine(t.TempDir())
	src := filepath.Join(t.TempDir(), "never.json")
	restored, used, err := e.RestorePristine(src)
	if err != nil {
		t.Fatalf("RestorePristine: %v", err)
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

// TestRestorePristinePruneFailureReturnsRestoredAndErr pins the documented
// contract: when the post-restore prune fails, RestorePristine reports
// restored=true AND a non-nil error, and a later retry (after the prune
// obstacle clears) restores the same pristine entry and prunes cleanly.
func TestRestorePristinePruneFailureReturnsRestoredAndErr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based prune failure not portable to windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	work := t.TempDir()
	e := NewEngine(root)

	src := filepath.Join(work, "config.json")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Backup(src); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := os.WriteFile(src, []byte("MODIFIED"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := e.dirFor(src)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	restored, entry, err := e.RestorePristine(src)
	if !restored || entry == "" {
		t.Fatalf("expected restored=true with entry, got restored=%v entry=%q", restored, entry)
	}
	if err == nil {
		t.Fatal("expected prune error, got nil")
	}
	got, rerr := os.ReadFile(src)
	if rerr != nil || string(got) != "original" {
		t.Fatalf("file must be restored despite prune error, got %q, %v", got, rerr)
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("MODIFIED-AGAIN"), 0o644); err != nil {
		t.Fatal(err)
	}
	restored, _, err = e.RestorePristine(src)
	if err != nil || !restored {
		t.Fatalf("retry must restore cleanly, got restored=%v err=%v", restored, err)
	}
	got, _ = os.ReadFile(src)
	if string(got) != "original" {
		t.Fatalf("retry restored %q, want original", got)
	}
	if has, herr := e.HasBackup(src); herr != nil || has {
		t.Fatalf("expected backups pruned after retry, HasBackup = %v, %v", has, herr)
	}
}

// TestDirForReusesLegacyEightCharDir proves backups written by older versions
// (8-hex-char hash suffix) stay discoverable: dirFor keeps using an existing
// legacy dir for both restore and new backups, while a fresh path gets a
// 16-hex-char dir.
func TestDirForReusesLegacyEightCharDir(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	e := NewEngine(root)

	src := filepath.Join(work, "config.json")
	clean := filepath.Clean(src)
	sum := sha256.Sum256([]byte(clean))
	full := hex.EncodeToString(sum[:])
	legacy := filepath.Join(root, sanitize(clean)+"-"+full[:8])
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(legacy, "20240101T000000.000000000"+contentSuffix)
	if err := os.WriteFile(entry, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := e.dirFor(src); got != legacy {
		t.Fatalf("dirFor = %q, want legacy dir %q", got, legacy)
	}
	if err := os.WriteFile(src, []byte("MODIFIED"), 0o644); err != nil {
		t.Fatal(err)
	}
	restored, used, err := e.RestoreLatest(src)
	if err != nil || !restored || used != entry {
		t.Fatalf("RestoreLatest = %v, %q, %v; want restore from %q", restored, used, err, entry)
	}
	got, _ := os.ReadFile(src)
	if string(got) != "original" {
		t.Fatalf("restored %q, want original", got)
	}

	bpath, err := e.Backup(src)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if filepath.Dir(bpath) != legacy {
		t.Fatalf("new backup went to %q, want legacy dir %q", filepath.Dir(bpath), legacy)
	}

	fresh := filepath.Join(work, "other.json")
	freshClean := filepath.Clean(fresh)
	freshSum := sha256.Sum256([]byte(freshClean))
	want := filepath.Join(root, sanitize(freshClean)+"-"+hex.EncodeToString(freshSum[:])[:16])
	if got := e.dirFor(fresh); got != want {
		t.Fatalf("dirFor(fresh) = %q, want 16-char dir %q", got, want)
	}
}
