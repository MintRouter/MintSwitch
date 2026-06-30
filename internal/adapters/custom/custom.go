// Package custom implements a generic core.ToolAdapter driven entirely by a
// user-supplied core.CustomToolDef. It lets users manage arbitrary
// OpenAI-compatible tools whose configuration is a JSON file: the def carries a
// JSON OBJECT template whose string values may be the placeholders
// ${API_KEY}/${BASE_URL}/${MODEL}. On Apply the template is parsed, deep-walked
// to substitute the active profile's fields (see core.SubstitutePlaceholders),
// stamped with the MintSwitch marker and written atomically with 0600
// permissions. The original file is backed up first so Restore reverts it.
package custom

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

// Ensure Adapter satisfies the shared tool adapter contract.
var _ core.ToolAdapter = (*Adapter)(nil)

// Adapter manages a single user-defined tool's JSON config on behalf of
// MintSwitch. All filesystem locations derive from the injected resolver and
// backup engine so tests can point HOME at a temp dir.
type Adapter struct {
	def core.CustomToolDef
	r   *paths.Resolver
	e   *backup.Engine
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
}

// New constructs a generic adapter for the given definition.
func New(def core.CustomToolDef, r *paths.Resolver, e *backup.Engine) *Adapter {
	return &Adapter{def: def, r: r, e: e, lookPath: exec.LookPath}
}

// ID returns the stable adapter identifier from the definition.
func (a *Adapter) ID() string { return a.def.ID }

// Name returns the display name from the definition.
func (a *Adapter) Name() string { return a.def.Name }

// configPath returns the absolute config path, expanding a leading "~".
func (a *Adapter) configPath() string {
	return expandHome(a.r, a.def.ConfigPath)
}

// ConfigPaths returns the single config file this adapter manages.
func (a *Adapter) ConfigPaths() []string { return []string{a.configPath()} }

// Detect reports whether the tool is installed. When BinaryName is set, it is
// resolved like a built-in (PATH or curated bin dirs); when empty the tool is a
// config-only provider and is always reported installed. The active path is the
// expanded config path and is returned even when not installed.
func (a *Adapter) Detect() (bool, string) {
	if a.def.BinaryName != "" {
		return a.r.BinaryResolvable(a.lookPath, a.def.BinaryName), a.configPath()
	}
	return true, a.configPath()
}

// Status inspects the current config relative to the given profile, mirroring
// the built-in adapters: not installed, default (no marker), applied (marker
// fingerprint matches) or modified externally (marker present, mismatch).
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

// Apply renders the template with the profile and writes it as the tool config.
// The pre-apply file is backed up only when it is not already MintSwitch-managed
// so the pristine original is what Restore reverts to even after repeated
// Applies. The rendered object is the whole file content with the MintSwitch
// marker injected at the root; an empty/missing existing file is handled by the
// backup engine's "absent" marker.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	obj, err := a.renderTemplate()
	if err != nil {
		return core.ApplyResult{}, err
	}
	path := a.configPath()
	existing, err := readConfig(path)
	if err != nil {
		return core.ApplyResult{}, err
	}
	var backupPath string
	if _, managed := extractMarker(existing); !managed {
		if backupPath, err = a.e.Backup(path); err != nil {
			return core.ApplyResult{}, err
		}
	}
	root := core.SubstitutePlaceholders(obj, p).(map[string]any)
	root[core.MarkerKey] = core.NewMarker(p, p.Label)
	if err := writeConfig(path, root); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: path,
		BackupPath:  backupPath,
		Message:     "Applied MintSwitch profile to " + a.def.Name + " config.",
	}, nil
}

// Restore reverts the config to its pre-apply state via the backup engine. It is
// a safe no-op when nothing was applied.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	path := a.configPath()
	restored, entry, err := a.e.RestoreLatest(path)
	if err != nil {
		return core.RestoreResult{}, err
	}
	msg := "No backup found; nothing to restore."
	if restored {
		msg = "Restored " + a.def.Name + " config to its pre-apply state."
	}
	return core.RestoreResult{ChangedPath: path, BackupPath: entry, Message: msg}, nil
}

// renderTemplate parses the definition's template and verifies it is a JSON
// object. Substitution is performed by the caller on the returned map.
func (a *Adapter) renderTemplate() (map[string]any, error) {
	var root any
	if err := json.Unmarshal([]byte(a.def.Template), &root); err != nil {
		return nil, fmt.Errorf("custom: template is not valid JSON: %w", err)
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("custom: template must be a JSON object")
	}
	return obj, nil
}

// expandHome expands a leading "~" or "~/" against the resolver's Home.
func expandHome(r *paths.Resolver, path string) string {
	if path == "~" {
		return r.Home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(r.Home, path[2:])
	}
	return path
}

// readConfig reads and parses the JSON config file. A missing file returns a nil
// map and no error.
func readConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

// writeConfig writes the config as indented JSON with 0600 permissions using an
// atomic temp-file + rename, creating parent directories as needed.
func writeConfig(path string, root map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".mintswitch-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
