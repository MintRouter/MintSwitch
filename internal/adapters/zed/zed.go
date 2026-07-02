// Package zed implements the core.ToolAdapter for Zed (https://zed.dev). It
// applies and restores a MintSwitch-managed OpenAI-compatible provider in
// Zed's settings.json — ~/.config/zed/settings.json (XDG-aware) on macOS and
// Linux, %APPDATA%\Zed\settings.json on Windows — upserting
// language_models.openai_compatible.mintrouter and agent.default_model while
// preserving all other existing settings.
//
// Zed forbids API keys in settings.json: for openai_compatible providers it
// reads the key from an environment variable generated from the provider ID
// (upper snake case + "_API_KEY"), i.e. MINTROUTER_API_KEY for this provider.
// Apply/Status messages therefore instruct the user to export that variable
// in the shell that launches Zed instead of writing the key to disk.
package zed

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

// providerID is the openai_compatible provider key MintSwitch writes under
// "language_models.openai_compatible". Zed derives the API-key environment
// variable from it: MINTROUTER_API_KEY.
const providerID = "mintrouter"

// modelMaxTokens is the context window advertised for the injected model.
const modelMaxTokens = 128000

// envKeyNote guides the user to provide the API key via the environment,
// since Zed refuses API keys stored in settings.json.
const envKeyNote = "API key: set the MINTROUTER_API_KEY environment variable" +
	" in the shell that launches Zed (Zed forbids API keys in settings.json)."

// Ensure Adapter satisfies the shared tool adapter contracts.
var (
	_ core.ToolAdapter          = (*Adapter)(nil)
	_ core.LegacyMarkerStripper = (*Adapter)(nil)
)

// Adapter manages Zed's settings on behalf of MintSwitch. The managed marker
// lives in the sidecar marker store, never in settings.json.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	m *markers.Store
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
	// appBundles are macOS app-bundle paths whose presence also counts as
	// installed; overridable in tests for determinism.
	appBundles []string
}

// New constructs a Zed adapter. All filesystem locations derive from the
// injected resolver, backup engine, and sidecar marker store so tests can
// point HOME at a temp dir.
func New(r *paths.Resolver, e *backup.Engine, m *markers.Store) *Adapter {
	return &Adapter{
		r:        r,
		e:        e,
		m:        m,
		lookPath: exec.LookPath,
		appBundles: []string{
			"/Applications/Zed.app",
			r.Join("Applications", "Zed.app"),
		},
	}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return "zed" }

// Name returns the display name.
func (a *Adapter) Name() string { return "Zed" }

// configPath returns the global Zed settings.json path: %APPDATA%\Zed on
// Windows, the XDG-aware ~/.config/zed elsewhere (see paths.Resolver.ZedConfigDir).
func (a *Adapter) configPath() string {
	return filepath.Join(a.r.ZedConfigDir(), "settings.json")
}

// ConfigPaths returns the candidate config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.configPath()}
}

// Detect reports whether Zed is installed: either the "zed" CLI is resolvable
// (via PATH or curated bin dirs) or a Zed.app bundle exists. A leftover
// settings file is not an installed signal. The active path is always the
// global settings file and is returned even when not installed, since
// Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	if a.r.BinaryResolvable(a.lookPath, "zed") {
		return true, a.configPath()
	}
	for _, bundle := range a.appBundles {
		if fi, err := os.Stat(bundle); err == nil && fi.IsDir() {
			return true, a.configPath()
		}
	}
	return false, a.configPath()
}

// Status inspects the current settings relative to the given profile. The
// marker is read from the sidecar store: no entry means Default; an entry
// whose managed provider block has been removed from the file also means
// Default (the file is back to an unmanaged state, e.g. after an external
// restore/wipe); otherwise the marker fingerprint decides Applied vs
// ModifiedExternally, exactly as with the legacy in-file marker.
func (a *Adapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	installed, path := a.Detect()
	if !installed {
		return core.StatusNotInstalled, core.StatusNotInstalled.Detail(), nil
	}
	marker, ok, err := a.m.Get(a.ID())
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
	if !hasManagedProvider(root) {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint == core.Fingerprint(fingerprintProfile(p)) {
		detail := core.StatusAppliedByMintSwitch.Detail() + " " + envKeyNote
		return core.StatusAppliedByMintSwitch, detail, nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up the existing settings (only when they are not already
// MintSwitch-managed), then idempotently upserts the MintSwitch
// openai_compatible provider and agent default model, preserving all other
// keys. The profile's API key is never written: Zed reads it from the
// MINTROUTER_API_KEY environment variable. The managed marker is recorded in
// the sidecar store — never in settings.json — and a leftover legacy in-file
// marker is stripped in the same write.
//
// "Already managed" (the backup gate) means a store entry OR a legacy in-file
// marker, so upgrading from a legacy-marker install never snapshots a managed
// file. The backup is created only on the first Apply over a
// pristine/unmanaged (or absent) file, so the pristine pre-MintSwitch snapshot
// is what Restore reverts to even after repeated Applies. Note: Zed settings
// may contain JSONC comments; they are parsed leniently but the file is
// rewritten as plain JSON (comments are preserved only in the backup).
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
	_, inStore, err := a.m.Get(a.ID())
	if err != nil {
		return core.ApplyResult{}, err
	}
	var backupPath string
	if _, hasLegacy := extractMarker(root); !inStore && !hasLegacy {
		backupPath, err = a.e.Backup(path)
		if err != nil {
			return core.ApplyResult{}, err
		}
	}

	languageModels, _ := root["language_models"].(map[string]any)
	if languageModels == nil {
		languageModels = map[string]any{}
	}
	compatible, _ := languageModels["openai_compatible"].(map[string]any)
	if compatible == nil {
		compatible = map[string]any{}
	}
	compatible[providerID] = map[string]any{
		"api_url": p.BaseURL,
		"available_models": []any{
			map[string]any{
				"name":         p.Model,
				"display_name": p.Model,
				"max_tokens":   modelMaxTokens,
			},
		},
	}
	languageModels["openai_compatible"] = compatible
	root["language_models"] = languageModels

	agent, _ := root["agent"].(map[string]any)
	if agent == nil {
		agent = map[string]any{}
	}
	agent["default_model"] = map[string]any{
		"provider": providerID,
		"model":    p.Model,
	}
	root["agent"] = agent
	delete(root, core.MarkerKey)

	if err := writeConfig(path, root); err != nil {
		return core.ApplyResult{}, err
	}
	if err := a.m.Put(a.ID(), core.NewMarker(fingerprintProfile(p), p.Label)); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch provider to Zed settings. " + envKeyNote,
	}, nil
}

// Restore reverts the settings to their pre-apply state via the backup engine
// and removes the tool's entry from the sidecar marker store. It is a safe
// no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	path := a.configPath()
	restored, entry, err := a.e.RestoreLatest(path)
	if err != nil {
		return core.RestoreResult{}, err
	}
	if err := a.m.Delete(a.ID()); err != nil {
		return core.RestoreResult{}, err
	}
	msg := "No backup found; nothing to restore."
	if restored {
		msg = "Restored Zed settings to their pre-apply state."
	}
	return core.RestoreResult{ChangedPath: path, BackupPath: entry, Message: msg}, nil
}

// StripLegacyMarker removes the legacy top-level marker key from
// settings.json, migrating its value into the sidecar store when the store
// has no entry for this tool yet. It is a no-op when the file is absent or
// carries no legacy marker; it never creates the file.
func (a *Adapter) StripLegacyMarker() error {
	path := a.configPath()
	root, err := readConfig(path)
	if err != nil {
		return err
	}
	if root == nil {
		return nil
	}
	legacy, ok := extractMarker(root)
	if !ok {
		if _, present := root[core.MarkerKey]; !present {
			return nil
		}
	}
	if ok {
		if _, inStore, err := a.m.Get(a.ID()); err == nil && !inStore {
			if err := a.m.Put(a.ID(), legacy); err != nil {
				return err
			}
		}
	}
	delete(root, core.MarkerKey)
	return writeConfig(path, root)
}

// readConfig reads and parses the settings file, tolerating Zed's JSONC
// dialect (comments and trailing commas). A missing file returns a nil map
// and no error.
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
	if err := json.Unmarshal(stripJSONC(data), &root); err != nil {
		return nil, err
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

// writeConfig writes the settings as indented JSON, atomically and with
// restrictive permissions, creating parent directories as needed.
func writeConfig(path string, root map[string]any) error {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return core.WriteFileAtomic(path, data, 0o600)
}

// fingerprintProfile returns a copy of the profile with the APIKey cleared,
// for fingerprinting only. Zed never writes the API key to settings.json (it
// is provided via MINTROUTER_API_KEY), so including it in the fingerprint
// would make Status report ModifiedExternally after a key rotation even
// though the managed file is unchanged.
func fingerprintProfile(p core.Profile) core.Profile {
	p.APIKey = ""
	return p
}

// hasManagedProvider reports whether the parsed settings still contain the
// MintSwitch openai_compatible provider block that Apply writes.
func hasManagedProvider(root map[string]any) bool {
	languageModels, _ := root["language_models"].(map[string]any)
	compatible, _ := languageModels["openai_compatible"].(map[string]any)
	_, ok := compatible[providerID]
	return ok
}

// extractMarker decodes the MintSwitch marker from the parsed settings, if present.
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
