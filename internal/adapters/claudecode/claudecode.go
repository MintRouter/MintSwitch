// Package claudecode implements the MintSwitch tool adapter for Claude Code.
//
// Claude Code (both the CLI and the VS Code extension, which share config) reads
// environment overrides from the top-level "env" object of ~/.claude/settings.json.
// The adapter injects the active profile's endpoint into that object as the
// ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_MODEL and (optionally)
// ANTHROPIC_SMALL_FAST_MODEL variables, preserving every other key in the file.
//
// Schema reference (verified 2026-06-30): https://code.claude.com/docs/en/env-vars
// and https://code.claude.com/docs/en/llm-gateway-connect.
package claudecode

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

const (
	id   = "claude-code"
	name = "Claude Code (CLI + IDE)"

	envKey            = "env"
	envBaseURL        = "ANTHROPIC_BASE_URL"
	envAuthToken      = "ANTHROPIC_AUTH_TOKEN"
	envModel          = "ANTHROPIC_MODEL"
	envSmallFastModel = "ANTHROPIC_SMALL_FAST_MODEL"
)

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

// Detect reports whether Claude Code is installed, defined solely as the
// "claude" CLI binary being resolvable (via PATH or a curated set of common
// bin dirs). A leftover ~/.claude dir/settings.json is not an installed signal,
// so an uninstall is reflected. The active path is always settings.json and is
// returned even when not installed, since Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	return a.r.BinaryResolvable(a.lookPath, "claude"), a.settingsPath()
}

// Status inspects settings.json relative to profile p. The marker is read from
// the sidecar store: no entry means Default; an entry whose managed env block
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
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	m, err := readJSON(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	if _, present := asObject(m[envKey])[envBaseURL]; !present {
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
// original. Limitation: if the file is already managed but no backup exists
// (e.g. the backups dir was deleted), no new backup is taken and Restore is a
// no-op — we cannot safely snapshot a managed file, and without the original we
// cannot distinguish our injected keys from the user's own to strip them.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	path := a.settingsPath()
	m, err := readJSON(path)
	if err != nil {
		return core.ApplyResult{}, err
	}
	_, inStore, err := a.m.Get(id)
	if err != nil {
		return core.ApplyResult{}, err
	}
	var backupPath string
	legacy, hasLegacy := extractLegacyMarker(m)
	if !inStore && !(hasLegacy && legacy.Managed) {
		backupPath, err = a.e.Backup(path)
		if err != nil {
			return core.ApplyResult{}, err
		}
	}

	env := asObject(m[envKey])
	env[envBaseURL] = p.BaseURL
	env[envAuthToken] = p.APIKey
	env[envModel] = p.Model
	if p.SmallFastModel != "" {
		env[envSmallFastModel] = p.SmallFastModel
	} else {
		delete(env, envSmallFastModel)
	}
	m[envKey] = env
	delete(m, core.MarkerKey)

	if err := writeJSON(path, m); err != nil {
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

// Restore reverts settings.json to its pre-apply state via the backup engine
// and removes the tool's entry from the sidecar marker store. It is a safe
// no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	path := a.settingsPath()
	restored, entry, err := a.e.RestoreLatest(path)
	if err != nil {
		return core.RestoreResult{}, err
	}
	if err := a.m.Delete(id); err != nil {
		return core.RestoreResult{}, err
	}
	msg := "No backup found; nothing to restore."
	if restored {
		msg = "Restored Claude Code settings.json to its pre-apply state."
	}
	return core.RestoreResult{ChangedPath: path, BackupPath: entry, Message: msg}, nil
}

// StripLegacyMarker removes the legacy top-level marker key from settings.json,
// migrating its value into the sidecar store when the store has no entry for
// this tool yet. It is a no-op when the file is absent or carries no legacy
// marker; it never creates the file.
func (a *Adapter) StripLegacyMarker() error {
	path := a.settingsPath()
	m, err := readJSON(path)
	if err != nil {
		return err
	}
	legacy, ok := extractLegacyMarker(m)
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
	return writeJSON(path, m)
}

var (
	_ core.ToolAdapter          = (*Adapter)(nil)
	_ core.LegacyMarkerStripper = (*Adapter)(nil)
)

// readJSON reads path as a JSON object. A missing file yields an empty object.
func readJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// writeJSON writes m as indented JSON to path atomically, creating parent
// dirs. The file is written with 0600 perms since it contains the profile's
// auth token.
func writeJSON(path string, m map[string]any) error {
	return core.WriteJSONObjectAtomic(path, m)
}

// asObject returns v as a JSON object, or a fresh object if v is not one.
func asObject(v any) map[string]any {
	if obj, ok := v.(map[string]any); ok {
		return obj
	}
	return map[string]any{}
}

// extractLegacyMarker pulls a legacy in-file MintSwitch marker out of a parsed
// settings object. It reports false when the key is absent or its value does
// not decode as a [core.Marker].
func extractLegacyMarker(m map[string]any) (core.Marker, bool) {
	raw, ok := m[core.MarkerKey]
	if !ok {
		return core.Marker{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return core.Marker{}, false
	}
	var marker core.Marker
	if err := json.Unmarshal(b, &marker); err != nil {
		return core.Marker{}, false
	}
	return marker, true
}
