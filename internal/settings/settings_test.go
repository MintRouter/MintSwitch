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
