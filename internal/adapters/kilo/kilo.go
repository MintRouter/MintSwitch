// Package kilo implements the core.ToolAdapter for Kilo Code
// (https://kilo.ai), a fork of OpenCode. It applies and restores a
// MintSwitch-managed OpenAI-compatible endpoint in Kilo's global config at
// ~/.config/kilo/kilo.json or kilo.jsonc (XDG-aware), using Kilo's built-in
// "openai-compatible" provider and setting it as the default model, while
// preserving all other existing config keys.
//
// Config-file resolution (verified against the OpenCode config loader Kilo
// forks): when both files exist, kilo.jsonc wins (it is checked first and
// merged last), so the adapter targets kilo.jsonc when present, otherwise
// kilo.json, and creates kilo.json when neither exists. A kilo.jsonc whose
// content is strict JSON is rewritten safely (valid JSON is valid JSONC); one
// carrying comments or other JSONC-only syntax is never rewritten — Status
// reports ModifiedExternally and Apply refuses — because Go's encoding/json
// round-trip would destroy the user's comments.
package kilo

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

// providerID is Kilo's built-in OpenAI-compatible provider key under "provider".
const providerID = "openai-compatible"

// jsoncDetail explains why a comment-carrying kilo.jsonc is not managed.
const jsoncDetail = "kilo.jsonc contains comments or other JSONC-only syntax; " +
	"MintSwitch cannot modify it without destroying them."

// errJSONC is returned by Apply when the active kilo.jsonc cannot be rewritten
// without destroying JSONC-only syntax such as comments.
var errJSONC = errors.New("kilo: " + jsoncDetail + " Edit the file manually or convert it to plain JSON")

// Ensure Adapter satisfies the shared tool adapter contract.
var _ core.ToolAdapter = (*Adapter)(nil)

// Adapter manages Kilo Code's configuration on behalf of MintSwitch.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New constructs a Kilo Code adapter. All filesystem locations derive from the
// injected resolver and backup engine so tests can point HOME at a temp dir.
func New(r *paths.Resolver, e *backup.Engine) *Adapter {
	return &Adapter{r: r, e: e, lookPath: exec.LookPath}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return "kilo" }

// Name returns the display name.
func (a *Adapter) Name() string { return "Kilo Code" }

// jsonPath returns the global kilo.json path (XDG-aware).
func (a *Adapter) jsonPath() string { return a.r.ConfigJoin("kilo", "kilo.json") }

// jsoncPath returns the global kilo.jsonc path (XDG-aware).
func (a *Adapter) jsoncPath() string { return a.r.ConfigJoin("kilo", "kilo.jsonc") }

// configPath returns the active global config file: kilo.jsonc when it exists
// (it overrides kilo.json in Kilo's merge order), otherwise kilo.json — which
// is also the file created when neither exists.
func (a *Adapter) configPath() string {
	if fileExists(a.jsoncPath()) {
		return a.jsoncPath()
	}
	return a.jsonPath()
}

// ConfigPaths returns the candidate config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.jsonPath(), a.jsoncPath()}
}

// Detect reports whether Kilo Code is installed, defined solely as the "kilo"
// CLI binary being resolvable (via PATH or a curated set of common bin dirs). A
// leftover global config file/dir is not an installed signal, so an uninstall
// is reflected. The active path is always returned even when not installed,
// since Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	return a.r.BinaryResolvable(a.lookPath, "kilo"), a.configPath()
}

// Status inspects the current config relative to the given profile. A
// kilo.jsonc carrying JSONC-only syntax reports ModifiedExternally: MintSwitch
// cannot rewrite it without destroying the user's comments.
func (a *Adapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	installed, path := a.Detect()
	if !installed {
		return core.StatusNotInstalled, core.StatusNotInstalled.Detail(), nil
	}
	root, strict, err := readConfig(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	if !strict {
		return core.StatusModifiedExternally, jsoncDetail, nil
	}
	marker, ok := extractMarker(root)
	if !ok {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint == core.Fingerprint(p) {
		return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
	}
	return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
}

// Apply backs up the existing config (only when it is not already
// MintSwitch-managed), then idempotently injects the MintSwitch endpoint under
// Kilo's built-in "openai-compatible" provider, the default model, and the
// managed marker, preserving all other keys. It refuses to touch a kilo.jsonc
// carrying JSONC-only syntax (see errJSONC).
//
// The backup is created only on the first Apply over a pristine/unmanaged (or
// absent) file, so the pristine pre-MintSwitch snapshot is what Restore reverts
// to even after repeated Applies.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	path := a.configPath()
	root, strict, err := readConfig(path)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if !strict {
		return core.ApplyResult{}, errJSONC
	}
	if root == nil {
		root = map[string]any{}
	}
	var backupPath string
	if _, managed := extractMarker(root); !managed {
		backupPath, err = a.e.Backup(path)
		if err != nil {
			return core.ApplyResult{}, err
		}
	}

	provider, _ := root["provider"].(map[string]any)
	if provider == nil {
		provider = map[string]any{}
	}
	provider[providerID] = map[string]any{
		"options": map[string]any{
			"baseURL": p.BaseURL,
			"apiKey":  p.APIKey,
		},
		"models": map[string]any{
			p.Model: map[string]any{"name": p.Model},
		},
	}
	root["provider"] = provider
	root["model"] = providerID + "/" + p.Model
	root[core.MarkerKey] = core.NewMarker(p, p.Label)

	if err := writeConfig(path, root); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch endpoint to Kilo Code config.",
	}, nil
}

// Restore reverts the config to its pre-apply state via the backup engine. It
// checks both candidate files (kilo.jsonc and kilo.json) since the active file
// may have changed since Apply. It is a safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	res := core.RestoreResult{
		ChangedPath: a.configPath(),
		Message:     "No backup found; nothing to restore.",
	}
	for _, path := range []string{a.jsoncPath(), a.jsonPath()} {
		restored, entry, err := a.e.RestoreLatest(path)
		if err != nil {
			return core.RestoreResult{}, err
		}
		if restored {
			res = core.RestoreResult{
				ChangedPath: path,
				BackupPath:  entry,
				Message:     "Restored Kilo Code config to its pre-apply state.",
			}
		}
	}
	return res, nil
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// readConfig reads and parses the JSON config file. A missing file returns a
// nil map, strict=true and no error. strict is false (with a nil map and no
// error) when a .jsonc file fails strict JSON parsing, i.e. it carries
// comments/trailing commas that a rewrite would destroy. A .json file that
// fails to parse is corrupt and returns the parse error.
func readConfig(path string) (root map[string]any, strict bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, err
	}
	if len(data) == 0 {
		return map[string]any{}, true, nil
	}
	if err := json.Unmarshal(data, &root); err != nil {
		if strings.HasSuffix(path, ".jsonc") {
			return nil, false, nil
		}
		return nil, false, err
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, true, nil
}

// writeConfig writes the config as indented JSON, atomically and with
// restrictive permissions, creating parent directories as needed.
func writeConfig(path string, root map[string]any) error {
	return core.WriteJSONObjectAtomic(path, root)
}

// extractMarker decodes the MintSwitch marker from the parsed config, if present.
func extractMarker(root map[string]any) (core.Marker, bool) {
	raw, ok := root[core.MarkerKey]
	if !ok {
		return core.Marker{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return core.Marker{}, false
	}
	var m core.Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return core.Marker{}, false
	}
	if !m.Managed {
		return core.Marker{}, false
	}
	return m, true
}
