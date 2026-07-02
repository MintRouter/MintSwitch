package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

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

func readConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m := map[string]any{}
	if err := toml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func entry(t *testing.T, path string) map[string]any {
	t.Helper()
	m := readConfig(t, path)
	servers, ok := m["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_servers missing: %+v", m)
	}
	e, ok := servers[core.DefaultMCPServerName].(map[string]any)
	if !ok {
		t.Fatalf("mintrouter entry missing: %+v", servers)
	}
	return e
}

func TestIDAndPaths(t *testing.T) {
	i, _ := newInjector(t)
	if i.ID() != "codex" {
		t.Fatalf("id = %q", i.ID())
	}
	if got := i.MCPConfigPaths(); len(got) != 1 || filepath.Base(got[0]) != "config.toml" {
		t.Fatalf("config paths = %v", got)
	}
}

func TestDetect(t *testing.T) {
	i, r := newInjector(t)
	if i.Detect() {
		t.Fatal("expected not installed")
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	if !i.Detect() {
		t.Fatal("expected installed once binary resolvable")
	}
	i.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if err := os.MkdirAll(r.Join(".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !i.Detect() {
		t.Fatal("expected installed once ~/.codex dir exists")
	}
}

func TestStatusTransitions(t *testing.T) {
	i, _ := newInjector(t)
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
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
	if e["url"] != core.DefaultMCPEndpoint {
		t.Fatalf("bad url: %+v", e)
	}
	if en, _ := e["enabled"].(bool); !en {
		t.Fatalf("expected enabled true: %+v", e)
	}
	headers, _ := e["http_headers"].(map[string]any)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "" +
		"openai_base_url = \"https://x\"\n" +
		"model = \"gpt-x\"\n\n" +
		"[mcp_servers.other]\n" +
		"url = \"https://other\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	m := readConfig(t, path)
	if m["openai_base_url"] != "https://x" || m["model"] != "gpt-x" {
		t.Fatalf("unrelated top-level key lost: %+v", m)
	}
	servers := m["mcp_servers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("other mcp server lost: %+v", servers)
	}
	if _, ok := servers["mintrouter"]; !ok {
		t.Fatal("mintrouter entry missing")
	}
}

func TestInjectIdempotent(t *testing.T) {
	i, _ := newInjector(t)
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject1: %v", err)
	}
	before, err := os.ReadFile(i.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject2: %v", err)
	}
	after, err := os.ReadFile(i.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("inject not idempotent:\n%s\n---\n%s", before, after)
	}
}

func TestRemoveRestoresAndStrips(t *testing.T) {
	i, _ := newInjector(t)
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPConfiguredByMintSwitch {
		t.Fatalf("want ByMintSwitch after inject, got %v", st)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPNotConfigured {
		t.Fatalf("want NotConfigured after remove, got %v", st)
	}
	// Preserved sibling servers survive a strip-only removal.
	path := i.configPath()
	if err := os.WriteFile(path, []byte("[mcp_servers.other]\nurl = \"https://other\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject2: %v", err)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove2: %v", err)
	}
	m := readConfig(t, path)
	servers, _ := m["mcp_servers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("sibling server lost after remove: %+v", m)
	}
	if _, ok := servers["mintrouter"]; ok {
		t.Fatalf("mintrouter not stripped: %+v", servers)
	}
}

func TestRemoveNoopWhenNothingApplied(t *testing.T) {
	i, _ := newInjector(t)
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove no-op: %v", err)
	}
}

func TestAPIKeyNotLeakedInResultsOrErrors(t *testing.T) {
	i, _ := newInjector(t)
	res, err := i.InjectMCP(spec())
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	const key = "sk-secret-token"
	if strings.Contains(res.Message, key) || strings.Contains(res.ChangedPath, key) || strings.Contains(res.BackupPath, key) {
		t.Fatalf("api key leaked in result: %+v", res)
	}
	s := spec()
	s.APIKey = ""
	if _, err := i.InjectMCP(s); err != nil && strings.Contains(err.Error(), key) {
		t.Fatalf("api key leaked in error: %v", err)
	}
	rm, err := i.RemoveMCP()
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if strings.Contains(rm.Message, key) {
		t.Fatalf("api key leaked in remove message: %+v", rm)
	}
}
