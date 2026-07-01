// Package factorydroid implements the MintSwitch MCP injector for Factory Droid.
//
// It writes the MintRouter Remote MCP server into the GLOBAL user config file
// ~/.factory/mcp.json (Factory Droid's MCP config dir), distinct from the
// endpoint adapter's ~/.factory/settings.json. The entry lives under the
// top-level "mcpServers" object as:
//
//	"mintrouter": { "type": "http", "url": "<endpoint>",
//	                "headers": { "Authorization": "Bearer <key>" } }
//
// This injector is intentionally separate from the endpoint ToolAdapter in
// internal/adapters/factorydroid: MCP injection is independent of the profile.
//
// Schema reference (verified 2026-07-01): Factory Droid MCP — ~/.factory/mcp.json
// with root key "mcpServers", type "http", and a headers.Authorization Bearer
// token.
package factorydroid

import (
	"os/exec"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

const (
	id           = "factory-droid"
	serversKey   = "mcpServers"
	typeHTTP     = "http"
	authHeader   = "Authorization"
	bearerPrefix = "Bearer "
)

// Injector writes/removes the MintRouter MCP server entry in ~/.factory/mcp.json.
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

// configPath returns the absolute path to the global ~/.factory/mcp.json.
func (i *Injector) configPath() string { return i.r.Join(".factory", "mcp.json") }

// MCPConfigPaths returns the config files this injector manages.
func (i *Injector) MCPConfigPaths() []string { return []string{i.configPath()} }

// Detect reports whether Factory Droid is installed, defined as the "droid" CLI
// binary being resolvable (reusing the shared binary resolver).
func (i *Injector) Detect() bool {
	return i.r.BinaryResolvable(i.lookPath, "droid")
}

// MCPStatus inspects ~/.factory/mcp.json relative to spec. See the
// package/contract docs for the status semantics.
func (i *Injector) MCPStatus(spec core.MCPServerSpec) (core.MCPStatus, string, error) {
	spec = spec.Normalized()
	if !i.Detect() {
		return core.MCPNotInstalled, "Factory Droid is not installed.", nil
	}
	m, err := core.ReadJSONObject(i.configPath())
	if err != nil {
		return core.MCPNotConfigured, "", err
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

// InjectMCP backs up ~/.factory/mcp.json only on the first modification of an
// unmanaged file, then idempotently writes the MintRouter server entry under
// "mcpServers", preserving every other key. The write is atomic at 0600.
func (i *Injector) InjectMCP(spec core.MCPServerSpec) (core.MCPResult, error) {
	spec = spec.Normalized()
	if strings.TrimSpace(spec.APIKey) == "" {
		return core.MCPResult{}, errMissingKey
	}
	path := i.configPath()
	m, err := core.ReadJSONObject(path)
	if err != nil {
		return core.MCPResult{}, err
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
		Message:     "Injected MintRouter MCP server into Factory Droid (~/.factory/mcp.json).",
	}, nil
}

// RemoveMCP reverts the injection: it restores ~/.factory/mcp.json from the
// latest backup when one exists, otherwise it strips only our named entry. It is
// a safe no-op when nothing was applied.
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
		return core.MCPResult{ChangedPath: path, BackupPath: entry, Message: "Restored Factory Droid ~/.factory/mcp.json to its pre-inject state."}, nil
	}
	m, err := core.ReadJSONObject(path)
	if err != nil {
		return core.MCPResult{}, err
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
	return core.MCPResult{ChangedPath: path, Message: "Removed the MintRouter MCP server entry from Factory Droid."}, nil
}

var _ core.MCPInjector = (*Injector)(nil)
