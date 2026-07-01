// Package antigravity implements the MintSwitch MCP injector for Antigravity.
//
// It writes the MintRouter Remote MCP server into the user config file
// ~/.gemini/antigravity/mcp_config.json. Antigravity uses a "mcpServers" root
// object whose remote entries are keyed by "serverUrl" (NOT "url") with a
// headers.Authorization Bearer token:
//
//	"mintrouter": { "serverUrl": "<endpoint>",
//	                "headers": { "Authorization": "Bearer <key>" } }
//
// MCP injection is independent of any endpoint profile and preserves every other
// key (including other mcpServers entries).
//
// Schema reference (verified 2026-07-01): Antigravity Remote MCP — root key
// "mcpServers" with a "serverUrl" field and a headers.Authorization Bearer
// token.
package antigravity

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

const (
	id           = "antigravity"
	serversKey   = "mcpServers"
	serverURLKey = "serverUrl"
	authHeader   = "Authorization"
	bearerPrefix = "Bearer "
)

// errMissingKey is returned by InjectMCP when no API key is present. Its message
// never contains the key.
var errMissingKey = errors.New("antigravity: a MintRouter API key is required to inject the MCP server")

// Injector writes/removes the MintRouter MCP server entry in
// ~/.gemini/antigravity/mcp_config.json. Construct it with [New].
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

// configPath returns the absolute path to ~/.gemini/antigravity/mcp_config.json.
func (i *Injector) configPath() string { return i.r.Join(".gemini", "antigravity", "mcp_config.json") }

// MCPConfigPaths returns the config files this injector manages.
func (i *Injector) MCPConfigPaths() []string { return []string{i.configPath()} }

// Detect reports whether Antigravity is installed: a ~/.antigravity or
// ~/.gemini/antigravity config directory exists, or the "agy" CLI binary is
// resolvable (via PATH or the shared curated bin dirs).
func (i *Injector) Detect() bool {
	if dirExists(i.r.Join(".antigravity")) {
		return true
	}
	if dirExists(i.r.Join(".gemini", "antigravity")) {
		return true
	}
	return i.r.BinaryResolvable(i.lookPath, "agy")
}

// MCPStatus inspects mcp_config.json relative to spec. See the contract docs for
// the status semantics.
func (i *Injector) MCPStatus(spec core.MCPServerSpec) (core.MCPStatus, string, error) {
	spec = spec.Normalized()
	if !i.Detect() {
		return core.MCPNotInstalled, "Antigravity is not installed.", nil
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

// InjectMCP backs up mcp_config.json only on the first modification of an
// unmanaged file, then idempotently writes the MintRouter server entry under
// "mcpServers", preserving every other key. The write is atomic at 0600 and
// creates the config directory when absent.
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
		Message:     "Injected MintRouter MCP server into Antigravity (mcp_config.json).",
	}, nil
}

// RemoveMCP reverts the injection: it restores mcp_config.json from the latest
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
		return core.MCPResult{ChangedPath: path, BackupPath: entry, Message: "Restored Antigravity mcp_config.json to its pre-inject state."}, nil
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
	return core.MCPResult{ChangedPath: path, Message: "Removed the MintRouter MCP server entry from Antigravity."}, nil
}

// serverEntry builds the JSON object written under mcpServers.<name> for spec.
// Antigravity remote servers are keyed by "serverUrl" (NOT "url").
func serverEntry(spec core.MCPServerSpec) map[string]any {
	return map[string]any{
		serverURLKey: spec.Endpoint,
		"headers": map[string]any{
			authHeader: bearerPrefix + spec.APIKey,
		},
	}
}

// entryMatchesSpec reports whether entry is exactly our MintSwitch-owned shape
// for spec: serverUrl == endpoint and the Authorization header equal to
// "Bearer <key>". A mismatch on any field means the entry was configured outside
// MintSwitch (or with a different key/endpoint).
func entryMatchesSpec(entry any, spec core.MCPServerSpec) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	if u, _ := obj[serverURLKey].(string); u != spec.Endpoint {
		return false
	}
	headers, ok := obj["headers"].(map[string]any)
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
	if u, _ := obj[serverURLKey].(string); u != endpoint {
		return false
	}
	headers, ok := obj["headers"].(map[string]any)
	if !ok {
		return false
	}
	auth, _ := headers[authHeader].(string)
	return strings.HasPrefix(auth, bearerPrefix)
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

var _ core.MCPInjector = (*Injector)(nil)
