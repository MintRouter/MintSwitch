// Package codex implements the MintSwitch MCP injector for OpenAI Codex.
//
// It writes the MintRouter Remote MCP server into the GLOBAL user config file
// ~/.codex/config.toml (TOML) — the same file the Codex endpoint adapter
// manages. Codex stores MCP servers under the top-level [mcp_servers] table,
// one sub-table per server:
//
//	[mcp_servers.mintrouter]
//	url = "<endpoint>"
//	enabled = true
//	[mcp_servers.mintrouter.http_headers]
//	Authorization = "Bearer <key>"
//
// This injector is intentionally separate from the endpoint ToolAdapter in
// internal/adapters/codex: MCP injection is independent of the profile and
// preserves every other key (including openai_base_url/model and the managed
// marker). It reuses the same TOML approach (github.com/pelletier/go-toml/v2,
// preserve-all-keys read/write) as that adapter.
package codex

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

const (
	id           = "codex"
	serversKey   = "mcp_servers"
	headersKey   = "http_headers"
	authHeader   = "Authorization"
	bearerPrefix = "Bearer "
)

// Injector writes/removes the MintRouter MCP server entry in ~/.codex/config.toml.
// Construct it with [New].
type Injector struct {
	r *paths.Resolver
	e *backup.Engine
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New returns an Injector that resolves paths via r and backs up via e.
func New(r *paths.Resolver, e *backup.Engine) *Injector {
	return &Injector{r: r, e: e, lookPath: exec.LookPath}
}

// ID returns the stable injector identifier.
func (i *Injector) ID() string { return id }

// configPath returns the absolute path to config.toml under the Codex home
// dir ($CODEX_HOME, default ~/.codex).
func (i *Injector) configPath() string { return filepath.Join(i.r.CodexDir(), "config.toml") }

// MCPConfigPaths returns the config files this injector manages.
func (i *Injector) MCPConfigPaths() []string { return []string{i.configPath()} }

// Detect reports whether Codex is installed, defined as the "codex" CLI binary
// being resolvable OR the Codex home dir existing (a Codex install/session
// leaves the config dir behind even when the CLI is not on the current PATH).
func (i *Injector) Detect() bool {
	if i.r.BinaryResolvable(i.lookPath, "codex") {
		return true
	}
	fi, err := os.Stat(i.r.CodexDir())
	return err == nil && fi.IsDir()
}

// MCPStatus inspects config.toml relative to spec. See the contract docs for the
// status semantics.
func (i *Injector) MCPStatus(spec core.MCPServerSpec) (core.MCPStatus, string, error) {
	spec = spec.Normalized()
	if !i.Detect() {
		return core.MCPNotInstalled, "Codex is not installed.", nil
	}
	cfg, err := readTOML(i.configPath())
	if err != nil {
		return core.MCPNotConfigured, "", err
	}
	servers := core.AsJSONObject(cfg[serversKey])
	entry, ok := servers[spec.Name]
	if !ok {
		return core.MCPNotConfigured, "No MintRouter MCP server is configured.", nil
	}
	if entryMatchesSpec(entry, spec) {
		return core.MCPConfiguredByMintSwitch, "MintRouter MCP server is configured by MintSwitch.", nil
	}
	return core.MCPConfiguredExternally, "A different MintRouter MCP entry exists (configured outside MintSwitch).", nil
}

// InjectMCP backs up config.toml only on the first modification of an unmanaged
// file, then idempotently writes the MintRouter server entry under
// [mcp_servers], preserving every other key. The file is written at 0600.
func (i *Injector) InjectMCP(spec core.MCPServerSpec) (core.MCPResult, error) {
	spec = spec.Normalized()
	if strings.TrimSpace(spec.APIKey) == "" {
		return core.MCPResult{}, errMissingKey
	}
	path := i.configPath()
	cfg, err := readTOML(path)
	if err != nil {
		return core.MCPResult{}, err
	}
	servers := core.AsJSONObject(cfg[serversKey])
	var backupPath string
	if !isOurShape(servers[spec.Name], spec.Endpoint) {
		backupPath, err = i.e.Backup(path)
		if err != nil {
			return core.MCPResult{}, err
		}
	}
	servers[spec.Name] = serverEntry(spec)
	cfg[serversKey] = servers
	if err := writeTOML(path, cfg); err != nil {
		return core.MCPResult{}, err
	}
	return core.MCPResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Injected MintRouter MCP server into Codex (config.toml).",
	}, nil
}

// RemoveMCP reverts the injection: it restores config.toml from the latest
// backup when one exists, otherwise it strips only our named entry. It is a safe
// no-op when nothing was applied.
func (i *Injector) RemoveMCP() (core.MCPResult, error) {
	path := i.configPath()
	has, err := i.e.HasBackup(path)
	if err != nil {
		return core.MCPResult{}, err
	}
	if has {
		_, entry, rerr := i.e.RestoreLatest(path)
		if rerr != nil {
			return core.MCPResult{}, rerr
		}
		return core.MCPResult{ChangedPath: path, BackupPath: entry, Message: "Restored Codex config.toml to its pre-inject state."}, nil
	}
	cfg, err := readTOML(path)
	if err != nil {
		return core.MCPResult{}, err
	}
	servers := core.AsJSONObject(cfg[serversKey])
	if _, ok := servers[core.DefaultMCPServerName]; !ok {
		return core.MCPResult{ChangedPath: path, Message: "No MintRouter MCP server configured; nothing to remove."}, nil
	}
	delete(servers, core.DefaultMCPServerName)
	if len(servers) == 0 {
		delete(cfg, serversKey)
	} else {
		cfg[serversKey] = servers
	}
	if err := writeTOML(path, cfg); err != nil {
		return core.MCPResult{}, err
	}
	return core.MCPResult{ChangedPath: path, Message: "Removed the MintRouter MCP server entry from Codex."}, nil
}

var _ core.MCPInjector = (*Injector)(nil)

// errMissingKey is returned by InjectMCP when no API key is present. Its message
// never contains the key.
var errMissingKey = errors.New("codex: a MintRouter API key is required to inject the MCP server")

// serverEntry builds the TOML table written under mcp_servers.<name> for spec:
// a remote HTTP MCP server with an explicit "enabled" flag and a Bearer token
// in http_headers.Authorization.
func serverEntry(spec core.MCPServerSpec) map[string]any {
	return map[string]any{
		"url":     spec.Endpoint,
		"enabled": true,
		headersKey: map[string]any{
			authHeader: bearerPrefix + spec.APIKey,
		},
	}
}

// entryMatchesSpec reports whether entry is exactly our MintSwitch-owned shape
// for spec: url == endpoint, enabled true, and the Authorization header equal to
// "Bearer <key>". A mismatch on any field means the entry was configured outside
// MintSwitch (or with a different key/endpoint).
func entryMatchesSpec(entry any, spec core.MCPServerSpec) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	if u, _ := obj["url"].(string); u != spec.Endpoint {
		return false
	}
	if en, _ := obj["enabled"].(bool); !en {
		return false
	}
	headers, ok := obj[headersKey].(map[string]any)
	if !ok {
		return false
	}
	auth, _ := headers[authHeader].(string)
	return auth == bearerPrefix+spec.APIKey
}

// isOurShape reports whether entry looks like a MintSwitch-managed entry for the
// given endpoint, independent of the exact key. It is used only for the
// backup-once decision so rotating the key does not re-snapshot a managed file
// (which would hide the pristine pre-inject original).
func isOurShape(entry any, endpoint string) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	if u, _ := obj["url"].(string); u != endpoint {
		return false
	}
	headers, ok := obj[headersKey].(map[string]any)
	if !ok {
		return false
	}
	auth, _ := headers[authHeader].(string)
	return strings.HasPrefix(auth, bearerPrefix)
}

// readTOML parses a TOML file into a generic map, preserving every key. A
// missing file yields an empty map (not an error) so InjectMCP can create the
// file from scratch. It mirrors internal/adapters/codex's TOML handling.
func readTOML(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	m := map[string]any{}
	if len(data) == 0 {
		return m, nil
	}
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// writeTOML marshals the map to TOML and writes it atomically with 0600 perms
// (the file may carry an auth token), creating the parent directory as needed.
func writeTOML(path string, m map[string]any) error {
	data, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	return core.WriteFileAtomic(path, data, 0o600)
}
