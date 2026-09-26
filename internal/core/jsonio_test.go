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

// TestReadJSONObjectStripsBOM proves a UTF-8 BOM (as written by some Windows
// editors) does not make an otherwise valid config unreadable.
func TestReadJSONObjectStripsBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bom.json")
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, `{"a":1}`...), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := ReadJSONObject(path)
	if err != nil {
		t.Fatalf("ReadJSONObject: %v", err)
	}
	if m["a"] != float64(1) {
		t.Fatalf("m = %v, want a=1", m)
	}

	// A file holding only a BOM reads as empty, like an empty file.
	if err := os.WriteFile(path, []byte{0xEF, 0xBB, 0xBF}, 0o600); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadJSONObject(path); err != nil || len(m) != 0 {
		t.Fatalf("BOM-only = %v, %v; want empty object", m, err)
	}
}
