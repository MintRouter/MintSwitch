package claudedesktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

// newAdapter returns an adapter over a temp HOME whose Detect probes a
// controllable fake app-bundle dir instead of /Applications.
func newAdapter(t *testing.T) (*Adapter, *paths.Resolver, string) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	a := New(r, backup.NewEngine(r.BackupsDir()), markers.NewStore(r.MarkersPath()))
	appDir := filepath.Join(home, "Applications", "Claude.app")
	a.appDirs = []string{appDir}
	return a, r, appDir
}

func installApp(t *testing.T, appDir string) {
	t.Helper()
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("mkdir app bundle: %v", err)
	}
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:   "work",
		APIKey:  "sk-secret-token",
		BaseURL: "https://api.example.com/v1",
		Model:   "claude-opus-5",
		Models:  []string{"gpt-5", "claude-opus-5", "claude-haiku-4-5"},
		ModelNames: map[string]string{
			"claude-opus-5":    "Claude Opus 5",
			"claude-haiku-4-5": "Claude Haiku 4.5",
			"gpt-5":            "GPT-5",
		},
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// modelNamesOf extracts the ordered "name" values from inferenceModels.
func modelNamesOf(t *testing.T, prov map[string]any) []string {
	t.Helper()
	arr, ok := prov["inferenceModels"].([]any)
	if !ok {
		t.Fatalf("inferenceModels missing or not an array: %v", prov["inferenceModels"])
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		obj := e.(map[string]any)
		out = append(out, obj["name"].(string))
	}
	return out
}

// TestDetectViaAppBundle proves Detect keys solely off the app bundle dir.
func TestDetectViaAppBundle(t *testing.T) {
	a, _, appDir := newAdapter(t)
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed without app bundle")
	}
	installApp(t, appDir)
	installed, path := a.Detect()
	if !installed {
		t.Fatal("expected installed via app bundle dir")
	}
	if filepath.Base(path) != "claude_desktop_config.json" {
		t.Fatalf("Detect() path = %q, want claude_desktop_config.json", path)
	}
}

// TestApplyWritesThreeFiles proves Apply produces the same shape as the
// verified hand-written 3P config: 3p mode, gateway provider with a stripped
// /v1 base URL and — in the default single-model mode — exactly the selected
// claude-* model (labelOverride from ModelNames), and _meta.json linking
// appliedId to the provider entry.
func TestApplyWritesThreeFiles(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	res, err := a.Apply(sampleProfile())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	cfg := readJSON(t, a.configPath())
	if cfg["deploymentMode"] != "3p" {
		t.Fatalf("deploymentMode = %v, want 3p", cfg["deploymentMode"])
	}
	meta := readJSON(t, a.metaPath())
	uuid := managedUUID(meta)
	if uuid == "" {
		t.Fatal("no MintRouter.AI entry in _meta.json")
	}
	if meta["appliedId"] != uuid {
		t.Fatalf("appliedId = %v, want %q", meta["appliedId"], uuid)
	}
	prov := readJSON(t, a.providerPath(uuid))
	if prov["inferenceProvider"] != "gateway" || prov["inferenceCredentialKind"] != "static" ||
		prov["inferenceGatewayAuthScheme"] != "bearer" {
		t.Fatalf("provider header wrong: %v", prov)
	}
	if prov["inferenceGatewayBaseUrl"] != "https://api.example.com" {
		t.Fatalf("baseUrl = %v, want /v1 stripped", prov["inferenceGatewayBaseUrl"])
	}
	if prov["inferenceGatewayApiKey"] != "sk-secret-token" {
		t.Fatal("api key not written")
	}
	names := modelNamesOf(t, prov)
	if len(names) != 1 || names[0] != "claude-opus-5" {
		t.Fatalf("inferenceModels = %v, want [claude-opus-5] (single-model mode)", names)
	}
	first := prov["inferenceModels"].([]any)[0].(map[string]any)
	if first["labelOverride"] != "Claude Opus 5" {
		t.Fatalf("labelOverride = %v, want Claude Opus 5", first["labelOverride"])
	}
	if res.ChangedPath != a.configPath() {
		t.Fatalf("ChangedPath = %q", res.ChangedPath)
	}
}

// TestApplyAllModels proves "All models" mode writes every claude-* model
// (selected first, non-claude filtered out regardless of mode) and that
// switching modes flips the fingerprint so Status reports ModifiedExternally
// until re-apply.
func TestApplyAllModels(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	p := sampleProfile()
	p.ApplyAllModels = true
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	uuid := managedUUID(readJSON(t, a.metaPath()))
	names := modelNamesOf(t, readJSON(t, a.providerPath(uuid)))
	want := []string{"claude-opus-5", "claude-haiku-4-5"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("inferenceModels = %v, want %v (all claude-*, selected first)", names, want)
	}
	st, _, err := a.Status(p)
	if err != nil || st != core.StatusAppliedByMintSwitch {
		t.Fatalf("Status in all mode = %v, %v; want Applied", st, err)
	}
	one := p
	one.ApplyAllModels = false
	st, _, err = a.Status(one)
	if err != nil || st != core.StatusModifiedExternally {
		t.Fatalf("Status after mode switch = %v, %v; want ModifiedExternally", st, err)
	}
}

// TestApplyPreservesForeignKeys proves Apply keeps the app's own keys in
// claude_desktop_config.json and foreign entries in _meta.json.
func TestApplyPreservesForeignKeys(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	mustWriteJSON(t, a.configPath(), map[string]any{"scale": 0, "locale": "en-US"})
	mustWriteJSON(t, a.metaPath(), map[string]any{
		"entries": []any{map[string]any{"id": "other-uuid", "name": "Other Provider"}},
	})
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	cfg := readJSON(t, a.configPath())
	if cfg["locale"] != "en-US" || cfg["deploymentMode"] != "3p" {
		t.Fatalf("config keys not preserved: %v", cfg)
	}
	meta := readJSON(t, a.metaPath())
	entries := meta["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %v, want foreign entry preserved plus MintSwitch entry", entries)
	}
}

// TestApplyIdempotentUUID proves a second Apply reuses the provider UUID and
// leaves exactly one MintRouter.AI entry.
func TestApplyIdempotentUUID(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	uuid1 := managedUUID(readJSON(t, a.metaPath()))
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	meta := readJSON(t, a.metaPath())
	if uuid2 := managedUUID(meta); uuid2 != uuid1 {
		t.Fatalf("uuid changed across Applies: %q -> %q", uuid1, uuid2)
	}
	count := 0
	for _, e := range meta["entries"].([]any) {
		if e.(map[string]any)["name"] == "MintRouter.AI" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("MintRouter.AI entries = %d, want 1", count)
	}
	if entries, err := os.ReadDir(filepath.Dir(a.metaPath())); err == nil {
		jsonCount := 0
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") && e.Name() != "_meta.json" {
				jsonCount++
			}
		}
		if jsonCount != 1 {
			t.Fatalf("provider files = %d, want 1 (no stale files)", jsonCount)
		}
	}
}

// TestApplyFallbackModel proves a non-claude selected model falls back to the
// first claude-* model and the message says so.
func TestApplyFallbackModel(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	p := sampleProfile()
	p.Model = "gpt-5"
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(res.Message, `"gpt-5"`) || !strings.Contains(res.Message, `"claude-opus-5"`) {
		t.Fatalf("message = %q, want fallback explanation", res.Message)
	}
	uuid := managedUUID(readJSON(t, a.metaPath()))
	names := modelNamesOf(t, readJSON(t, a.providerPath(uuid)))
	if names[0] != "claude-opus-5" {
		t.Fatalf("first model = %q, want claude-opus-5", names[0])
	}
}

// TestApplyNoClaudeModels proves Apply fails before writing anything when the
// profile has no claude-* model.
func TestApplyNoClaudeModels(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	p := sampleProfile()
	p.Model = "gpt-5"
	p.Models = []string{"gpt-5", "gemini-3-pro"}
	if _, err := a.Apply(p); err == nil {
		t.Fatal("expected error for profile without claude-* models")
	}
	if _, err := os.Stat(a.configPath()); !os.IsNotExist(err) {
		t.Fatal("config file must not be created on failed Apply")
	}
}

// TestStatusLifecycle covers Default -> Applied -> ModifiedExternally (other
// profile) -> Default after external de-3p.
func TestStatusLifecycle(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	p := sampleProfile()

	st, _, err := a.Status(p)
	if err != nil || st != core.StatusDefault {
		t.Fatalf("initial Status = %v, %v; want Default", st, err)
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	st, _, err = a.Status(p)
	if err != nil || st != core.StatusAppliedByMintSwitch {
		t.Fatalf("Status after Apply = %v, %v; want Applied", st, err)
	}
	other := p
	other.APIKey = "sk-different"
	st, _, err = a.Status(other)
	if err != nil || st != core.StatusModifiedExternally {
		t.Fatalf("Status with other profile = %v, %v; want ModifiedExternally", st, err)
	}
	cfg := readJSON(t, a.configPath())
	delete(cfg, "deploymentMode")
	mustWriteJSON(t, a.configPath(), cfg)
	st, _, err = a.Status(p)
	if err != nil || st != core.StatusDefault {
		t.Fatalf("Status after external de-3p = %v, %v; want Default", st, err)
	}
}

// TestStatusNotInstalled proves Status short-circuits without the app bundle.
func TestStatusNotInstalled(t *testing.T) {
	a, _, _ := newAdapter(t)
	st, _, err := a.Status(sampleProfile())
	if err != nil || st != core.StatusNotInstalled {
		t.Fatalf("Status = %v, %v; want NotInstalled", st, err)
	}
}

// TestRestoreRevertsPreexistingFiles proves Restore brings back the pristine
// pre-apply contents of files that existed before the first Apply.
func TestRestoreRevertsPreexistingFiles(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	mustWriteJSON(t, a.configPath(), map[string]any{"locale": "en-US"})
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	uuid := managedUUID(readJSON(t, a.metaPath()))
	if _, err := a.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	cfg := readJSON(t, a.configPath())
	if cfg["locale"] != "en-US" {
		t.Fatalf("locale lost: %v", cfg)
	}
	if _, ok := cfg["deploymentMode"]; ok {
		t.Fatalf("deploymentMode still present after Restore: %v", cfg)
	}
	if _, err := os.Stat(a.providerPath(uuid)); !os.IsNotExist(err) {
		t.Fatal("provider file should be gone after Restore")
	}
	st, _, err := a.Status(sampleProfile())
	if err != nil || st != core.StatusDefault {
		t.Fatalf("Status after Restore = %v, %v; want Default", st, err)
	}
}

// TestRestoreRemovesCreatedTree proves Restore over a tree MintSwitch created
// from scratch removes the files and the now-empty directories.
func TestRestoreRemovesCreatedTree(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(a.baseDir()); !os.IsNotExist(err) {
		t.Fatalf("Claude-3p dir should be removed when MintSwitch created it: %v", err)
	}
}

// TestRestoreNoOpWhenNeverApplied proves Restore is safe with nothing applied.
func TestRestoreNoOpWhenNeverApplied(t *testing.T) {
	a, _, appDir := newAdapter(t)
	installApp(t, appDir)
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !strings.Contains(res.Message, "Nothing to restore") {
		t.Fatalf("message = %q", res.Message)
	}
}

// TestOrphanRemnant proves a lost marker still surfaces ModifiedExternally
// and that Restore strips the managed pieces.
func TestOrphanRemnant(t *testing.T) {
	a, r, appDir := newAdapter(t)
	installApp(t, appDir)
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := os.Remove(r.MarkersPath()); err != nil {
		t.Fatalf("remove marker store: %v", err)
	}
	st, detail, err := a.Status(sampleProfile())
	if err != nil || st != core.StatusModifiedExternally {
		t.Fatalf("Status = %v, %v; want ModifiedExternally for orphan remnant", st, err)
	}
	if !strings.Contains(detail, "marker is missing") {
		t.Fatalf("detail = %q", detail)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	st, _, err = a.Status(sampleProfile())
	if err != nil || st != core.StatusDefault {
		t.Fatalf("Status after orphan Restore = %v, %v; want Default", st, err)
	}
}

// TestSupportsModel proves the ModelFilter contract.
func TestSupportsModel(t *testing.T) {
	a, _, _ := newAdapter(t)
	if !a.SupportsModel("claude-opus-5") {
		t.Fatal("claude-* must be supported")
	}
	if a.SupportsModel("gpt-5") {
		t.Fatal("non-claude model must not be supported")
	}
	var _ core.ModelFilter = a
}

// TestStripV1Suffix pins the base-URL normalization edge cases.
func TestStripV1Suffix(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/v1":       "https://api.example.com",
		"https://api.example.com/v1/":      "https://api.example.com",
		"https://api.example.com":          "https://api.example.com",
		"https://api.example.com/v1beta":   "https://api.example.com/v1beta",
		"https://api.example.com/v1/extra": "https://api.example.com/v1/extra",
	}
	for in, want := range cases {
		if got := stripV1Suffix(in); got != want {
			t.Fatalf("stripV1Suffix(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBaseDirPerOS pins the 3P data directory per OS: the macOS path must
// never change (existing users' backups and markers were recorded against
// it) and Windows uses %LOCALAPPDATA%\Claude-3p per the data-storage docs,
// falling back to Home\AppData\Local when LOCALAPPDATA is unset.
func TestBaseDirPerOS(t *testing.T) {
	a, r, _ := newAdapter(t)
	a.goos = "darwin"
	if got, want := a.baseDir(), filepath.Join(r.Home, "Library", "Application Support", "Claude-3p"); got != want {
		t.Fatalf("darwin baseDir = %q, want %q", got, want)
	}
	a.goos = "windows"
	if got, want := a.baseDir(), filepath.Join(r.Home, "AppData", "Local", "Claude-3p"); got != want {
		t.Fatalf("windows baseDir fallback = %q, want %q", got, want)
	}
	local := t.TempDir()
	r.LocalAppData = local
	if got, want := a.baseDir(), filepath.Join(local, "Claude-3p"); got != want {
		t.Fatalf("windows baseDir = %q, want %q", got, want)
	}
}

// TestDetectWindows proves the Windows probe locations key off %LOCALAPPDATA%
// and that Detect reports installed — with the config path under the Windows
// 3P data dir — once one of them exists.
func TestDetectWindows(t *testing.T) {
	for _, dir := range []string{
		filepath.Join("AnthropicClaude"),
		filepath.Join("Programs", "Claude"),
	} {
		t.Run(dir, func(t *testing.T) {
			home := t.TempDir()
			r := &paths.Resolver{
				Home:         home,
				DataDir:      filepath.Join(home, "data"),
				LocalAppData: filepath.Join(home, "AppData", "Local"),
			}
			a := New(r, backup.NewEngine(r.BackupsDir()), markers.NewStore(r.MarkersPath()))
			a.goos = "windows"
			a.appDirs = defaultAppDirs(r, "windows")
			if installed, _ := a.Detect(); installed {
				t.Fatal("expected not installed without app dir")
			}
			installApp(t, filepath.Join(r.LocalAppData, dir))
			installed, path := a.Detect()
			if !installed {
				t.Fatalf("expected installed via %s", dir)
			}
			if want := filepath.Join(r.LocalAppData, "Claude-3p", "claude_desktop_config.json"); path != want {
				t.Fatalf("Detect path = %q, want %q", path, want)
			}
		})
	}
}

// TestDetectLinuxFalse proves Detect stays not-installed on Linux: the app
// has no Linux build, so the probed dirs are the macOS-shaped ones, which do
// not exist there.
func TestDetectLinuxFalse(t *testing.T) {
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	a := New(r, backup.NewEngine(r.BackupsDir()), markers.NewStore(r.MarkersPath()))
	a.goos = "linux"
	a.appDirs = []string{r.Join("Applications", "Claude.app")}
	if installed, _ := a.Detect(); installed {
		t.Fatal("expected not installed on linux")
	}
}

func mustWriteJSON(t *testing.T, path string, m map[string]any) {
	t.Helper()
	if err := core.WriteJSONObjectAtomic(path, m); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
