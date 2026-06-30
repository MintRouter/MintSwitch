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
