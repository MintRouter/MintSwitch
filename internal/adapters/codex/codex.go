// Package codex implements the core.ToolAdapter for OpenAI Codex (the Codex
// CLI and IDE extension), which share configuration under ~/.codex.
//
// Codex stores user configuration in ~/.codex/config.toml (TOML) and file-based
// credentials in ~/.codex/auth.json (JSON). To point the built-in "openai"
// provider at an OpenAI-compatible proxy/router, MintSwitch sets the top-level
// openai_base_url and model keys in config.toml (leaving model_provider at its
// default "openai") and writes the API key to auth.json as OPENAI_API_KEY.
// Apply also sets auth_mode="apikey" in auth.json: when a ChatGPT session also
// exists (auth_mode="chatgpt" with tokens present), Codex uses the OAuth token
// and ignores OPENAI_API_KEY, returning 401 against a proxy openai_base_url.
// See https://developers.openai.com/codex/config-advanced and
// https://developers.openai.com/codex/config-sample.
package codex

import (
	"errors"
	"fmt"
	"os/exec"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

// authKeyName is the JSON key Codex reads the API key from in auth.json.
const authKeyName = "OPENAI_API_KEY"

// authModeKey is the auth.json field selecting Codex's credential source, and
// authModeAPIKey is the value forcing it to use OPENAI_API_KEY (matching the
// AuthMode enum's lowercase serialization in OpenAI codex).
const (
	authModeKey    = "auth_mode"
	authModeAPIKey = "apikey"
)

// Adapter applies/restores a MintSwitch profile to the Codex configuration.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New constructs a Codex adapter using the injected resolver and backup engine.
func New(r *paths.Resolver, e *backup.Engine) *Adapter {
	return &Adapter{r: r, e: e, lookPath: exec.LookPath}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return "codex" }

// Name returns the display name.
func (a *Adapter) Name() string { return "Codex (CLI + IDE extension)" }

// configPath returns the absolute path to ~/.codex/config.toml.
func (a *Adapter) configPath() string { return a.r.Join(".codex", "config.toml") }

// authPath returns the absolute path to ~/.codex/auth.json.
func (a *Adapter) authPath() string { return a.r.Join(".codex", "auth.json") }

// ConfigPaths returns the config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.configPath(), a.authPath()}
}

// Detect reports whether Codex is installed, defined solely as the "codex" CLI
// binary being resolvable (via PATH or a curated set of common bin dirs). A
// leftover ~/.codex dir is not an installed signal, so an uninstall is
// reflected. The active path is always config.toml and is returned even when not
// installed, since Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	return a.r.BinaryResolvable(a.lookPath, "codex"), a.configPath()
}

// Status inspects config.toml relative to the given profile.
func (a *Adapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	installed, _ := a.Detect()
	if !installed {
		return core.StatusNotInstalled, core.StatusNotInstalled.Detail(), nil
	}
	cfg, err := readTOML(a.configPath())
	if err != nil {
		return core.StatusDefault, "", err
	}
	fp, ok := markerFingerprint(cfg)
	if !ok {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if fp == core.Fingerprint(p) {
		return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up both files (only when config.toml is not already
// MintSwitch-managed), then injects openai_base_url + model and the managed
// marker into config.toml and OPENAI_API_KEY plus auth_mode="apikey" into
// auth.json, preserving all other existing keys in each file.
//
// The backups are created only on the first Apply over a pristine/unmanaged (or
// absent) config, so the pristine pre-MintSwitch snapshots are what Restore
// reverts to even after repeated Applies. config.toml's marker is the single
// source of truth for "managed": auth.json carries no marker but is gated by it
// so both files snapshot the same pre-MintSwitch point in time. Backing up an
// already-managed config would snapshot a managed state (prior profile's key +
// marker) and hide the pristine original. Limitation: if config.toml is already
// managed but no backup exists (e.g. the backups dir was deleted, or a marker
// was left by an older version), no new backup is taken and Restore is a no-op —
// we cannot safely snapshot a managed file, and without the original we cannot
// distinguish our injected keys from the user's own to strip them.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	cfgPath, authPath := a.configPath(), a.authPath()

	cfg, err := readTOML(cfgPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	var cfgBackup string
	if _, managed := markerFingerprint(cfg); !managed {
		cfgBackup, err = a.e.Backup(cfgPath)
		if err != nil {
			return core.ApplyResult{}, err
		}
		if _, err := a.e.Backup(authPath); err != nil {
			return core.ApplyResult{}, err
		}
	}

	cfg["openai_base_url"] = p.BaseURL
	cfg["model"] = p.Model
	marker, err := markerMap(p)
	if err != nil {
		return core.ApplyResult{}, err
	}
	cfg[core.MarkerKey] = marker
	if err := writeTOML(cfgPath, cfg); err != nil {
		return core.ApplyResult{}, err
	}

	auth, err := readJSON(authPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	auth[authKeyName] = p.APIKey
	auth[authModeKey] = authModeAPIKey
	if err := writeJSON(authPath, auth); err != nil {
		return core.ApplyResult{}, err
	}

	return core.ApplyResult{
		ChangedPath: cfgPath,
		BackupPath:  cfgBackup,
		Message:     "Applied MintSwitch profile to Codex config.toml and auth.json.",
	}, nil
}

// Restore reverts both config.toml and auth.json from their latest backups.
// Both restores are attempted best-effort even when one fails, so an error on
// config.toml never silently skips auth.json (or vice versa); failures are
// joined into a single error naming each file. When config.toml is restored
// but auth.json has no backup, the message states that the MintSwitch API key
// may still be present in auth.json rather than reporting an unqualified
// success.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	cfgPath, authPath := a.configPath(), a.authPath()
	cfgRestored, cfgEntry, cfgErr := a.e.RestoreLatest(cfgPath)
	authRestored, _, authErr := a.e.RestoreLatest(authPath)
	if cfgErr != nil {
		cfgErr = fmt.Errorf("restore config.toml: %w", cfgErr)
	}
	if authErr != nil {
		authErr = fmt.Errorf("restore auth.json: %w", authErr)
	}
	if err := errors.Join(cfgErr, authErr); err != nil {
		return core.RestoreResult{}, err
	}
	var msg string
	switch {
	case cfgRestored && authRestored:
		msg = "Restored Codex config.toml and auth.json from backup."
	case cfgRestored:
		msg = "Restored Codex config.toml from backup; no backup found for auth.json, so the MintSwitch API key may still be present there."
	case authRestored:
		msg = "Restored Codex auth.json from backup; no backup found for config.toml."
	default:
		msg = "No backup found; nothing to restore."
	}
	return core.RestoreResult{
		ChangedPath: cfgPath,
		BackupPath:  cfgEntry,
		Message:     msg,
	}, nil
}

var _ core.ToolAdapter = (*Adapter)(nil)
