package installer

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeRunner records the command it was asked to run and returns a canned
// result, so tests never invoke real npm.
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
		{"factory-droid", []string{"npm", "install", "-g", "droid"}, []string{"npm", "uninstall", "-g", "droid"}},
		{"pi", []string{"npm", "install", "-g", "--ignore-scripts", "@earendil-works/pi-coding-agent"}, []string{"npm", "uninstall", "-g", "@earendil-works/pi-coding-agent"}},
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

func TestPiGetsIgnoreScripts(t *testing.T) {
	args, err := InstallArgs("pi")
	if err != nil {
		t.Fatalf("InstallArgs(pi): %v", err)
	}
	found := false
	for _, a := range args {
		if a == "--ignore-scripts" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pi install args missing --ignore-scripts: %v", args)
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

func TestUninstallRunsCommand(t *testing.T) {
	fr := &fakeRunner{out: "removed 1 package"}
	inst := NewWithLookPath(fr, okLook)
	_, out, err := inst.Uninstall(context.Background(), "opencode")
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if out != "removed 1 package" {
		t.Fatalf("output = %q", out)
	}
	if !reflect.DeepEqual(fr.args, []string{"uninstall", "-g", "opencode-ai"}) {
		t.Fatalf("runner args = %v", fr.args)
	}
}

func TestNpmMissing(t *testing.T) {
	fr := &fakeRunner{}
	inst := NewWithLookPath(fr, missLook)
	args, _, err := inst.Install(context.Background(), "pi")
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
