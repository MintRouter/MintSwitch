package kilo

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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func entry(t *testing.T, path string) map[string]any {
	t.Helper()
	m := readJSON(t, path)
	servers, ok := m["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp missing: %+v", m)
	}
	e, ok := servers[core.DefaultMCPServerName].(map[string]any)
	if !ok {
		t.Fatalf("mintrouter entry missing: %+v", servers)
	}
	return e
}

func TestIDAndPaths(t *testing.T) {
	i, _ := newInjector(t)
	if i.ID() != "kilo" {
		t.Fatalf("id = %q", i.ID())
	}
	got := i.MCPConfigPaths()
	if len(got) != 2 || filepath.Base(got[0]) != "kilo.json" || filepath.Base(got[1]) != "kilo.jsonc" {
		t.Fatalf("config paths = %v", got)
	}
}

func TestDetect(t *testing.T) {
	i, _ := newInjector(t)
	if i.Detect() {
		t.Fatal("expected not installed")
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	if !i.Detect() {
		t.Fatal("expected installed once binary resolvable")
	}
}

func TestStatusTransitions(t *testing.T) {
	i, _ := newInjector(t)
	if st, _, _ := i.MCPStatus(spec()); st != core.MCPNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}
	i.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
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
	if filepath.Base(res.ChangedPath) != "kilo.json" {
		t.Fatalf("expected kilo.json created, got %q", res.ChangedPath)
	}
	e := entry(t, res.ChangedPath)
	if e["type"] != "remote" || e["url"] != core.DefaultMCPEndpoint {
		t.Fatalf("bad entry: %+v", e)
	}
	if en, _ := e["enabled"].(bool); !en {
		t.Fatalf("expected enabled true: %+v", e)
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
	path := i.jsonPath()
	existing := `{"model":"x/y","provider":{"openai-compatible":{"options":{}}},"mcp":{"other":{"type":"remote","url":"https://x"}}}`
	writeFile(t, path, existing)
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	m := readJSON(t, path)
	if m["model"] != "x/y" {
		t.Fatalf("unrelated top-level key lost: %+v", m)
	}
	if _, ok := m["provider"].(map[string]any); !ok {
		t.Fatalf("provider block lost: %+v", m)
	}
	servers := m["mcp"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("other mcp server lost: %+v", servers)
	}
	if _, ok := servers["mintrouter"]; !ok {
		t.Fatal("mintrouter entry missing")
	}
}

func TestRemoveRestoresBackup(t *testing.T) {
	i, _ := newInjector(t)
	path := i.jsonPath()
	pristine := `{"model":"x/y"}` + "\n"
	writeFile(t, path, pristine)
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != pristine {
		t.Fatalf("not restored to pristine: %q", got)
	}
}

func TestRemoveStripsEntryWithoutBackup(t *testing.T) {
	i, _ := newInjector(t)
	path := i.jsonPath()
	existing := `{"mcp":{"mintrouter":{"type":"remote","url":"https://x"},"other":{"type":"remote","url":"https://y"}}}`
	writeFile(t, path, existing)
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	m := readJSON(t, path)
	servers := m["mcp"].(map[string]any)
	if _, ok := servers["mintrouter"]; ok {
		t.Fatalf("mintrouter entry not stripped: %+v", servers)
	}
	if _, ok := servers["other"]; !ok {
		t.Fatalf("other server lost: %+v", servers)
	}
}

func TestRemoveNoOpWhenNothingConfigured(t *testing.T) {
	i, _ := newInjector(t)
	res, err := i.RemoveMCP()
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected no backup path, got %q", res.BackupPath)
	}
}
