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

func TestResolverLocalAppDataDir(t *testing.T) {
	home := t.TempDir()
	local := t.TempDir()
	r := &Resolver{Home: home}
	if got, want := r.LocalAppDataDir(), filepath.Join(home, "AppData", "Local"); got != want {
		t.Errorf("LocalAppDataDir fallback = %q, want %q", got, want)
	}
	r.LocalAppData = local
	if got := r.LocalAppDataDir(); got != local {
		t.Errorf("LocalAppDataDir = %q, want %q", got, local)
	}
	if got, want := r.PackagesDir(), filepath.Join(local, "Packages"); got != want {
		t.Errorf("PackagesDir = %q, want %q", got, want)
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
	if filepath.Base(r.DataDir) != dataDirName {
		t.Errorf("DataDir base = %q, want %q", filepath.Base(r.DataDir), dataDirName)
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
}

// TestCodexAndClaudeDirDefaults proves the defaults (~/.codex, ~/.claude)
// apply when no env override is set.
func TestCodexAndClaudeDirDefaults(t *testing.T) {
	home := t.TempDir()
	r := &Resolver{Home: home}
	if got, want := r.CodexDir(), filepath.Join(home, ".codex"); got != want {
		t.Errorf("CodexDir = %q, want %q", got, want)
	}
	if got, want := r.ClaudeDir(), filepath.Join(home, ".claude"); got != want {
		t.Errorf("ClaudeDir = %q, want %q", got, want)
	}
}

// TestBinaryResolvable covers the no-subprocess binary lookup: a lookPath hit,
// an executable file in a curated HOME-derived dir (the narrow-PATH GUI case),
// and the negative cases (absent, and a non-executable file).
func TestBinaryResolvable(t *testing.T) {
	home := t.TempDir()
	r := &Resolver{Home: home}
	miss := func(string) (string, error) { return "", errors.New("not found") }

	if r.BinaryResolvable(miss, "sometool") {
		t.Fatal("expected not resolvable with empty home and missing PATH")
	}

	hit := func(string) (string, error) { return "/usr/local/bin/sometool", nil }
	if !r.BinaryResolvable(hit, "sometool") {
		t.Fatal("expected resolvable via lookPath hit")
	}

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sometool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !r.BinaryResolvable(miss, "sometool") {
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
		{"bat shim", "opencode.bat", "opencode"},
		{"bare name without exec bit", "sometool", "sometool"},
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

// TestResolveBinaryWindowsCodexStandaloneDir pins the Codex standalone CLI
// installer's bin dir (%LOCALAPPDATA%\Programs\OpenAI\Codex\bin) as a curated
// search dir on Windows only: the installer does not add it to PATH, so a
// codex.exe there must still resolve. Unix must ignore it.
func TestResolveBinaryWindowsCodexStandaloneDir(t *testing.T) {
	home := t.TempDir()
	local := t.TempDir()
	r := &Resolver{Home: home, LocalAppData: local}
	miss := func(string) (string, error) { return "", errors.New("not found") }

	codexBin := filepath.Join(local, "Programs", "OpenAI", "Codex", "bin")
	if err := os.MkdirAll(codexBin, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(codexBin, "codex.exe")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := r.resolveBinary(miss, "codex", "windows")
	if !ok || got != p {
		t.Fatalf("resolveBinary(codex, windows) = %q, %v; want %q, true", got, ok, p)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if _, ok := r.resolveBinary(miss, "codex", goos); ok {
			t.Fatalf("expected Codex standalone dir to not be searched on %s", goos)
		}
	}
	// Search-only: the standalone-deletion bound (UserBinDirs) must not
	// include the Codex installer dir.
	for _, dir := range r.UserBinDirs() {
		if dir == codexBin {
			t.Fatal("UserBinDirs must not include the Codex standalone dir")
		}
	}
}

// TestResolveBinaryWindowsNodeDirs pins npm's default global prefix
// (%APPDATA%\npm) and the Node.js install dir (%ProgramFiles%\nodejs) as
// search-only dirs on Windows, so a GUI app with a stale PATH still finds npm
// and npm-global CLIs. Unix must ignore them and UserBinDirs (the deletion
// bound) must never include them.
func TestResolveBinaryWindowsNodeDirs(t *testing.T) {
	home := t.TempDir()
	appData := t.TempDir()
	programFiles := t.TempDir()
	r := &Resolver{Home: home, NativeConfigDir: appData, ProgramFiles: programFiles}
	miss := func(string) (string, error) { return "", errors.New("not found") }

	npmPrefix := filepath.Join(appData, "npm")
	nodeDir := filepath.Join(programFiles, "nodejs")
	for _, d := range []string{npmPrefix, nodeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	codexShim := filepath.Join(npmPrefix, "codex.cmd")
	npmShim := filepath.Join(nodeDir, "npm.cmd")
	for _, p := range []string{codexShim, npmShim} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for bin, want := range map[string]string{"codex": codexShim, "npm": npmShim} {
		got, ok := r.resolveBinary(miss, bin, "windows")
		if !ok || got != want {
			t.Fatalf("resolveBinary(%s, windows) = %q, %v; want %q, true", bin, got, ok, want)
		}
		for _, goos := range []string{"darwin", "linux"} {
			if _, ok := r.resolveBinary(miss, bin, goos); ok {
				t.Fatalf("expected Windows Node dirs to not be searched on %s", goos)
			}
		}
	}
	for _, dir := range r.UserBinDirs() {
		if dir == npmPrefix || dir == nodeDir {
			t.Fatalf("UserBinDirs must not include search-only dir %q", dir)
		}
	}
}

// TestWindowsNodeDirFallbacks proves the %APPDATA% and %ProgramFiles%
// fallbacks derive from Home and the Windows default respectively, and that
// Windows gets no Unix system bin dirs by default.
func TestWindowsNodeDirFallbacks(t *testing.T) {
	home := t.TempDir()
	r := &Resolver{Home: home}
	if got, want := r.RoamingAppDataDir(), filepath.Join(home, "AppData", "Roaming"); got != want {
		t.Errorf("RoamingAppDataDir fallback = %q, want %q", got, want)
	}
	if got, want := r.ProgramFilesDir(), `C:\Program Files`; got != want {
		t.Errorf("ProgramFilesDir fallback = %q, want %q", got, want)
	}
	r.NativeConfigDir, r.ProgramFiles = "/appdata", "/pf"
	if got := r.RoamingAppDataDir(); got != "/appdata" {
		t.Errorf("RoamingAppDataDir = %q, want /appdata", got)
	}
	if got := r.ProgramFilesDir(); got != "/pf" {
		t.Errorf("ProgramFilesDir = %q, want /pf", got)
	}
	if dirs := systemBinDirs("windows"); len(dirs) != 0 {
		t.Errorf("systemBinDirs(windows) = %v, want none", dirs)
	}
	if dirs := systemBinDirs("darwin"); len(dirs) == 0 {
		t.Error("systemBinDirs(darwin) empty, want Homebrew/local prefixes")
	}
}
