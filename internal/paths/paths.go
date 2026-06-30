// Package paths provides cross-platform path resolution with an injectable
// home/base directory so adapters and services can be tested against a
// temporary HOME (t.TempDir()) instead of the real user environment.
package paths

import (
	"os"
	"path/filepath"
)

// Resolver resolves filesystem locations for tools and for MintSwitch's own
// data. Every field is exported so tests can construct a Resolver pointing at
// temporary directories; production code uses [NewResolver].
type Resolver struct {
	// Home is the user's home directory (e.g. "/Users/alice").
	Home string
	// DataDir is MintSwitch's own data directory where backups and settings
	// live (e.g. "~/Library/Application Support/mintswitch").
	DataDir string
	// ConfigHome, when non-empty, overrides the XDG config base directory. When
	// empty, [Resolver.ConfigDir] falls back to Home/.config.
	ConfigHome string
}

// NewResolver builds a Resolver from the current environment. Home defaults to
// os.UserHomeDir, DataDir to os.UserConfigDir()+"/mintswitch", and ConfigHome
// to the XDG_CONFIG_HOME environment variable (if set).
func NewResolver() (*Resolver, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &Resolver{
		Home:       home,
		DataDir:    filepath.Join(cfg, "mintswitch"),
		ConfigHome: os.Getenv("XDG_CONFIG_HOME"),
	}, nil
}

// Join joins the given path elements under Home and returns an absolute path,
// e.g. r.Join(".claude", "settings.json").
func (r *Resolver) Join(elem ...string) string {
	return filepath.Join(append([]string{r.Home}, elem...)...)
}

// ConfigDir returns the XDG config base directory: ConfigHome when set,
// otherwise Home/.config.
func (r *Resolver) ConfigDir() string {
	if r.ConfigHome != "" {
		return r.ConfigHome
	}
	return filepath.Join(r.Home, ".config")
}

// ConfigJoin joins the given path elements under [Resolver.ConfigDir], e.g.
// r.ConfigJoin("opencode", "opencode.json").
func (r *Resolver) ConfigJoin(elem ...string) string {
	return filepath.Join(append([]string{r.ConfigDir()}, elem...)...)
}

// DataJoin joins the given path elements under DataDir.
func (r *Resolver) DataJoin(elem ...string) string {
	return filepath.Join(append([]string{r.DataDir}, elem...)...)
}

// BackupsDir returns MintSwitch's backups root (DataDir/backups).
func (r *Resolver) BackupsDir() string {
	return r.DataJoin("backups")
}

// SettingsPath returns the path to MintSwitch's own settings file
// (DataDir/settings.json).
func (r *Resolver) SettingsPath() string {
	return r.DataJoin("settings.json")
}
