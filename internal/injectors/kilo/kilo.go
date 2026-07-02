// Package kilo implements the MintSwitch MCP injector for Kilo Code.
//
// It writes the MintRouter Remote MCP server into the GLOBAL user config file
// ~/.config/kilo/kilo.json or kilo.jsonc (XDG-aware) — the same file the Kilo
// endpoint adapter manages. Kilo (a fork of OpenCode) uses OpenCode's schema:
// the entry lives under the top-level "mcp" object (NOT "mcpServers") as a
// remote server:
//
//	"mintrouter": { "type": "remote", "url": "<endpoint>", "enabled": true,
//	                "headers": { "Authorization": "Bearer <key>" } }
//
// This injector is intentionally separate from the endpoint ToolAdapter in
// internal/adapters/kilo: MCP injection is independent of the profile and
// preserves every other key (including the endpoint provider block).
//
// Schema reference (verified 2026-07-02 from kilo.ai/docs "Using MCP in Kilo
// Code"): root key "mcp" with type "remote", an "enabled" flag, and a
// headers.Authorization Bearer token — identical to OpenCode Remote MCP.
//
// JSONC caveat: a kilo.jsonc whose content is strict JSON is rewritten safely;
// one carrying comments or other JSONC-only syntax is never rewritten (status
// reports ConfiguredExternally and InjectMCP refuses) because Go's
// encoding/json round-trip would destroy the user's comments.
package kilo

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

const (
	id           = "kilo"
	serversKey   = "mcp"
	typeRemote   = "remote"
	authHeader   = "Authorization"
	bearerPrefix = "Bearer "
)

// jsoncDetail explains why a comment-carrying kilo.jsonc is not managed.
const jsoncDetail = "kilo.jsonc contains comments or other JSONC-only syntax; " +
	"MintSwitch cannot modify it without destroying them."

// errJSONC is returned by InjectMCP when the active kilo.jsonc cannot be
// rewritten without destroying JSONC-only syntax such as comments.
var errJSONC = errors.New("kilo: " + jsoncDetail + " Edit the file manually or convert it to plain JSON")

// Injector writes/removes the MintRouter MCP server entry in the global Kilo
// config. Construct it with [New].
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

// jsonPath returns the global kilo.json path (XDG-aware).
func (i *Injector) jsonPath() string { return i.r.ConfigJoin("kilo", "kilo.json") }

// jsoncPath returns the global kilo.jsonc path (XDG-aware).
func (i *Injector) jsoncPath() string { return i.r.ConfigJoin("kilo", "kilo.jsonc") }

// configPath returns the active global config file: kilo.jsonc when it exists
// (it overrides kilo.json in Kilo's merge order), otherwise kilo.json.
func (i *Injector) configPath() string {
	if fileExists(i.jsoncPath()) {
		return i.jsoncPath()
	}
	return i.jsonPath()
}

// MCPConfigPaths returns the config files this injector manages.
func (i *Injector) MCPConfigPaths() []string { return []string{i.jsonPath(), i.jsoncPath()} }

// Detect reports whether Kilo Code is installed, defined as the "kilo" CLI
// binary being resolvable (reusing the shared binary resolver).
func (i *Injector) Detect() bool {
	return i.r.BinaryResolvable(i.lookPath, "kilo")
}

// MCPStatus inspects the active Kilo config relative to spec. A kilo.jsonc
// carrying JSONC-only syntax reports ConfiguredExternally: MintSwitch cannot
// rewrite it without destroying the user's comments.
func (i *Injector) MCPStatus(spec core.MCPServerSpec) (core.MCPStatus, string, error) {
	spec = spec.Normalized()
	if !i.Detect() {
		return core.MCPNotInstalled, "Kilo Code is not installed.", nil
	}
	m, strict, err := readConfig(i.configPath())
	if err != nil {
		return core.MCPNotConfigured, "", err
	}
	if !strict {
		return core.MCPConfiguredExternally, jsoncDetail, nil
	}
	servers := core.AsJSONObject(m[serversKey])
	entry, ok := servers[spec.Name]
	if !ok {
		return core.MCPNotConfigured, "No MintRouter MCP server is configured.", nil
	}
	if entryMatchesSpec(entry, spec) {
		return core.MCPConfiguredByMintSwitch, "MintRouter MCP server is configured by MintSwitch.", nil
	}
	return core.MCPConfiguredExternally, "A different MintRouter MCP entry exists (configured outside MintSwitch).", nil
}

// InjectMCP backs up the active Kilo config only on the first modification of
// an unmanaged file, then idempotently writes the MintRouter server entry under
// "mcp", preserving every other key. The write is atomic at 0600. It refuses to
// touch a kilo.jsonc carrying JSONC-only syntax (see errJSONC).
func (i *Injector) InjectMCP(spec core.MCPServerSpec) (core.MCPResult, error) {
	spec = spec.Normalized()
	if strings.TrimSpace(spec.APIKey) == "" {
		return core.MCPResult{}, errMissingKey
	}
	path := i.configPath()
	m, strict, err := readConfig(path)
	if err != nil {
		return core.MCPResult{}, err
	}
	if !strict {
		return core.MCPResult{}, errJSONC
	}
	servers := core.AsJSONObject(m[serversKey])
	var backupPath string
	if !isOurShape(servers[spec.Name], spec.Endpoint) {
		backupPath, err = i.e.Backup(path)
		if err != nil {
			return core.MCPResult{}, err
		}
	}
	servers[spec.Name] = serverEntry(spec)
	m[serversKey] = servers
	if err := core.WriteJSONObjectAtomic(path, m); err != nil {
		return core.MCPResult{}, err
	}
	return core.MCPResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Injected MintRouter MCP server into Kilo Code config.",
	}, nil
}

// RemoveMCP reverts the injection: it restores the Kilo config from the latest
// backup when one exists (checking both candidate files), otherwise it strips
// only our named entry. It is a safe no-op when nothing was applied, and it
// never rewrites a comment-carrying kilo.jsonc.
func (i *Injector) RemoveMCP() (core.MCPResult, error) {
	for _, path := range []string{i.jsoncPath(), i.jsonPath()} {
		has, err := i.e.HasBackup(path)
		if err != nil {
			return core.MCPResult{}, err
		}
		if !has {
			continue
		}
		_, entry, rerr := i.e.RestoreLatest(path)
		if rerr != nil {
			return core.MCPResult{}, rerr
		}
		return core.MCPResult{ChangedPath: path, BackupPath: entry, Message: "Restored Kilo Code config to its pre-inject state."}, nil
	}
	path := i.configPath()
	m, strict, err := readConfig(path)
	if err != nil {
		return core.MCPResult{}, err
	}
	if !strict {
		return core.MCPResult{ChangedPath: path, Message: jsoncDetail + " Nothing was removed."}, nil
	}
	servers := core.AsJSONObject(m[serversKey])
	if _, ok := servers[core.DefaultMCPServerName]; !ok {
		return core.MCPResult{ChangedPath: path, Message: "No MintRouter MCP server configured; nothing to remove."}, nil
	}
	delete(servers, core.DefaultMCPServerName)
	if len(servers) == 0 {
		delete(m, serversKey)
	} else {
		m[serversKey] = servers
	}
	if err := core.WriteJSONObjectAtomic(path, m); err != nil {
		return core.MCPResult{}, err
	}
	return core.MCPResult{ChangedPath: path, Message: "Removed the MintRouter MCP server entry from Kilo Code."}, nil
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// readConfig reads path as a JSON object. A missing or empty file yields an
// empty object and strict=true so callers can merge without special-casing
// first-run. strict is false (with a nil map and no error) when a .jsonc file
// fails strict JSON parsing, i.e. it carries comments/trailing commas that a
// rewrite would destroy. A .json file that fails to parse is corrupt and
// returns the parse error.
func readConfig(path string) (m map[string]any, strict bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, true, nil
		}
		return nil, false, err
	}
	if len(data) == 0 {
		return map[string]any{}, true, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		if strings.HasSuffix(path, ".jsonc") {
			return nil, false, nil
		}
		return nil, false, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, true, nil
}

var _ core.MCPInjector = (*Injector)(nil)
