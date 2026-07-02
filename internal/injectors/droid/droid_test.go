package droid

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

func newInjector(t *testing.T) (*Injector, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	i := New(r, backup.NewEngine(r.MCPBackupsDir()))
	i.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return i, r
}

func spec() core.MCPServerSpec {
	return core.MCPServerSpec{Name: core.DefaultMCPServerName, Endpoint: core.DefaultMCPEndpoint, APIKey: "sk-secret-token"}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func entry(t *testing.T, path string) map[string]any {
	t.Helper()
	m := readJSON(t, path)
	servers, ok := m[serversKey].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing: %+v", m)
	}
	e, ok := servers[core.DefaultMCPServerName].(map[string]any)
	if !ok {
		t.Fatalf("mintrouter entry missing: %+v", servers)
	}
	return e
}

func TestIDAndPaths(t *testing.T) {
	i, _ := newInjector(t)
	if i.ID() != "droid" {
		t.Fatalf("id = %q", i.ID())
	}
	if got := i.MCPConfigPaths(); len(got) != 1 || filepath.Base(got[0]) != "mcp.json" {
		t.Fatalf("config paths = %v", got)
	}
}

// TestDetect proves the contract: a resolvable "droid" binary OR an existing
// ~/.factory directory is an installed signal.
func TestDetect(t *testing.T) {
	i, r := newInjector(t)
	if i.Detect() {
		t.Fatal("expected not installed")
	}
	if err := os.MkdirAll(r.Join(".factory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !i.Detect() {
		t.Fatal("expected installed via ~/.factory dir")
	}
	if err := os.RemoveAll(r.Join(".factory")); err != nil {
		t.Fatal(err)
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	if !i.Detect() {
		t.Fatal("expected installed once binary resolvable")
	}
}

func TestStatusTransitions(t *testing.T) {
	i, _ := newInjector(t)
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/droid", nil }
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPNotConfigured {
		t.Fatalf("want NotConfigured, got %v", st)
	}
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPConfiguredByMintSwitch {
		t.Fatalf("want ConfiguredByMintSwitch, got %v", st)
	}
	other := spec()
	other.APIKey = "different-key"
	if st, _, _ := i.MCPStatus(other); st != core.MCPConfiguredExternally {
		t.Fatalf("want ConfiguredExternally with different key, got %v", st)
	}
}

func TestInjectWritesEntryAnd0600(t *testing.T) {
	i, _ := newInjector(t)
	res, err := i.InjectMCP(spec())
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	e := entry(t, res.ChangedPath)
	if e["type"] != "http" || e["url"] != core.DefaultMCPEndpoint {
		t.Fatalf("bad entry: %+v", e)
	}
	headers, _ := e["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer sk-secret-token" {
		t.Fatalf("bad auth header: %+v", headers)
	}
	fi, err := os.Stat(res.ChangedPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", fi.Mode().Perm())
	}
}

func TestInjectMissingKeyErrors(t *testing.T) {
	i, _ := newInjector(t)
	s := spec()
	s.APIKey = "   "
	_, err := i.InjectMCP(s)
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("error leaks key material: %v", err)
	}
}

func TestInjectPreservesExistingKeys(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{"someSetting":true,"mcpServers":{"other":{"type":"http","url":"https://x"}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	m := readJSON(t, path)
	if m["someSetting"] != true {
		t.Fatalf("unrelated top-level key lost: %+v", m)
	}
	servers := m[serversKey].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("other mcp server lost: %+v", servers)
	}
	if _, ok := servers[core.DefaultMCPServerName]; !ok {
		t.Fatal("mintrouter entry missing")
	}
}

func TestRemoveRestoresBackup(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"mcpServers":{"other":{"type":"http","url":"https://x"}}}` + "\n")
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
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("not byte-for-byte restored: %q", got)
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
		t.Fatalf("expected file removed, got err=%v", err)
	}
}

func TestRemoveWithoutBackupStripsOnlyOurEntry(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"mcpServers":{"other":{"type":"http","url":"https://x"},` +
		`"mintrouter":{"type":"http","url":"https://y","headers":{"Authorization":"Bearer z"}}}}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	m := readJSON(t, path)
	servers := m[serversKey].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("other server lost: %+v", servers)
	}
	if _, ok := servers[core.DefaultMCPServerName]; ok {
		t.Fatalf("mintrouter entry not removed: %+v", servers)
	}
}

func TestRemoveNothingToRemoveNoOp(t *testing.T) {
	i, _ := newInjector(t)
	res, err := i.RemoveMCP()
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected no backup path, got %q", res.BackupPath)
	}
	if _, err := os.Stat(i.configPath()); !os.IsNotExist(err) {
		t.Fatalf("expected no file created, got err=%v", err)
	}
}
