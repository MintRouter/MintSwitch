// Package installer installs and uninstalls the supported AI coding tools.
//
// Install always uses npm's global package management: each tool ID maps to a
// whitelisted npm package (and optional extra flags); user input is never
// interpolated into a command — only the fixed package names below are ever
// passed to npm.
//
// Uninstall is install-method aware. A tool can be installed via npm, Homebrew,
// or a standalone (curl) installer, so removing it by always running
// "npm uninstall -g" silently no-ops for the brew/curl cases. Uninstall resolves
// the tool's CLI binary path and classifies the install method from it, then
// runs the matching action: "brew uninstall" for Homebrew, "npm uninstall -g"
// for npm-global, or deleting the exact binary file for a standalone install.
//
// External processes run through a [CommandRunner] seam and file deletion runs
// through an injectable remove func, so tests exercise command construction and
// the deletion path without ever invoking real npm/brew or touching real files.
package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mintswitch/internal/paths"
)

// ErrNpmMissing is returned when the npm executable cannot be found on PATH.
var ErrNpmMissing = errors.New("installer: npm not found on PATH")

// ErrBrewMissing is returned when a Homebrew-installed tool is being uninstalled
// but the brew executable cannot be found on PATH.
var ErrBrewMissing = errors.New("installer: brew not found on PATH")

// ErrUnknownTool is returned when a toolID has no whitelisted npm package.
var ErrUnknownTool = errors.New("installer: unknown tool")

// ErrUnknownMethod is returned when Uninstall cannot determine how a tool was
// installed (the binary is unresolvable, or it resolves outside every known
// Homebrew/npm/curated location). It signals a safe no-op: nothing destructive
// is done and a clear, user-facing message is returned for the UI to show.
var ErrUnknownMethod = errors.New("installer: could not determine install method")

// binSpec maps a tool ID to its CLI binary name (for resolving the installed
// path) and the name passed to "brew uninstall" when the tool was installed via
// Homebrew. brew uninstall <name> handles both formulae and casks.
type binSpec struct {
	bin  string
	brew string
}

// binaries maps each supported tool ID to its CLI binary and brew name. The IDs
// match the adapter ID() values; the bin names match the adapters' Detect()
// lookups (claude, codex, opencode).
var binaries = map[string]binSpec{
	"claude-code": {bin: "claude", brew: "claude"},
	"codex":       {bin: "codex", brew: "codex"},
	"opencode":    {bin: "opencode", brew: "opencode"},
}

// brewPrefixes are the Homebrew install prefixes used as a secondary signal when
// classifying an install method (the primary signal is a symlink target in a
// Cellar/Caskroom).
var brewPrefixes = []string{"/opt/homebrew", "/usr/local"}

// Package describes the npm package backing a tool plus any extra flags that
// must be passed to `npm install` for that package.
type Package struct {
	// NpmPackage is the exact, whitelisted npm package name.
	NpmPackage string
	// ExtraFlags are install-only flags inserted before the package name.
	ExtraFlags []string
}

// packages whitelists each supported tool ID to its npm package. The IDs match
// the adapter ID() values in internal/adapters. Values verified from official
// docs (2026).
var packages = map[string]Package{
	"claude-code": {NpmPackage: "@anthropic-ai/claude-code"},
	"codex":       {NpmPackage: "@openai/codex"},
	"opencode":    {NpmPackage: "opencode-ai"},
	"droid":       {NpmPackage: "droid"},
	"kilo":        {NpmPackage: "@kilocode/cli"},
}

// Spec returns the whitelisted package for toolID and whether it is known.
func Spec(toolID string) (Package, bool) {
	p, ok := packages[toolID]
	return p, ok
}

// InstallArgs returns the full argv for installing toolID, e.g.
// ["npm","install","-g","@kilocode/cli"].
// It returns ErrUnknownTool for an unrecognised toolID.
func InstallArgs(toolID string) ([]string, error) {
	p, ok := packages[toolID]
	if !ok {
		return nil, ErrUnknownTool
	}
	args := []string{"npm", "install", "-g"}
	args = append(args, p.ExtraFlags...)
	args = append(args, p.NpmPackage)
	return args, nil
}

// UninstallArgs returns the full argv for uninstalling toolID, e.g.
// ["npm","uninstall","-g","@openai/codex"]. It returns ErrUnknownTool for an
// unrecognised toolID.
func UninstallArgs(toolID string) ([]string, error) {
	p, ok := packages[toolID]
	if !ok {
		return nil, ErrUnknownTool
	}
	return []string{"npm", "uninstall", "-g", p.NpmPackage}, nil
}

// CommandRunner runs an external command and returns its combined stdout+stderr
// output. It is the seam tests replace with a fake to avoid real npm calls.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// ExecRunner is the production [CommandRunner] backed by os/exec. It captures
// combined stdout and stderr so install logs and npm errors are both surfaced.
type ExecRunner struct{}

// Run executes name with args and returns the combined output and any error.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// Installer builds and runs install/uninstall actions for a tool. Install is
// npm-only; Uninstall is install-method aware (npm / Homebrew / standalone) and
// uses resolve to locate the tool's binary, userBinDirs to bound where a
// standalone binary may be deleted, and remove to perform the deletion.
type Installer struct {
	runner   CommandRunner
	lookPath func(file string) (string, error)
	// resolve resolves a binary name to its absolute path; nil falls back to
	// exec.LookPath (PATH only). Production wires it to paths.Resolver.ResolveBinary.
	resolve func(name string) (string, bool)
	// userBinDirs are the only directories a standalone binary may be deleted
	// from (the HOME-derived curated dirs). Empty disables standalone deletion.
	userBinDirs []string
	// remove deletes a standalone binary file; nil defaults to os.Remove.
	remove func(path string) error
}

// New returns an Installer that runs commands via runner and detects npm with
// the real exec.LookPath. It resolves binaries via PATH only (no curated dirs);
// use [NewMethodAware] for full install-method-aware uninstall.
func New(runner CommandRunner) *Installer {
	return &Installer{runner: runner, lookPath: exec.LookPath, remove: os.Remove}
}

// NewWithLookPath is like [New] but injects the executable-detection function.
// It is the seam used by tests to simulate npm/brew being present or missing.
func NewWithLookPath(runner CommandRunner, lookPath func(string) (string, error)) *Installer {
	return &Installer{runner: runner, lookPath: lookPath, remove: os.Remove}
}

// NewMethodAware returns the production Installer: commands run via runner,
// executables are detected with exec.LookPath, binaries are resolved with the
// resolver's no-subprocess [paths.Resolver.ResolveBinary], standalone deletions
// are bounded to the resolver's curated [paths.Resolver.UserBinDirs], and files
// are removed with os.Remove.
func NewMethodAware(runner CommandRunner, r *paths.Resolver) *Installer {
	return NewWithResolver(runner, exec.LookPath,
		func(name string) (string, bool) { return r.ResolveBinary(exec.LookPath, name) },
		r.UserBinDirs(), os.Remove)
}

// NewWithResolver is the fully-injected Installer seam used by both
// [NewMethodAware] and tests: lookPath detects npm/brew, resolve locates a tool
// binary, userBinDirs bounds standalone deletion, and remove performs it (nil
// defaults to os.Remove). It lets tests exercise method-aware uninstall without
// touching real PATH, brew/npm, or the filesystem.
func NewWithResolver(runner CommandRunner, lookPath func(string) (string, error), resolve func(string) (string, bool), userBinDirs []string, remove func(string) error) *Installer {
	if remove == nil {
		remove = os.Remove
	}
	return &Installer{
		runner:      runner,
		lookPath:    lookPath,
		resolve:     resolve,
		userBinDirs: userBinDirs,
		remove:      remove,
	}
}

// Install installs toolID globally via npm. It returns the exact argv that was
// (or would be) run so callers can show it to the user, the command's combined
// output, and any error. For an unknown tool it returns ErrUnknownTool with nil
// args; when npm is missing it returns the intended args plus ErrNpmMissing.
func (i *Installer) Install(ctx context.Context, toolID string) ([]string, string, error) {
	args, err := InstallArgs(toolID)
	if err != nil {
		return nil, "", err
	}
	if _, err := i.lookPath("npm"); err != nil {
		return args, "", ErrNpmMissing
	}
	out, runErr := i.runner.Run(ctx, args[0], args[1:]...)
	return args, out, runErr
}

// Uninstall removes toolID using the method it was actually installed with. It
// resolves the tool's CLI binary, classifies the install method from the
// resolved path, and runs the matching action:
//   - Homebrew  -> "brew uninstall <name>" (args + ErrBrewMissing when brew absent)
//   - npm-global -> "npm uninstall -g <pkg>" (args + ErrNpmMissing when npm absent)
//   - standalone -> deletes the exact resolved binary file (returned as ["rm", path])
//
// For an unknown tool it returns ErrUnknownTool. When the binary cannot be
// resolved or the method is indeterminate, it does nothing destructive and
// returns a clear, user-facing message with ErrUnknownMethod. The return
// contract otherwise matches [Installer.Install].
func (i *Installer) Uninstall(ctx context.Context, toolID string) ([]string, string, error) {
	spec, ok := binaries[toolID]
	if !ok {
		return nil, "", ErrUnknownTool
	}
	resolve := i.resolve
	if resolve == nil {
		resolve = func(name string) (string, bool) {
			p, err := i.lookPath(name)
			if err != nil {
				return "", false
			}
			return p, true
		}
	}
	resolved, found := resolve(spec.bin)
	if !found {
		return nil, fmt.Sprintf("Could not determine how %s was installed: the %q binary was not found. Remove it manually.", toolID, spec.bin), ErrUnknownMethod
	}

	switch classifyMethod(resolved, i.userBinDirs) {
	case methodHomebrew:
		args := []string{"brew", "uninstall", spec.brew}
		if _, err := i.lookPath("brew"); err != nil {
			return args, "", ErrBrewMissing
		}
		out, runErr := i.runner.Run(ctx, args[0], args[1:]...)
		return args, out, runErr
	case methodNpm:
		args, _ := UninstallArgs(toolID)
		if _, err := i.lookPath("npm"); err != nil {
			return args, "", ErrNpmMissing
		}
		out, runErr := i.runner.Run(ctx, args[0], args[1:]...)
		return args, out, runErr
	case methodStandalone:
		remove := i.remove
		if remove == nil {
			remove = os.Remove
		}
		args := []string{"rm", resolved}
		if err := remove(resolved); err != nil {
			return args, "", err
		}
		return args, fmt.Sprintf("Removed standalone binary %s", resolved), nil
	default:
		return nil, fmt.Sprintf("Could not determine how %s was installed (%s). Remove it manually.", toolID, resolved), ErrUnknownMethod
	}
}

// uninstallMethod is the classified install method of a resolved binary.
type uninstallMethod int

const (
	methodUnknown uninstallMethod = iota
	methodHomebrew
	methodNpm
	methodStandalone
)

// classifyMethod determines how the binary at resolved was installed, using
// first-match-wins ordering over authoritative signals:
//  1. Homebrew: the path (or its symlink target) lies in a Cellar/Caskroom.
//  2. npm-global: the path (or target) lies under a node_modules / .npm-global tree.
//  3. Homebrew: the path (or target) sits under a brew prefix (/opt/homebrew, /usr/local).
//  4. standalone: the resolved path is a regular file directly inside a curated
//     userBinDir — the only case that authorises deleting the file.
//
// Anything else is methodUnknown (a safe no-op). The Cellar/node_modules checks
// precede the brew-prefix check so an npm-global package installed under a
// Homebrew node prefix is still classified as npm.
func classifyMethod(resolved string, userBinDirs []string) uninstallMethod {
	candidates := []string{resolved}
	if target := symlinkTarget(resolved); target != "" {
		candidates = append(candidates, target)
	}
	for _, p := range candidates {
		if strings.Contains(p, "/Cellar/") || strings.Contains(p, "/Caskroom/") {
			return methodHomebrew
		}
	}
	for _, p := range candidates {
		if strings.Contains(p, "/node_modules/") || strings.Contains(p, "/.npm-global/") {
			return methodNpm
		}
	}
	for _, p := range candidates {
		if underAnyPrefix(p, brewPrefixes) {
			return methodHomebrew
		}
	}
	if inCuratedDir(resolved, userBinDirs) && isRegularFile(resolved) {
		return methodStandalone
	}
	return methodUnknown
}

// symlinkTarget returns the cleaned, absolute target of path when path is a
// symlink (one level, which is enough for a Homebrew bin -> Cellar link), or ""
// otherwise. It uses Lstat/Readlink and so works on dangling symlinks too.
func symlinkTarget(path string) string {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target)
}

// underAnyPrefix reports whether p equals or is nested under any of prefixes.
func underAnyPrefix(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if p == pre || strings.HasPrefix(p, pre+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// inCuratedDir reports whether path sits directly inside one of dirs (exact
// parent-directory match). It never matches nested subdirectories, so deletion
// is confined to files placed directly in a curated bin dir.
func inCuratedDir(path string, dirs []string) bool {
	parent := filepath.Dir(path)
	for _, d := range dirs {
		if parent == d {
			return true
		}
	}
	return false
}

// isRegularFile reports whether path is a regular file (not a directory or
// symlink), guarding the deletion from ever targeting a directory.
func isRegularFile(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}
