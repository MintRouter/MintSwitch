// Package opencode implements the core.ToolAdapter for OpenCode
// (https://opencode.ai). It applies and restores a MintSwitch-managed
// OpenAI-compatible endpoint in OpenCode's global JSON config at
// ~/.config/opencode/opencode.json (XDG-aware), injecting a custom provider
// using the "@ai-sdk/openai-compatible" package and setting it as the default
// model, while preserving all other existing config keys.
package opencode

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

// providerID is the custom provider key MintSwitch writes under "provider".
const providerID = "mintrouter"

// providerName is the human-friendly display name for the provider.
const providerName = "MintSwitch (MintRouter)"

// npmPackage is the AI SDK package used for OpenAI-compatible endpoints.
const npmPackage = "@ai-sdk/openai-compatible"

// Ensure Adapter satisfies the shared tool adapter contract.
var _ core.ToolAdapter = (*Adapter)(nil)

// Adapter manages OpenCode's configuration on behalf of MintSwitch.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New constructs an OpenCode adapter. All filesystem locations derive from the
// injected resolver and backup engine so tests can point HOME at a temp dir.
func New(r *paths.Resolver, e *backup.Engine) *Adapter {
	return &Adapter{r: r, e: e, lookPath: exec.LookPath}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return "opencode" }

// Name returns the display name.
func (a *Adapter) Name() string { return "OpenCode" }

// configPath returns the global opencode.json path (XDG-aware).
func (a *Adapter) configPath() string {
	return a.r.ConfigJoin("opencode", "opencode.json")
}

// ConfigPaths returns the candidate config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.configPath()}
}

// Detect reports whether OpenCode appears installed. It is considered installed
// if its global config file or directory exists, or if the "opencode" binary is
// found on PATH. The active path is always the global config file.
func (a *Adapter) Detect() (bool, string) {
	path := a.configPath()
	if _, err := os.Stat(path); err == nil {
		return true, path
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		return true, path
	}
	look := a.lookPath
	if look == nil {
		look = exec.LookPath
	}
	if _, err := look("opencode"); err == nil {
		return true, path
	}
	return false, path
}

// Status inspects the current config relative to the given profile.
func (a *Adapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	installed, path := a.Detect()
	if !installed {
		return core.StatusNotInstalled, core.StatusNotInstalled.Detail(), nil
	}
	root, err := readConfig(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	marker, ok := extractMarker(root)
	if !ok {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint == core.Fingerprint(p) {
		return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up the existing config (only when it is not already
// MintSwitch-managed), then idempotently injects the MintSwitch provider,
// default model, and managed marker, preserving all other keys.
//
// The backup is created only on the first Apply over a pristine/unmanaged (or
// absent) file, so the pristine pre-MintSwitch snapshot is what Restore reverts
// to even after repeated Applies. Backing up an already-managed file would
// snapshot a managed state (prior profile's key + marker) and hide the pristine
// original. Limitation: if the file is already managed but no backup exists
// (e.g. the backups dir was deleted, or a marker was left by an older version),
// no new backup is taken and Restore is a no-op — we cannot safely snapshot a
// managed file, and without the original we cannot distinguish our injected keys
// from the user's own to strip them.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	path := a.configPath()
	root, err := readConfig(path)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if root == nil {
		root = map[string]any{}
	}
	var backupPath string
	if _, managed := extractMarker(root); !managed {
		backupPath, err = a.e.Backup(path)
		if err != nil {
			return core.ApplyResult{}, err
		}
	}

	provider, _ := root["provider"].(map[string]any)
	if provider == nil {
		provider = map[string]any{}
	}
	provider[providerID] = map[string]any{
		"npm":  npmPackage,
		"name": providerName,
		"options": map[string]any{
			"baseURL": p.BaseURL,
			"apiKey":  p.APIKey,
		},
		"models": map[string]any{
			p.Model: map[string]any{"name": p.Model},
		},
	}
	root["provider"] = provider
	root["model"] = providerID + "/" + p.Model
	root[core.MarkerKey] = core.NewMarker(p, p.Label)

	if err := writeConfig(path, root); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch provider to OpenCode config.",
	}, nil
}

// Restore reverts the config to its pre-apply state via the backup engine. It
// is a safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	path := a.configPath()
	restored, entry, err := a.e.RestoreLatest(path)
	if err != nil {
		return core.RestoreResult{}, err
	}
	msg := "No backup found; nothing to restore."
	if restored {
		msg = "Restored OpenCode config to its pre-apply state."
	}
	return core.RestoreResult{ChangedPath: path, BackupPath: entry, Message: msg}, nil
}

// readConfig reads and parses the JSON config file. A missing file returns a
// nil map and no error.
func readConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

// writeConfig writes the config as indented JSON with restrictive permissions,
// creating parent directories as needed.
func writeConfig(path string, root map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// extractMarker decodes the MintSwitch marker from the parsed config, if present.
func extractMarker(root map[string]any) (core.Marker, bool) {
	raw, ok := root[core.MarkerKey]
	if !ok {
		return core.Marker{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return core.Marker{}, false
	}
	var m core.Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return core.Marker{}, false
	}
	if !m.Managed {
		return core.Marker{}, false
	}
	return m, true
}
