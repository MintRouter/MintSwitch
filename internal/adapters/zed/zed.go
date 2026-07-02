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

// orphanDetail explains the orphan-remnant state: the settings still carry the
// MintSwitch provider but the managed marker is gone (e.g. a previous restore
// was interrupted after clearing the marker).
const orphanDetail = "The MintSwitch provider is still present but the managed marker is missing " +
	"(a previous restore may have been interrupted). Restore Default will remove it."

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
// marker is read from the sidecar store: no entry means Default — unless the
// file still carries the MintSwitch provider block (see orphanRemnantAt),
// which reports ModifiedExternally so the UI offers Restore even after the
// marker was lost (e.g. an interrupted restore). An entry whose managed
// provider block has been removed from the file also means Default (the file
// is back to an unmanaged state, e.g. after an external restore/wipe);
// otherwise the marker fingerprint decides Applied vs ModifiedExternally,
// exactly as with the legacy in-file marker.
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
		if a.orphanRemnantAt(path) {
			return core.StatusModifiedExternally, orphanDetail, nil
		}
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
	legacy, hasLegacy := core.ExtractLegacyMarker(root)
	if !inStore && !(hasLegacy && legacy.Managed) {
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

// Restore reverts the settings to their pristine pre-MintSwitch state via the
// backup engine (oldest snapshot; all entries are pruned after a successful
// restore) and removes the tool's entry from the sidecar marker store. When
// no backup exists but the file is still MintSwitch-managed (marker in store
// and the managed provider block present, or — with the marker lost — the
// provider block still in the file, see orphanRemnantAt), it falls back to
// stripping the managed provider and agent default model, preserving every
// other setting. It is a safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	return core.RestoreSingleFile(core.SingleFileRestore{
		ToolID:          a.ID(),
		Path:            a.configPath(),
		Store:           a.m,
		RestorePristine: a.e.RestorePristine,
		OrphanRemnantAt: a.orphanRemnantAt,
		StripManaged:    a.stripManaged,
		RestoredMessage: "Restored Zed settings to their pre-apply state.",
		StrippedMessage: "No backup found; removed the MintSwitch provider from Zed settings.",
	})
}

// orphanRemnantAt reports whether the settings file at path still carries the
// MintSwitch provider block (language_models.openai_compatible.mintrouter)
// without requiring a marker. The "mintrouter" provider key is
// MintSwitch-specific — no tool or user writes it independently — so its
// presence alone is a reliable remnant signal. A missing or corrupt file is
// never a remnant (JSONC comments are tolerated by readConfig, and a pure
// user file without the block is never rewritten, so its comments survive).
func (a *Adapter) orphanRemnantAt(path string) bool {
	root, err := readConfig(path)
	if err != nil || root == nil {
		return false
	}
	return hasManagedProvider(root)
}

// stripManaged removes the MintSwitch openai_compatible provider block from
// settings.json (dropping emptied parent objects) and the agent default_model
// when it still points at the removed provider, preserving every other
// setting. It is the Restore fallback when no pristine backup exists. Gated
// on the managed signal (the provider block being present) so an unmanaged
// file is never rewritten; it never creates the file. Like Apply, it rewrites
// the file as plain JSON, so JSONC comments are not preserved.
func (a *Adapter) stripManaged(path string) (bool, error) {
	root, err := readConfig(path)
	if err != nil {
		return false, err
	}
	if root == nil || !hasManagedProvider(root) {
		return false, nil
	}
	languageModels, _ := root["language_models"].(map[string]any)
	compatible, _ := languageModels["openai_compatible"].(map[string]any)
	delete(compatible, providerID)
	if len(compatible) == 0 {
		delete(languageModels, "openai_compatible")
	} else {
		languageModels["openai_compatible"] = compatible
	}
	if len(languageModels) == 0 {
		delete(root, "language_models")
	} else {
		root["language_models"] = languageModels
	}
	if agent, _ := root["agent"].(map[string]any); agent != nil {
		if dm, _ := agent["default_model"].(map[string]any); dm != nil {
			if pid, _ := dm["provider"].(string); pid == providerID {
				delete(agent, "default_model")
				if len(agent) == 0 {
					delete(root, "agent")
				} else {
					root["agent"] = agent
				}
			}
		}
	}
	return true, writeConfig(path, root)
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
	legacy, ok := core.ExtractLegacyMarker(root)
	if !ok {
		if _, present := root[core.MarkerKey]; !present {
			return nil
		}
	}
	if ok && legacy.Managed {
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
	return core.WriteJSONObjectAtomic(path, root)
}

// fingerprintProfile returns a copy of the profile with the APIKey cleared,
// for fingerprinting only. Zed never writes the API key to settings.json (it
// is provided via MINTROUTER_API_KEY), so including it in the fingerprint
// would make Status report ModifiedExternally after a key rotation even
// though the managed file is unchanged. This deliberately diverges from the
// other adapters, which fingerprint the full profile via core.Fingerprint;
// TestFingerprintProfileIgnoresOnlyAPIKey guards against drift.
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
