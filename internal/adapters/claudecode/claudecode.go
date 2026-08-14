// Package claudecode implements the MintSwitch tool adapter for Claude Code.
//
// Claude Code (both the CLI and the VS Code extension, which share config) reads
// environment overrides from the top-level "env" object of ~/.claude/settings.json.
// The adapter injects the active profile's endpoint into that object as the
// ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_MODEL,
// ANTHROPIC_DEFAULT_OPUS_MODEL, ANTHROPIC_DEFAULT_SONNET_MODEL,
// ANTHROPIC_DEFAULT_HAIKU_MODEL, ANTHROPIC_SMALL_FAST_MODEL and
// CLAUDE_CODE_SUBAGENT_MODEL variables, preserving every other key in the file.
// In "All models" mode it additionally sets
// CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1 so Claude Code (>= v2.1.129)
// adds the gateway's claude-*/anthropic-* models to its /model picker.
//
// Every model variable is always written so no Claude Code request can fall
// back to an Anthropic default model, which fails on gateways that do not
// serve it. ANTHROPIC_MODEL only pins the main session; the opus/sonnet/haiku
// tier aliases (used by subagents, plan mode and background tasks) resolve via
// the ANTHROPIC_DEFAULT_*_MODEL variables, and a subagent declaring a full
// model ID bypasses even those unless CLAUDE_CODE_SUBAGENT_MODEL is set (that
// var forces every subagent onto the main model — the accepted trade-off for
// gateway-only setups). Tier values come from the profile's OpusModel /
// SonnetModel / HaikuModel when set and fall back to the main model
// otherwise; see resolveTiers.
//
// Values that diverge from the profile as stored (Claude Code specifics;
// other adapters keep the profile verbatim):
//
//   - ANTHROPIC_BASE_URL: Claude Code appends "/v1/messages" to the base URL
//     itself, so a single trailing "/v1" path segment is stripped before the
//     write ("https://host/v1" → "https://host", "https://host/api/v1" →
//     "https://host/api"). URLs without the suffix — including "/v1beta" or a
//     mid-path "v1" — are written unchanged.
//   - ANTHROPIC_SMALL_FAST_MODEL: deprecated in favor of
//     ANTHROPIC_DEFAULT_HAIKU_MODEL but still read by older Claude Code
//     versions, so it is written with the same resolved haiku value
//     (HaikuModel, else SmallFastModel, else the main model).
//
// Schema reference (verified 2026-07-17): https://code.claude.com/docs/en/env-vars
// and https://code.claude.com/docs/en/model-config.
package claudecode

import (
	"os/exec"
	"path/filepath"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

const (
	id   = "claude-code"
	name = "Claude Code (CLI & VS Code extension)"

	envKey            = "env"
	envBaseURL        = "ANTHROPIC_BASE_URL"
	envAuthToken      = "ANTHROPIC_AUTH_TOKEN"
	envModel          = "ANTHROPIC_MODEL"
	envSmallFastModel = "ANTHROPIC_SMALL_FAST_MODEL"
	envDefaultOpus    = "ANTHROPIC_DEFAULT_OPUS_MODEL"
	envDefaultSonnet  = "ANTHROPIC_DEFAULT_SONNET_MODEL"
	envDefaultHaiku   = "ANTHROPIC_DEFAULT_HAIKU_MODEL"
	envSubagentModel  = "CLAUDE_CODE_SUBAGENT_MODEL"
	// envModelDiscovery makes Claude Code (>= v2.1.129) query the gateway's
	// model list and add its claude-*/anthropic-* models to the /model picker.
	// Written as "1" in "All models" mode; removed in single-model mode.
	envModelDiscovery = "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"
)

// managedEnvKeys is every env variable Apply can write; stripManaged removes
// exactly this set. Order matters only for readability.
var managedEnvKeys = []string{
	envBaseURL, envAuthToken, envModel,
	envDefaultOpus, envDefaultSonnet, envDefaultHaiku,
	envSmallFastModel, envSubagentModel, envModelDiscovery,
}

// orphanDetail explains the orphan-remnant state: settings.json still carries
// the MintSwitch-injected env keys but the managed marker is gone (e.g. a
// previous restore was interrupted after clearing the marker).
const orphanDetail = "The MintSwitch env overrides are still present but the managed marker is missing " +
	"(a previous restore may have been interrupted). Restore Default will remove them."

// Adapter applies/restores a MintSwitch profile for Claude Code via its
// ~/.claude/settings.json file. The managed marker lives in the sidecar
// marker store, never in settings.json: Claude Code validates the file
// strictly and rejects unknown top-level keys. Construct it with [New].
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	m *markers.Store
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New returns an Adapter that resolves paths via r, backs up via e, and
// records its managed marker in m.
func New(r *paths.Resolver, e *backup.Engine, m *markers.Store) *Adapter {
	return &Adapter{r: r, e: e, m: m, lookPath: exec.LookPath}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return id }

// Name returns the display name.
func (a *Adapter) Name() string { return name }

// settingsPath returns the absolute path to settings.json under Claude Code's
// config dir ($CLAUDE_CONFIG_DIR, default ~/.claude).
func (a *Adapter) settingsPath() string { return filepath.Join(a.r.ClaudeDir(), "settings.json") }

// ConfigPaths returns the config files this adapter manages.
func (a *Adapter) ConfigPaths() []string { return []string{a.settingsPath()} }

// Detect reports whether Claude Code is installed: either the "claude" CLI
// binary is resolvable (via PATH or a curated set of common bin dirs) or the
// Claude Code editor extension is present (see extensionInstalled). A leftover
// ~/.claude dir/settings.json is not an installed signal, so an uninstall is
// reflected. The active path is always settings.json and is returned even when
// not installed, since Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	installed := a.r.BinaryResolvable(a.lookPath, "claude") || a.extensionInstalled()
	return installed, a.settingsPath()
}

// extensionInstalled reports whether the Claude Code editor extension is
// installed, by globbing for anthropic.claude-code-* under the VS Code and
// Cursor per-user extension dirs. Extension-only installs share
// ~/.claude/settings.json with the CLI, so the adapter covers them without a
// resolvable binary. Only filesystem globs — no subprocesses — so it is safe
// to call on every Detect/ListTools.
func (a *Adapter) extensionInstalled() bool {
	for _, editorDir := range []string{".vscode", ".cursor"} {
		pattern := a.r.Join(editorDir, "extensions", "anthropic.claude-code-*")
		if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}

// Status inspects settings.json relative to profile p. The marker is read from
// the sidecar store: no entry means Default — unless the file still carries
// the full MintSwitch injection signature (see orphanRemnantAt), which
// reports ModifiedExternally so the UI offers Restore even after the marker
// was lost (e.g. an interrupted restore). An entry whose managed env block
// (ANTHROPIC_BASE_URL) has been removed from the file also means Default (the
// file is back to an unmanaged state, e.g. after an external restore/wipe);
// otherwise the marker fingerprint decides Applied vs ModifiedExternally,
// exactly as with the legacy in-file marker.
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
		if a.orphanRemnantAt(path) {
			return core.StatusModifiedExternally, orphanDetail, nil
		}
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	m, err := core.ReadJSONObject(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	if _, present := core.AsJSONObject(m[envKey])[envBaseURL]; !present {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint == core.Fingerprint(p) {
		return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up settings.json (only when it is not already MintSwitch-managed),
// then idempotently injects the profile's endpoint into the top-level "env"
// object, preserving all other keys. The managed marker is recorded in the
// sidecar store — never in settings.json, which Claude Code validates strictly —
// and a leftover legacy in-file marker is stripped in the same write.
//
// "Already managed" (the backup gate) means a store entry OR a legacy in-file
// marker, so upgrading from a legacy-marker install never snapshots a managed
// file. The backup is created only on the first Apply over a pristine/unmanaged
// (or absent) file, so the pristine pre-MintSwitch snapshot is what Restore
// reverts to even after repeated Applies. Backing up an already-managed file
// would snapshot a managed state (prior profile's token) and hide the pristine
// original. If the file is already managed but no backup exists (e.g. the
// backups dir was deleted), no new backup is taken — we cannot safely snapshot
// a managed file; Restore then falls back to stripping the managed env keys.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	path := a.settingsPath()
	m, err := core.ReadJSONObject(path)
	if err != nil {
		return core.ApplyResult{}, err
	}
	_, inStore, err := a.m.Get(id)
	if err != nil {
		return core.ApplyResult{}, err
	}
	var backupPath string
	legacy, hasLegacy := core.ExtractLegacyMarker(m)
	if !inStore && !(hasLegacy && legacy.Managed) {
		backupPath, err = a.e.Backup(path)
		if err != nil {
			return core.ApplyResult{}, err
		}
	}

	opus, sonnet, haiku := resolveTiers(p)
	env := core.AsJSONObject(m[envKey])
	env[envBaseURL] = stripV1Suffix(p.BaseURL)
	env[envAuthToken] = p.APIKey
	env[envModel] = p.Model
	env[envDefaultOpus] = opus
	env[envDefaultSonnet] = sonnet
	env[envDefaultHaiku] = haiku
	env[envSmallFastModel] = haiku
	env[envSubagentModel] = p.Model
	// "All models" mode enables gateway model discovery so the /model picker
	// lists the gateway's claude-*/anthropic-* models; single-model mode
	// removes the key so a mode switch back is fully reverted on re-apply.
	if p.ApplyAllModels {
		env[envModelDiscovery] = "1"
	} else {
		delete(env, envModelDiscovery)
	}
	m[envKey] = env
	delete(m, core.MarkerKey)

	if err := core.WriteJSONObjectAtomic(path, m); err != nil {
		return core.ApplyResult{}, err
	}
	if err := a.m.Put(id, core.NewMarker(p, p.Label)); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch endpoint to Claude Code settings.json.",
	}, nil
}

// Restore reverts settings.json to its pristine pre-MintSwitch state via the
// backup engine (oldest snapshot; all entries are pruned after a successful
// restore) and removes the tool's entry from the sidecar marker store. When
// no backup exists but the file is still MintSwitch-managed (marker in store
// and env.ANTHROPIC_BASE_URL present, or — with the marker lost — the full
// injection signature still in the file, see orphanRemnantAt), it falls back
// to stripping the managed env keys, preserving every other setting. It is a
// safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	return core.RestoreSingleFile(core.SingleFileRestore{
		ToolID:          id,
		Path:            a.settingsPath(),
		Store:           a.m,
		RestorePristine: a.e.RestorePristine,
		OrphanRemnantAt: a.orphanRemnantAt,
		StripManaged:    a.stripManaged,
		RestoredMessage: "Restored Claude Code settings.json to its pre-apply state.",
		StrippedMessage: "No backup found; removed the MintSwitch-managed env keys from Claude Code settings.json.",
	})
}

// orphanRemnantAt reports whether the settings file at path still carries the
// MintSwitch injection signature without requiring a marker: the "env" object
// holding the four core managed keys (ANTHROPIC_BASE_URL,
// ANTHROPIC_AUTH_TOKEN, ANTHROPIC_MODEL, ANTHROPIC_SMALL_FAST_MODEL). This is
// a subset of what Apply writes today, kept at the legacy four keys so
// remnants written by older MintSwitch versions (pre tier variables) are
// still detected. It is deliberately stricter than
// stripManaged's gate because a user could set ANTHROPIC_BASE_URL (or a
// subset of these variables) for their own gateway: only the complete
// signature is treated as a MintSwitch remnant, so a hand-written config
// never shows Restore or gets stripped by mistake. A missing or corrupt file
// is never a remnant.
func (a *Adapter) orphanRemnantAt(path string) bool {
	m, err := core.ReadJSONObject(path)
	if err != nil {
		return false
	}
	env := core.AsJSONObject(m[envKey])
	for _, k := range []string{envBaseURL, envAuthToken, envModel, envSmallFastModel} {
		if _, ok := env[k]; !ok {
			return false
		}
	}
	return true
}

// stripManaged removes the MintSwitch-managed env keys from settings.json,
// preserving every other key (the "env" object itself is dropped when it
// becomes empty). It is the Restore fallback when no pristine backup exists:
// the user's own prior values are unrecoverable, but what Apply injected can
// be surgically dropped. Gated on the managed signal (env.ANTHROPIC_BASE_URL
// present) so an unmanaged file is never rewritten; it never creates the file.
func (a *Adapter) stripManaged(path string) (bool, error) {
	m, err := core.ReadJSONObject(path)
	if err != nil {
		return false, err
	}
	env := core.AsJSONObject(m[envKey])
	if _, present := env[envBaseURL]; !present {
		return false, nil
	}
	for _, k := range managedEnvKeys {
		delete(env, k)
	}
	if len(env) == 0 {
		delete(m, envKey)
	} else {
		m[envKey] = env
	}
	return true, core.WriteJSONObjectAtomic(path, m)
}

// StripLegacyMarker removes the legacy top-level marker key from settings.json,
// migrating its value into the sidecar store when the store has no entry for
// this tool yet. It is a no-op when the file is absent or carries no legacy
// marker; it never creates the file.
func (a *Adapter) StripLegacyMarker() error {
	path := a.settingsPath()
	m, err := core.ReadJSONObject(path)
	if err != nil {
		return err
	}
	legacy, ok := core.ExtractLegacyMarker(m)
	if !ok {
		if _, present := m[core.MarkerKey]; !present {
			return nil
		}
	}
	if ok && legacy.Managed {
		if _, inStore, err := a.m.Get(id); err == nil && !inStore {
			if err := a.m.Put(id, legacy); err != nil {
				return err
			}
		}
	}
	delete(m, core.MarkerKey)
	return core.WriteJSONObjectAtomic(path, m)
}

var (
	_ core.ToolAdapter          = (*Adapter)(nil)
	_ core.LegacyMarkerStripper = (*Adapter)(nil)
)

// resolveTiers returns the models to pin Claude Code's opus/sonnet/haiku tier
// aliases to: the profile's per-tier model when set, else the main model. The
// haiku tier additionally prefers the profile's SmallFastModel over the main
// model, preserving the pre-tier behaviour of ANTHROPIC_SMALL_FAST_MODEL.
func resolveTiers(p core.Profile) (opus, sonnet, haiku string) {
	opus, sonnet, haiku = p.Model, p.Model, p.Model
	if p.OpusModel != "" {
		opus = p.OpusModel
	}
	if p.SonnetModel != "" {
		sonnet = p.SonnetModel
	}
	if p.SmallFastModel != "" {
		haiku = p.SmallFastModel
	}
	if p.HaikuModel != "" {
		haiku = p.HaikuModel
	}
	return opus, sonnet, haiku
}

// stripV1Suffix removes exactly one trailing "/v1" path segment from baseURL
// (ignoring a trailing slash), since Claude Code appends "/v1/messages" to
// ANTHROPIC_BASE_URL itself. URLs without the suffix are returned unchanged;
// "/v1beta" or a "v1" segment mid-path is never stripped.
func stripV1Suffix(baseURL string) string {
	trimmed := strings.TrimSuffix(baseURL, "/")
	if strings.HasSuffix(trimmed, "/v1") {
		return strings.TrimSuffix(trimmed, "/v1")
	}
	return baseURL
}
