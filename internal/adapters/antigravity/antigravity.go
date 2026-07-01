// Package antigravity implements the core.ToolAdapter for Google Antigravity.
// It applies and restores a MintSwitch-managed OpenAI-compatible endpoint in
// Antigravity's global JSON settings at ~/.antigravity/settings.json, setting
// both the top-level "antigravity.ai.*" / "antigravity.openai-compatible.*"
// keys and their "antigravity.agent.*" variants to the "openai-compatible"
// provider, while preserving all other existing settings keys.
package antigravity

import (
	"os/exec"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

// providerValue is the provider identifier MintSwitch writes into Antigravity.
const providerValue = "openai-compatible"

// binName is the Antigravity CLI binary name used as an install signal.
const binName = "agy"

// Ensure Adapter satisfies the shared tool adapter contract.
var _ core.ToolAdapter = (*Adapter)(nil)

// Adapter manages Antigravity's configuration on behalf of MintSwitch.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New constructs an Antigravity adapter. All filesystem locations derive from
// the injected resolver and backup engine so tests can point HOME at a temp dir.
func New(r *paths.Resolver, e *backup.Engine) *Adapter {
	return &Adapter{r: r, e: e, lookPath: exec.LookPath}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return "antigravity" }

// Name returns the display name.
func (a *Adapter) Name() string { return "Antigravity" }

// configPath returns the absolute path to ~/.antigravity/settings.json.
func (a *Adapter) configPath() string { return a.r.Join(".antigravity", "settings.json") }

// ConfigPaths returns the candidate config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.configPath()}
}

// Detect reports whether Antigravity is installed. It is considered installed
// when the "agy" CLI binary is resolvable, or when either the ~/.antigravity or
// ~/.gemini/antigravity directory exists. The active path is always the global
// settings file and is returned even when not installed, since Status/Apply
// rely on it.
func (a *Adapter) Detect() (bool, string) {
	installed := a.r.BinaryResolvable(a.lookPath, binName) ||
		dirExists(a.r.Join(".antigravity")) ||
		dirExists(a.r.Join(".gemini", "antigravity"))
	return installed, a.configPath()
}

// Status inspects the current settings relative to the given profile.
func (a *Adapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	installed, path := a.Detect()
	if !installed {
		return core.StatusNotInstalled, core.StatusNotInstalled.Detail(), nil
	}
	root, err := readConfig(path)
	if err != nil {
		return core.StatusDefault, "", err
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

// Apply backs up the existing settings (only when it is not already
// MintSwitch-managed), then idempotently sets the openai-compatible provider
// keys (including the "antigravity.agent.*" variants) and the managed marker,
// preserving all other keys. See the OpenCode adapter for the rationale behind
// backing up only pristine/unmanaged files.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	path := a.configPath()
	root, err := readConfig(path)
	if err != nil {
		return core.ApplyResult{}, err
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

	root["antigravity.ai.provider"] = providerValue
	root["antigravity.openai-compatible.endpoint"] = p.BaseURL
	root["antigravity.openai-compatible.apiKey"] = p.APIKey
	root["antigravity.ai.model"] = p.Model
	root["antigravity.agent.provider"] = providerValue
	root["antigravity.agent.openai-compatible.endpoint"] = p.BaseURL
	root["antigravity.agent.openai-compatible.apiKey"] = p.APIKey
	root["antigravity.agent.model"] = p.Model
	root[core.MarkerKey] = core.NewMarker(p, p.Label)

	if err := writeConfig(path, root); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch endpoint to Antigravity settings.",
	}, nil
}

// Restore reverts the settings to their pre-apply state via the backup engine.
// It is a safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	path := a.configPath()
	restored, entry, err := a.e.RestoreLatest(path)
	if err != nil {
		return core.RestoreResult{}, err
	}
	msg := "No backup found; nothing to restore."
	if restored {
		msg = "Restored Antigravity settings to their pre-apply state."
	}
	return core.RestoreResult{ChangedPath: path, BackupPath: entry, Message: msg}, nil
}
