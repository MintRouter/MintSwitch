// Package paths provides cross-platform path resolution with an injectable
// home/base directory so adapters and services can be tested against a
// temporary HOME (t.TempDir()) instead of the real user environment.
package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	// NativeConfigDir is the OS-native per-user config root from
	// os.UserConfigDir(): %APPDATA% on Windows, ~/Library/Application Support on
	// macOS, $XDG_CONFIG_HOME or ~/.config on Linux. Tools that follow the
	// native convention on Windows (e.g. Zed) resolve their config under it.
	NativeConfigDir string
	// CodexHome, when non-empty, overrides the Codex home directory used by
	// [Resolver.CodexDir]. NewResolver seeds it from $CODEX_HOME.
	CodexHome string
	// ClaudeConfigDir, when non-empty, overrides Claude Code's config directory
	// used by [Resolver.ClaudeDir]. NewResolver seeds it from $CLAUDE_CONFIG_DIR.
	ClaudeConfigDir string
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
		Home:            home,
		DataDir:         filepath.Join(cfg, "mintswitch"),
		ConfigHome:      os.Getenv("XDG_CONFIG_HOME"),
		NativeConfigDir: cfg,
		CodexHome:       os.Getenv("CODEX_HOME"),
		ClaudeConfigDir: os.Getenv("CLAUDE_CONFIG_DIR"),
		SystemBinDirs:   []string{"/opt/homebrew/bin", "/usr/local/bin"},
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

// ZedConfigDir returns the directory holding Zed's settings.json:
// %APPDATA%\Zed on Windows (Zed follows the native convention there),
// ~/.config/zed (XDG-aware) on macOS and Linux, per Zed's documentation.
func (r *Resolver) ZedConfigDir() string {
	return r.zedConfigDir(runtime.GOOS)
}

// zedConfigDir is the GOOS-parameterised implementation of
// [Resolver.ZedConfigDir], so tests can exercise every branch from any host
// OS. On Windows it falls back to the XDG-style dir when NativeConfigDir is
// unset (test resolvers constructed without it).
func (r *Resolver) zedConfigDir(goos string) string {
	if goos == "windows" && r.NativeConfigDir != "" {
		return filepath.Join(r.NativeConfigDir, "Zed")
	}
	return r.ConfigJoin("zed")
}

// CodexDir returns the Codex home directory: CodexHome ($CODEX_HOME) when set,
// otherwise Home/.codex (the documented default on every OS).
func (r *Resolver) CodexDir() string {
	if r.CodexHome != "" {
		return r.CodexHome
	}
	return filepath.Join(r.Home, ".codex")
}

// ClaudeDir returns Claude Code's config directory: ClaudeConfigDir
// ($CLAUDE_CONFIG_DIR) when set, otherwise Home/.claude (the documented
// default on every OS).
func (r *Resolver) ClaudeDir() string {
	if r.ClaudeConfigDir != "" {
		return r.ClaudeConfigDir
	}
	return filepath.Join(r.Home, ".claude")
}

// ClaudeJSONPath returns the path of Claude Code's global .claude.json (the
// MCP-servers file): $CLAUDE_CONFIG_DIR/.claude.json when the override is set
// (per Claude Code's settings docs), otherwise Home/.claude.json.
func (r *Resolver) ClaudeJSONPath() string {
	if r.ClaudeConfigDir != "" {
		return filepath.Join(r.ClaudeConfigDir, ".claude.json")
	}
	return filepath.Join(r.Home, ".claude.json")
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

// UserBinDirs returns the HOME-derived curated bin directories (~/.local/bin,
// ~/.npm-global/bin, ~/bin). These are the only directories MintSwitch will ever
// delete a standalone tool binary from. It derives everything from Home and
// never consults os.UserHomeDir, so tests pointing Home at a temp dir stay
// isolated; an empty Home yields no directories.
func (r *Resolver) UserBinDirs() []string {
	if r.Home == "" {
		return nil
	}
	return []string{
		filepath.Join(r.Home, ".local", "bin"),
		filepath.Join(r.Home, ".npm-global", "bin"),
		filepath.Join(r.Home, "bin"),
	}
}

// binDirs returns the bounded, curated set of directories searched for tool
// binaries: the HOME-derived user dirs (see UserBinDirs) plus the configured
// SystemBinDirs.
func (r *Resolver) binDirs() []string {
	return append(r.UserBinDirs(), r.SystemBinDirs...)
}

// BinaryResolvable reports whether binName is installed as a resolvable CLI. It
// is a thin boolean wrapper over [Resolver.ResolveBinary]; see that method for
// the lookup order and the no-subprocess guarantee.
func (r *Resolver) BinaryResolvable(lookPath func(string) (string, error), binName string) bool {
	_, ok := r.ResolveBinary(lookPath, binName)
	return ok
}

// ResolveBinary resolves binName to the absolute path of the executable that
// [Resolver.BinaryResolvable] would find. It first consults lookPath (the
// process PATH; exec.LookPath in production) and then a bounded, curated set of
// common global-bin directories (see binDirs), so a Finder-launched app with a
// narrow PATH still resolves CLIs installed via "npm install -g" or a curl
// installer. It performs only filesystem stats and never spawns a subprocess, so
// it is safe to call on every Detect/ListTools. A nil lookPath defaults to
// exec.LookPath. ok is false (and path empty) when binName cannot be resolved.
func (r *Resolver) ResolveBinary(lookPath func(string) (string, error), binName string) (string, bool) {
	return r.resolveBinary(lookPath, binName, runtime.GOOS)
}

// resolveBinary is the GOOS-parameterised implementation of
// [Resolver.ResolveBinary], so tests can exercise the Windows and Unix
// branches from any host OS.
func (r *Resolver) resolveBinary(lookPath func(string) (string, error), binName, goos string) (string, bool) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if p, err := lookPath(binName); err == nil {
		return p, true
	}
	for _, dir := range r.binDirs() {
		for _, name := range binCandidates(binName, goos) {
			p := filepath.Join(dir, name)
			if isExecutableFile(p, goos) {
				return p, true
			}
		}
	}
	return "", false
}

// binCandidates returns the file names probed for binName inside a curated bin
// dir. On Windows executables carry an extension, so the PATHEXT-style
// candidates .exe/.cmd/.bat are probed first (npm shims are .cmd); elsewhere
// only the bare name is probed.
func binCandidates(binName, goos string) []string {
	if goos != "windows" {
		return []string{binName}
	}
	return []string{binName + ".exe", binName + ".cmd", binName + ".bat", binName}
}

// isExecutableFile reports whether path is a regular file that qualifies as an
// executable. On Unix an executable bit must be set; on Windows there are no
// exec bits (Go reports 0666/0444), so any regular file qualifies. Symlinks are
// followed (os.Stat), so an npm-global symlink to a real binary resolves
// correctly.
func isExecutableFile(path, goos string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if goos == "windows" {
		return true
	}
	return fi.Mode().Perm()&0o111 != 0
}
