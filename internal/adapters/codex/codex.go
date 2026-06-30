// Package codex implements the core.ToolAdapter for OpenAI Codex (the Codex
// CLI and IDE extension), which share configuration under ~/.codex.
//
// Codex stores user configuration in ~/.codex/config.toml (TOML) and file-based
// credentials in ~/.codex/auth.json (JSON). To point the built-in "openai"
// provider at an OpenAI-compatible proxy/router, MintConfig sets the top-level
// openai_base_url and model keys in config.toml (leaving model_provider at its
// default "openai") and writes the API key to auth.json as OPENAI_API_KEY.
// See https://developers.openai.com/codex/config-advanced and
// https://developers.openai.com/codex/config-sample.
package codex

import (
	"os"

	"mintconfig/internal/backup"
	"mintconfig/internal/core"
	"mintconfig/internal/paths"
)

// authKeyName is the JSON key Codex reads the API key from in auth.json.
const authKeyName = "OPENAI_API_KEY"

// Adapter applies/restores a MintConfig profile to the Codex configuration.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
}

// New constructs a Codex adapter using the injected resolver and backup engine.
func New(r *paths.Resolver, e *backup.Engine) *Adapter {
	return &Adapter{r: r, e: e}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return "codex" }

// Name returns the display name.
func (a *Adapter) Name() string { return "Codex (CLI + IDE extension)" }

// configPath returns the absolute path to ~/.codex/config.toml.
func (a *Adapter) configPath() string { return a.r.Join(".codex", "config.toml") }

// authPath returns the absolute path to ~/.codex/auth.json.
func (a *Adapter) authPath() string { return a.r.Join(".codex", "auth.json") }

// dir returns the ~/.codex directory used for detection.
func (a *Adapter) dir() string { return a.r.Join(".codex") }

// ConfigPaths returns the config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.configPath(), a.authPath()}
}

// Detect reports whether ~/.codex exists. The active path is config.toml.
func (a *Adapter) Detect() (bool, string) {
	info, err := os.Stat(a.dir())
	if err != nil || !info.IsDir() {
		return false, ""
	}
	return true, a.configPath()
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
		return core.StatusAppliedByMintConfig, core.StatusAppliedByMintConfig.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up both files, then injects openai_base_url + model and the
// managed marker into config.toml and OPENAI_API_KEY into auth.json, preserving
// all other existing keys in each file.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	cfgPath, authPath := a.configPath(), a.authPath()
	cfgBackup, err := a.e.Backup(cfgPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if _, err := a.e.Backup(authPath); err != nil {
		return core.ApplyResult{}, err
	}

	cfg, err := readTOML(cfgPath)
	if err != nil {
		return core.ApplyResult{}, err
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
	if err := writeJSON(authPath, auth); err != nil {
		return core.ApplyResult{}, err
	}

	return core.ApplyResult{
		ChangedPath: cfgPath,
		BackupPath:  cfgBackup,
		Message:     "Applied MintConfig profile to Codex config.toml and auth.json.",
	}, nil
}

// Restore reverts both config.toml and auth.json from their latest backups.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	cfgPath, authPath := a.configPath(), a.authPath()
	_, cfgEntry, err := a.e.RestoreLatest(cfgPath)
	if err != nil {
		return core.RestoreResult{}, err
	}
	if _, _, err := a.e.RestoreLatest(authPath); err != nil {
		return core.RestoreResult{}, err
	}
	return core.RestoreResult{
		ChangedPath: cfgPath,
		BackupPath:  cfgEntry,
		Message:     "Restored Codex config.toml and auth.json from backup.",
	}, nil
}

var _ core.ToolAdapter = (*Adapter)(nil)
