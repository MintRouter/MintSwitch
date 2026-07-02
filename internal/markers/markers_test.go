package markers

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"mintswitch/internal/core"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "data", "markers.json"))
}

func sampleMarker() core.Marker {
	return core.Marker{
		Managed:      true,
		ProfileLabel: "work",
		Fingerprint:  "abc123",
		AppliedAt:    time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
		Version:      core.MarkerVersion,
	}
}

// TestGetMissingFile proves a store whose backing file does not exist reports
// "no marker" without error and without creating the file.
func TestGetMissingFile(t *testing.T) {
	s := newStore(t)
	_, ok, err := s.Get("claude-code")
	if err != nil || ok {
		t.Fatalf("Get on missing file = ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("Get must not create the store file, stat err=%v", err)
	}
}

// TestPutGetDeleteRoundtrip covers the full lifecycle across independent tool
// IDs, including persistence across a fresh Store over the same file.
func TestPutGetDeleteRoundtrip(t *testing.T) {
	s := newStore(t)
	m := sampleMarker()
	if err := s.Put("claude-code", m); err != nil {
		t.Fatalf("Put: %v", err)
	}
	other := m
	other.ProfileLabel = "personal"
	if err := s.Put("codex", other); err != nil {
		t.Fatalf("Put codex: %v", err)
	}

	got, ok, err := NewStore(s.Path()).Get("claude-code")
	if err != nil || !ok {
		t.Fatalf("Get after reopen = ok=%v err=%v", ok, err)
	}
	if got != m {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", got, m)
	}

	if err := s.Delete("claude-code"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get("claude-code"); ok {
		t.Fatal("entry still present after Delete")
	}
	if _, ok, _ := s.Get("codex"); !ok {
		t.Fatal("Delete removed an unrelated tool's entry")
	}
}

// TestPutOverwrites proves Put replaces a prior entry for the same tool.
func TestPutOverwrites(t *testing.T) {
	s := newStore(t)
	first := sampleMarker()
	second := first
	second.Fingerprint = "def456"
	if err := s.Put("claude-code", first); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("claude-code", second); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("claude-code")
	if err != nil || !ok || got.Fingerprint != "def456" {
		t.Fatalf("Get = %+v ok=%v err=%v, want overwritten fingerprint", got, ok, err)
	}
}

// TestFilePermissions proves the store file is written 0600 (it marks managed
// state and should stay private like the rest of the data dir).
func TestFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits not meaningful on windows")
	}
	s := newStore(t)
	if err := s.Put("claude-code", sampleMarker()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", fi.Mode().Perm())
	}
}

// TestCorruptFile covers the corrupt-store contract: Get surfaces an error,
// while Put and Delete heal the file with a valid store.
func TestCorruptFile(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("claude-code"); err == nil {
		t.Fatal("Get on corrupt store must return an error")
	}
	if err := s.Put("claude-code", sampleMarker()); err != nil {
		t.Fatalf("Put must heal a corrupt store: %v", err)
	}
	if _, ok, err := s.Get("claude-code"); err != nil || !ok {
		t.Fatalf("Get after healing Put = ok=%v err=%v", ok, err)
	}

	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("claude-code"); err != nil {
		t.Fatalf("Delete must heal a corrupt store: %v", err)
	}
	if _, ok, err := s.Get("claude-code"); err != nil || ok {
		t.Fatalf("Get after healing Delete = ok=%v err=%v, want empty store", ok, err)
	}
}

// TestDeleteMissingIsNoOp proves Delete neither errors nor creates the file
// when nothing was ever stored.
func TestDeleteMissingIsNoOp(t *testing.T) {
	s := newStore(t)
	if err := s.Delete("claude-code"); err != nil {
		t.Fatalf("Delete on missing store: %v", err)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("Delete must not create the store file, stat err=%v", err)
	}
}
