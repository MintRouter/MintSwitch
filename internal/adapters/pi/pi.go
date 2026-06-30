// Package pi implements the MintSwitch tool adapter for Pi (earendil-works).
//
// Pi reads custom providers from ~/.pi/agent/models.json. The adapter registers
// a MintSwitch-managed provider keyed "mintswitch" with the active profile's
// OpenAI-compatible endpoint: baseUrl, api "openai-completions", apiKey,
// authHeader true (so Pi sends "Authorization: Bearer <apiKey>"), and a single
// models entry for the profile's Model. Every other provider, model and
// top-level key in the file is preserved.
//
// Schema reference (verified 2026-06-30):
// https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models.md
package pi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

const (
	id   = "pi"
	name = "Pi (earendil-works)"

	providersKey = "providers"
	providerKey  = "mintswitch"

	keyBaseURL    = "baseUrl"
	keyAPI        = "api"
	keyAPIKey     = "apiKey"
	keyAuthHeader = "authHeader"
	keyModels     = "models"
	keyID         = "id"

	apiType = "openai-completions"
)

// Adapter applies/restores a MintSwitch profile for Pi via its
// ~/.pi/agent/models.json file. Construct it with [New].
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

// modelsPath returns the absolute path to ~/.pi/agent/models.json.
func (a *Adapter) modelsPath() string { return a.r.Join(".pi", "agent", "models.json") }

// configDir returns the absolute path to ~/.pi.
func (a *Adapter) configDir() string { return a.r.Join(".pi") }

// ConfigPaths returns the config files this adapter manages.
func (a *Adapter) ConfigPaths() []string { return []string{a.modelsPath()} }

// Detect reports whether Pi is installed by checking for the ~/.pi directory.
// The active path is always models.json.
func (a *Adapter) Detect() (bool, string) {
	path := a.modelsPath()
	if fi, err := os.Stat(a.configDir()); err == nil && fi.IsDir() {
		return true, path
	}
	return false, path
}

// Status inspects models.json relative to profile p. See the package contract
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
		return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up models.json (only when it is not already MintSwitch-managed),
// then idempotently registers the MintSwitch provider with the profile's
// endpoint and writes the managed marker, preserving all other providers, models
// and top-level keys.
//
// The backup is created only on the first Apply over a pristine/unmanaged (or
// absent) file, so the pristine pre-MintSwitch snapshot is what Restore reverts
// to even after repeated Applies. Backing up an already-managed file would
// snapshot a managed state (prior profile's key + marker) and hide the pristine
// original. Limitation: if the file is already managed but no backup exists
// (e.g. the backups dir was deleted, or a marker was left by an older version),
// no new backup is taken and Restore is a no-op — we cannot safely snapshot a
// managed file, and without the original we cannot distinguish our injected keys
// from the user's own to strip them.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	path := a.modelsPath()
	m, err := readJSON(path)
	if err != nil {
		return core.ApplyResult{}, err
	}
	var backupPath string
	if marker, ok := extractMarker(m); !ok || !marker.Managed {
		backupPath, err = a.e.Backup(path)
		if err != nil {
			return core.ApplyResult{}, err
		}
	}

	providers := asObject(m[providersKey])
	prov := asObject(providers[providerKey])
	prov[keyBaseURL] = p.BaseURL
	prov[keyAPI] = apiType
	prov[keyAPIKey] = p.APIKey
	prov[keyAuthHeader] = true
	prov[keyModels] = upsertModel(prov[keyModels], p.Model)
	providers[providerKey] = prov
	m[providersKey] = providers
	m[core.MarkerKey] = core.NewMarker(p, p.Label)

	if err := writeJSON(path, m); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch endpoint to Pi models.json.",
	}, nil
}

// Restore reverts models.json to its pre-apply state via the backup engine. It
// is a safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	path := a.modelsPath()
	restored, entry, err := a.e.RestoreLatest(path)
	if err != nil {
		return core.RestoreResult{}, err
	}
	msg := "No backup found; nothing to restore."
	if restored {
		msg = "Restored Pi models.json to its pre-apply state."
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
// written with 0600 perms since it contains the profile's auth token.
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

// asObject returns v as a JSON object, or a fresh object if v is not one.
func asObject(v any) map[string]any {
	if obj, ok := v.(map[string]any); ok {
		return obj
	}
	return map[string]any{}
}

// upsertModel returns the models array with an entry for modelID, adding one
// only when absent so re-Apply does not duplicate it and other models survive.
func upsertModel(v any, modelID string) []any {
	models, _ := v.([]any)
	for _, item := range models {
		if obj, ok := item.(map[string]any); ok && obj[keyID] == modelID {
			return models
		}
	}
	return append(models, map[string]any{keyID: modelID})
}

// extractMarker pulls the MintSwitch marker out of a parsed models.json object.
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
