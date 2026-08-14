// Package pi implements the core.ToolAdapter for Pi (https://pi.dev), the
// @earendil-works/pi-coding-agent CLI. It applies and restores a
// MintSwitch-managed OpenAI-compatible provider across Pi's two global config
// files under ~/.pi/agent: models.json — upserting a custom provider
// "mintrouter" of the form { baseUrl, api: "openai-completions", apiKey,
// models: [{id, name}] } under the top-level "providers" map — and
// settings.json — setting defaultProvider/defaultModel — while preserving all
// other existing keys in each file. The managed marker lives in the sidecar
// marker store, never in Pi's own files.
//
// Schema reference (verified 2026-08-14 from
// github.com/earendil-works/pi packages/coding-agent/docs/models.md and
// settings.md): models.json carries { "providers": { <id>: { baseUrl, api,
// apiKey, models: [{ id, name, ... }] } } }; settings.json carries flat
// defaultProvider (provider key) and defaultModel (model id).
package pi

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

// id is the stable adapter identifier, also the tool's key in the marker store.
const id = "pi"

// providerID is the custom provider key MintSwitch writes under "providers" in
// models.json and into settings.json's defaultProvider.
const providerID = "mintrouter"

// apiType is Pi's API type for OpenAI-compatible Chat Completions endpoints.
const apiType = "openai-completions"

// orphanDetail explains the orphan-remnant state: models.json still carries
// the MintSwitch provider but the managed marker is gone (e.g. a previous
// restore was interrupted after clearing the marker).
const orphanDetail = "The MintSwitch provider is still present but the managed marker is missing " +
	"(a previous restore may have been interrupted). Restore Default will remove it."

// settingsDriftDetail explains the settings-drift state: models.json still
// carries the MintSwitch provider, but settings.json no longer selects it as
// the default — typically because the user switched models inside Pi (/model
// rewrites defaultProvider/defaultModel). Pi then routes traffic elsewhere, so
// the profile must be re-applied.
const settingsDriftDetail = "settings.json no longer selects the MintSwitch provider/model as default " +
	"(Pi's /model picker likely changed it), so Pi bypasses the configured endpoint. Apply the profile again to fix this."

// Ensure Adapter satisfies the shared adapter contract.
var _ core.ToolAdapter = (*Adapter)(nil)

// Adapter manages Pi's configuration on behalf of MintSwitch. The managed
// marker lives in the sidecar marker store, never in models.json/settings.json.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	m *markers.Store
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
	// writeSettings writes settings.json; overridable in tests to inject write
	// failures into the second half of Apply's two-file write. Defaults to
	// core.WriteJSONObjectAtomic.
	writeSettings func(string, map[string]any) error
}

// New constructs a Pi adapter that resolves paths via r, backs up via e, and
// records its managed marker in m. All filesystem locations derive from the
// injected dependencies so tests can point HOME at a temp dir.
func New(r *paths.Resolver, e *backup.Engine, m *markers.Store) *Adapter {
	return &Adapter{r: r, e: e, m: m, lookPath: exec.LookPath, writeSettings: core.WriteJSONObjectAtomic}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return id }

// Name returns the display name.
func (a *Adapter) Name() string { return "Pi" }

// modelsPath returns the absolute path to Pi's global custom-models file
// (~/.pi/agent/models.json).
func (a *Adapter) modelsPath() string { return a.r.Join(".pi", "agent", "models.json") }

// settingsPath returns the absolute path to Pi's global settings file
// (~/.pi/agent/settings.json).
func (a *Adapter) settingsPath() string { return a.r.Join(".pi", "agent", "settings.json") }

// ConfigPaths returns the config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.modelsPath(), a.settingsPath()}
}

// Detect reports whether Pi is installed, defined solely as the "pi" CLI
// binary being resolvable (via PATH or a curated set of common bin dirs). A
// leftover ~/.pi dir is not an installed signal, so an uninstall is reflected.
// The active path is always models.json and is returned even when not
// installed, since Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	return a.r.BinaryResolvable(a.lookPath, "pi"), a.modelsPath()
}

// Status inspects models.json and settings.json relative to the given profile.
// The marker is read from the sidecar store: no entry means Default — unless
// models.json still carries the MintSwitch provider block (see orphanRemnant),
// which reports ModifiedExternally so the UI offers Restore even after the
// marker was lost (e.g. an interrupted restore). An entry whose managed
// provider block (providers.mintrouter) has been removed from the file also
// means Default (the file is back to an unmanaged state, e.g. after an
// external restore/wipe); otherwise the marker fingerprint decides Applied vs
// ModifiedExternally. Even with a matching fingerprint, settings.json must
// still select the MintSwitch provider and model as default: Pi's /model
// picker rewrites defaultProvider/defaultModel behind MintSwitch's back, so
// that state reports ModifiedExternally (settingsDriftDetail) instead of a
// false Applied.
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
		if a.orphanRemnant() {
			return core.StatusModifiedExternally, orphanDetail, nil
		}
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	root, err := core.ReadJSONObject(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	providers, _ := root["providers"].(map[string]any)
	if _, present := providers[providerID]; !present {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint != core.Fingerprint(p) {
		return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
	}
	if a.settingsDrifted(p) {
		return core.StatusModifiedExternally, settingsDriftDetail, nil
	}
	return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
}

// settingsDrifted reports whether settings.json no longer selects the
// MintSwitch provider and model that Apply wrote for the given profile:
// defaultProvider flipped away from "mintrouter", defaultModel was changed, or
// the file is unreadable/corrupt. It is only meaningful when models.json is
// confirmed MintSwitch-managed with a matching fingerprint, so any mismatch
// here is by definition an external change.
func (a *Adapter) settingsDrifted(p core.Profile) bool {
	settings, err := core.ReadJSONObject(a.settingsPath())
	if err != nil {
		return true
	}
	if prov, _ := settings["defaultProvider"].(string); prov != providerID {
		return true
	}
	model, _ := settings["defaultModel"].(string)
	return model != p.Model
}

// Apply backs up both files (only when Pi is not already MintSwitch-managed),
// then upserts the MintSwitch provider under "providers" in models.json and
// sets defaultProvider/defaultModel in settings.json, preserving all other
// existing keys in each file. The managed marker is recorded in the sidecar
// store — never in Pi's own files.
//
// "Already managed" (the backup gate) means a store entry, so the backups are
// created only on the first Apply over a pristine/unmanaged (or absent)
// config: the pristine pre-MintSwitch snapshots are what Restore reverts to
// even after repeated Applies. settings.json carries no provider block but is
// gated by the same check so both files snapshot the same pre-MintSwitch
// point in time. If Pi is already managed but no backup exists (e.g. the
// backups dir was deleted), no new backup is taken — we cannot safely
// snapshot a managed file; Restore then falls back to stripping the managed
// keys.
//
// Write order matters: models.json is written first, settings.json second
// (with a best-effort rollback of models.json when the settings.json write
// fails). If the process dies between the two writes, settings.json — the
// file that switches Pi's default onto the provider — is still pristine, so
// Pi never selects a provider that does not exist in models.json. The
// leftover provider entry never routes traffic anywhere on its own, and the
// next Apply or Restore overwrites or strips it.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	modelsPath, settingsPath := a.modelsPath(), a.settingsPath()

	// Read both files up front so a corrupt file fails the Apply before
	// anything is written.
	models, err := core.ReadJSONObject(modelsPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	settings, err := core.ReadJSONObject(settingsPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	origModels, readErr := os.ReadFile(modelsPath)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return core.ApplyResult{}, readErr
	}
	modelsExisted := readErr == nil

	_, inStore, err := a.m.Get(id)
	if err != nil {
		return core.ApplyResult{}, err
	}
	var modelsBackup string
	if !inStore {
		modelsBackup, err = a.e.Backup(modelsPath)
		if err != nil {
			return core.ApplyResult{}, err
		}
		if _, err := a.e.Backup(settingsPath); err != nil {
			return core.ApplyResult{}, err
		}
	}

	providers, _ := models["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	// One entry per applied model ([core.Profile.ApplyModels]: just the
	// selected model, or every provider model in "All models" mode).
	modelEntries := make([]any, 0)
	for _, m := range p.ApplyModels() {
		modelEntries = append(modelEntries, map[string]any{"id": m, "name": m})
	}
	providers[providerID] = map[string]any{
		"baseUrl": p.BaseURL,
		"api":     apiType,
		"apiKey":  p.APIKey,
		"models":  modelEntries,
	}
	models["providers"] = providers
	if err := core.WriteJSONObjectAtomic(modelsPath, models); err != nil {
		return core.ApplyResult{}, err
	}
	// rollbackModels best-effort reverts models.json to its pre-Apply bytes
	// when the settings.json half of the two-file write fails, so a failed
	// Apply never leaves the MintSwitch provider (and API key) behind in
	// models.json while settings.json was left untouched.
	rollbackModels := func() {
		if modelsExisted {
			_ = core.WriteFileAtomic(modelsPath, origModels, 0o600)
		} else {
			_ = os.Remove(modelsPath)
		}
	}

	settings["defaultProvider"] = providerID
	settings["defaultModel"] = p.Model
	if err := a.writeSettings(settingsPath, settings); err != nil {
		rollbackModels()
		return core.ApplyResult{}, err
	}

	if err := a.m.Put(id, core.NewMarker(p, p.Label)); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: modelsPath,
		BackupPath:  modelsBackup,
		Message:     "Applied MintSwitch provider to Pi models.json and settings.json.",
	}, nil
}

// Restore reverts models.json and settings.json to their pristine
// pre-MintSwitch state via the backup engine (oldest snapshots; all entries
// are pruned after a successful restore). Both restores are attempted
// best-effort even when one fails, so an error on models.json never silently
// skips settings.json (or vice versa); failures are joined into a single
// error naming each file. When a file has no backup but Pi is still
// MintSwitch-managed (marker in store, or — with the marker lost — the
// provider block still in models.json, see orphanRemnant), Restore falls back
// to stripping the managed keys from it — providers.mintrouter in models.json,
// defaultProvider and defaultModel in settings.json — preserving every other
// key. It is a safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	modelsPath, settingsPath := a.modelsPath(), a.settingsPath()
	_, inStore, err := a.m.Get(id)
	if err != nil {
		return core.RestoreResult{}, err
	}
	orphan := !inStore && a.orphanRemnant()
	modelsRestored, modelsEntry, modelsErr := a.e.RestorePristine(modelsPath)
	settingsRestored, _, settingsErr := a.e.RestorePristine(settingsPath)
	if modelsErr != nil {
		modelsErr = fmt.Errorf("restore models.json: %w", modelsErr)
	}
	if settingsErr != nil {
		settingsErr = fmt.Errorf("restore settings.json: %w", settingsErr)
	}
	if err := errors.Join(modelsErr, settingsErr); err != nil {
		return core.RestoreResult{}, err
	}
	var modelsStripped, settingsStripped bool
	if !modelsRestored && (inStore || orphan) {
		modelsStripped, err = stripManagedModels(modelsPath)
		if err != nil {
			return core.RestoreResult{}, err
		}
	}
	if !settingsRestored && (inStore || orphan) {
		settingsStripped, err = stripManagedSettings(settingsPath)
		if err != nil {
			return core.RestoreResult{}, err
		}
	}
	if err := a.m.Delete(id); err != nil {
		return core.RestoreResult{}, err
	}
	var msg string
	switch {
	case modelsRestored && settingsRestored:
		msg = "Restored Pi models.json and settings.json from backup."
	case modelsRestored && settingsStripped:
		msg = "Restored Pi models.json from backup; no backup found for settings.json, so the MintSwitch default provider/model was removed from it."
	case modelsRestored:
		msg = "Restored Pi models.json from backup; no backup found for settings.json."
	case settingsRestored && modelsStripped:
		msg = "Restored Pi settings.json from backup; no backup found for models.json, so the MintSwitch provider was removed from it."
	case settingsRestored:
		msg = "Restored Pi settings.json from backup; no backup found for models.json."
	case modelsStripped || settingsStripped:
		msg = "No backup found; removed the MintSwitch-managed keys from the Pi config files."
	default:
		msg = "No backup found; nothing to restore."
	}
	return core.RestoreResult{
		ChangedPath: modelsPath,
		BackupPath:  modelsEntry,
		Message:     msg,
	}, nil
}

// orphanRemnant reports whether models.json still carries the MintSwitch
// provider block (providers.mintrouter) without requiring a marker. The
// "mintrouter" provider key is MintSwitch-specific — no tool or user writes
// it independently — so its presence alone is a reliable remnant signal. A
// missing or corrupt file is never a remnant, so this probe can never make
// Status error or Restore touch a file it could not safely rewrite.
func (a *Adapter) orphanRemnant() bool {
	root, err := core.ReadJSONObject(a.modelsPath())
	if err != nil {
		return false
	}
	providers, _ := root["providers"].(map[string]any)
	_, present := providers[providerID]
	return present
}

// stripManagedModels removes the MintSwitch provider block
// (providers.mintrouter) from models.json, dropping the "providers" object
// when it becomes empty. It is the Restore fallback when no pristine backup
// exists. Gated on the managed signal (providers.mintrouter present) so an
// unmanaged file is never rewritten; it never creates the file.
func stripManagedModels(path string) (bool, error) {
	root, err := core.ReadJSONObject(path)
	if err != nil {
		return false, err
	}
	providers, _ := root["providers"].(map[string]any)
	if _, present := providers[providerID]; !present {
		return false, nil
	}
	delete(providers, providerID)
	if len(providers) == 0 {
		delete(root, "providers")
	} else {
		root["providers"] = providers
	}
	return true, core.WriteJSONObjectAtomic(path, root)
}

// stripManagedSettings removes defaultProvider and defaultModel from
// settings.json, preserving every other key. Apply always overwrites both, so
// with the managed signal still present the current values are MintSwitch's.
// Gated on defaultProvider still pointing at the MintSwitch provider so an
// unmanaged file (or one the user already repointed) is never rewritten; it
// never creates the file.
func stripManagedSettings(path string) (bool, error) {
	settings, err := core.ReadJSONObject(path)
	if err != nil {
		return false, err
	}
	if prov, _ := settings["defaultProvider"].(string); prov != providerID {
		return false, nil
	}
	delete(settings, "defaultProvider")
	delete(settings, "defaultModel")
	return true, core.WriteJSONObjectAtomic(path, settings)
}
