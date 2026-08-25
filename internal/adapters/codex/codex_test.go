package codex

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	a := New(r, backup.NewEngine(r.BackupsDir()), markers.NewStore(r.MarkersPath()))
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	// Pin every desktop-app probe to temp-dir locations so a real ChatGPT
	// install on the host machine never leaks into the tests.
	a.goos = "darwin"
	a.macAppBundles = []string{filepath.Join(home, "Applications", "ChatGPT.app")}
	a.linuxLibDir = filepath.Join(home, "usr", "lib", "chatgpt")
	return a, home
}

// TestDetectViaPATHBinary proves a fresh "npm install -g" is detected via the
// "codex" binary on PATH even before ~/.codex exists.
func TestDetectViaPATHBinary(t *testing.T) {
	a, _ := newAdapter(t)
	if ok, _ := a.Detect(); ok {
		t.Fatal("expected not detected with empty home and no binary")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	ok, active := a.Detect()
	if !ok {
		t.Fatal("expected detected via codex binary on PATH")
	}
	if !strings.HasSuffix(active, filepath.Join(".codex", "config.toml")) {
		t.Fatalf("unexpected active path %q", active)
	}
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:   "work",
		APIKey:  "sk-test-123",
		BaseURL: "https://proxy.example.com/v1",
		Model:   "gpt-5.5",
	}
}

// TestConfigPathsHonorCodexHome proves the adapter follows the documented
// CODEX_HOME override (wired into the resolver by paths.NewResolver).
func TestConfigPathsHonorCodexHome(t *testing.T) {
	a, _ := newAdapter(t)
	override := t.TempDir()
	a.r.CodexHome = override
	want := []string{
		filepath.Join(override, "config.toml"),
		filepath.Join(override, "auth.json"),
	}
	got := a.ConfigPaths()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ConfigPaths = %v, want %v", got, want)
	}
}

// TestDetect proves the binary-based contract: a leftover ~/.codex dir is NOT
// an installed signal; only a resolvable "codex" binary is.
func TestDetect(t *testing.T) {
	a, home := newAdapter(t)
	if ok, _ := a.Detect(); ok {
		t.Fatal("expected not detected before codex binary is resolvable")
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Detect(); ok {
		t.Fatal("~/.codex present + binary absent must be NOT detected")
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	ok, active := a.Detect()
	if !ok {
		t.Fatal("expected detected once codex binary is resolvable")
	}
	if !strings.HasSuffix(active, filepath.Join(".codex", "config.toml")) {
		t.Fatalf("unexpected active path %q", active)
	}
}

// installBundle writes a fake macOS .app bundle with the given Info.plist
// bytes at appDir.
func installBundle(t *testing.T, appDir string, plist []byte) {
	t.Helper()
	contents := filepath.Join(appDir, "Contents")
	if err := os.MkdirAll(contents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), plist, 0o644); err != nil {
		t.Fatal(err)
	}
}

// xmlPlist returns a minimal XML Info.plist carrying the given bundle ID.
func xmlPlist(bundleID string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
	<key>CFBundleIdentifier</key><string>` + bundleID + `</string>
</dict></plist>
`)
}

// TestDetectDesktopMacOS proves the macOS desktop probe keys off the
// com.openai.codex bundle ID in Contents/Info.plist, never the app name: the
// unified ChatGPT.app and the legacy Codex.app count (XML or binary plist),
// while the old chat-only ChatGPT.app (bundle ID com.openai.chat, on machines
// that never migrated) and a bundle without a readable plist do not.
func TestDetectDesktopMacOS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		app    string
		plist  []byte
		wantOK bool
	}{
		{"unified ChatGPT.app xml plist", "ChatGPT.app", xmlPlist("com.openai.codex"), true},
		{"legacy Codex.app", "Codex.app", xmlPlist("com.openai.codex"), true},
		{"binary-style plist", "ChatGPT.app", append([]byte("bplist00\x00\x14\x01"), []byte("com.openai.codex\x00\x08")...), true},
		{"old chat-only ChatGPT.app", "ChatGPT.app", xmlPlist("com.openai.chat"), false},
		{"bundle without Info.plist", "ChatGPT.app", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, home := newAdapter(t)
			appDir := filepath.Join(home, "Applications", tc.app)
			a.macAppBundles = []string{appDir}
			if tc.plist == nil {
				if err := os.MkdirAll(filepath.Join(appDir, "Contents"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				installBundle(t, appDir, tc.plist)
			}
			ok, active := a.Detect()
			if ok != tc.wantOK {
				t.Fatalf("Detect() = %v, want %v", ok, tc.wantOK)
			}
			if !strings.HasSuffix(active, filepath.Join(".codex", "config.toml")) {
				t.Fatalf("unexpected active path %q", active)
			}
		})
	}
}

// TestDetectDesktopWindows proves the Windows probes are the user-accessible
// dirs only (the MSIX binary sits in the protected WindowsApps dir): the
// OpenAI.Codex_* package data dir under %LOCALAPPDATA%\Packages — exact
// family or a re-signed suffix, dirs only — and the app's runtime dir
// %LOCALAPPDATA%\OpenAI\Codex.
func TestDetectDesktopWindows(t *testing.T) {
	newWindowsAdapter := func(t *testing.T) *Adapter {
		a, home := newAdapter(t)
		a.goos = "windows"
		a.r.LocalAppData = filepath.Join(home, "AppData", "Local")
		return a
	}
	t.Run("msix package data dir", func(t *testing.T) {
		a := newWindowsAdapter(t)
		pkgs := filepath.Join(a.r.LocalAppData, "Packages")
		// Unrelated entries and plain files must never match.
		if err := os.MkdirAll(filepath.Join(pkgs, "OpenAI.ChatGPT_xyz"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkgs, "OpenAI.Codex_notadir"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if ok, _ := a.Detect(); ok {
			t.Fatal("unrelated Packages entries must not detect")
		}
		if err := os.MkdirAll(filepath.Join(pkgs, "OpenAI.Codex_2p2nqsd0c76g0"), 0o755); err != nil {
			t.Fatal(err)
		}
		if ok, _ := a.Detect(); !ok {
			t.Fatal("expected detected via OpenAI.Codex_* package data dir")
		}
	})
	t.Run("runtime dir", func(t *testing.T) {
		a := newWindowsAdapter(t)
		if ok, _ := a.Detect(); ok {
			t.Fatal("expected not detected without any probe dir")
		}
		if err := os.MkdirAll(filepath.Join(a.r.LocalAppData, "OpenAI", "Codex"), 0o755); err != nil {
			t.Fatal(err)
		}
		if ok, _ := a.Detect(); !ok {
			t.Fatal("expected detected via %LOCALAPPDATA%\\OpenAI\\Codex runtime dir")
		}
	})
}

// TestDetectDesktopLinux proves the Linux probes for the official .deb/.rpm
// app: the "chatgpt" launcher binary being resolvable, or the /usr/lib/chatgpt
// payload dir (via the test seam) existing.
func TestDetectDesktopLinux(t *testing.T) {
	t.Run("chatgpt binary", func(t *testing.T) {
		a, _ := newAdapter(t)
		a.goos = "linux"
		if ok, _ := a.Detect(); ok {
			t.Fatal("expected not detected without binary or lib dir")
		}
		a.lookPath = func(name string) (string, error) {
			if name == "chatgpt" {
				return "/usr/bin/chatgpt", nil
			}
			return "", errors.New("not found")
		}
		if ok, _ := a.Detect(); !ok {
			t.Fatal("expected detected via chatgpt binary")
		}
	})
	t.Run("lib dir", func(t *testing.T) {
		a, _ := newAdapter(t)
		a.goos = "linux"
		if err := os.MkdirAll(a.linuxLibDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if ok, _ := a.Detect(); !ok {
			t.Fatal("expected detected via /usr/lib/chatgpt payload dir")
		}
		// The macOS-shaped and Windows probes must not fire on linux even when
		// their dirs exist.
		a.goos = "darwin"
		if ok, _ := a.Detect(); ok {
			t.Fatal("linux lib dir must not detect on darwin")
		}
	})
}

// TestNameBySurface proves the display name's parenthetical (the UI card
// subtitle) reflects the installed surface: CLI-only and nothing-installed
// keep the generic CLI + IDE form, desktop-only says Desktop app, and both
// surfaces are named together.
func TestNameBySurface(t *testing.T) {
	a, home := newAdapter(t)
	if got, want := a.Name(), "ChatGPT (Codex CLI + IDE)"; got != want {
		t.Fatalf("nothing installed: Name() = %q, want %q", got, want)
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	if got, want := a.Name(), "ChatGPT (Codex CLI + IDE)"; got != want {
		t.Fatalf("CLI only: Name() = %q, want %q", got, want)
	}
	installBundle(t, filepath.Join(home, "Applications", "ChatGPT.app"), xmlPlist("com.openai.codex"))
	if got, want := a.Name(), "ChatGPT (Codex CLI + Desktop app)"; got != want {
		t.Fatalf("both surfaces: Name() = %q, want %q", got, want)
	}
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if got, want := a.Name(), "ChatGPT (Desktop app)"; got != want {
		t.Fatalf("desktop only: Name() = %q, want %q", got, want)
	}
}

// TestDesktopOnlyStatusApplyRestore proves a desktop-only install (no codex
// binary anywhere) is a fully manageable card: installed, Status reaches the
// config-reading branch, and Apply/Restore work on ~/.codex as usual.
func TestDesktopOnlyStatusApplyRestore(t *testing.T) {
	a, home := newAdapter(t)
	installBundle(t, filepath.Join(home, "Applications", "ChatGPT.app"), xmlPlist("com.openai.codex"))
	p := sampleProfile()
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default, got %v", st)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want Applied, got %v", st)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default after restore, got %v", st)
	}
}

func TestStatusTransitions(t *testing.T) {
	a, home := newAdapter(t)
	p := sampleProfile()

	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("want NotInstalled, got %v", st)
	}

	// Binary resolvable from here so Status reaches the config-reading branch.
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default, got %v", st)
	}

	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want Applied, got %v", st)
	}

	other := p
	other.Model = "gpt-4o"
	if st, _, _ := a.Status(other); st != core.StatusModifiedExternally {
		t.Fatalf("want ModifiedExternally, got %v", st)
	}
}

// TestStatusAuthDrift pins the post-merger regression: the unified ChatGPT
// app shares ~/.codex, so a ChatGPT sign-in rewrites auth.json (auth_mode
// back to "chatgpt") while config.toml still carries the MintSwitch settings.
// Codex then ignores OPENAI_API_KEY and bypasses the proxy, so Status must
// report ModifiedExternally (authDriftDetail) instead of a false Applied —
// and a re-Apply must recover to Applied.
func TestStatusAuthDrift(t *testing.T) {
	drifts := map[string]func(auth map[string]any){
		"chatgpt sign-in flips auth_mode": func(auth map[string]any) {
			auth[authModeKey] = "chatgpt"
			auth["tokens"] = map[string]any{"id_token": "oauth-tok"}
		},
		"api key replaced": func(auth map[string]any) {
			auth[authKeyName] = "sk-someone-else"
		},
		"api key removed": func(auth map[string]any) {
			delete(auth, authKeyName)
		},
	}
	for name, mutate := range drifts {
		t.Run(name, func(t *testing.T) {
			a, _ := newAdapter(t)
			a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
			p := sampleProfile()
			if _, err := a.Apply(p); err != nil {
				t.Fatalf("apply: %v", err)
			}

			auth, err := core.ReadJSONObject(a.authPath())
			if err != nil {
				t.Fatal(err)
			}
			mutate(auth)
			if err := core.WriteJSONObjectAtomic(a.authPath(), auth); err != nil {
				t.Fatal(err)
			}

			st, detail, err := a.Status(p)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if st != core.StatusModifiedExternally || detail != authDriftDetail {
				t.Fatalf("drifted status = %v %q, want ModifiedExternally + authDriftDetail", st, detail)
			}

			// Re-applying the profile rewrites auth.json and recovers Applied.
			if _, err := a.Apply(p); err != nil {
				t.Fatalf("re-apply: %v", err)
			}
			if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
				t.Fatalf("status after re-apply = %v, want Applied", st)
			}
		})
	}
}

// TestStatusAuthDriftCorruptAuth proves an unreadable auth.json under a
// managed config never reports a false Applied: Codex cannot be using the
// MintSwitch key, so Status reports ModifiedExternally.
func TestStatusAuthDriftCorruptAuth(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.WriteFile(a.authPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, detail, err := a.Status(p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != core.StatusModifiedExternally || detail != authDriftDetail {
		t.Fatalf("corrupt-auth status = %v %q, want ModifiedExternally + authDriftDetail", st, detail)
	}
}

func TestApplyNewFiles(t *testing.T) {
	a, home := newAdapter(t)
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}

	cfg, err := readTOML(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg["openai_base_url"] != p.BaseURL || cfg["model"] != p.Model {
		t.Fatalf("config not written: %+v", cfg)
	}
	if _, present := cfg[core.MarkerKey]; present {
		t.Fatalf("legacy marker written to config.toml: %+v", cfg)
	}
	marker, ok, err := a.m.Get(a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || marker.Fingerprint != core.Fingerprint(p) {
		t.Fatalf("store marker fingerprint mismatch: %+v ok=%v", marker, ok)
	}

	auth, err := core.ReadJSONObject(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if auth[authKeyName] != p.APIKey {
		t.Fatalf("auth key not written: %+v", auth)
	}
	if auth[authModeKey] != authModeAPIKey {
		t.Fatalf("auth_mode not set to apikey: %+v", auth)
	}
}

// TestApplyAllModelsWritesCatalog proves "All models" mode writes the
// mintswitch-models.json catalog (one entry per profile model, each carrying
// base_instructions and its advertised context window, falling back to
// defaultContextWindow) and points config.toml's model_catalog_json at it; a
// re-apply in single-model mode removes both again; and a mode switch is
// detected via the fingerprint until re-apply.
func TestApplyAllModelsWritesCatalog(t *testing.T) {
	a, home := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	p := sampleProfile()
	p.Models = []string{"gpt-5.5", "gpt-5.5-mini"}
	p.ModelContextWindows = map[string]int{"gpt-5.5": 1_048_576}
	p.ApplyAllModels = true
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}

	catalogPath := filepath.Join(home, ".codex", catalogFileName)
	cfg, err := readTOML(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg[catalogKey] != catalogPath {
		t.Fatalf("%s = %v, want %q", catalogKey, cfg[catalogKey], catalogPath)
	}
	catalog, err := core.ReadJSONObject(catalogPath)
	if err != nil {
		t.Fatalf("catalog not written: %v", err)
	}
	entries, _ := catalog["models"].([]any)
	if len(entries) != 2 {
		t.Fatalf("catalog models = %v, want 2 entries", entries)
	}
	wantWindows := []float64{1_048_576, defaultContextWindow}
	for i, id := range []string{"gpt-5.5", "gpt-5.5-mini"} {
		entry := entries[i].(map[string]any)
		if entry["slug"] != id {
			t.Fatalf("entry %d slug = %v, want %q", i, entry["slug"], id)
		}
		if instr, _ := entry["base_instructions"].(string); instr == "" {
			t.Fatalf("entry %d missing base_instructions", i)
		}
		if w, _ := entry["context_window"].(float64); w != wantWindows[i] {
			t.Fatalf("entry %d context_window = %v, want %v", i, entry["context_window"], wantWindows[i])
		}
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want Applied in all mode, got %v", st)
	}

	one := p
	one.ApplyAllModels = false
	if st, _, _ := a.Status(one); st != core.StatusModifiedExternally {
		t.Fatalf("want ModifiedExternally after mode switch, got %v", st)
	}
	if _, err := a.Apply(one); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(catalogPath); !os.IsNotExist(err) {
		t.Fatalf("catalog file not removed on single-model re-apply: %v", err)
	}
	cfg, err = readTOML(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cfg[catalogKey]; present {
		t.Fatalf("%s not removed on single-model re-apply: %+v", catalogKey, cfg)
	}
}

// TestApplyKeepsUserCatalogRef proves a hand-configured model_catalog_json
// pointing at the user's own file is never touched by a single-model Apply.
func TestApplyKeepsUserCatalogRef(t *testing.T) {
	a, home := newAdapter(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userRef := "/home/user/my-catalog.json"
	if err := writeTOML(filepath.Join(codexDir, "config.toml"),
		map[string]any{catalogKey: userRef}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	cfg, err := readTOML(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg[catalogKey] != userRef {
		t.Fatalf("user %s changed: %v", catalogKey, cfg[catalogKey])
	}
}

// TestApplyReviewModel pins the review-tier contract: a profile pinning
// ReviewModel writes the top-level review_model key and — even in
// single-model mode — the model catalog, whose appended entry carries the
// review model's advertised context window (falling back to
// defaultContextWindow for the session model); a re-apply without the pin is
// first detected via the fingerprint, then removes the key, the catalog
// reference and the catalog file again.
func TestApplyReviewModel(t *testing.T) {
	a, home := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	p := sampleProfile()
	p.ReviewModel = "gpt-5.5-review"
	p.ModelContextWindows = map[string]int{"gpt-5.5-review": 400_000}
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(home, ".codex", "config.toml")
	catalogPath := filepath.Join(home, ".codex", catalogFileName)
	cfg, err := readTOML(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg[reviewModelKey] != "gpt-5.5-review" {
		t.Fatalf("%s = %v, want gpt-5.5-review", reviewModelKey, cfg[reviewModelKey])
	}
	if cfg[catalogKey] != catalogPath {
		t.Fatalf("%s = %v, want %q", catalogKey, cfg[catalogKey], catalogPath)
	}
	catalog, err := core.ReadJSONObject(catalogPath)
	if err != nil {
		t.Fatalf("catalog not written: %v", err)
	}
	entries, _ := catalog["models"].([]any)
	if len(entries) != 2 {
		t.Fatalf("catalog models = %v, want 2 entries", entries)
	}
	wantWindows := []float64{defaultContextWindow, 400_000}
	for i, id := range []string{"gpt-5.5", "gpt-5.5-review"} {
		entry := entries[i].(map[string]any)
		if entry["slug"] != id {
			t.Fatalf("entry %d slug = %v, want %q", i, entry["slug"], id)
		}
		if w, _ := entry["context_window"].(float64); w != wantWindows[i] {
			t.Fatalf("entry %d context_window = %v, want %v", i, entry["context_window"], wantWindows[i])
		}
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want Applied with review model, got %v", st)
	}

	unpinned := sampleProfile()
	if st, _, _ := a.Status(unpinned); st != core.StatusModifiedExternally {
		t.Fatalf("want ModifiedExternally after unpinning review model, got %v", st)
	}
	if _, err := a.Apply(unpinned); err != nil {
		t.Fatal(err)
	}
	cfg, err = readTOML(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cfg[reviewModelKey]; present {
		t.Fatalf("%s not removed on unpinned re-apply: %+v", reviewModelKey, cfg)
	}
	if _, present := cfg[catalogKey]; present {
		t.Fatalf("%s not removed on unpinned re-apply: %+v", catalogKey, cfg)
	}
	if _, err := os.Stat(catalogPath); !os.IsNotExist(err) {
		t.Fatalf("catalog file not removed on unpinned re-apply: %v", err)
	}
	if st, _, _ := a.Status(unpinned); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want Applied after unpinned re-apply, got %v", st)
	}
}

// TestCatalogDedupsReviewModel proves a ReviewModel already among the applied
// models gets no duplicate catalog entry.
func TestCatalogDedupsReviewModel(t *testing.T) {
	a, home := newAdapter(t)
	p := sampleProfile()
	p.Models = []string{"gpt-5.5", "gpt-5.5-mini"}
	p.ApplyAllModels = true
	p.ReviewModel = "gpt-5.5-mini"
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	catalog, err := core.ReadJSONObject(filepath.Join(home, ".codex", catalogFileName))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := catalog["models"].([]any)
	if len(entries) != 2 {
		t.Fatalf("catalog models = %v, want 2 deduped entries", entries)
	}
}

// TestApplyKeepsUserReviewModel proves a user's own review_model is never
// deleted by a first Apply over an unmanaged config when the profile pins no
// review model.
func TestApplyKeepsUserReviewModel(t *testing.T) {
	a, home := newAdapter(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTOML(filepath.Join(codexDir, "config.toml"),
		map[string]any{reviewModelKey: "user-review"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	cfg, err := readTOML(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg[reviewModelKey] != "user-review" {
		t.Fatalf("user %s changed: %v", reviewModelKey, cfg[reviewModelKey])
	}
}

// TestReviewModelRestorePristine proves Restore after a review-model Apply
// returns config.toml byte-for-byte to its pristine state (including the
// user's own review_model value the Apply overwrote) and removes the catalog
// file.
func TestReviewModelRestorePristine(t *testing.T) {
	a, home := newAdapter(t)
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	origCfg := "other = \"keep\"\nreview_model = \"user-review\"\n"
	if err := os.WriteFile(cfgPath, []byte(origCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	p := sampleProfile()
	p.ReviewModel = "pinned-review"
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	cfg, err := readTOML(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg[reviewModelKey] != "pinned-review" {
		t.Fatalf("%s = %v, want pinned-review", reviewModelKey, cfg[reviewModelKey])
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != origCfg {
		t.Fatalf("config.toml not restored to pristine: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", catalogFileName)); !os.IsNotExist(err) {
		t.Fatalf("catalog file not removed on restore: %v", err)
	}
}

// TestRestoreNoBackupStripsReviewModel proves the no-backup Restore fallback
// strips the MintSwitch-written review_model along with the other managed
// keys while preserving the user's own settings.
func TestRestoreNoBackupStripsReviewModel(t *testing.T) {
	a, home := newAdapter(t)
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := sampleProfile()
	p.ReviewModel = "pinned-review"
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(a.r.BackupsDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	cfg, err := readTOML(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cfg[reviewModelKey]; present {
		t.Fatalf("%s must be stripped: %v", reviewModelKey, cfg)
	}
	if cfg["approval_policy"] != "never" {
		t.Fatalf("user config keys must be preserved: %v", cfg)
	}
}

// TestRestoreRemovesCatalog proves Restore deletes the MintSwitch catalog
// file and strips the managed model_catalog_json reference when no pristine
// backup covers config.toml.
func TestRestoreRemovesCatalog(t *testing.T) {
	a, home := newAdapter(t)
	p := sampleProfile()
	p.Models = []string{"gpt-5.5", "gpt-5.5-mini"}
	p.ApplyAllModels = true
	if _, err := a.Apply(p); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(home, ".codex", catalogFileName)
	if _, err := os.Stat(catalogPath); err != nil {
		t.Fatalf("catalog missing after apply: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(catalogPath); !os.IsNotExist(err) {
		t.Fatalf("catalog file not removed on restore: %v", err)
	}
}

func TestApplyPreservesExistingKeys(t *testing.T) {
	a, home := newAdapter(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(codexDir, "config.toml")
	authPath := filepath.Join(codexDir, "auth.json")
	existingCfg := "model_provider = \"openai\"\napproval_policy = \"on-request\"\n\n[mcp_servers.context7]\nenabled = true\n"
	if err := os.WriteFile(cfgPath, []byte(existingCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{\"tokens\":{\"id_token\":\"keep-me\"}}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}

	cfg, err := readTOML(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg["model_provider"] != "openai" || cfg["approval_policy"] != "on-request" {
		t.Fatalf("unrelated top-level keys lost: %+v", cfg)
	}
	mcp, ok := cfg["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_servers table lost: %+v", cfg)
	}
	if _, ok := mcp["context7"]; !ok {
		t.Fatalf("nested mcp server lost: %+v", mcp)
	}
	roundTrip, _ := toml.Marshal(cfg)
	if !strings.Contains(string(roundTrip), "openai_base_url") {
		t.Fatalf("expected openai_base_url in output:\n%s", roundTrip)
	}

	auth, err := core.ReadJSONObject(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := auth["tokens"]; !ok {
		t.Fatalf("auth.json tokens lost: %+v", auth)
	}
	if auth[authModeKey] != authModeAPIKey {
		t.Fatalf("auth_mode not set to apikey: %+v", auth)
	}
}

// writeLegacyConfig writes a config.toml carrying the legacy in-file marker
// for profile p plus a user key, mimicking a pre-store MintSwitch apply.
func writeLegacyConfig(t *testing.T, a *Adapter, p core.Profile) string {
	t.Helper()
	path := a.configPath()
	marker := core.NewMarker(p, p.Label)
	cfg := map[string]any{
		"sandbox_mode":    "read-only",
		"openai_base_url": p.BaseURL,
		"model":           p.Model,
		core.MarkerKey: map[string]any{
			"managed":      marker.Managed,
			"profileLabel": marker.ProfileLabel,
			"fingerprint":  marker.Fingerprint,
			"appliedAt":    marker.AppliedAt.Format(time.RFC3339),
			"version":      marker.Version,
		},
	}
	if err := writeTOML(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStatusDefaultWhenBaseURLRemoved proves a store entry alone does not
// report Applied: when the managed openai_base_url was removed from the file
// (e.g. an external restore/wipe), Status falls back to Default.
func TestStatusDefaultWhenBaseURLRemoved(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.WriteFile(a.configPath(), []byte("model = \"gpt-5.5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default when openai_base_url is gone, got %v", st)
	}
}

// TestApplyStripsLegacyMarker proves an Apply over a legacy-marker config
// removes the table in the same write and records the fresh marker in the
// store, without snapshotting the managed file (backup gate honors the legacy
// marker).
func TestApplyStripsLegacyMarker(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	p := sampleProfile()
	path := writeLegacyConfig(t, a, p)

	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("legacy-managed file must not be backed up, got %q", res.BackupPath)
	}
	cfg, err := readTOML(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg[core.MarkerKey]; ok {
		t.Fatalf("legacy marker not stripped on Apply: %+v", cfg)
	}
	if cfg["sandbox_mode"] != "read-only" {
		t.Fatalf("user key lost: %+v", cfg)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("want AppliedByMintSwitch, got %v", st)
	}
}

// TestStripLegacyMarkerMigrates is the startup-sweep case: a config carrying
// the legacy marker gets it removed and migrated into the store even though
// the user never pressed Apply.
func TestStripLegacyMarkerMigrates(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	path := writeLegacyConfig(t, a, p)

	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip: %v", err)
	}
	cfg, err := readTOML(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg[core.MarkerKey]; ok {
		t.Fatalf("legacy marker still in file: %+v", cfg)
	}
	if cfg["sandbox_mode"] != "read-only" || cfg["openai_base_url"] != p.BaseURL {
		t.Fatalf("existing keys lost: %+v", cfg)
	}
	marker, ok, err := a.m.Get(a.ID())
	if err != nil || !ok {
		t.Fatalf("store entry after migrate = ok=%v err=%v", ok, err)
	}
	if marker.Fingerprint != core.Fingerprint(p) {
		t.Fatalf("migrated fingerprint mismatch: %q", marker.Fingerprint)
	}
}

// TestStripLegacyMarkerKeepsExistingStoreEntry proves the sweep never
// overwrites a marker already recorded in the store (the store is newer truth).
func TestStripLegacyMarkerKeepsExistingStoreEntry(t *testing.T) {
	a, _ := newAdapter(t)
	stored := core.NewMarker(secondProfile(), "personal")
	if err := a.m.Put(a.ID(), stored); err != nil {
		t.Fatal(err)
	}
	writeLegacyConfig(t, a, sampleProfile())

	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip: %v", err)
	}
	marker, ok, err := a.m.Get(a.ID())
	if err != nil || !ok {
		t.Fatalf("store entry = ok=%v err=%v", ok, err)
	}
	if marker.Fingerprint != stored.Fingerprint {
		t.Fatalf("store entry overwritten by legacy marker: %+v", marker)
	}
	cfg, err := readTOML(a.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cfg[core.MarkerKey]; present {
		t.Fatal("legacy marker still in file")
	}
}

// TestStripLegacyMarkerNoOp proves the sweep neither creates a missing file
// nor rewrites a clean one.
func TestStripLegacyMarkerNoOp(t *testing.T) {
	a, _ := newAdapter(t)
	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip on missing file: %v", err)
	}
	if _, err := os.Stat(a.configPath()); !os.IsNotExist(err) {
		t.Fatalf("strip must not create config.toml, stat err=%v", err)
	}

	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := []byte("sandbox_mode = \"read-only\"\n")
	if err := os.WriteFile(path, clean, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.StripLegacyMarker(); err != nil {
		t.Fatalf("strip on clean file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(clean) {
		t.Fatalf("clean file rewritten: %q", got)
	}
}

// TestOrphanStatusAndRestoreWithBackup is the regression for the lost-marker
// gap: with the sidecar marker gone but the pristine backups intact, Status
// must report ModifiedExternally (so the UI offers Restore instead of treating
// the tool as never applied) and Restore must still revert byte-for-byte.
func TestOrphanStatusAndRestoreWithBackup(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	cfgPath, authPath := a.configPath(), a.authPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	originalCfg := "approval_policy = \"never\"\n"
	originalAuth := `{"auth_mode":"chatgpt"}` + "\n"
	if err := os.WriteFile(cfgPath, []byte(originalCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(originalAuth), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Simulate the lost marker (e.g. an interrupted earlier restore).
	if err := a.m.Delete(a.ID()); err != nil {
		t.Fatal(err)
	}

	st, detail, err := a.Status(sampleProfile())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != core.StatusModifiedExternally || detail != orphanDetail {
		t.Fatalf("orphan status = %v %q, want ModifiedExternally + orphanDetail", st, detail)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for path, want := range map[string]string{cfgPath: originalCfg, authPath: originalAuth} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s not byte-for-byte restored: %q", path, got)
		}
	}
}

// TestOrphanRestoreNoBackupStrips covers the orphan-no-backup branch: marker
// gone AND backups gone, but the files still carry the full MintSwitch
// signature — Restore must strip the managed keys from both files while
// preserving the user's own settings, and Status must offer Restore
// beforehand.
func TestOrphanRestoreNoBackupStrips(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	cfgPath, authPath := a.configPath(), a.authPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"id_token":"tok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := os.RemoveAll(a.r.BackupsDir()); err != nil {
		t.Fatal(err)
	}
	if err := a.m.Delete(a.ID()); err != nil {
		t.Fatal(err)
	}

	if st, _, _ := a.Status(sampleProfile()); st != core.StatusModifiedExternally {
		t.Fatalf("orphan status = %v, want ModifiedExternally", st)
	}
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	want := "No backup found; removed the MintSwitch-managed keys from the Codex config files."
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	cfg, err := readTOML(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cfg["openai_base_url"]; present {
		t.Fatalf("openai_base_url must be stripped: %v", cfg)
	}
	if _, present := cfg["model"]; present {
		t.Fatalf("model must be stripped: %v", cfg)
	}
	if cfg["approval_policy"] != "never" {
		t.Fatalf("user config keys must be preserved: %v", cfg)
	}
	auth, err := core.ReadJSONObject(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := auth[authKeyName]; present {
		t.Fatalf("OPENAI_API_KEY must be stripped: %v", auth)
	}
	if _, present := auth[authModeKey]; present {
		t.Fatalf("auth_mode must be stripped: %v", auth)
	}
	if _, present := auth["tokens"]; !present {
		t.Fatalf("user auth keys must be preserved: %v", auth)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after strip = %v, want Default", st)
	}
}

// TestPureUserConfigNeverOrphan proves the no-false-positive contract: a
// hand-written config that never saw Apply — even one carrying a proxy
// openai_base_url + model with an API-key login under a ChatGPT auth_mode —
// stays Default (no Restore button) and Restore leaves both files
// byte-for-byte untouched.
func TestPureUserConfigNeverOrphan(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	cfgPath, authPath := a.configPath(), a.authPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	userCfg := "model = \"gpt-5.5\"\nopenai_base_url = \"https://my-proxy.example.com/v1\"\n"
	userAuth := `{"OPENAI_API_KEY":"sk-user","auth_mode":"chatgpt"}`
	if err := os.WriteFile(cfgPath, []byte(userCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(userAuth), 0o600); err != nil {
		t.Fatal(err)
	}

	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("pure user config status = %v, want Default", st)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for path, want := range map[string]string{cfgPath: userCfg, authPath: userAuth} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("pure user file %s rewritten: %q", path, got)
		}
	}
}

// TestApplyMalformedAuthFailsBeforeAnyWrite pins the up-front read: a
// malformed auth.json fails the Apply before either file is written, so
// config.toml is never touched (or created) and no managed marker is
// recorded.
func TestApplyMalformedAuthFailsBeforeAnyWrite(t *testing.T) {
	t.Run("existing config untouched", func(t *testing.T) {
		a, _ := newAdapter(t)
		cfgPath, authPath := a.configPath(), a.authPath()
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			t.Fatal(err)
		}
		userCfg := "model = \"user-model\"\n"
		if err := os.WriteFile(cfgPath, []byte(userCfg), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authPath, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := a.Apply(sampleProfile()); err == nil {
			t.Fatal("expected Apply to fail on malformed auth.json")
		}
		got, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != userCfg {
			t.Fatalf("config.toml must be untouched: %q, want %q", got, userCfg)
		}
		if _, inStore, err := a.m.Get(a.ID()); err != nil || inStore {
			t.Fatalf("marker after failed Apply = inStore=%v err=%v, want absent", inStore, err)
		}
	})

	t.Run("config never created", func(t *testing.T) {
		a, _ := newAdapter(t)
		cfgPath, authPath := a.configPath(), a.authPath()
		if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authPath, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := a.Apply(sampleProfile()); err == nil {
			t.Fatal("expected Apply to fail on malformed auth.json")
		}
		if _, err := os.Stat(cfgPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("config.toml must not be created, stat err = %v", err)
		}
		if _, inStore, err := a.m.Get(a.ID()); err != nil || inStore {
			t.Fatalf("marker after failed Apply = inStore=%v err=%v, want absent", inStore, err)
		}
	})
}

// TestApplyRollsBackAuthWhenConfigWriteFails pins the two-file atomicity fix
// for the auth-first write order: when the config.toml half of Apply fails,
// auth.json is rolled back to its pre-Apply state (original bytes for an
// existing file, removal for a created one) and no managed marker is
// recorded, so a failed Apply never leaves the MintSwitch key behind in
// auth.json.
func TestApplyRollsBackAuthWhenConfigWriteFails(t *testing.T) {
	failWrite := func(string, map[string]any) error { return errors.New("injected config write failure") }

	t.Run("existing auth restored", func(t *testing.T) {
		a, _ := newAdapter(t)
		a.writeConfig = failWrite
		authPath := a.authPath()
		if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
			t.Fatal(err)
		}
		userAuth := `{"tokens":{"id_token":"keep-me"}}`
		if err := os.WriteFile(authPath, []byte(userAuth), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := a.Apply(sampleProfile()); err == nil {
			t.Fatal("expected Apply to fail on config.toml write")
		}
		got, err := os.ReadFile(authPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != userAuth {
			t.Fatalf("auth.json not rolled back: %q, want %q", got, userAuth)
		}
		if _, inStore, err := a.m.Get(a.ID()); err != nil || inStore {
			t.Fatalf("marker after failed Apply = inStore=%v err=%v, want absent", inStore, err)
		}
	})

	t.Run("created auth removed", func(t *testing.T) {
		a, _ := newAdapter(t)
		a.writeConfig = failWrite
		authPath := a.authPath()

		p := sampleProfile()
		if _, err := a.Apply(p); err == nil {
			t.Fatal("expected Apply to fail on config.toml write")
		}
		if _, err := os.Stat(authPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("auth.json must be removed by rollback, stat err = %v", err)
		}
		if _, inStore, err := a.m.Get(a.ID()); err != nil || inStore {
			t.Fatalf("marker after failed Apply = inStore=%v err=%v, want absent", inStore, err)
		}
	})
}

// TestApplyWriteOrderAuthFirst pins the crash-window contract: auth.json must
// be written before config.toml, so a kill between the two writes leaves the
// traffic-redirecting config.toml pristine (the leftover auth key is inert).
func TestApplyWriteOrderAuthFirst(t *testing.T) {
	a, _ := newAdapter(t)
	var authHadKeyAtConfigWrite bool
	a.writeConfig = func(path string, m map[string]any) error {
		auth, err := core.ReadJSONObject(a.authPath())
		if err != nil {
			t.Fatalf("read auth.json during config write: %v", err)
		}
		_, authHadKeyAtConfigWrite = auth[authKeyName]
		return writeTOML(path, m)
	}
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatal(err)
	}
	if !authHadKeyAtConfigWrite {
		t.Fatal("auth.json must carry the API key before config.toml is written")
	}
}
