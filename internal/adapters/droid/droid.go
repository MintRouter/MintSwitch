// Package droid implements the core.ToolAdapter for Factory Droid
// (https://factory.ai). It applies and restores a MintSwitch-managed
// OpenAI-compatible endpoint in Droid's global JSON settings at
// ~/.factory/settings.json, upserting a single MintSwitch-owned entry in the
// "customModels" array (BYOK, provider "generic-chat-completion-api") and
// setting the top-level "model" to the selected model, while preserving all
// other existing config keys.
//
// Schema reference (verified 2026-07-02): Factory Droid BYOK — customModels
// entries carry camelCase keys {model, displayName, baseUrl, apiKey, provider,
// maxOutputTokens}.
package droid

import (
	"os"
	"os/exec"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

// entryDisplayName identifies the MintSwitch-owned customModels entry.
const entryDisplayName = "MintSwitch (MintRouter)"

// providerType is the Droid BYOK provider for OpenAI-compatible endpoints.
const providerType = "generic-chat-completion-api"

// customModelsKey is the settings.json array holding BYOK model entries.
const customModelsKey = "customModels"

// defaultMaxOutputTokens is written to the entry; Droid requires the field.
const defaultMaxOutputTokens = 32768

// Ensure Adapter satisfies the shared tool adapter contract.
var _ core.ToolAdapter = (*Adapter)(nil)

// Adapter manages Factory Droid's configuration on behalf of MintSwitch.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New constructs a Factory Droid adapter. All filesystem locations derive from
// the injected resolver and backup engine so tests can point HOME at a temp dir.
func New(r *paths.Resolver, e *backup.Engine) *Adapter {
	return &Adapter{r: r, e: e, lookPath: exec.LookPath}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return "droid" }

// Name returns the display name.
func (a *Adapter) Name() string { return "Factory Droid" }

// configPath returns the global settings.json path under ~/.factory.
func (a *Adapter) configPath() string {
	return a.r.Join(".factory", "settings.json")
}

// ConfigPaths returns the candidate config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.configPath()}
}

// Detect reports whether Factory Droid is installed: either the "droid" CLI
// binary is resolvable (via PATH or the curated common bin dirs, which include
// ~/.local/bin) or the ~/.factory directory exists (Droid always creates it).
// The active path is always the global settings file and is returned even when
// not installed, since Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	path := a.configPath()
	if a.r.BinaryResolvable(a.lookPath, "droid") {
		return true, path
	}
	if fi, err := os.Stat(a.r.Join(".factory")); err == nil && fi.IsDir() {
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
	root, err := core.ReadJSONObject(path)
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
// MintSwitch-managed), then idempotently upserts the MintSwitch customModels
// entry, sets the top-level "model" to the selected model, and writes the
// managed marker, preserving all other keys (including the user's own
// customModels entries). The write is atomic at 0600.
//
// The backup is created only on the first Apply over a pristine/unmanaged (or
// absent) file, so the pristine pre-MintSwitch snapshot is what Restore reverts
// to even after repeated Applies.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	path := a.configPath()
	root, err := core.ReadJSONObject(path)
	if err != nil {
		return core.ApplyResult{}, err
	}
	var backupPath string
	if _, managed := extractMarker(root); !managed {
		backupPath, err = a.e.Backup(path)
		if err != nil {
			return core.ApplyResult{}, err
		}
	}

	upsertCustomModel(root, customModelEntry(p))
	root["model"] = p.Model
	root[core.MarkerKey] = core.NewMarker(p, p.Label)

	if err := core.WriteJSONObjectAtomic(path, root); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch custom model to Factory Droid settings.",
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
		msg = "Restored Factory Droid settings to their pre-apply state."
	}
	return core.RestoreResult{ChangedPath: path, BackupPath: entry, Message: msg}, nil
}
