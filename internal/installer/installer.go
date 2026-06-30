// Package installer installs and uninstalls the supported AI coding tools via
// npm's global package management. Each tool ID maps to a whitelisted npm
// package (and optional extra flags); user input is never interpolated into a
// command — only the fixed package names below are ever passed to npm.
//
// The npm process is run through a [CommandRunner] seam so tests can exercise
// command construction and error paths without invoking real npm.
package installer

import (
	"context"
	"errors"
	"os/exec"
)

// ErrNpmMissing is returned when the npm executable cannot be found on PATH.
var ErrNpmMissing = errors.New("installer: npm not found on PATH")

// ErrUnknownTool is returned when a toolID has no whitelisted npm package.
var ErrUnknownTool = errors.New("installer: unknown tool")

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
	"claude-code":   {NpmPackage: "@anthropic-ai/claude-code"},
	"codex":         {NpmPackage: "@openai/codex"},
	"opencode":      {NpmPackage: "opencode-ai"},
	"factory-droid": {NpmPackage: "droid"},
	"pi":            {NpmPackage: "@earendil-works/pi-coding-agent", ExtraFlags: []string{"--ignore-scripts"}},
}

// Spec returns the whitelisted package for toolID and whether it is known.
func Spec(toolID string) (Package, bool) {
	p, ok := packages[toolID]
	return p, ok
}

// InstallArgs returns the full argv for installing toolID, e.g.
// ["npm","install","-g","--ignore-scripts","@earendil-works/pi-coding-agent"].
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

// Installer builds and runs npm install/uninstall commands for a tool.
type Installer struct {
	runner   CommandRunner
	lookPath func(file string) (string, error)
}

// New returns an Installer that runs commands via runner and detects npm with
// the real exec.LookPath.
func New(runner CommandRunner) *Installer {
	return &Installer{runner: runner, lookPath: exec.LookPath}
}

// NewWithLookPath is like [New] but injects the npm-detection function. It is
// the seam used by tests to simulate npm being present or missing.
func NewWithLookPath(runner CommandRunner, lookPath func(string) (string, error)) *Installer {
	return &Installer{runner: runner, lookPath: lookPath}
}

// Install installs toolID globally via npm. It returns the exact argv that was
// (or would be) run so callers can show it to the user, the command's combined
// output, and any error. For an unknown tool it returns ErrUnknownTool with nil
// args; when npm is missing it returns the intended args plus ErrNpmMissing.
func (i *Installer) Install(ctx context.Context, toolID string) ([]string, string, error) {
	return i.run(ctx, toolID, InstallArgs)
}

// Uninstall uninstalls toolID globally via npm. Its return contract matches
// [Installer.Install].
func (i *Installer) Uninstall(ctx context.Context, toolID string) ([]string, string, error) {
	return i.run(ctx, toolID, UninstallArgs)
}

// run resolves argv via build, checks npm availability, then executes it.
func (i *Installer) run(ctx context.Context, toolID string, build func(string) ([]string, error)) ([]string, string, error) {
	args, err := build(toolID)
	if err != nil {
		return nil, "", err
	}
	if _, err := i.lookPath("npm"); err != nil {
		return args, "", ErrNpmMissing
	}
	out, runErr := i.runner.Run(ctx, args[0], args[1:]...)
	return args, out, runErr
}
