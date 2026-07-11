package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWriteFileAtomic covers the shared atomic-write helper: it creates parent
// dirs, writes the exact bytes, applies the requested perm, replaces an
// existing file, and leaves no temp file behind.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.json")

	if err := WriteFileAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first" {
		t.Fatalf("read = %q, %v; want %q", data, err, "first")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("perm = %v, want 0600", fi.Mode().Perm())
		}
	}

	// Overwrite (rename over an existing file) must succeed and replace content.
	if err := WriteFileAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic overwrite: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "second" {
		t.Fatalf("read after overwrite = %q, %v; want %q", data, err, "second")
	}

	// No temp files may remain next to the target.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

// TestWriteJSONObjectAtomic proves the JSON wrapper writes indented JSON with
// a trailing newline via the atomic helper.
func TestWriteJSONObjectAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "obj.json")
	if err := WriteJSONObjectAtomic(path, map[string]any{"k": "v"}); err != nil {
		t.Fatalf("WriteJSONObjectAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"k\": \"v\"\n}\n"
	if string(data) != want {
		t.Fatalf("content = %q, want %q", data, want)
	}
	m, err := ReadJSONObject(path)
	if err != nil || m["k"] != "v" {
		t.Fatalf("ReadJSONObject = %v, %v", m, err)
	}
}
