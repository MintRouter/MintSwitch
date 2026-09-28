package omp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"mintswitch/internal/core"
)

// modelEntry is one models[] item of an omp provider block. contextWindow /
// maxTokens are omp's optional per-model limits (defaults 128000 / 16384
// when omitted, as in Pi); they come from what the endpoint advertised so
// omp's auto-compaction does not fire far too early on 200k–1M models.
type modelEntry struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	ContextWindow int    `yaml:"contextWindow,omitempty"`
	MaxTokens     int    `yaml:"maxTokens,omitempty"`
}

// providerBlock is the providers.<id> entry MintSwitch writes to models.yml.
type providerBlock struct {
	BaseURL string       `yaml:"baseUrl"`
	API     string       `yaml:"api"`
	APIKey  string       `yaml:"apiKey"`
	Models  []modelEntry `yaml:"models"`
}

// providerNode builds the providers.mintrouter block for p. One entry per
// applied model ([core.Profile.ApplyModels]: just the selected model, or every
// provider model in "All models" mode). "id" stays the canonical model ID;
// "name" is display-only, so it takes the profile's ModelNames display name
// when one exists. A pinned SmallFastModel is folded into the catalog too:
// omp cannot resolve a modelRoles.smol selector naming a model missing from
// the provider's models list.
func providerNode(p core.Profile) (*yaml.Node, error) {
	applyModels := slices.Clone(p.ApplyModels())
	if p.SmallFastModel != "" && !slices.Contains(applyModels, p.SmallFastModel) {
		applyModels = append(applyModels, p.SmallFastModel)
	}
	entries := make([]modelEntry, 0, len(applyModels))
	for _, m := range applyModels {
		name := m
		if label := p.ModelNames[m]; label != "" {
			name = label
		}
		entry := modelEntry{ID: m, Name: name}
		if w := p.ContextWindow(m); w > 0 {
			entry.ContextWindow = w
		}
		if n := p.MaxOutputTokens(m); n > 0 {
			entry.MaxTokens = n
		}
		entries = append(entries, entry)
	}
	return encodeNode(providerBlock{BaseURL: p.BaseURL, API: apiType, APIKey: p.APIKey, Models: entries})
}

// Apply backs up both files (only when omp is not already MintSwitch-managed),
// then upserts the MintSwitch provider under "providers" in models.yml and
// sets modelRoles.default (and modelRoles.smol when the profile pins a
// small/fast model) in config.yml, preserving every other key, comment and
// ordering in each file. The managed marker is recorded in the sidecar store
// — never in omp's own files.
//
// "Already managed" (the backup gate) means a store entry, so the backups are
// created only on the first Apply over a pristine/unmanaged (or absent)
// config: the pristine pre-MintSwitch snapshots are what Restore reverts to
// even after repeated Applies. config.yml carries no provider block but is
// gated by the same check so both files snapshot the same pre-MintSwitch
// point in time. If omp is already managed but no backup exists (e.g. the
// backups dir was deleted), no new backup is taken — we cannot safely
// snapshot a managed file; Restore then falls back to stripping the managed
// keys.
//
// Write order matters: models.yml is written first, config.yml second (with a
// best-effort rollback of models.yml when the config.yml write fails). If the
// process dies between the two writes, config.yml — the file that switches
// omp's default onto the provider — is still pristine, so omp never selects a
// provider that does not exist in models.yml. The leftover provider entry
// never routes traffic anywhere on its own, and the next Apply or Restore
// overwrites or strips it.
//
// modelRoles.smol is written when the profile pins a SmallFastModel and
// removed when it does not — but only when the current value carries the
// mintrouter/ prefix (i.e. MintSwitch wrote it); a user-set smol role on
// another provider is never touched.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	modelsPath, configPath := a.modelsPath(), a.configPath()

	// Read both files up front so a corrupt or unmergeable file fails the
	// Apply before anything is written.
	models, err := readWithLegacy(modelsPath, a.legacyModelsPath())
	if err != nil {
		return core.ApplyResult{}, err
	}
	cfg, err := readWithLegacy(configPath, a.legacySettingsPath())
	if err != nil {
		return core.ApplyResult{}, err
	}
	providers, err := ensureMapping(models.root, "providers", modelsPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	roles, err := ensureMapping(cfg.root, "modelRoles", configPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	block, err := providerNode(p)
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
		if _, err := a.e.Backup(configPath); err != nil {
			return core.ApplyResult{}, err
		}
	}

	mapSet(providers, providerID, block)
	if err := models.write(modelsPath); err != nil {
		return core.ApplyResult{}, err
	}
	// rollbackModels best-effort reverts models.yml to its pre-Apply bytes
	// when the config.yml half of the two-file write fails, so a failed Apply
	// never leaves the MintSwitch provider (and API key) behind in models.yml
	// while config.yml was left untouched.
	rollbackModels := func() {
		if modelsExisted {
			_ = core.WriteFileAtomic(modelsPath, origModels, 0o600)
		} else {
			_ = os.Remove(modelsPath)
		}
	}

	mapSet(roles, roleDefault, strNode(selectorPrefix+p.Model))
	if p.SmallFastModel != "" {
		mapSet(roles, roleSmol, strNode(selectorPrefix+p.SmallFastModel))
	} else if strings.HasPrefix(scalarString(mapGet(roles, roleSmol)), selectorPrefix) {
		mapDelete(roles, roleSmol)
	}
	if err := a.writeConfig(configPath, cfg); err != nil {
		rollbackModels()
		return core.ApplyResult{}, err
	}

	if err := a.m.Put(id, core.NewMarker(p, p.Label)); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: modelsPath,
		BackupPath:  modelsBackup,
		Message: fmt.Sprintf("Applied MintSwitch provider to oh-my-pi %s and %s.",
			filepath.Base(modelsPath), filepath.Base(configPath)),
	}, nil
}

// Restore reverts models.yml and config.yml to their pristine pre-MintSwitch
// state via the backup engine (oldest snapshots; all entries are pruned after
// a successful restore). Both restores are attempted best-effort even when
// one fails, so an error on models.yml never silently skips config.yml (or
// vice versa); failures are joined into a single error naming each file.
// When a file has no backup but omp is still MintSwitch-managed (marker in
// store, or — with the marker lost — the provider block still in models.yml,
// see orphanRemnant), Restore falls back to stripping the managed keys from
// it — providers.mintrouter in models.yml, the mintrouter/ default and smol
// roles in config.yml — preserving every other key. It is a safe no-op when
// nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	modelsPath, configPath := a.modelsPath(), a.configPath()
	modelsName, configName := filepath.Base(modelsPath), filepath.Base(configPath)
	_, inStore, err := a.m.Get(id)
	if err != nil {
		return core.RestoreResult{}, err
	}
	orphan := !inStore && a.orphanRemnant()
	modelsRestored, modelsEntry, modelsErr := a.e.RestorePristine(modelsPath)
	configRestored, _, configErr := a.e.RestorePristine(configPath)
	if modelsErr != nil {
		modelsErr = fmt.Errorf("restore %s: %w", modelsName, modelsErr)
	}
	if configErr != nil {
		configErr = fmt.Errorf("restore %s: %w", configName, configErr)
	}
	if err := errors.Join(modelsErr, configErr); err != nil {
		return core.RestoreResult{}, err
	}
	var modelsStripped, configStripped bool
	if !modelsRestored && (inStore || orphan) {
		modelsStripped, err = stripManagedModels(modelsPath)
		if err != nil {
			return core.RestoreResult{}, err
		}
	}
	if !configRestored && (inStore || orphan) {
		configStripped, err = stripManagedConfig(configPath)
		if err != nil {
			return core.RestoreResult{}, err
		}
	}
	if err := a.m.Delete(id); err != nil {
		return core.RestoreResult{}, err
	}
	var msg string
	switch {
	case modelsRestored && configRestored:
		msg = fmt.Sprintf("Restored oh-my-pi %s and %s from backup.", modelsName, configName)
	case modelsRestored && configStripped:
		msg = fmt.Sprintf("Restored oh-my-pi %s from backup; no backup found for %s, so the MintSwitch model roles were removed from it.", modelsName, configName)
	case modelsRestored:
		msg = fmt.Sprintf("Restored oh-my-pi %s from backup; no backup found for %s.", modelsName, configName)
	case configRestored && modelsStripped:
		msg = fmt.Sprintf("Restored oh-my-pi %s from backup; no backup found for %s, so the MintSwitch provider was removed from it.", configName, modelsName)
	case configRestored:
		msg = fmt.Sprintf("Restored oh-my-pi %s from backup; no backup found for %s.", configName, modelsName)
	case modelsStripped || configStripped:
		msg = "No backup found; removed the MintSwitch-managed keys from the oh-my-pi config files."
	default:
		msg = "No backup found; nothing to restore."
	}
	return core.RestoreResult{
		ChangedPath: modelsPath,
		BackupPath:  modelsEntry,
		Message:     msg,
	}, nil
}

// orphanRemnant reports whether models.yml still carries the MintSwitch
// provider block (providers.mintrouter) without requiring a marker. The
// "mintrouter" provider key is MintSwitch-specific — no tool or user writes
// it independently — so its presence alone is a reliable remnant signal. A
// missing or corrupt file is never a remnant, so this probe can never make
// Status error or Restore touch a file it could not safely rewrite.
func (a *Adapter) orphanRemnant() bool {
	d, err := readDocument(a.modelsPath())
	return err == nil && managedProvider(d) != nil
}

// stripManagedModels removes the MintSwitch provider block
// (providers.mintrouter) from models.yml, dropping the "providers" mapping
// when it becomes empty. It is the Restore fallback when no pristine backup
// exists. Gated on the managed signal (providers.mintrouter present) so an
// unmanaged file is never rewritten; it never creates the file.
func stripManagedModels(path string) (bool, error) {
	d, err := readDocument(path)
	if err != nil {
		return false, err
	}
	providers := mapGet(d.root, "providers")
	if !mapDelete(providers, providerID) {
		return false, nil
	}
	if len(providers.Content) == 0 {
		mapDelete(d.root, "providers")
	}
	return true, d.write(path)
}

// stripManagedConfig removes the modelRoles.default and modelRoles.smol
// entries that point at the MintSwitch provider from config.yml, preserving
// every other key (a role the user repointed at another provider is left
// alone) and dropping the "modelRoles" mapping when it becomes empty. It
// never creates the file and never rewrites one carrying no managed role.
func stripManagedConfig(path string) (bool, error) {
	d, err := readDocument(path)
	if err != nil {
		return false, err
	}
	roles := mapGet(d.root, "modelRoles")
	stripped := false
	for _, role := range []string{roleDefault, roleSmol} {
		if strings.HasPrefix(scalarString(mapGet(roles, role)), selectorPrefix) {
			mapDelete(roles, role)
			stripped = true
		}
	}
	if !stripped {
		return false, nil
	}
	if len(roles.Content) == 0 {
		mapDelete(d.root, "modelRoles")
	}
	return true, d.write(path)
}
