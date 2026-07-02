package paths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolverJoinAndData(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	r := &Resolver{Home: home, DataDir: data}

	if got, want := r.Join(".claude", "settings.json"), filepath.Join(home, ".claude", "settings.json"); got != want {
		t.Errorf("Join = %q, want %q", got, want)
	}
	if got, want := r.DataJoin("backups"), filepath.Join(data, "backups"); got != want {
		t.Errorf("DataJoin = %q, want %q", got, want)
	}
	if got, want := r.BackupsDir(), filepath.Join(data, "backups"); got != want {
		t.Errorf("BackupsDir = %q, want %q", got, want)
	}
	if got, want := r.SettingsPath(), filepath.Join(data, "settings.json"); got != want {
		t.Errorf("SettingsPath = %q, want %q", got, want)
	}
}

func TestResolverConfigDir(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	tests := []struct {
		name       string
		configHome string
		want       string
	}{
		{"defaults to home/.config", "", filepath.Join(home, ".config")},
		{"honors XDG override", xdg, xdg},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Resolver{Home: home, ConfigHome: tt.configHome}
			if got := r.ConfigDir(); got != tt.want {
				t.Fatalf("ConfigDir = %q, want %q", got, tt.want)
			}
			if got, want := r.ConfigJoin("opencode", "opencode.json"), filepath.Join(tt.want, "opencode", "opencode.json"); got != want {
				t.Fatalf("ConfigJoin = %q, want %q", got, want)
			}
		})
	}
}

func TestNewResolverUsesXDGEnv(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	r, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if r.Home == "" {
		t.Error("Home empty")
	}
	if r.ConfigHome != xdg {
		t.Errorf("ConfigHome = %q, want %q", r.ConfigHome, xdg)
	}
	if filepath.Base(r.DataDir) != "mintswitch" {
		t.Errorf("DataDir base = %q, want mintswitch", filepath.Base(r.DataDir))
	}
	if r.NativeConfigDir == "" {
		t.Error("NativeConfigDir empty; want os.UserConfigDir value")
	}
}

// TestNewResolverToolEnvOverrides proves the documented per-tool env overrides
// (CODEX_HOME, CLAUDE_CONFIG_DIR) are picked up from the environment.
func TestNewResolverToolEnvOverrides(t *testing.T) {
	codexHome := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	r, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if got := r.CodexDir(); got != codexHome {
		t.Errorf("CodexDir = %q, want %q", got, codexHome)
	}
	if got := r.ClaudeDir(); got != claudeDir {
		t.Errorf("ClaudeDir = %q, want %q", got, claudeDir)
	}
	if got, want := r.ClaudeJSONPath(), filepath.Join(claudeDir, ".claude.json"); got != want {
		t.Errorf("ClaudeJSONPath = %q, want %q", got, want)
	}
}

// TestCodexAndClaudeDirDefaults proves the defaults (~/.codex, ~/.claude,
// ~/.claude.json) apply when no env override is set.
func TestCodexAndClaudeDirDefaults(t *testing.T) {
	home := t.TempDir()
	r := &Resolver{Home: home}
	if got, want := r.CodexDir(), filepath.Join(home, ".codex"); got != want {
		t.Errorf("CodexDir = %q, want %q", got, want)
	}
	if got, want := r.ClaudeDir(), filepath.Join(home, ".claude"); got != want {
		t.Errorf("ClaudeDir = %q, want %q", got, want)
	}
	if got, want := r.ClaudeJSONPath(), filepath.Join(home, ".claude.json"); got != want {
		t.Errorf("ClaudeJSONPath = %q, want %q", got, want)
	}
}

// TestZedConfigDirPerOS covers all three GOOS branches of zedConfigDir:
// Windows uses the native config dir (%APPDATA%\Zed), macOS and Linux use the
// XDG-aware ~/.config/zed, and Windows without a NativeConfigDir (test
// resolvers) falls back to the XDG-style dir.
func TestZedConfigDirPerOS(t *testing.T) {
	home := t.TempDir()
	native := t.TempDir()
	tests := []struct {
		name   string
		goos   string
		native string
		want   string
	}{
		{"windows uses APPDATA/Zed", "windows", native, filepath.Join(native, "Zed")},
		{"darwin uses ~/.config/zed", "darwin", native, filepath.Join(home, ".config", "zed")},
		{"linux uses ~/.config/zed", "linux", native, filepath.Join(home, ".config", "zed")},
		{"windows without native falls back", "windows", "", filepath.Join(home, ".config", "zed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Resolver{Home: home, NativeConfigDir: tt.native}
			if got := r.zedConfigDir(tt.goos); got != tt.want {
				t.Fatalf("zedConfigDir(%q) = %q, want %q", tt.goos, got, tt.want)
			}
		})
	}
}

// TestBinaryResolvable covers the no-subprocess binary lookup: a lookPath hit,
// an executable file in a curated HOME-derived dir (the narrow-PATH GUI case),
// and the negative cases (absent, and a non-executable file).
func TestBinaryResolvable(t *testing.T) {
	home := t.TempDir()
	r := &Resolver{Home: home}
	miss := func(string) (string, error) { return "", errors.New("not found") }

	if r.BinaryResolvable(miss, "droid") {
		t.Fatal("expected not resolvable with empty home and missing PATH")
	}

	hit := func(string) (string, error) { return "/usr/local/bin/droid", nil }
	if !r.BinaryResolvable(hit, "droid") {
		t.Fatal("expected resolvable via lookPath hit")
	}

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "droid"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !r.BinaryResolvable(miss, "droid") {
		t.Fatal("expected resolvable via curated ~/.local/bin even with missing PATH")
	}

	if err := os.WriteFile(filepath.Join(binDir, "plain"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r.BinaryResolvable(miss, "plain") {
		t.Fatal("expected a non-executable file to not count as resolvable")
	}
}

// TestResolveBinaryWindows covers the Windows branch of the curated-dir
// lookup: no exec bit is required (Go never reports one on Windows) and the
// PATHEXT-style candidates (.exe, .cmd, .bat) are probed.
func TestResolveBinaryWindows(t *testing.T) {
	home := t.TempDir()
	r := &Resolver{Home: home}
	miss := func(string) (string, error) { return "", errors.New("not found") }
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		file string
		bin  string
	}{
		{"exe suffix", "codex.exe", "codex"},
		{"cmd shim", "claude.cmd", "claude"},
		{"bat shim", "kilo.bat", "kilo"},
		{"bare name without exec bit", "droid", "droid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(binDir, tt.file)
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok := r.resolveBinary(miss, tt.bin, "windows")
			if !ok || got != p {
				t.Fatalf("resolveBinary(%q, windows) = %q, %v; want %q, true", tt.bin, got, ok, p)
			}
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		})
	}

	if _, ok := r.resolveBinary(miss, "absent", "windows"); ok {
		t.Fatal("expected absent binary to not resolve on windows")
	}
	// The Unix branch must not pick up extension-suffixed files nor files
	// without an exec bit.
	if err := os.WriteFile(filepath.Join(binDir, "codex.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.resolveBinary(miss, "codex", "linux"); ok {
		t.Fatal("expected codex.exe to not resolve as codex on linux")
	}
}
