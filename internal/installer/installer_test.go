package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeRunner records the command it was asked to run and returns a canned
// result, so tests never invoke real npm/brew.
type fakeRunner struct {
	name string
	args []string
	out  string
	err  error
	runs int
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.runs++
	f.name = name
	f.args = args
	return f.out, f.err
}

// okLook simulates npm being present; missLook simulates it being absent.
func okLook(string) (string, error)   { return "/usr/bin/npm", nil }
func missLook(string) (string, error) { return "", errors.New("not found") }

func TestArgsPerTool(t *testing.T) {
	tests := []struct {
		toolID      string
		wantInstall []string
		wantUninst  []string
	}{
		{"claude-code", []string{"npm", "install", "-g", "@anthropic-ai/claude-code"}, []string{"npm", "uninstall", "-g", "@anthropic-ai/claude-code"}},
		{"codex", []string{"npm", "install", "-g", "@openai/codex"}, []string{"npm", "uninstall", "-g", "@openai/codex"}},
		{"opencode", []string{"npm", "install", "-g", "opencode-ai"}, []string{"npm", "uninstall", "-g", "opencode-ai"}},
	}
	for _, tt := range tests {
		t.Run(tt.toolID, func(t *testing.T) {
			got, err := InstallArgs(tt.toolID)
			if err != nil {
				t.Fatalf("InstallArgs error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.wantInstall) {
				t.Fatalf("InstallArgs(%q) = %v, want %v", tt.toolID, got, tt.wantInstall)
			}
			gotU, err := UninstallArgs(tt.toolID)
			if err != nil {
				t.Fatalf("UninstallArgs error: %v", err)
			}
			if !reflect.DeepEqual(gotU, tt.wantUninst) {
				t.Fatalf("UninstallArgs(%q) = %v, want %v", tt.toolID, gotU, tt.wantUninst)
			}
		})
	}
}

func TestUnknownTool(t *testing.T) {
	if _, err := InstallArgs("nope"); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("InstallArgs(nope) err = %v, want ErrUnknownTool", err)
	}
	if _, err := UninstallArgs("nope"); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("UninstallArgs(nope) err = %v, want ErrUnknownTool", err)
	}
	inst := NewWithLookPath(&fakeRunner{}, okLook)
	if _, _, err := inst.Install(context.Background(), "nope"); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("Install(nope) err = %v, want ErrUnknownTool", err)
	}
}

func TestInstallRunsCommand(t *testing.T) {
	fr := &fakeRunner{out: "added 1 package"}
	inst := NewWithLookPath(fr, okLook)
	args, out, err := inst.Install(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if out != "added 1 package" {
		t.Fatalf("output = %q", out)
	}
	if fr.runs != 1 || fr.name != "npm" {
		t.Fatalf("runner not invoked correctly: runs=%d name=%q", fr.runs, fr.name)
	}
	wantArgs := []string{"install", "-g", "@openai/codex"}
	if !reflect.DeepEqual(fr.args, wantArgs) {
		t.Fatalf("runner args = %v, want %v", fr.args, wantArgs)
	}
	if !reflect.DeepEqual(args, []string{"npm", "install", "-g", "@openai/codex"}) {
		t.Fatalf("returned argv = %v", args)
	}
}

// resolveTo returns a resolve func that always reports path as the resolved
// binary, regardless of the requested name.
func resolveTo(path string) func(string) (string, bool) {
	return func(string) (string, bool) { return path, true }
}

// TestUninstallHomebrewSymlink: a binary that is a symlink into a Cellar is
// classified as Homebrew and removed via `brew uninstall <name>`; no file is
// deleted.
func TestUninstallHomebrewSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "opencode")
	// Target need not exist; Readlink resolves dangling symlinks.
	if err := os.Symlink("/opt/homebrew/Cellar/opencode/1.0/bin/opencode", link); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{out: "Uninstalling opencode"}
	var removed []string
	inst := NewWithResolver(fr, okLook, resolveTo(link), nil,
		func(p string) error { removed = append(removed, p); return nil })

	args, out, err := inst.Uninstall(context.Background(), "opencode")
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if !reflect.DeepEqual(args, []string{"brew", "uninstall", "opencode"}) {
		t.Fatalf("args = %v", args)
	}
	if fr.runs != 1 || fr.name != "brew" || !reflect.DeepEqual(fr.args, []string{"uninstall", "opencode"}) {
		t.Fatalf("runner invoked wrong: runs=%d name=%q args=%v", fr.runs, fr.name, fr.args)
	}
	if out == "" {
		t.Fatal("expected brew output")
	}
	if len(removed) != 0 {
		t.Fatalf("brew uninstall must not delete files: %v", removed)
	}
}

// TestUninstallNpmPrefix: a binary under an npm global prefix keeps the
// `npm uninstall -g <pkg>` behaviour; no file is deleted.
func TestUninstallNpmPrefix(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, ".npm-global", "bin", "codex")
	fr := &fakeRunner{out: "removed 1 package"}
	var removed []string
	inst := NewWithResolver(fr, okLook, resolveTo(p), nil,
		func(s string) error { removed = append(removed, s); return nil })

	args, _, err := inst.Uninstall(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if !reflect.DeepEqual(args, []string{"npm", "uninstall", "-g", "@openai/codex"}) {
		t.Fatalf("args = %v", args)
	}
	if fr.runs != 1 || fr.name != "npm" || !reflect.DeepEqual(fr.args, []string{"uninstall", "-g", "@openai/codex"}) {
		t.Fatalf("runner invoked wrong: runs=%d name=%q args=%v", fr.runs, fr.name, fr.args)
	}
	if len(removed) != 0 {
		t.Fatalf("npm uninstall must not delete files: %v", removed)
	}
}

// TestUninstallStandaloneDeletesFile: a curl/standalone binary inside a curated
// dir (~/.local/bin) is removed by deleting that exact file; no command runs.
func TestUninstallStandaloneDeletesFile(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	var removed []string
	inst := NewWithResolver(fr, okLook, resolveTo(bin), []string{binDir},
		func(p string) error { removed = append(removed, p); return nil })

	args, out, err := inst.Uninstall(context.Background(), "opencode")
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if !reflect.DeepEqual(args, []string{"rm", bin}) {
		t.Fatalf("args = %v", args)
	}
	if !reflect.DeepEqual(removed, []string{bin}) {
		t.Fatalf("removed = %v, want [%s]", removed, bin)
	}
	if fr.runs != 0 {
		t.Fatalf("standalone delete must not run a command: runs=%d", fr.runs)
	}
	if out == "" {
		t.Fatal("expected a delete message")
	}
}

// TestUninstallUnknownMethod: a binary resolving outside every known location is
// a safe no-op — no command, no deletion, a clear message, ErrUnknownMethod.
func TestUninstallUnknownMethod(t *testing.T) {
	fr := &fakeRunner{}
	var removed []string
	inst := NewWithResolver(fr, okLook, resolveTo("/usr/bin/codex"),
		[]string{"/home/u/.local/bin"},
		func(p string) error { removed = append(removed, p); return nil })

	args, out, err := inst.Uninstall(context.Background(), "codex")
	if !errors.Is(err, ErrUnknownMethod) {
		t.Fatalf("err = %v, want ErrUnknownMethod", err)
	}
	if args != nil {
		t.Fatalf("args = %v, want nil", args)
	}
	if out == "" {
		t.Fatal("expected a clear user-facing message")
	}
	if fr.runs != 0 || len(removed) != 0 {
		t.Fatalf("no-op violated: runs=%d removed=%v", fr.runs, removed)
	}
}

// TestUninstallUnresolvable: when the binary cannot be resolved at all, Uninstall
// is a safe no-op with a clear message and ErrUnknownMethod.
func TestUninstallUnresolvable(t *testing.T) {
	fr := &fakeRunner{}
	inst := NewWithResolver(fr, okLook, func(string) (string, bool) { return "", false }, nil, nil)
	args, out, err := inst.Uninstall(context.Background(), "opencode")
	if !errors.Is(err, ErrUnknownMethod) {
		t.Fatalf("err = %v, want ErrUnknownMethod", err)
	}
	if args != nil || out == "" {
		t.Fatalf("args = %v, out = %q", args, out)
	}
	if fr.runs != 0 {
		t.Fatalf("runner should not run: %d", fr.runs)
	}
}

// TestUninstallNeverDeletesOutsideCuratedDirs is the safety guarantee: even a
// real executable file is NEVER deleted when it lives outside the curated dirs.
func TestUninstallNeverDeletesOutsideCuratedDirs(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "somewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(outside, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A curated dir that exists but does NOT contain the resolved binary.
	curated := filepath.Join(root, ".local", "bin")
	fr := &fakeRunner{}
	var removed []string
	inst := NewWithResolver(fr, okLook, resolveTo(bin), []string{curated},
		func(p string) error { removed = append(removed, p); return nil })

	_, _, err := inst.Uninstall(context.Background(), "opencode")
	if !errors.Is(err, ErrUnknownMethod) {
		t.Fatalf("err = %v, want ErrUnknownMethod (safe no-op)", err)
	}
	if len(removed) != 0 {
		t.Fatalf("SAFETY VIOLATION: deleted outside curated dir: %v", removed)
	}
	if fr.runs != 0 {
		t.Fatalf("no command expected: runs=%d", fr.runs)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("file outside curated dirs must remain on disk: %v", err)
	}
}

// TestClassifyMethodWindowsPaths proves classification works for Windows-style
// backslash paths: the global npm shim dir (%APPDATA%\npm), a node_modules
// tree, and an unknown location. The paths need not exist — classification is
// purely lexical for the npm/brew signals.
func TestClassifyMethodWindowsPaths(t *testing.T) {
	tests := []struct {
		name     string
		resolved string
		want     uninstallMethod
	}{
		{
			"npm shim under AppData/Roaming/npm",
			`C:\Users\alice\AppData\Roaming\npm\codex.cmd`,
			methodNpm,
		},
		{
			"node_modules tree with backslashes",
			`C:\Users\alice\AppData\Roaming\npm\node_modules\@openai\codex\bin\codex.exe`,
			methodNpm,
		},
		{
			".npm-global with backslashes",
			`C:\Users\alice\.npm-global\bin\codex.cmd`,
			methodNpm,
		},
		{
			"unknown windows location",
			`C:\Program Files\Codex\codex.exe`,
			methodUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyMethod(tt.resolved, nil); got != tt.want {
				t.Fatalf("classifyMethod(%q) = %v, want %v", tt.resolved, got, tt.want)
			}
		})
	}
}

func TestNpmMissing(t *testing.T) {
	fr := &fakeRunner{}
	inst := NewWithLookPath(fr, missLook)
	args, _, err := inst.Install(context.Background(), "opencode")
	if !errors.Is(err, ErrNpmMissing) {
		t.Fatalf("Install err = %v, want ErrNpmMissing", err)
	}
	if fr.runs != 0 {
		t.Fatalf("runner should not run when npm missing: runs=%d", fr.runs)
	}
	// The intended command is still returned so the UI can show it.
	if len(args) == 0 {
		t.Fatal("expected intended argv even when npm missing")
	}
}

// TestPlanUninstallMethods verifies every preview classification and proves
// planning itself never runs a command or removes a file.
func TestPlanUninstallMethods(t *testing.T) {
	root := t.TempDir()
	brewLink := filepath.Join(root, "brew-opencode")
	if err := os.Symlink("/opt/homebrew/Cellar/opencode/1.0/bin/opencode", brewLink); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	standalone := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(standalone, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		toolID     string
		resolved   string
		found      bool
		look       func(string) (string, error)
		wantMethod string
		wantAction string
		wantArgs   []string
		wantTarget string
		wantCan    bool
		wantWarn   bool
		wantErr    error
	}{
		{
			name: "npm", toolID: "codex", resolved: filepath.Join(root, ".npm-global", "bin", "codex"), found: true,
			look: okLook, wantMethod: UninstallMethodNPM, wantAction: UninstallActionRunCommand,
			wantArgs: []string{"npm", "uninstall", "-g", "@openai/codex"}, wantTarget: "@openai/codex", wantCan: true,
		},
		{
			name: "homebrew", toolID: "opencode", resolved: brewLink, found: true,
			look: okLook, wantMethod: UninstallMethodHomebrew, wantAction: UninstallActionRunCommand,
			wantArgs: []string{"brew", "uninstall", "opencode"}, wantTarget: "opencode", wantCan: true,
		},
		{
			name: "standalone", toolID: "opencode", resolved: standalone, found: true,
			look: okLook, wantMethod: UninstallMethodStandalone, wantAction: UninstallActionDeleteFile,
			wantArgs: []string{"rm", standalone}, wantTarget: standalone, wantCan: true, wantWarn: true,
		},
		{
			name: "unknown location", toolID: "codex", resolved: "/usr/bin/codex", found: true,
			look: okLook, wantMethod: UninstallMethodUnknown, wantAction: UninstallActionManual,
			wantTarget: "/usr/bin/codex", wantWarn: true, wantErr: ErrUnknownMethod,
		},
		{
			name: "unresolvable", toolID: "opencode", found: false,
			look: okLook, wantMethod: UninstallMethodUnknown, wantAction: UninstallActionManual,
			wantTarget: "opencode", wantWarn: true, wantErr: ErrUnknownMethod,
		},
		{
			name: "npm missing", toolID: "codex", resolved: filepath.Join(root, ".npm-global", "bin", "codex"), found: true,
			look: missLook, wantMethod: UninstallMethodNPM, wantAction: UninstallActionRunCommand,
			wantArgs: []string{"npm", "uninstall", "-g", "@openai/codex"}, wantTarget: "@openai/codex", wantWarn: true, wantErr: ErrNpmMissing,
		},
		{
			name: "brew missing", toolID: "opencode", resolved: brewLink, found: true,
			look: missLook, wantMethod: UninstallMethodHomebrew, wantAction: UninstallActionRunCommand,
			wantArgs: []string{"brew", "uninstall", "opencode"}, wantTarget: "opencode", wantWarn: true, wantErr: ErrBrewMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &fakeRunner{}
			var removed []string
			resolve := func(string) (string, bool) { return tt.resolved, tt.found }
			inst := NewWithResolver(fr, tt.look, resolve, []string{binDir},
				func(p string) error { removed = append(removed, p); return nil })

			plan, err := inst.PlanUninstall(tt.toolID)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if plan.Method != tt.wantMethod || plan.Action != tt.wantAction || plan.Target != tt.wantTarget || plan.CanExecute != tt.wantCan {
				t.Fatalf("plan = %+v", plan)
			}
			if !reflect.DeepEqual(plan.Args, tt.wantArgs) {
				t.Fatalf("args = %v, want %v", plan.Args, tt.wantArgs)
			}
			if (plan.Warning != "") != tt.wantWarn {
				t.Fatalf("warning = %q, want non-empty=%v", plan.Warning, tt.wantWarn)
			}
			if fr.runs != 0 || len(removed) != 0 {
				t.Fatalf("planning must be read-only: runs=%d removed=%v", fr.runs, removed)
			}
		})
	}
}

func TestPlanUninstallUnknownTool(t *testing.T) {
	inst := NewWithResolver(&fakeRunner{}, okLook, resolveTo("/usr/bin/nope"), nil, nil)
	plan, err := inst.PlanUninstall("nope")
	if !errors.Is(err, ErrUnknownTool) || !reflect.DeepEqual(plan, UninstallPlan{}) {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

// TestUninstallReplansAtExecution proves a previously previewed path is never
// accepted or replayed: Uninstall resolves again and executes the new method.
func TestUninstallReplansAtExecution(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	previewTarget := filepath.Join(binDir, "codex")
	if err := os.WriteFile(previewTarget, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	executionTarget := filepath.Join(root, ".npm-global", "bin", "codex")
	resolved := []string{previewTarget, executionTarget}
	calls := 0
	resolve := func(string) (string, bool) {
		p := resolved[calls]
		calls++
		return p, true
	}
	fr := &fakeRunner{out: "removed"}
	var removed []string
	inst := NewWithResolver(fr, okLook, resolve, []string{binDir},
		func(p string) error { removed = append(removed, p); return nil })

	preview, err := inst.PlanUninstall("codex")
	if err != nil || preview.Method != UninstallMethodStandalone || preview.Target != previewTarget {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	args, _, err := inst.Uninstall(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if calls != 2 || !reflect.DeepEqual(args, []string{"npm", "uninstall", "-g", "@openai/codex"}) {
		t.Fatalf("calls=%d args=%v", calls, args)
	}
	if fr.runs != 1 || len(removed) != 0 {
		t.Fatalf("execution did not use fresh npm plan: runs=%d removed=%v", fr.runs, removed)
	}
	if _, err := os.Stat(previewTarget); err != nil {
		t.Fatalf("preview target must remain: %v", err)
	}
}
