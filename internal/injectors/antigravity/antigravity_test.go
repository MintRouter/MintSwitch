package antigravity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

func newInjector(t *testing.T) (*Injector, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	i := New(r, backup.NewEngine(r.BackupsDir()))
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
	servers, ok := m["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing: %+v", m)
	}
	e, ok := servers[core.DefaultMCPServerName].(map[string]any)
	if !ok {
		t.Fatalf("mintrouter entry missing: %+v", servers)
	}
	return e
}

func writeExisting(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIDAndPaths(t *testing.T) {
	i, _ := newInjector(t)
	if i.ID() != "antigravity" {
		t.Fatalf("id = %q", i.ID())
	}
	got := i.MCPConfigPaths()
	if len(got) != 1 || filepath.Base(got[0]) != "mcp_config.json" {
		t.Fatalf("config paths = %v", got)
	}
}

func TestDetect(t *testing.T) {
	i, r := newInjector(t)
	if i.Detect() {
		t.Fatal("expected not installed")
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/agy", nil }
	if !i.Detect() {
		t.Fatal("expected installed once binary resolvable")
	}
	i.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if err := os.MkdirAll(r.Join(".antigravity"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !i.Detect() {
		t.Fatal("expected installed once ~/.antigravity dir exists")
	}
}

func TestStatusTransitions(t *testing.T) {
	i, _ := newInjector(t)
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/agy", nil }
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

func TestInjectWritesServerURLHeadersAnd0600(t *testing.T) {
	i, _ := newInjector(t)
	res, err := i.InjectMCP(spec())
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	e := entry(t, res.ChangedPath)
	if e["serverUrl"] != core.DefaultMCPEndpoint {
		t.Fatalf("bad serverUrl: %+v", e)
	}
	if _, ok := e["url"]; ok {
		t.Fatalf("must use serverUrl not url: %+v", e)
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
	s.APIKey = ""
	if _, err := i.InjectMCP(s); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestInjectPreservesExistingServers(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	existing := `{"theme":"dark","mcpServers":{"other":{"serverUrl":"https://x"}}}`
	writeExisting(t, path, existing)
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	m := readJSON(t, path)
	if m["theme"] != "dark" {
		t.Fatalf("unrelated top-level key lost: %+v", m)
	}
	servers := m["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("other mcp server lost: %+v", servers)
	}
	if _, ok := servers["mintrouter"]; !ok {
		t.Fatal("mintrouter entry missing")
	}
}

func TestRemoveRestoresPristineFile(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	original := `{"theme":"dark"}` + "\n"
	writeExisting(t, path, original)
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
	if string(got) != original {
		t.Fatalf("file not restored byte-for-byte: %q", got)
	}
}

func TestRemoveThenStatusNotConfigured(t *testing.T) {
	i, _ := newInjector(t)
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/agy", nil }
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPNotConfigured {
		t.Fatalf("want NotConfigured after remove, got %v", st)
	}
}

func TestRemoveStripsEntryPreservingOthers(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	existing := `{"theme":"x","mcpServers":{"mintrouter":{"serverUrl":"` +
		core.DefaultMCPEndpoint + `","headers":{"Authorization":"Bearer k"}},"other":{"serverUrl":"https://y"}}}`
	writeExisting(t, path, existing)
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
