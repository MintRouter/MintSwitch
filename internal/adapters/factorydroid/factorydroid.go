// Package factorydroid implements the MintConfig tool adapter for Factory Droid.
//
// Factory Droid (the "droid" CLI) reads custom OpenAI-compatible endpoints from
// the top-level "customModels" array of ~/.factory/settings.json. Each entry is a
// JSON object with model, displayName, baseUrl, apiKey and provider fields. The
// adapter injects a single MintConfig-owned entry (identified by the stable
// displayName) so re-applies update rather than duplicate it, sets the top-level
// default "model" so the custom model is selected, and preserves every other
// setting and customModels entry in the file.
//
// Schema reference (verified 2026-06-30): https://docs.factory.ai/cli/byok
// Supported fields: model (required), displayName, baseUrl (required), apiKey
// (required), provider (required; one of "anthropic", "openai",
// "generic-chat-completion-api"). The legacy ~/.factory/config.json format is out
// of scope.
package factorydroid

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"mintconfig/internal/backup"
	"mintconfig/internal/core"
	"mintconfig/internal/paths"
)

const (
	id   = "factory-droid"
	name = "Factory Droid"

	customModelsKey = "customModels"
	defaultModelKey = "model"

	// managedDisplayName is the stable, MintConfig-owned displayName used to
	// locate and update our customModels entry on re-Apply instead of appending
	// a duplicate.
	managedDisplayName = "MintConfig (MintRouter)"

	// providerOpenAI selects Factory's OpenAI Responses API provider for the
	// MintRouter endpoint, per the task contract.
	providerOpenAI = "openai"
)

// Adapter applies/restores a MintConfig profile for Factory Droid via its
// ~/.factory/settings.json file. Construct it with [New].
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
}

// New returns an Adapter that resolves paths via r and backs up via e.
func New(r *paths.Resolver, e *backup.Engine) *Adapter {
	return &Adapter{r: r, e: e}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return id }

// Name returns the display name.
func (a *Adapter) Name() string { return name }

// settingsPath returns the absolute path to ~/.factory/settings.json.
func (a *Adapter) settingsPath() string { return a.r.Join(".factory", "settings.json") }

// configDir returns the absolute path to ~/.factory.
func (a *Adapter) configDir() string { return a.r.Join(".factory") }

// ConfigPaths returns the config files this adapter manages.
func (a *Adapter) ConfigPaths() []string { return []string{a.settingsPath()} }

// Detect reports whether Factory Droid is installed by checking for the
// ~/.factory directory or its settings.json file. The active path is always
// settings.json.
func (a *Adapter) Detect() (bool, string) {
	path := a.settingsPath()
	if fi, err := os.Stat(a.configDir()); err == nil && fi.IsDir() {
		return true, path
	}
	if _, err := os.Stat(path); err == nil {
		return true, path
	}
	return false, path
}

// Status inspects settings.json relative to profile p. See the package contract
// for the status semantics.
func (a *Adapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	installed, path := a.Detect()
	if !installed {
		return core.StatusNotInstalled, core.StatusNotInstalled.Detail(), nil
	}
	m, err := readJSON(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	marker, ok := extractMarker(m)
	if !ok {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint == core.Fingerprint(p) {
		return core.StatusAppliedByMintConfig, core.StatusAppliedByMintConfig.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up settings.json, then idempotently injects the profile's endpoint
// as the MintConfig-owned entry in the top-level "customModels" array, sets the
// top-level default "model", and writes the MintConfig managed marker. All other
// keys and customModels entries are preserved.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	path := a.settingsPath()
	backupPath, err := a.e.Backup(path)
	if err != nil {
		return core.ApplyResult{}, err
	}
	m, err := readJSON(path)
	if err != nil {
		return core.ApplyResult{}, err
	}

	entry := map[string]any{
		"model":       p.Model,
		"displayName": managedDisplayName,
		"baseUrl":     p.BaseURL,
		"apiKey":      p.APIKey,
		"provider":    providerOpenAI,
	}

	models := asArray(m[customModelsKey])
	updated := false
	for i, raw := range models {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if dn, _ := obj["displayName"].(string); dn == managedDisplayName {
			models[i] = entry
			updated = true
			break
		}
	}
	if !updated {
		models = append(models, entry)
	}
	m[customModelsKey] = models
	m[defaultModelKey] = p.Model
	m[core.MarkerKey] = core.NewMarker(p, p.Label)

	if err := writeJSON(path, m); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintConfig endpoint to Factory Droid settings.json.",
	}, nil
}

// Restore reverts settings.json to its pre-apply state via the backup engine. It
// is a safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	path := a.settingsPath()
	restored, entry, err := a.e.RestoreLatest(path)
	if err != nil {
		return core.RestoreResult{}, err
	}
	msg := "No backup found; nothing to restore."
	if restored {
		msg = "Restored Factory Droid settings.json to its pre-apply state."
	}
	return core.RestoreResult{ChangedPath: path, BackupPath: entry, Message: msg}, nil
}

var _ core.ToolAdapter = (*Adapter)(nil)

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

// writeJSON writes m as indented JSON to path, creating parent dirs. The file is
// written with 0600 perms since it contains the profile's API key.
func writeJSON(path string, m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// asArray returns v as a JSON array, or a fresh empty array if v is not one.
func asArray(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return []any{}
}

// extractMarker pulls the MintConfig marker out of a parsed settings object.
func extractMarker(m map[string]any) (core.Marker, bool) {
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
