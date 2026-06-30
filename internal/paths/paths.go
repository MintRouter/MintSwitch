// Package paths provides cross-platform path resolution with an injectable
// home/base directory so adapters and services can be tested against a
// temporary HOME (t.TempDir()) instead of the real user environment.
package paths

import (
	"os"
	"os/exec"
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
	// SystemBinDirs are absolute, system-wide executable directories searched by
	// [Resolver.BinaryResolvable] in addition to the HOME-derived ones.
	// NewResolver seeds the common macOS/Linux locations; tests can leave it nil
	// to scan only HOME-derived dirs for determinism.
	SystemBinDirs []string
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
		Home:          home,
		DataDir:       filepath.Join(cfg, "mintswitch"),
		ConfigHome:    os.Getenv("XDG_CONFIG_HOME"),
		SystemBinDirs: []string{"/opt/homebrew/bin", "/usr/local/bin"},
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

// binDirs returns the bounded, curated set of directories searched for tool
// binaries: the HOME-derived user dirs (~/.local/bin, ~/.npm-global/bin, ~/bin)
// plus the configured SystemBinDirs. It derives everything from Home and never
// consults os.UserHomeDir, so tests pointing Home at a temp dir stay isolated.
func (r *Resolver) binDirs() []string {
	var dirs []string
	if r.Home != "" {
		dirs = append(dirs,
			filepath.Join(r.Home, ".local", "bin"),
			filepath.Join(r.Home, ".npm-global", "bin"),
			filepath.Join(r.Home, "bin"),
		)
	}
	return append(dirs, r.SystemBinDirs...)
}

// BinaryResolvable reports whether binName is installed as a resolvable CLI. It
// first consults lookPath (the process PATH; exec.LookPath in production) and
// then a bounded, curated set of common global-bin directories (see binDirs), so
// a Finder-launched app with a narrow PATH still detects CLIs installed via
// "npm install -g" or a curl installer. It performs only filesystem stats and
// never spawns a subprocess, so it is safe to call on every Detect/ListTools
// (e.g. on window focus). A nil lookPath defaults to exec.LookPath.
func (r *Resolver) BinaryResolvable(lookPath func(string) (string, error), binName string) bool {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(binName); err == nil {
		return true
	}
	for _, dir := range r.binDirs() {
		if isExecutableFile(filepath.Join(dir, binName)) {
			return true
		}
	}
	return false
}

// isExecutableFile reports whether path is a regular file with an executable
// bit set. Symlinks are followed (os.Stat), so an npm-global symlink to a real
// binary resolves correctly.
func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}
