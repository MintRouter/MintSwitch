// Package claudedesktop implements the core.ToolAdapter for the Claude
// Desktop app's third-party gateway ("3P") deployment mode.
//
// Claude Desktop reads its deployment mode from claude_desktop_config.json
// under ~/Library/Application Support/Claude-3p/ and, in 3P mode, loads the
// gateway provider description from configLibrary/<uuid>.json referenced by
// configLibrary/_meta.json (appliedId + entries). The adapter manages exactly
// those three files:
//
//   - claude_desktop_config.json: deploymentMode is set to "3p"; every other
//     key (the app writes many of its own) is preserved.
//   - configLibrary/<uuid>.json: a "gateway" provider with static bearer
//     credentials, the profile's base URL (one trailing "/v1" path segment
//     stripped — Claude Desktop appends the API path itself) and API key, and
//     an inferenceModels list limited to "claude-*" models: Claude Desktop's
//     validator rejects non-Anthropic model names. Each entry carries a
//     labelOverride from the profile's ModelNames when one is set.
//   - configLibrary/_meta.json: appliedId points at the managed provider
//     entry, which is named "MintRouter.AI".
//
// The provider UUID is generated once and reused across Applies by reading
// the "MintRouter.AI" entry back from _meta.json, so Apply is idempotent and
// never litters configLibrary with stale provider files.
//
// The selected profile model leads inferenceModels when it is a claude-*
// model; otherwise the first claude-* model is the effective default and
// Apply's message says so. A profile without any claude-* model fails Apply
// before anything is written.
//
// File shapes verified against a working hand-written 3P configuration
// (2026-08). Claude Desktop is detected via its macOS app bundle at
// /Applications/Claude.app or ~/Applications/Claude.app; there is no npm
// install path for it.
package claudedesktop

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

const (
	id   = "claude-desktop"
	name = "Claude Desktop"

	// providerEntryName is the display name of the MintSwitch-managed provider
	// entry in _meta.json; it doubles as the signature that lets the adapter
	// find (and reuse) its own provider UUID.
	providerEntryName = "MintRouter.AI"

	// modelPrefix gates which profile models are written to inferenceModels:
	// Claude Desktop's 3P validator rejects non-Anthropic model names.
	modelPrefix = "claude-"

	deploymentModeKey = "deploymentMode"
	deploymentMode3P  = "3p"
)

// orphanDetail explains the orphan-remnant state: the 3P config files still
// carry the full MintSwitch signature but the managed marker is gone (e.g. a
// previous restore was interrupted after clearing the marker).
const orphanDetail = "The MintSwitch 3P gateway config is still present but the managed marker is missing " +
	"(a previous restore may have been interrupted). Restore Default will remove it."

// Adapter applies/restores a MintSwitch profile for the Claude Desktop app
// via its 3P-mode config files. The managed marker lives in the sidecar
// marker store, never in the tool's own files. Construct it with [New].
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	m *markers.Store
	// appDirs are the app-bundle paths probed by Detect; overridable in tests.
	appDirs []string
}

// New returns an Adapter that resolves paths via r, backs up via e, and
// records its managed marker in m.
func New(r *paths.Resolver, e *backup.Engine, m *markers.Store) *Adapter {
	return &Adapter{
		r: r, e: e, m: m,
		appDirs: []string{
			"/Applications/Claude.app",
			r.Join("Applications", "Claude.app"),
		},
	}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return id }

// Name returns the display name.
func (a *Adapter) Name() string { return name }

// baseDir returns Claude Desktop's 3P data directory
// (~/Library/Application Support/Claude-3p). It is derived from Home rather
// than the native config dir so tests pointing Home at a temp dir stay
// isolated; on macOS — the only OS the app's 3P mode targets — the two are
// the same directory.
func (a *Adapter) baseDir() string {
	return a.r.Join("Library", "Application Support", "Claude-3p")
}

// configPath returns the absolute path to claude_desktop_config.json.
func (a *Adapter) configPath() string {
	return filepath.Join(a.baseDir(), "claude_desktop_config.json")
}

// libraryDir returns the configLibrary directory holding the provider file
// and _meta.json.
func (a *Adapter) libraryDir() string { return filepath.Join(a.baseDir(), "configLibrary") }

// metaPath returns the absolute path to configLibrary/_meta.json.
func (a *Adapter) metaPath() string { return filepath.Join(a.libraryDir(), "_meta.json") }

// providerPath returns the absolute path to the provider file for the given
// UUID.
func (a *Adapter) providerPath(uuid string) string {
	return filepath.Join(a.libraryDir(), uuid+".json")
}

// ConfigPaths returns the config files this adapter manages. The provider
// file is included only when its UUID is discoverable from _meta.json.
func (a *Adapter) ConfigPaths() []string {
	out := []string{a.configPath(), a.metaPath()}
	if meta, err := core.ReadJSONObject(a.metaPath()); err == nil {
		if uuid := managedUUID(meta); uuid != "" {
			out = append(out, a.providerPath(uuid))
		}
	}
	return out
}

// Detect reports whether the Claude Desktop app is installed, defined as its
// app bundle existing at /Applications/Claude.app or ~/Applications/Claude.app.
// The active path is always claude_desktop_config.json and is returned even
// when not installed, since Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	for _, dir := range a.appDirs {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return true, a.configPath()
		}
	}
	return false, a.configPath()
}

// SupportsModel reports whether the given model can be written to Claude
// Desktop's inferenceModels: only "claude-*" model names pass its validator.
// The service uses this ([core.ModelFilter]) to limit the per-tool dropdown.
func (a *Adapter) SupportsModel(model string) bool {
	return strings.HasPrefix(model, modelPrefix)
}

// Status inspects the 3P config files relative to profile p. The marker is
// read from the sidecar store: no entry means Default — unless the files
// still carry the full MintSwitch signature (see orphanRemnant), which
// reports ModifiedExternally so the UI offers Restore even after the marker
// was lost. An entry whose managed signal (deploymentMode "3p" in
// claude_desktop_config.json) has been removed also means Default; otherwise
// the marker fingerprint decides Applied vs ModifiedExternally.
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
	cfg, err := core.ReadJSONObject(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	if mode, _ := cfg[deploymentModeKey].(string); mode != deploymentMode3P {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint == core.Fingerprint(p) {
		return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up the three managed files (only when not already
// MintSwitch-managed), then writes the gateway provider file, updates
// _meta.json and finally flips claude_desktop_config.json to 3P mode,
// preserving every other key in the shared files. The provider UUID is
// reused from an existing "MintRouter.AI" entry in _meta.json so repeated
// Applies are idempotent. A profile without any claude-* model fails before
// anything is written; a selected model that is not claude-* falls back to
// the first claude-* model and the returned message says so.
//
// Write order matters: the provider file and _meta.json land first, and
// claude_desktop_config.json — the file that flips the app into 3P mode —
// last, so a crash mid-Apply never leaves the app in 3P mode pointing at a
// missing provider config. Backups are taken only on the first Apply over an
// unmanaged state, so the pristine pre-MintSwitch snapshots are what Restore
// reverts to even after repeated Applies.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	models, fellBack := claudeModels(p)
	if len(models) == 0 {
		return core.ApplyResult{}, errors.New(
			"claudedesktop: the profile has no claude-* model; Claude Desktop's 3P mode only accepts claude-* model names")
	}

	cfgPath, metaPath := a.configPath(), a.metaPath()
	// Read every file up front so a corrupt one fails the Apply before
	// anything is written.
	cfg, err := core.ReadJSONObject(cfgPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	meta, err := core.ReadJSONObject(metaPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	uuid := managedUUID(meta)
	if uuid == "" {
		if uuid, err = newUUID(); err != nil {
			return core.ApplyResult{}, err
		}
	}
	provPath := a.providerPath(uuid)

	_, inStore, err := a.m.Get(id)
	if err != nil {
		return core.ApplyResult{}, err
	}
	var backupPath string
	if !inStore {
		if backupPath, err = a.e.Backup(cfgPath); err != nil {
			return core.ApplyResult{}, err
		}
		if _, err = a.e.Backup(metaPath); err != nil {
			return core.ApplyResult{}, err
		}
		if _, err = a.e.Backup(provPath); err != nil {
			return core.ApplyResult{}, err
		}
	}

	if err := core.WriteJSONObjectAtomic(provPath, providerObject(p, models)); err != nil {
		return core.ApplyResult{}, err
	}
	meta["appliedId"] = uuid
	meta["entries"] = upsertEntry(asArray(meta["entries"]), uuid)
	if err := core.WriteJSONObjectAtomic(metaPath, meta); err != nil {
		return core.ApplyResult{}, err
	}
	cfg[deploymentModeKey] = deploymentMode3P
	if err := core.WriteJSONObjectAtomic(cfgPath, cfg); err != nil {
		return core.ApplyResult{}, err
	}

	if err := a.m.Put(id, core.NewMarker(p, p.Label)); err != nil {
		return core.ApplyResult{}, err
	}
	msg := "Applied MintSwitch gateway to Claude Desktop (3P mode)."
	if fellBack {
		msg = fmt.Sprintf(
			"Applied MintSwitch gateway to Claude Desktop (3P mode). The selected model %q is not a claude-* model, which Claude Desktop requires, so %q leads the model list instead.",
			p.Model, models[0])
	}
	return core.ApplyResult{
		ChangedPath: cfgPath,
		BackupPath:  backupPath,
		Message:     msg,
	}, nil
}

// Restore reverts the three managed files to their pristine pre-MintSwitch
// state via the backup engine (oldest snapshots; entries pruned after a
// successful restore) and removes the tool's entry from the sidecar marker
// store. All restores are attempted best-effort even when one fails. When a
// file has no backup but the config is still MintSwitch-managed (marker in
// store, or — with the marker lost — the full signature still present, see
// orphanRemnant), Restore falls back to stripping the managed pieces:
// deploymentMode from claude_desktop_config.json, the MintRouter.AI entry
// (and appliedId) from _meta.json, and the provider file itself. Directories
// left empty are removed, so a Claude-3p tree created entirely by MintSwitch
// disappears; a real app-data directory is never touched (os.Remove refuses
// non-empty dirs). It is a safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	cfgPath, metaPath := a.configPath(), a.metaPath()
	// Resolve the provider UUID before any file is reverted or stripped.
	uuid := ""
	if meta, err := core.ReadJSONObject(metaPath); err == nil {
		uuid = managedUUID(meta)
	}
	_, inStore, err := a.m.Get(id)
	if err != nil {
		return core.RestoreResult{}, err
	}
	orphan := !inStore && a.orphanRemnant()

	cfgRestored, cfgEntry, cfgErr := a.e.RestorePristine(cfgPath)
	metaRestored, _, metaErr := a.e.RestorePristine(metaPath)
	var provRestored bool
	var provErr error
	if uuid != "" {
		provRestored, _, provErr = a.e.RestorePristine(a.providerPath(uuid))
	}
	if cfgErr != nil {
		cfgErr = fmt.Errorf("restore claude_desktop_config.json: %w", cfgErr)
	}
	if metaErr != nil {
		metaErr = fmt.Errorf("restore _meta.json: %w", metaErr)
	}
	if provErr != nil {
		provErr = fmt.Errorf("restore provider config: %w", provErr)
	}
	if err := errors.Join(cfgErr, metaErr, provErr); err != nil {
		return core.RestoreResult{}, err
	}

	stripped := false
	if inStore || orphan {
		if !cfgRestored {
			ok, err := a.stripConfig(cfgPath)
			if err != nil {
				return core.RestoreResult{}, err
			}
			stripped = stripped || ok
		}
		if !metaRestored {
			ok, err := a.stripMeta(metaPath, uuid)
			if err != nil {
				return core.RestoreResult{}, err
			}
			stripped = stripped || ok
		}
		if !provRestored && uuid != "" {
			if err := os.Remove(a.providerPath(uuid)); err == nil {
				stripped = true
			} else if !errors.Is(err, fs.ErrNotExist) {
				return core.RestoreResult{}, err
			}
		}
	}
	if err := a.m.Delete(id); err != nil {
		return core.RestoreResult{}, err
	}
	// Best-effort cleanup: os.Remove only deletes empty directories, so a
	// Claude-3p tree that MintSwitch created from scratch vanishes while a
	// real app-data directory (full of the app's own files) stays put.
	_ = os.Remove(a.libraryDir())
	_ = os.Remove(a.baseDir())

	msg := "Nothing to restore for Claude Desktop."
	switch {
	case cfgRestored || metaRestored || provRestored:
		msg = "Restored the Claude Desktop 3P config files to their pre-apply state."
	case stripped:
		msg = "No backup found; removed the MintSwitch-managed 3P gateway config from Claude Desktop."
	}
	return core.RestoreResult{
		ChangedPath: cfgPath,
		BackupPath:  cfgEntry,
		Message:     msg,
	}, nil
}

// orphanRemnant reports whether the config files still carry the FULL
// MintSwitch signature without requiring a marker: claude_desktop_config.json
// in 3P mode AND _meta.json holding a "MintRouter.AI" entry AND that entry's
// provider file describing a gateway provider. The provider entry name is
// MintSwitch's own, so a genuinely hand-written third-party config (under a
// different name) never shows Restore or gets stripped by mistake. A missing
// or corrupt file is never a remnant.
func (a *Adapter) orphanRemnant() bool {
	cfg, err := core.ReadJSONObject(a.configPath())
	if err != nil {
		return false
	}
	if mode, _ := cfg[deploymentModeKey].(string); mode != deploymentMode3P {
		return false
	}
	meta, err := core.ReadJSONObject(a.metaPath())
	if err != nil {
		return false
	}
	uuid := managedUUID(meta)
	if uuid == "" {
		return false
	}
	prov, err := core.ReadJSONObject(a.providerPath(uuid))
	if err != nil {
		return false
	}
	kind, _ := prov["inferenceProvider"].(string)
	return kind == "gateway"
}

// stripConfig removes deploymentMode from claude_desktop_config.json,
// dropping the app back to its default (1P) mode while preserving every
// other key. Gated on the managed signal (deploymentMode "3p") so an
// unmanaged file is never rewritten; it never creates the file.
func (a *Adapter) stripConfig(path string) (bool, error) {
	cfg, err := core.ReadJSONObject(path)
	if err != nil {
		return false, err
	}
	if mode, _ := cfg[deploymentModeKey].(string); mode != deploymentMode3P {
		return false, nil
	}
	delete(cfg, deploymentModeKey)
	return true, core.WriteJSONObjectAtomic(path, cfg)
}

// stripMeta removes the MintSwitch provider entry (matched by uuid or by the
// "MintRouter.AI" name) and its appliedId reference from _meta.json,
// preserving any other entries. The file is deleted outright when nothing
// but the managed pieces remained. It never creates the file.
func (a *Adapter) stripMeta(path, uuid string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	meta, err := core.ReadJSONObject(path)
	if err != nil {
		return false, err
	}
	entries := asArray(meta["entries"])
	kept := make([]any, 0, len(entries))
	for _, e := range entries {
		obj := core.AsJSONObject(e)
		entryID, _ := obj["id"].(string)
		entryName, _ := obj["name"].(string)
		if entryName == providerEntryName || (uuid != "" && entryID == uuid) {
			continue
		}
		kept = append(kept, e)
	}
	changed := len(kept) != len(entries)
	if applied, _ := meta["appliedId"].(string); applied != "" && (applied == uuid || len(kept) == 0) {
		delete(meta, "appliedId")
		changed = true
	}
	if !changed {
		return false, nil
	}
	if len(kept) == 0 && len(meta) <= 1 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return true, nil
	}
	meta["entries"] = kept
	return true, core.WriteJSONObjectAtomic(path, meta)
}

var _ core.ToolAdapter = (*Adapter)(nil)

// claudeModels returns the profile's claude-* models in inferenceModels
// order: the selected model leads when it is itself a claude-* model,
// followed by the remaining claude-* members of Models in their saved order.
// When the selected model is not claude-*, the first claude-* model becomes
// the effective default and fellBack is true. An empty result means the
// profile has no claude-* model at all.
func claudeModels(p core.Profile) (models []string, fellBack bool) {
	seen := map[string]bool{}
	add := func(m string) {
		if strings.HasPrefix(m, modelPrefix) && !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}
	add(p.Model)
	for _, m := range p.Models {
		add(m)
	}
	return models, !strings.HasPrefix(p.Model, modelPrefix)
}

// providerObject builds the configLibrary/<uuid>.json contents for the given
// profile and (already filtered and ordered) model list. labelOverride is set
// from the profile's ModelNames when a display name exists for the model.
func providerObject(p core.Profile, models []string) map[string]any {
	entries := make([]any, 0, len(models))
	for _, m := range models {
		e := map[string]any{"name": m}
		if label := p.ModelNames[m]; label != "" {
			e["labelOverride"] = label
		}
		entries = append(entries, e)
	}
	return map[string]any{
		"inferenceProvider":          "gateway",
		"inferenceCredentialKind":    "static",
		"inferenceGatewayAuthScheme": "bearer",
		"inferenceGatewayBaseUrl":    stripV1Suffix(p.BaseURL),
		"inferenceGatewayApiKey":     p.APIKey,
		"inferenceModels":            entries,
	}
}

// managedUUID returns the id of the "MintRouter.AI" entry in _meta.json, or
// "" when no such entry exists. Reusing it keeps the provider UUID stable
// across Applies.
func managedUUID(meta map[string]any) string {
	for _, e := range asArray(meta["entries"]) {
		obj := core.AsJSONObject(e)
		if entryName, _ := obj["name"].(string); entryName == providerEntryName {
			if entryID, _ := obj["id"].(string); entryID != "" {
				return entryID
			}
		}
	}
	return ""
}

// upsertEntry returns entries with the MintSwitch provider entry set to
// {id: uuid, name: "MintRouter.AI"}, replacing any previous MintSwitch entry
// and preserving all others.
func upsertEntry(entries []any, uuid string) []any {
	kept := make([]any, 0, len(entries)+1)
	for _, e := range entries {
		obj := core.AsJSONObject(e)
		entryID, _ := obj["id"].(string)
		entryName, _ := obj["name"].(string)
		if entryName == providerEntryName || entryID == uuid {
			continue
		}
		kept = append(kept, e)
	}
	return append(kept, map[string]any{"id": uuid, "name": providerEntryName})
}

// asArray returns v as a JSON array, or nil when v is not one.
func asArray(v any) []any {
	arr, _ := v.([]any)
	return arr
}

// newUUID returns a random RFC 4122 version-4 UUID string.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// stripV1Suffix removes exactly one trailing "/v1" path segment from baseURL
// (ignoring a trailing slash): Claude Desktop appends the API path to
// inferenceGatewayBaseUrl itself. URLs without the suffix are returned
// unchanged; "/v1beta" or a "v1" segment mid-path is never stripped.
func stripV1Suffix(baseURL string) string {
	trimmed := strings.TrimSuffix(baseURL, "/")
	if strings.HasSuffix(trimmed, "/v1") {
		return strings.TrimSuffix(trimmed, "/v1")
	}
	return baseURL
}
