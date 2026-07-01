package claudecode

import (
	"os"
	"testing"

	"mintswitch/internal/core"
)

func TestRemoveRestoresPristineFile(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	original := []byte(`{"theme":"dark"}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after remove: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("file not restored byte-for-byte: %q", got)
	}
}

func TestRemoveDeletesCreatedFile(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected created file removed, stat err=%v", err)
	}
}

// TestInjectBacksUpOnce proves a second inject (key rotation) does not snapshot
// the managed state, so RemoveMCP reverts to the pristine pre-inject original.
func TestInjectBacksUpOnce(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	original := []byte(`{"theme":"light"}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject 1: %v", err)
	}
	rotated := spec()
	rotated.APIKey = "sk-rotated"
	if _, err := i.InjectMCP(rotated); err != nil {
		t.Fatalf("inject 2: %v", err)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("expected pristine original after remove, got %q", got)
	}
}

// TestRemoveStripsEntryWhenNoBackup proves the no-backup path strips only our
// entry and preserves other servers and top-level keys.
func TestRemoveStripsEntryWhenNoBackup(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	existing := `{"theme":"x","mcpServers":{"mintrouter":{"type":"http","url":"` +
		core.DefaultMCPEndpoint + `","headers":{"Authorization":"Bearer k"}},"other":{"type":"http","url":"https://y"}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := i.RemoveMCP()
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected no backup path, got %q", res.BackupPath)
	}
	m := readJSON(t, path)
	if m["theme"] != "x" {
		t.Fatalf("top-level key lost: %+v", m)
	}
	servers := m["mcpServers"].(map[string]any)
	if _, ok := servers["mintrouter"]; ok {
		t.Fatal("mintrouter entry should be stripped")
	}
	if _, ok := servers["other"]; !ok {
		t.Fatal("other server should be preserved")
	}
}

// TestRemoveDropsEmptyServersKey proves stripping the only entry removes the
// mcpServers object entirely while keeping other keys.
func TestRemoveDropsEmptyServersKey(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	existing := `{"theme":"x","mcpServers":{"mintrouter":{"type":"http","url":"` +
		core.DefaultMCPEndpoint + `","headers":{"Authorization":"Bearer k"}}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	m := readJSON(t, path)
	if _, ok := m["mcpServers"]; ok {
		t.Fatalf("empty mcpServers should be dropped: %+v", m)
	}
	if m["theme"] != "x" {
		t.Fatalf("top-level key lost: %+v", m)
	}
}

func TestRemoveNoOpWhenNothing(t *testing.T) {
	i, _ := newInjector(t)
	res, err := i.RemoveMCP()
	if err != nil {
		t.Fatalf("remove no-op: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected empty backup path, got %q", res.BackupPath)
	}
}
