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
	"mintswitch/internal/markers"
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

// orphanDetail explains the orphan-remnant state: the settings still carry the
// MintSwitch customModels entry but the managed marker is gone (e.g. a
// previous restore was interrupted after clearing the marker).
const orphanDetail = "The MintSwitch custom model is still present but the managed marker is missing " +
	"(a previous restore may have been interrupted). Restore Default will remove it."

// Ensure Adapter satisfies the shared tool adapter contracts.
var (
	_ core.ToolAdapter          = (*Adapter)(nil)
	_ core.LegacyMarkerStripper = (*Adapter)(nil)
)

// Adapter manages Factory Droid's configuration on behalf of MintSwitch. The
// managed marker lives in the sidecar marker store, never in settings.json.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	m *markers.Store
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New constructs a Factory Droid adapter. All filesystem locations derive from
// the injected resolver, backup engine, and sidecar marker store so tests can
// point HOME at a temp dir.
func New(r *paths.Resolver, e *backup.Engine, m *markers.Store) *Adapter {
	return &Adapter{r: r, e: e, m: m, lookPath: exec.LookPath}
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

// Status inspects the current config relative to the given profile. The
// marker is read from the sidecar store: no entry means Default — unless the
// file still carries the MintSwitch customModels entry (see orphanRemnantAt),
// which reports ModifiedExternally so the UI offers Restore even after the
// marker was lost (e.g. an interrupted restore). An entry whose managed
// customModels entry has been removed from the file also means Default (the
// file is back to an unmanaged state, e.g. after an external restore/wipe);
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
	root, err := core.ReadJSONObject(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	if !hasManagedEntry(root) {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint == core.Fingerprint(p) {
		return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up the existing config (only when it is not already
// MintSwitch-managed), then idempotently upserts the MintSwitch customModels
// entry and sets the top-level "model" to the selected model, preserving all
// other keys (including the user's own customModels entries). The write is
// atomic at 0600. The managed marker is recorded in the sidecar store — never
// in settings.json — and a leftover legacy in-file marker is stripped in the
// same write.
//
// "Already managed" (the backup gate) means a store entry OR a legacy in-file
// marker, so upgrading from a legacy-marker install never snapshots a managed
// file. The backup is created only on the first Apply over a
// pristine/unmanaged (or absent) file, so the pristine pre-MintSwitch snapshot
// is what Restore reverts to even after repeated Applies.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	path := a.configPath()
	root, err := core.ReadJSONObject(path)
	if err != nil {
		return core.ApplyResult{}, err
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

	upsertCustomModel(root, customModelEntry(p))
	root["model"] = p.Model
	delete(root, core.MarkerKey)

	if err := core.WriteJSONObjectAtomic(path, root); err != nil {
		return core.ApplyResult{}, err
	}
	if err := a.m.Put(a.ID(), core.NewMarker(p, p.Label)); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch custom model to Factory Droid settings.",
	}, nil
}

// Restore reverts the config to its pristine pre-MintSwitch state via the
// backup engine (oldest snapshot; all entries are pruned after a successful
// restore) and removes the tool's entry from the sidecar marker store. When
// no backup exists but the file is still MintSwitch-managed (marker in store
// and the MintSwitch customModels entry present, or — with the marker lost —
// the entry still in the file, see orphanRemnantAt), it falls back to
// stripping the managed entry, preserving every other setting. It is a safe
// no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	return core.RestoreSingleFile(core.SingleFileRestore{
		ToolID:          a.ID(),
		Path:            a.configPath(),
		Store:           a.m,
		RestorePristine: a.e.RestorePristine,
		OrphanRemnantAt: a.orphanRemnantAt,
		StripManaged:    a.stripManaged,
		RestoredMessage: "Restored Factory Droid settings to their pre-apply state.",
		StrippedMessage: "No backup found; removed the MintSwitch custom model from Factory Droid settings.",
	})
}

// orphanRemnantAt reports whether the settings file at path still carries the
// MintSwitch-owned customModels entry without requiring a marker. The entry
// is identified by its reserved displayName ("MintSwitch (MintRouter)"),
// which is MintSwitch-specific — no tool or user writes it independently — so
// its presence alone is a reliable remnant signal. A missing or corrupt file
// is never a remnant.
func (a *Adapter) orphanRemnantAt(path string) bool {
	root, err := core.ReadJSONObject(path)
	if err != nil {
		return false
	}
	return hasManagedEntry(root)
}

// stripManaged removes the MintSwitch-owned customModels entry from
// settings.json, preserving every other setting. It is the Restore fallback
// when no pristine backup exists. Gated on the managed entry being present so
// an unmanaged file is never rewritten; it never creates the file.
func (a *Adapter) stripManaged(path string) (bool, error) {
	root, err := core.ReadJSONObject(path)
	if err != nil {
		return false, err
	}
	if !removeManagedEntry(root) {
		return false, nil
	}
	return true, core.WriteJSONObjectAtomic(path, root)
}

// StripLegacyMarker removes the legacy top-level marker key from
// settings.json, migrating its value into the sidecar store when the store
// has no entry for this tool yet. It is a no-op when the file is absent or
// carries no legacy marker; it never creates the file.
func (a *Adapter) StripLegacyMarker() error {
	path := a.configPath()
	root, err := core.ReadJSONObject(path)
	if err != nil {
		return err
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
	return core.WriteJSONObjectAtomic(path, root)
}
