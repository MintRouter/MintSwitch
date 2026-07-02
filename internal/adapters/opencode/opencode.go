// Package opencode implements the core.ToolAdapter for OpenCode
// (https://opencode.ai). It applies and restores a MintSwitch-managed
// OpenAI-compatible endpoint in OpenCode's global JSON config at
// ~/.config/opencode/opencode.json (XDG-aware), injecting a custom provider
// using the "@ai-sdk/openai-compatible" package and setting it as the default
// model, while preserving all other existing config keys. The managed marker
// lives in the sidecar marker store, never in opencode.json: OpenCode
// validates the file with a strict zod schema and rejects unknown top-level
// keys.
package opencode

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

// id is the stable adapter identifier, also the tool's key in the marker store.
const id = "opencode"

// providerID is the custom provider key MintSwitch writes under "provider".
const providerID = "mintrouter"

// providerName is the human-friendly display name for the provider.
const providerName = "MintSwitch (MintRouter)"

// npmPackage is the AI SDK package used for OpenAI-compatible endpoints.
const npmPackage = "@ai-sdk/openai-compatible"

// Ensure Adapter satisfies the shared adapter contracts.
var (
	_ core.ToolAdapter          = (*Adapter)(nil)
	_ core.LegacyMarkerStripper = (*Adapter)(nil)
)

// Adapter manages OpenCode's configuration on behalf of MintSwitch.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	m *markers.Store
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New constructs an OpenCode adapter that resolves paths via r, backs up via e,
// and records its managed marker in m. All filesystem locations derive from the
// injected dependencies so tests can point HOME at a temp dir.
func New(r *paths.Resolver, e *backup.Engine, m *markers.Store) *Adapter {
	return &Adapter{r: r, e: e, m: m, lookPath: exec.LookPath}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return id }

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

// Detect reports whether OpenCode is installed, defined solely as the "opencode"
// CLI binary being resolvable (via PATH or a curated set of common bin dirs). A
// leftover global config file/dir is not an installed signal, so an uninstall is
// reflected. The active path is always the global config file and is returned
// even when not installed, since Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	return a.r.BinaryResolvable(a.lookPath, "opencode"), a.configPath()
}

// Status inspects the current config relative to the given profile. The marker
// is read from the sidecar store: no entry means Default; an entry whose
// managed provider block ("provider".mintrouter) has been removed from the file
// also means Default (the file is back to an unmanaged state, e.g. after an
// external restore/wipe); otherwise the marker fingerprint decides Applied vs
// ModifiedExternally, exactly as with the legacy in-file marker.
func (a *Adapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	installed, path := a.Detect()
	if !installed {
		return core.StatusNotInstalled, core.StatusNotInstalled.Detail(), nil
	}
	marker, ok, err := a.m.Get(id)
	if err != nil {
		return core.StatusDefault, "", err
	}
	if !ok || !marker.Managed {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	root, err := readConfig(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	provider, _ := root["provider"].(map[string]any)
	if _, present := provider[providerID]; !present {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint == core.Fingerprint(p) {
		return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up the existing config (only when it is not already
// MintSwitch-managed), then idempotently injects the MintSwitch provider and
// default model, preserving all other keys. The managed marker is recorded in
// the sidecar store — never in opencode.json, which OpenCode validates strictly —
// and a leftover legacy in-file marker is stripped in the same write.
//
// "Already managed" (the backup gate) means a store entry OR a legacy in-file
// marker, so upgrading from a legacy-marker install never snapshots a managed
// file. The backup is created only on the first Apply over a pristine/unmanaged
// (or absent) file, so the pristine pre-MintSwitch snapshot is what Restore
// reverts to even after repeated Applies. Backing up an already-managed file
// would snapshot a managed state (prior profile's key) and hide the pristine
// original. If the file is already managed but no backup exists (e.g. the
// backups dir was deleted), no new backup is taken — we cannot safely snapshot
// a managed file; Restore then falls back to stripping the managed provider.
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
	_, inStore, err := a.m.Get(id)
	if err != nil {
		return core.ApplyResult{}, err
	}
	var backupPath string
	legacy, hasLegacy := extractLegacyMarker(root)
	if !inStore && !(hasLegacy && legacy.Managed) {
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
	delete(root, core.MarkerKey)

	if err := writeConfig(path, root); err != nil {
		return core.ApplyResult{}, err
	}
	if err := a.m.Put(id, core.NewMarker(p, p.Label)); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch provider to OpenCode config.",
	}, nil
}

// Restore reverts the config to its pristine pre-MintSwitch state via the
// backup engine (oldest snapshot; all entries are pruned after a successful
// restore) and removes the tool's entry from the sidecar marker store. When
// no backup exists but the file is still MintSwitch-managed (marker in store
// and provider.mintrouter present), it falls back to stripping the managed
// provider, preserving every other key. It is a safe no-op when nothing was
// applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	path := a.configPath()
	_, inStore, err := a.m.Get(id)
	if err != nil {
		return core.RestoreResult{}, err
	}
	restored, entry, err := a.e.RestorePristine(path)
	if err != nil {
		return core.RestoreResult{}, err
	}
	stripped := false
	if !restored && inStore {
		stripped, err = a.stripManaged(path)
		if err != nil {
			return core.RestoreResult{}, err
		}
	}
	if err := a.m.Delete(id); err != nil {
		return core.RestoreResult{}, err
	}
	msg := "No backup found; nothing to restore."
	switch {
	case restored:
		msg = "Restored OpenCode config to its pre-apply state."
	case stripped:
		msg = "No backup found; removed the MintSwitch provider from OpenCode config."
	}
	return core.RestoreResult{ChangedPath: path, BackupPath: entry, Message: msg}, nil
}

// stripManaged removes the MintSwitch provider block (provider.mintrouter)
// from opencode.json, dropping the "provider" object when it becomes empty,
// and clears the default "model" when it still points at the removed
// provider. It is the Restore fallback when no pristine backup exists. Gated
// on the managed signal (provider.mintrouter present) so an unmanaged file is
// never rewritten; it never creates the file.
func (a *Adapter) stripManaged(path string) (bool, error) {
	root, err := readConfig(path)
	if err != nil {
		return false, err
	}
	provider, _ := root["provider"].(map[string]any)
	if _, present := provider[providerID]; !present {
		return false, nil
	}
	delete(provider, providerID)
	if len(provider) == 0 {
		delete(root, "provider")
	} else {
		root["provider"] = provider
	}
	if m, _ := root["model"].(string); strings.HasPrefix(m, providerID+"/") {
		delete(root, "model")
	}
	return true, writeConfig(path, root)
}

// StripLegacyMarker removes the legacy top-level marker key from opencode.json,
// migrating its value into the sidecar store when the store has no entry for
// this tool yet. It is a no-op when the file is absent or carries no legacy
// marker; it never creates the file.
func (a *Adapter) StripLegacyMarker() error {
	path := a.configPath()
	root, err := readConfig(path)
	if err != nil {
		return err
	}
	if root == nil {
		return nil
	}
	if _, present := root[core.MarkerKey]; !present {
		return nil
	}
	if legacy, ok := extractLegacyMarker(root); ok && legacy.Managed {
		if _, inStore, err := a.m.Get(id); err == nil && !inStore {
			if err := a.m.Put(id, legacy); err != nil {
				return err
			}
		}
	}
	delete(root, core.MarkerKey)
	return writeConfig(path, root)
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

// writeConfig writes the config as indented JSON, atomically and with
// restrictive permissions, creating parent directories as needed.
func writeConfig(path string, root map[string]any) error {
	return core.WriteJSONObjectAtomic(path, root)
}

// extractLegacyMarker pulls a legacy in-file MintSwitch marker out of the
// parsed config. It reports false when the key is absent or its value does not
// decode as a [core.Marker].
func extractLegacyMarker(root map[string]any) (core.Marker, bool) {
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
	return m, true
}
