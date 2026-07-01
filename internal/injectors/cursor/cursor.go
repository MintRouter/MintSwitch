// Package cursor implements the MintSwitch MCP injector for Cursor.
//
// It writes the MintRouter Remote MCP server into the GLOBAL user config file
// ~/.cursor/mcp.json (NOT the project-level .cursor/mcp.json, which would risk
// committing a secret). The entry lives under the top-level "mcpServers" object
// as:
//
//	"mintrouter": { "url": "<endpoint>",
//	                "headers": { "Authorization": "Bearer <key>" } }
//
// Cursor auto-detects a remote streamable-HTTP server from the "url" field, so
// no "type" field is written.
//
// This injector is intentionally separate from the endpoint ToolAdapter: MCP
// injection is independent of the profile and preserves every other key.
//
// Schema reference (verified 2026-07-01): Cursor MCP docs — ~/.cursor/mcp.json
// with root key "mcpServers", a remote server declared by "url" (transport
// auto-detected), and a headers.Authorization Bearer token.
package cursor

import (
	"os/exec"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

const (
	id           = "cursor"
	serversKey   = "mcpServers"
	authHeader   = "Authorization"
	bearerPrefix = "Bearer "
)

// Injector writes/removes the MintRouter MCP server entry in ~/.cursor/mcp.json.
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

// configPath returns the absolute path to the global ~/.cursor/mcp.json.
func (i *Injector) configPath() string { return i.r.Join(".cursor", "mcp.json") }

// MCPConfigPaths returns the config files this injector manages.
func (i *Injector) MCPConfigPaths() []string { return []string{i.configPath()} }

// Detect reports whether Cursor is installed, defined as the "cursor" CLI
// binary being resolvable (reusing the shared binary resolver).
func (i *Injector) Detect() bool {
	return i.r.BinaryResolvable(i.lookPath, "cursor")
}

// MCPStatus inspects ~/.cursor/mcp.json relative to spec. See the
// package/contract docs for the status semantics.
func (i *Injector) MCPStatus(spec core.MCPServerSpec) (core.MCPStatus, string, error) {
	spec = spec.Normalized()
	if !i.Detect() {
		return core.MCPNotInstalled, "Cursor is not installed.", nil
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

// InjectMCP backs up ~/.cursor/mcp.json only on the first modification of an
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
		Message:     "Injected MintRouter MCP server into Cursor (~/.cursor/mcp.json).",
	}, nil
}

// RemoveMCP reverts the injection: it restores ~/.cursor/mcp.json from the
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
		return core.MCPResult{ChangedPath: path, BackupPath: entry, Message: "Restored Cursor ~/.cursor/mcp.json to its pre-inject state."}, nil
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
	return core.MCPResult{ChangedPath: path, Message: "Removed the MintRouter MCP server entry from Cursor."}, nil
}

var _ core.MCPInjector = (*Injector)(nil)
