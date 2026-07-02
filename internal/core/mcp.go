package core

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultMCPServerName is the MCP server entry name MintSwitch owns in a tool's
// config. MintSwitch only ever reads/writes the entry under this name.
const DefaultMCPServerName = "mintrouter"

// DefaultMCPEndpoint is the MintRouter Remote MCP endpoint injected into tools.
// It is a single, trivially-changeable constant so the endpoint can be updated
// in one place.
const DefaultMCPEndpoint = "https://mintrouter.ai/mcp"

// MCPServerSpec describes the MintRouter Remote MCP server an injector writes
// into a tool's config. APIKey is a secret bearer token and is never serialized
// (json:"-") so it cannot leak through bindings or logs.
type MCPServerSpec struct {
	Name     string `json:"name,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	APIKey   string `json:"-"`
}

// Normalized returns a copy of the spec with empty Name/Endpoint filled from the
// package defaults, so injectors always operate on a complete spec.
func (s MCPServerSpec) Normalized() MCPServerSpec {
	if s.Name == "" {
		s.Name = DefaultMCPServerName
	}
	if s.Endpoint == "" {
		s.Endpoint = DefaultMCPEndpoint
	}
	return s
}

// MCPStatus describes the configuration state of a tool's MCP config relative to
// the MintSwitch-owned server entry.
type MCPStatus int

const (
	// MCPNotInstalled means the tool was not detected on the system.
	MCPNotInstalled MCPStatus = iota
	// MCPNotConfigured means the tool is installed but carries no MintSwitch MCP
	// server entry.
	MCPNotConfigured
	// MCPConfiguredByMintSwitch means the MintSwitch MCP server entry is present
	// with our exact endpoint + auth shape.
	MCPConfiguredByMintSwitch
	// MCPConfiguredExternally means an entry under our name exists but differs
	// from our shape, so it was configured outside MintSwitch and must not be
	// clobbered without explicit user action.
	MCPConfiguredExternally
)

// String returns a stable lower-case identifier for the status.
func (s MCPStatus) String() string {
	switch s {
	case MCPNotInstalled:
		return "not_installed"
	case MCPNotConfigured:
		return "not_configured"
	case MCPConfiguredByMintSwitch:
		return "configured_by_mintswitch"
	case MCPConfiguredExternally:
		return "configured_externally"
	default:
		return "unknown"
	}
}

// MCPResult summarizes the outcome of an inject/remove operation. It mirrors
// [ApplyResult] and carries only display-safe fields (never a secret).
type MCPResult struct {
	// ChangedPath is the tool config file that was written.
	ChangedPath string `json:"changed_path,omitempty"`
	// BackupPath is the backup created/used, if any.
	BackupPath string `json:"backup_path,omitempty"`
	// Message is a human-readable summary, safe to display (no secrets).
	Message string `json:"message,omitempty"`
}

// MCPInjector is the contract for injecting the MintRouter Remote MCP server
// into a single AI tool. It is deliberately separate from [ToolAdapter] (which
// owns the endpoint/Profile) so MCP injection is independent of the profile.
//
// Implementations must derive all filesystem locations from an injected
// paths.Resolver so tests can point HOME at a temporary directory.
type MCPInjector interface {
	// ID returns a stable identifier, e.g. "claude-code".
	ID() string
	// MCPConfigPaths returns the absolute config file paths this injector manages.
	MCPConfigPaths() []string
	// Detect reports whether the tool is installed.
	Detect() (installed bool)
	// MCPStatus inspects the current config relative to spec.
	MCPStatus(spec MCPServerSpec) (MCPStatus, string, error)
	// InjectMCP backs up the config on first modification, then idempotently
	// writes the MintSwitch-owned server entry, preserving all other keys.
	InjectMCP(spec MCPServerSpec) (MCPResult, error)
	// RemoveMCP reverts the injection: restore from backup when one exists, else
	// strip only our entry. It is a safe no-op when nothing was applied.
	RemoveMCP() (MCPResult, error)
}

// ReadJSONObject reads path as a JSON object. A missing or empty file yields an
// empty object so callers can merge without special-casing first-run.
func ReadJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// WriteJSONObjectAtomic writes m as indented JSON to path via a sibling temp
// file + rename, creating parent dirs. The file carries 0600 perms since it may
// contain an auth token.
func WriteJSONObjectAtomic(path string, m map[string]any) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return WriteFileAtomic(path, data, 0o600)
}

// WriteFileAtomic writes data to path atomically: it creates parent
// directories (0700), writes to a sibling temp file with the given perm,
// fsyncs, then renames over path (os.Rename replaces an existing file on
// every supported OS). A crash mid-write can never leave a truncated config
// behind.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mintswitch-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// AsJSONObject returns v as a JSON object, or a fresh object when v is not one.
func AsJSONObject(v any) map[string]any {
	if obj, ok := v.(map[string]any); ok {
		return obj
	}
	return map[string]any{}
}
