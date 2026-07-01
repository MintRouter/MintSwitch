package claudecode

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

func TestIDAndPaths(t *testing.T) {
	i, _ := newInjector(t)
	if i.ID() != "claude-code" {
		t.Fatalf("id = %q", i.ID())
	}
	if got := i.MCPConfigPaths(); len(got) != 1 || filepath.Base(got[0]) != ".claude.json" {
		t.Fatalf("config paths = %v", got)
	}
}

func TestDetect(t *testing.T) {
	i, _ := newInjector(t)
	if i.Detect() {
		t.Fatal("expected not installed")
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	if !i.Detect() {
		t.Fatal("expected installed once binary resolvable")
	}
}

func TestStatusTransitions(t *testing.T) {
	i, _ := newInjector(t)
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
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
	s.APIKey = ""
	if _, err := i.InjectMCP(s); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestInjectPreservesExistingKeys(t *testing.T) {
	i, _ := newInjector(t)
	path := i.configPath()
	existing := `{"numStartups":7,"mcpServers":{"other":{"type":"http","url":"https://x"}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	m := readJSON(t, path)
	if m["numStartups"].(float64) != 7 {
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
