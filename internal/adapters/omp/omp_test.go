package omp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

func newAdapter(t *testing.T) (*Adapter, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	data := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: data}
	a := New(r, backup.NewEngine(r.BackupsDir()), markers.NewStore(r.MarkersPath()))
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return a, r
}

// installed makes Detect see a resolvable "omp" binary.
func installed(a *Adapter) {
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/omp", nil }
}

func sampleProfile() core.Profile {
	return core.Profile{
		Label:   "work",
		APIKey:  "sk-test-123",
		BaseURL: "https://router.example.com/v1",
		Model:   "gpt-mint",
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func readYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(readText(t, path)), &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// providerOf returns providers.mintrouter of a parsed models.yml.
func providerOf(t *testing.T, models map[string]any) map[string]any {
	t.Helper()
	providers, _ := models["providers"].(map[string]any)
	prov, ok := providers[providerID].(map[string]any)
	if !ok {
		t.Fatalf("mintrouter provider missing: %v", models)
	}
	return prov
}

// modelEntryOf returns the models[] item with the given id from a parsed
// models.yml.
func modelEntryOf(t *testing.T, models map[string]any, id string) map[string]any {
	t.Helper()
	for _, e := range providerOf(t, models)["models"].([]any) {
		entry := e.(map[string]any)
		if entry["id"] == id {
			return entry
		}
	}
	t.Fatalf("model entry %q missing", id)
	return nil
}

// roleOf returns modelRoles.<role> of a parsed config.yml ("" when absent).
func roleOf(cfg map[string]any, role string) any {
	roles, _ := cfg["modelRoles"].(map[string]any)
	return roles[role]
}

func notExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s absent, stat err=%v", path, err)
	}
}

// countBackups counts snapshot entries (.bak / .absent) under the backups root.
func countBackups(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(d.Name(), ".bak") || strings.HasSuffix(d.Name(), ".absent")) {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIDName(t *testing.T) {
	a, _ := newAdapter(t)
	if a.ID() != "omp" || a.Name() != "oh-my-pi" {
		t.Fatalf("unexpected id/name: %q %q", a.ID(), a.Name())
	}
}

func TestConfigPaths(t *testing.T) {
	a, r := newAdapter(t)
	got := a.ConfigPaths()
	want := []string{r.Join(".omp", "agent", "models.yml"), r.Join(".omp", "agent", "config.yml")}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ConfigPaths = %v, want %v", got, want)
	}
}

// TestDetect proves the binary-based contract: a leftover ~/.omp dir is NOT an
// installed signal; only a resolvable "omp" binary is.
func TestDetect(t *testing.T) {
	a, r := newAdapter(t)
	if inst, _ := a.Detect(); inst {
		t.Fatal("expected not installed with empty home")
	}
	if err := os.MkdirAll(a.agentDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if inst, _ := a.Detect(); inst {
		t.Fatal("config dir present + binary absent must be NOT installed")
	}
	installed(a)
	if inst, path := a.Detect(); !inst || path != r.Join(".omp", "agent", "models.yml") {
		t.Fatalf("expected installed via PATH binary, got %v %q", inst, path)
	}
}

func TestApplyNewFilesAndStatus(t *testing.T) {
	a, _ := newAdapter(t)
	p := sampleProfile()
	if st, _, _ := a.Status(p); st != core.StatusNotInstalled {
		t.Fatalf("expected NotInstalled, got %v", st)
	}
	installed(a)
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ChangedPath != a.modelsPath() || res.BackupPath == "" {
		t.Fatalf("result = %+v, want ChangedPath models.yml and a backup entry", res)
	}
	for _, path := range []string{a.modelsPath(), a.configPath()} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected file created: %v", err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s perm = %o, want 0600", path, fi.Mode().Perm())
		}
	}
	models := readYAML(t, a.modelsPath())
	prov := providerOf(t, models)
	if prov["baseUrl"] != p.BaseURL || prov["api"] != apiType || prov["apiKey"] != p.APIKey {
		t.Fatalf("provider wrong: %v", prov)
	}
	list := prov["models"].([]any)
	if len(list) != 1 {
		t.Fatalf("models list = %v, want 1 entry", list)
	}
	entry := list[0].(map[string]any)
	if entry["id"] != p.Model || entry["name"] != p.Model {
		t.Fatalf("entry = %v, want id/name %q", entry, p.Model)
	}
	if _, ok := entry["contextWindow"]; ok {
		t.Fatalf("contextWindow must be omitted when unknown: %v", entry)
	}
	if _, ok := entry["maxTokens"]; ok {
		t.Fatalf("maxTokens must be omitted when unknown: %v", entry)
	}
	if got := inputOf(t, entry); !slices.Equal(got, []string{"text", "image"}) {
		t.Fatalf("input = %v, want [text image] fallback when unknown", got)
	}
	if entry["reasoning"] != false {
		t.Fatalf("reasoning = %v, want explicit false when no levels are known", entry["reasoning"])
	}
	cfg := readYAML(t, a.configPath())
	if got := roleOf(cfg, roleDefault); got != selectorPrefix+p.Model {
		t.Fatalf("modelRoles.default = %v, want %q", got, selectorPrefix+p.Model)
	}
	if strings.Contains(readText(t, a.configPath()), p.APIKey) {
		t.Fatal("api key must not leak into config.yml")
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch, got %v", st)
	}
	other := sampleProfile()
	other.Model = "different"
	if st, _, _ := a.Status(other); st != core.StatusModifiedExternally {
		t.Fatalf("expected ModifiedExternally, got %v", st)
	}
}

// TestApplyWritesContextWindowAndMaxTokens proves a model entry carries
// contextWindow / maxTokens only when the profile knows the endpoint-advertised
// values, and that those limits do not affect the fingerprint.
func TestApplyWritesContextWindowAndMaxTokens(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.Models = []string{"gpt-mint", "unknown"}
	p.ApplyAllModels = true
	p.ModelContextWindows = map[string]int{"gpt-mint": 200_000}
	p.ModelMaxOutputTokens = map[string]int{"gpt-mint": 32_768}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	models := readYAML(t, a.modelsPath())
	known := modelEntryOf(t, models, "gpt-mint")
	if known["contextWindow"] != 200_000 || known["maxTokens"] != 32_768 {
		t.Fatalf("known entry = %v, want contextWindow 200000 maxTokens 32768", known)
	}
	unknown := modelEntryOf(t, models, "unknown")
	if _, ok := unknown["contextWindow"]; ok {
		t.Fatalf("contextWindow must be omitted when unknown: %v", unknown)
	}
	if _, ok := unknown["maxTokens"]; ok {
		t.Fatalf("maxTokens must be omitted when unknown: %v", unknown)
	}
	p.ModelContextWindows, p.ModelMaxOutputTokens = nil, nil
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("limits must not affect the fingerprint; got %v", st)
	}
}

// inputOf returns a parsed models[] entry's input list as strings.
func inputOf(t *testing.T, entry map[string]any) []string {
	t.Helper()
	raw, ok := entry["input"].([]any)
	if !ok {
		t.Fatalf("input missing or not a list: %v", entry)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

// TestApplyWritesInputAndReasoning proves every models[] entry carries
// omp's per-model `input` (endpoint-advertised modalities filtered to
// text/image, fallback [text, image] when unknown) and an explicit
// `reasoning` boolean (true only when the endpoint advertised reasoning
// levels), and that neither affects the fingerprint.
func TestApplyWritesInputAndReasoning(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.Models = []string{"gpt-mint", "glm-mint", "kimi-mint", "unknown", "odd-mint"}
	p.ApplyAllModels = true
	p.ModelInputModalities = map[string][]string{
		"gpt-mint":  {"text", "image"},
		"glm-mint":  {"text"},
		"kimi-mint": {"image", "text", "audio", "pdf"},
		"odd-mint":  {"audio"},
	}
	p.ModelReasoningLevels = map[string][]string{
		"gpt-mint": {"low", "medium", "high"},
		"unknown":  {"medium"},
	}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	models := readYAML(t, a.modelsPath())
	cases := []struct {
		id        string
		input     []string
		reasoning bool
	}{
		{"gpt-mint", []string{"text", "image"}, true},
		{"glm-mint", []string{"text"}, false},
		{"kimi-mint", []string{"text", "image"}, false},
		{"unknown", []string{"text", "image"}, true},
		{"odd-mint", []string{"text", "image"}, false},
	}
	for _, tc := range cases {
		entry := modelEntryOf(t, models, tc.id)
		if got := inputOf(t, entry); !slices.Equal(got, tc.input) {
			t.Fatalf("%s input = %v, want %v", tc.id, got, tc.input)
		}
		got, ok := entry["reasoning"].(bool)
		if !ok {
			t.Fatalf("%s reasoning missing or not a bool: %v", tc.id, entry)
		}
		if got != tc.reasoning {
			t.Fatalf("%s reasoning = %v, want %v", tc.id, got, tc.reasoning)
		}
	}
	p.ModelInputModalities, p.ModelReasoningLevels = nil, nil
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("modalities/levels must not affect the fingerprint; got %v", st)
	}

	// Re-Apply after the levels disappeared must turn reasoning back off:
	// the key is always written, never left over from the previous Apply.
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if got := modelEntryOf(t, readYAML(t, a.modelsPath()), "gpt-mint")["reasoning"]; got != false {
		t.Fatalf("reasoning after levels vanished = %v, want false", got)
	}
}

// TestApplyModelEntryYAMLShape pins the rendered text of one models[] entry:
// input in flow style ([text, image]) and reasoning as a plain boolean, with
// omp's two-space indentation and the other keys untouched.
func TestApplyModelEntryYAMLShape(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.ModelInputModalities = map[string][]string{"gpt-mint": {"text", "image"}}
	p.ModelReasoningLevels = map[string][]string{"gpt-mint": {"low", "high"}}
	p.ModelContextWindows = map[string]int{"gpt-mint": 200_000}
	p.ModelMaxOutputTokens = map[string]int{"gpt-mint": 32_768}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := "providers:\n" +
		"  mintrouter:\n" +
		"    baseUrl: https://router.example.com/v1\n" +
		"    api: openai-completions\n" +
		"    apiKey: sk-test-123\n" +
		"    models:\n" +
		"      - id: gpt-mint\n" +
		"        name: gpt-mint\n" +
		"        input: [text, image]\n" +
		"        reasoning: true\n" +
		"        contextWindow: 200000\n" +
		"        maxTokens: 32768\n"
	if got := readText(t, a.modelsPath()); got != want {
		t.Fatalf("models.yml =\n%s\nwant:\n%s", got, want)
	}
}

// TestApplyAllModels proves "All models" mode writes one models entry per
// profile model (selected first) while modelRoles.default stays the selected
// model, and that a mode switch is detected via the fingerprint until re-apply.
func TestApplyAllModels(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.Models = []string{"gpt-mint", "claude-mint"}
	p.ApplyAllModels = true
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	list := providerOf(t, readYAML(t, a.modelsPath()))["models"].([]any)
	if len(list) != 2 {
		t.Fatalf("models list = %v, want 2 entries", list)
	}
	for i, id := range []string{"gpt-mint", "claude-mint"} {
		entry := list[i].(map[string]any)
		if entry["id"] != id || entry["name"] != id {
			t.Fatalf("entry %d = %v, want id/name %q", i, entry, id)
		}
	}
	if got := roleOf(readYAML(t, a.configPath()), roleDefault); got != selectorPrefix+p.Model {
		t.Fatalf("modelRoles.default = %v, want selected model", got)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch, got %v", st)
	}
	one := p
	one.ApplyAllModels = false
	if st, _, _ := a.Status(one); st != core.StatusModifiedExternally {
		t.Fatalf("expected ModifiedExternally after mode switch, got %v", st)
	}
}

// TestApplyModelDisplayNames pins the display-name UX: "name" takes the
// ModelNames display name when present, "id" always stays canonical.
func TestApplyModelDisplayNames(t *testing.T) {
	t.Run("single-model with display name", func(t *testing.T) {
		a, _ := newAdapter(t)
		p := sampleProfile()
		p.ModelNames = map[string]string{"gpt-mint": "GPT Mint"}
		if _, err := a.Apply(p); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if got := modelEntryOf(t, readYAML(t, a.modelsPath()), "gpt-mint")["name"]; got != "GPT Mint" {
			t.Fatalf("name = %v, want display name %q", got, "GPT Mint")
		}
	})

	t.Run("all-models mixed", func(t *testing.T) {
		a, _ := newAdapter(t)
		p := sampleProfile()
		p.Models = []string{"gpt-mint", "claude-mint"}
		p.ApplyAllModels = true
		p.ModelNames = map[string]string{"claude-mint": "Claude Mint"}
		if _, err := a.Apply(p); err != nil {
			t.Fatalf("apply: %v", err)
		}
		models := readYAML(t, a.modelsPath())
		if got := modelEntryOf(t, models, "gpt-mint")["name"]; got != "gpt-mint" {
			t.Fatalf("gpt-mint name = %v, want ID fallback", got)
		}
		if got := modelEntryOf(t, models, "claude-mint")["name"]; got != "Claude Mint" {
			t.Fatalf("claude-mint name = %v, want display name %q", got, "Claude Mint")
		}
	})
}

// TestApplyPreservesExistingKeysCommentsAndOrder proves the node-tree rewrite
// keeps the user's comments, other providers/keys and key order in both files,
// while modelRoles.default is replaced (carrying over its inline comment).
func TestApplyPreservesExistingKeysCommentsAndOrder(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	writeFile(t, a.modelsPath(), "# my custom models\nproviders:\n  own:\n    baseUrl: https://own.example.com # trailing\n    api: anthropic-messages\n    apiKey: k\n    models: []\n")
	writeFile(t, a.configPath(), "theme: dark\nretry:\n  fallbackChains:\n    default: []\nmodelRoles:\n  default: other/x # picked\n")
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}

	modelsText := readText(t, a.modelsPath())
	for _, want := range []string{"# my custom models", "# trailing"} {
		if !strings.Contains(modelsText, want) {
			t.Fatalf("models.yml lost comment %q:\n%s", want, modelsText)
		}
	}
	providers := readYAML(t, a.modelsPath())["providers"].(map[string]any)
	if own, ok := providers["own"].(map[string]any); !ok || own["baseUrl"] != "https://own.example.com" {
		t.Fatalf("existing provider not preserved: %v", providers)
	}
	if _, ok := providers[providerID]; !ok {
		t.Fatalf("mintrouter provider missing: %v", providers)
	}
	if strings.Index(modelsText, "own:") > strings.Index(modelsText, "mintrouter:") {
		t.Fatalf("existing provider must keep its position:\n%s", modelsText)
	}

	cfgText := readText(t, a.configPath())
	if !strings.Contains(cfgText, "# picked") {
		t.Fatalf("config.yml lost inline comment on modelRoles.default:\n%s", cfgText)
	}
	cfg := readYAML(t, a.configPath())
	if cfg["theme"] != "dark" {
		t.Fatalf("unrelated config key not preserved: %v", cfg)
	}
	chains := cfg["retry"].(map[string]any)["fallbackChains"].(map[string]any)
	if _, ok := chains["default"]; !ok {
		t.Fatalf("nested config key not preserved: %v", cfg)
	}
	if got := roleOf(cfg, roleDefault); got != selectorPrefix+"gpt-mint" {
		t.Fatalf("modelRoles.default = %v, want replaced", got)
	}
	theme, retry, roles := strings.Index(cfgText, "theme:"), strings.Index(cfgText, "retry:"), strings.Index(cfgText, "modelRoles:")
	if !(theme < retry && retry < roles) {
		t.Fatalf("config.yml key order not preserved:\n%s", cfgText)
	}
}

// TestApplySmallFastModel pins the modelRoles.smol handling: written (and the
// small model folded into the catalog) when the profile pins one, removed on
// re-Apply without it, and a user-set smol role never touched.
func TestApplySmallFastModel(t *testing.T) {
	t.Run("writes smol and catalog entry", func(t *testing.T) {
		a, _ := newAdapter(t)
		installed(a)
		p := sampleProfile()
		p.SmallFastModel = "small-mint"
		if _, err := a.Apply(p); err != nil {
			t.Fatalf("apply: %v", err)
		}
		models := readYAML(t, a.modelsPath())
		if got := modelEntryOf(t, models, "small-mint")["id"]; got != "small-mint" {
			t.Fatalf("small model entry = %v", got)
		}
		if n := len(providerOf(t, models)["models"].([]any)); n != 2 {
			t.Fatalf("models list has %d entries, want 2", n)
		}
		cfg := readYAML(t, a.configPath())
		if got := roleOf(cfg, roleSmol); got != selectorPrefix+"small-mint" {
			t.Fatalf("modelRoles.smol = %v, want %q", got, selectorPrefix+"small-mint")
		}
		if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
			t.Fatalf("expected AppliedByMintSwitch, got %v", st)
		}

		p.SmallFastModel = ""
		if _, err := a.Apply(p); err != nil {
			t.Fatalf("re-apply: %v", err)
		}
		cfg = readYAML(t, a.configPath())
		if got := roleOf(cfg, roleSmol); got != nil {
			t.Fatalf("modelRoles.smol = %v, want removed", got)
		}
		if got := roleOf(cfg, roleDefault); got != selectorPrefix+"gpt-mint" {
			t.Fatalf("modelRoles.default = %v, want kept", got)
		}
		if n := len(providerOf(t, readYAML(t, a.modelsPath()))["models"].([]any)); n != 1 {
			t.Fatalf("models list has %d entries after re-apply, want 1", n)
		}
	})

	t.Run("user smol role untouched", func(t *testing.T) {
		a, _ := newAdapter(t)
		installed(a)
		writeFile(t, a.configPath(), "modelRoles:\n  smol: other/y\n")
		if _, err := a.Apply(sampleProfile()); err != nil {
			t.Fatalf("apply: %v", err)
		}
		cfg := readYAML(t, a.configPath())
		if got := roleOf(cfg, roleSmol); got != "other/y" {
			t.Fatalf("user smol role = %v, want other/y", got)
		}
		if got := roleOf(cfg, roleDefault); got != selectorPrefix+"gpt-mint" {
			t.Fatalf("modelRoles.default = %v", got)
		}
	})
}

func TestRestoreDeletesCreatedFiles(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, p := range []string{a.modelsPath(), a.configPath()} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected file created: %v", err)
		}
	}
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Message != "Restored oh-my-pi models.yml and config.yml from backup." {
		t.Fatalf("message = %q", res.Message)
	}
	notExist(t, a.modelsPath())
	notExist(t, a.configPath())
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after restore = %v, want Default", st)
	}
}

func TestRestoreRevertsExisting(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	origModels := "# keep\nproviders:\n  own:\n    baseUrl: https://x\n    api: openai-completions\n    apiKey: k\n    models: []\n"
	origConfig := "theme: dark\n"
	writeFile(t, a.modelsPath(), origModels)
	writeFile(t, a.configPath(), origConfig)
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readText(t, a.modelsPath()); got != origModels {
		t.Fatalf("models.yml not byte-for-byte restored: %q", got)
	}
	if got := readText(t, a.configPath()); got != origConfig {
		t.Fatalf("config.yml not byte-for-byte restored: %q", got)
	}
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after restore = %v, want Default", st)
	}
}

// TestReApplyIdempotent proves a second Apply takes no new snapshot and
// produces byte-identical files.
func TestReApplyIdempotent(t *testing.T) {
	a, r := newAdapter(t)
	installed(a)
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply1: %v", err)
	}
	models1, cfg1 := readText(t, a.modelsPath()), readText(t, a.configPath())
	backups := countBackups(t, r.BackupsDir())
	if backups != 2 {
		t.Fatalf("backups after first apply = %d, want 2 (models + config)", backups)
	}
	res, err := a.Apply(p)
	if err != nil {
		t.Fatalf("apply2: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("re-apply must not report a new backup, got %q", res.BackupPath)
	}
	if got := countBackups(t, r.BackupsDir()); got != backups {
		t.Fatalf("backups after re-apply = %d, want %d", got, backups)
	}
	if got := readText(t, a.modelsPath()); got != models1 {
		t.Fatalf("models.yml changed on re-apply:\n%s\n---\n%s", models1, got)
	}
	if got := readText(t, a.configPath()); got != cfg1 {
		t.Fatalf("config.yml changed on re-apply:\n%s\n---\n%s", cfg1, got)
	}
	if n := len(readYAML(t, a.modelsPath())["providers"].(map[string]any)); n != 1 {
		t.Fatalf("expected single provider after re-apply, got %d", n)
	}
	if st, _, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch after re-apply, got %v", st)
	}
}

func TestRestoreNoBackupNoOp(t *testing.T) {
	a, _ := newAdapter(t)
	res, err := a.Restore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.BackupPath != "" || res.Message != "No backup found; nothing to restore." {
		t.Fatalf("unexpected result: %+v", res)
	}
	notExist(t, a.modelsPath())
	notExist(t, a.configPath())
}

// TestStatusDefaultWhenProviderRemoved proves a store entry alone does not
// report Applied: when providers.mintrouter was removed from models.yml,
// Status falls back to Default.
func TestStatusDefaultWhenProviderRemoved(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	writeFile(t, a.modelsPath(), "providers: {}\n")
	if st, _, _ := a.Status(p); st != core.StatusDefault {
		t.Fatalf("want Default when managed provider block is gone, got %v", st)
	}
}

// TestStatusConfigDrift pins the /model-picker case: models.yml still matches
// the fingerprint but config.yml no longer selects the MintSwitch provider
// (repointed, missing, or unreadable) — ModifiedExternally + configDriftDetail.
func TestStatusConfigDrift(t *testing.T) {
	cases := []struct {
		name   string
		config string
	}{
		{"other provider", "modelRoles:\n  default: anthropic/claude-x\n"},
		{"missing default role", "theme: dark\n"},
		{"missing modelRoles", "modelRoles: ~\n"},
		{"non-scalar default", "modelRoles:\n  default: [a]\n"},
		{"corrupt yaml", "modelRoles: [\n"},
		{"no provider half", "modelRoles:\n  default: gpt-mint\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAdapter(t)
			installed(a)
			p := sampleProfile()
			if _, err := a.Apply(p); err != nil {
				t.Fatalf("apply: %v", err)
			}
			writeFile(t, a.configPath(), tc.config)
			st, detail, err := a.Status(p)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if st != core.StatusModifiedExternally || detail != configDriftDetail {
				t.Fatalf("drift status = %v %q, want ModifiedExternally + configDriftDetail", st, detail)
			}
		})
	}
}

// TestStatusModelOnlyDrift pins the milder case: modelRoles.default still
// selects mintrouter, only the model part changed — modelDriftDetail.
func TestStatusModelOnlyDrift(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.Models = []string{p.Model, "other-mint"}
	p.ApplyAllModels = true
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	writeFile(t, a.configPath(), "modelRoles:\n  default: mintrouter/other-mint\n")
	st, detail, err := a.Status(p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != core.StatusModifiedExternally || detail != modelDriftDetail {
		t.Fatalf("model drift status = %v %q, want ModifiedExternally + modelDriftDetail", st, detail)
	}
}

// TestStatusThinkingSuffixStillApplied proves a thinking-level suffix appended
// by omp's /model picker ("mintrouter/<model>:high") still counts as Applied.
func TestStatusThinkingSuffixStillApplied(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	p := sampleProfile()
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	writeFile(t, a.configPath(), "modelRoles:\n  default: mintrouter/gpt-mint:high\n")
	if st, detail, _ := a.Status(p); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("status with thinking suffix = %v %q, want AppliedByMintSwitch", st, detail)
	}
}

func TestSplitSelector(t *testing.T) {
	cases := []struct {
		in              string
		provider, model string
		ok              bool
	}{
		{"mintrouter/gpt-mint", "mintrouter", "gpt-mint", true},
		{"mintrouter/org/model-v1", "mintrouter", "org/model-v1", true},
		{"mintrouter/gpt-mint:high", "mintrouter", "gpt-mint:high", true},
		{"  mintrouter/gpt-mint\n", "mintrouter", "gpt-mint", true},
		{"gpt-mint", "", "", false},
		{"/gpt-mint", "", "", false},
		{"mintrouter/", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		prov, model, ok := splitSelector(tc.in)
		if ok != tc.ok {
			t.Errorf("splitSelector(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && (prov != tc.provider || model != tc.model) {
			t.Errorf("splitSelector(%q) = %q %q, want %q %q", tc.in, prov, model, tc.provider, tc.model)
		}
	}
}

func TestModelMatches(t *testing.T) {
	cases := []struct {
		got, want string
		match     bool
	}{
		{"qwen", "qwen", true},
		{"qwen:high", "qwen", true},
		{"qwen:max", "qwen:max", true},
		{"qwen:max", "qwen", true},
		{"qwen", "qwen:max", false},
		{"qwen:max:high", "qwen:max", true},
		{"other", "qwen", false},
		{"qwen-2:high", "qwen", false},
		{":high", "", false},
	}
	for _, tc := range cases {
		if got := modelMatches(tc.got, tc.want); got != tc.match {
			t.Errorf("modelMatches(%q, %q) = %v, want %v", tc.got, tc.want, got, tc.match)
		}
	}
}

// TestRestoreNoBackupStripsManagedKeys covers the missing-backup fallback:
// Restore must strip providers.mintrouter and the mintrouter/ roles while
// preserving every other key and comment, dropping a map only when it empties.
func TestRestoreNoBackupStripsManagedKeys(t *testing.T) {
	t.Run("preserves other keys and comments", func(t *testing.T) {
		a, r := newAdapter(t)
		installed(a)
		writeFile(t, a.modelsPath(), "# mine\nproviders:\n  own:\n    baseUrl: https://x # trailing\n    api: openai-completions\n    apiKey: k\n    models: []\n")
		writeFile(t, a.configPath(), "theme: dark\nmodelRoles:\n  slow: other/z\n")
		p := sampleProfile()
		p.SmallFastModel = "small-mint"
		if _, err := a.Apply(p); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if err := os.RemoveAll(r.BackupsDir()); err != nil {
			t.Fatal(err)
		}
		res, err := a.Restore()
		if err != nil {
			t.Fatalf("restore: %v", err)
		}
		want := "No backup found; removed the MintSwitch-managed keys from the oh-my-pi config files."
		if res.Message != want {
			t.Fatalf("message = %q, want %q", res.Message, want)
		}
		modelsText := readText(t, a.modelsPath())
		if !strings.Contains(modelsText, "# mine") || !strings.Contains(modelsText, "# trailing") {
			t.Fatalf("models.yml comments lost:\n%s", modelsText)
		}
		providers := readYAML(t, a.modelsPath())["providers"].(map[string]any)
		if _, present := providers[providerID]; present {
			t.Fatalf("mintrouter must be stripped: %v", providers)
		}
		if _, ok := providers["own"]; !ok {
			t.Fatalf("user provider must be preserved: %v", providers)
		}
		cfg := readYAML(t, a.configPath())
		if cfg["theme"] != "dark" {
			t.Fatalf("user config key must be preserved: %v", cfg)
		}
		if roleOf(cfg, roleDefault) != nil || roleOf(cfg, roleSmol) != nil {
			t.Fatalf("mintrouter roles must be stripped: %v", cfg)
		}
		if roleOf(cfg, "slow") != "other/z" {
			t.Fatalf("user role must be preserved: %v", cfg)
		}
		if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
			t.Fatalf("status after strip = %v, want Default", st)
		}
	})

	t.Run("drops emptied maps", func(t *testing.T) {
		a, r := newAdapter(t)
		installed(a)
		writeFile(t, a.modelsPath(), "other: 1\n")
		writeFile(t, a.configPath(), "theme: dark\n")
		if _, err := a.Apply(sampleProfile()); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if err := os.RemoveAll(r.BackupsDir()); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Restore(); err != nil {
			t.Fatalf("restore: %v", err)
		}
		models := readYAML(t, a.modelsPath())
		if _, present := models["providers"]; present || models["other"] != 1 {
			t.Fatalf("empty providers map must be dropped, other kept: %v", models)
		}
		cfg := readYAML(t, a.configPath())
		if _, present := cfg["modelRoles"]; present || cfg["theme"] != "dark" {
			t.Fatalf("empty modelRoles map must be dropped, theme kept: %v", cfg)
		}
	})
}

// TestOrphanStatusAndRestore covers the lost-marker gap: marker gone but
// models.yml still carries the MintSwitch provider — Status must report
// ModifiedExternally (orphanDetail) and Restore must still revert/strip.
func TestOrphanStatusAndRestore(t *testing.T) {
	origModels := "providers:\n  own:\n    baseUrl: https://x\n    api: openai-completions\n    apiKey: k\n    models: []\n"
	setup := func(t *testing.T) (*Adapter, *paths.Resolver) {
		t.Helper()
		a, r := newAdapter(t)
		installed(a)
		writeFile(t, a.modelsPath(), origModels)
		if _, err := a.Apply(sampleProfile()); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if err := a.m.Delete(id); err != nil {
			t.Fatal(err)
		}
		st, detail, err := a.Status(sampleProfile())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if st != core.StatusModifiedExternally || detail != orphanDetail {
			t.Fatalf("orphan status = %v %q, want ModifiedExternally + orphanDetail", st, detail)
		}
		return a, r
	}

	t.Run("with backup reverts", func(t *testing.T) {
		a, _ := setup(t)
		if _, err := a.Restore(); err != nil {
			t.Fatalf("restore: %v", err)
		}
		if got := readText(t, a.modelsPath()); got != origModels {
			t.Fatalf("models.yml not byte-for-byte restored: %q", got)
		}
		notExist(t, a.configPath())
	})

	t.Run("without backup strips", func(t *testing.T) {
		a, r := setup(t)
		if err := os.RemoveAll(r.BackupsDir()); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Restore(); err != nil {
			t.Fatalf("restore: %v", err)
		}
		providers := readYAML(t, a.modelsPath())["providers"].(map[string]any)
		if _, present := providers[providerID]; present {
			t.Fatalf("mintrouter must be stripped: %v", providers)
		}
		if _, ok := providers["own"]; !ok {
			t.Fatalf("user provider must be preserved: %v", providers)
		}
		cfg := readYAML(t, a.configPath())
		if _, present := cfg["modelRoles"]; present {
			t.Fatalf("mintrouter roles must be stripped: %v", cfg)
		}
		if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
			t.Fatalf("status after orphan restore = %v, want Default", st)
		}
	})
}

// TestPureUserConfigNeverOrphan proves the no-false-positive contract: a
// hand-written config that never saw Apply stays Default and Restore leaves
// both files byte-for-byte untouched.
func TestPureUserConfigNeverOrphan(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	userModels := "providers:\n  own:\n    baseUrl: https://x\n    api: openai-completions\n    apiKey: k\n    models: []\n"
	userConfig := "modelRoles:\n  default: own/m1\n"
	writeFile(t, a.modelsPath(), userModels)
	writeFile(t, a.configPath(), userConfig)
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("pure user config status = %v, want Default", st)
	}
	if _, err := a.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readText(t, a.modelsPath()); got != userModels {
		t.Fatalf("pure user models.yml rewritten: %q", got)
	}
	if got := readText(t, a.configPath()); got != userConfig {
		t.Fatalf("pure user config.yml rewritten: %q", got)
	}
}

// TestApplyConfigWriteFailureRollsBackModels pins the two-file atomicity
// contract: when the config.yml write fails, models.yml must be rolled back to
// its pre-Apply bytes (no leaked API key) and no marker recorded.
func TestApplyConfigWriteFailureRollsBackModels(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	orig := "providers:\n  own:\n    baseUrl: https://x\n    api: openai-completions\n    apiKey: k\n    models: []\n"
	writeFile(t, a.modelsPath(), orig)
	a.writeConfig = func(string, *document) error { return errors.New("disk full") }
	if _, err := a.Apply(sampleProfile()); err == nil {
		t.Fatal("expected apply error")
	}
	if got := readText(t, a.modelsPath()); got != orig {
		t.Fatalf("models.yml not rolled back: %q", got)
	}
	if _, ok, _ := a.m.Get(id); ok {
		t.Fatal("marker must not be recorded on failed apply")
	}
	notExist(t, a.configPath())
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusDefault {
		t.Fatalf("status after failed apply = %v, want Default", st)
	}
}

// TestApplyConfigWriteFailureRemovesCreatedModels is the created-file variant
// of the rollback: models.yml did not exist before Apply, so a failed
// config.yml write must delete it entirely.
func TestApplyConfigWriteFailureRemovesCreatedModels(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	a.writeConfig = func(string, *document) error { return errors.New("disk full") }
	if _, err := a.Apply(sampleProfile()); err == nil {
		t.Fatal("expected apply error")
	}
	notExist(t, a.modelsPath())
	if _, ok, _ := a.m.Get(id); ok {
		t.Fatal("marker must not be recorded on failed apply")
	}
}

func TestApplyInvalidProfile(t *testing.T) {
	a, r := newAdapter(t)
	installed(a)
	p := sampleProfile()
	p.APIKey = ""
	if _, err := a.Apply(p); err == nil {
		t.Fatal("expected validation error")
	}
	notExist(t, a.modelsPath())
	notExist(t, a.configPath())
	if n := countBackups(t, r.BackupsDir()); n != 0 {
		t.Fatalf("invalid apply must not take backups, got %d", n)
	}
	if _, ok, _ := a.m.Get(id); ok {
		t.Fatal("marker must not be recorded on invalid apply")
	}
}

// TestApplyUsesYamlSpelling proves the .yaml fallback: when only
// models.yaml/config.yaml exist, Apply edits those and never creates .yml.
func TestApplyUsesYamlSpelling(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	modelsYAML := filepath.Join(a.agentDir(), "models.yaml")
	configYAML := filepath.Join(a.agentDir(), "config.yaml")
	writeFile(t, modelsYAML, "providers:\n  own:\n    baseUrl: https://x\n")
	writeFile(t, configYAML, "theme: dark\n")
	if a.modelsPath() != modelsYAML || a.configPath() != configYAML {
		t.Fatalf("paths = %q %q, want .yaml spellings", a.modelsPath(), a.configPath())
	}
	res, err := a.Apply(sampleProfile())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ChangedPath != modelsYAML {
		t.Fatalf("ChangedPath = %q, want %q", res.ChangedPath, modelsYAML)
	}
	providerOf(t, readYAML(t, modelsYAML))
	if got := roleOf(readYAML(t, configYAML), roleDefault); got != selectorPrefix+"gpt-mint" {
		t.Fatalf("config.yaml modelRoles.default = %v", got)
	}
	notExist(t, filepath.Join(a.agentDir(), "models.yml"))
	notExist(t, filepath.Join(a.agentDir(), "config.yml"))
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch, got %v", st)
	}
}

// TestApplySeedsFromLegacyJSON proves that creating the YAML files seeds them
// from omp's legacy models.json / settings.json (which are left untouched),
// rendering whole-number JSON floats as integers.
func TestApplySeedsFromLegacyJSON(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	legacyModels := `{"providers":{"spark":{"baseUrl":"https://spark.example.com","api":"openai-completions","apiKey":"k","models":[{"id":"m","contextWindow":200000,"reasoning":true,"input":["text"]}]}}}`
	legacySettings := `{"theme":"dark"}`
	writeFile(t, a.legacyModelsPath(), legacyModels)
	writeFile(t, a.legacySettingsPath(), legacySettings)
	p := sampleProfile()
	p.ModelInputModalities = map[string][]string{"gpt-mint": {"text", "image"}}
	p.ModelReasoningLevels = map[string][]string{"gpt-mint": {"high"}}
	if _, err := a.Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	modelsText := readText(t, a.modelsPath())
	if strings.Contains(modelsText, "2e+05") || !strings.Contains(modelsText, "contextWindow: 200000") {
		t.Fatalf("legacy contextWindow not rendered as integer:\n%s", modelsText)
	}
	models := readYAML(t, a.modelsPath())
	providers := models["providers"].(map[string]any)
	spark, ok := providers["spark"].(map[string]any)
	if !ok || spark["baseUrl"] != "https://spark.example.com" {
		t.Fatalf("legacy provider not seeded: %v", providers)
	}
	sparkModel := spark["models"].([]any)[0].(map[string]any)
	if sparkModel["reasoning"] != true || !slices.Equal(inputOf(t, sparkModel), []string{"text"}) {
		t.Fatalf("legacy model input/reasoning not seeded verbatim: %v", sparkModel)
	}
	if _, ok := providers[providerID]; !ok {
		t.Fatalf("mintrouter provider missing: %v", providers)
	}
	mint := modelEntryOf(t, models, "gpt-mint")
	if mint["reasoning"] != true || !slices.Equal(inputOf(t, mint), []string{"text", "image"}) {
		t.Fatalf("mintrouter entry input/reasoning wrong on legacy-seeded file: %v", mint)
	}
	cfgText := readText(t, a.configPath())
	if !strings.Contains(cfgText, "theme: dark") {
		t.Fatalf("legacy settings not seeded:\n%s", cfgText)
	}
	if got := roleOf(readYAML(t, a.configPath()), roleDefault); got != selectorPrefix+"gpt-mint" {
		t.Fatalf("modelRoles.default = %v", got)
	}
	if got := readText(t, a.legacyModelsPath()); got != legacyModels {
		t.Fatalf("legacy models.json modified: %q", got)
	}
	if got := readText(t, a.legacySettingsPath()); got != legacySettings {
		t.Fatalf("legacy settings.json modified: %q", got)
	}
}

// TestApplyRejectsNonMappingProviders proves a models.yml whose top-level
// providers is a sequence fails Apply before any write or backup.
func TestApplyRejectsNonMappingProviders(t *testing.T) {
	a, r := newAdapter(t)
	installed(a)
	origModels := "providers:\n  - a\n"
	origConfig := "theme: dark\n"
	writeFile(t, a.modelsPath(), origModels)
	writeFile(t, a.configPath(), origConfig)
	_, err := a.Apply(sampleProfile())
	if err == nil || !strings.Contains(err.Error(), `"providers" is not a mapping`) {
		t.Fatalf("apply err = %v, want not-a-mapping error", err)
	}
	if got := readText(t, a.modelsPath()); got != origModels {
		t.Fatalf("models.yml modified: %q", got)
	}
	if got := readText(t, a.configPath()); got != origConfig {
		t.Fatalf("config.yml modified: %q", got)
	}
	if n := countBackups(t, r.BackupsDir()); n != 0 {
		t.Fatalf("failed apply must not take backups, got %d", n)
	}
	if _, ok, _ := a.m.Get(id); ok {
		t.Fatal("marker must not be recorded on failed apply")
	}
}

// TestApplyRejectsMultiDocument proves a multi-document YAML stream fails
// Apply and leaves both files untouched.
func TestApplyRejectsMultiDocument(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	multi := "a: 1\n---\nb: 2\n"
	writeFile(t, a.modelsPath(), multi)
	_, err := a.Apply(sampleProfile())
	if err == nil || !strings.Contains(err.Error(), "multi-document") {
		t.Fatalf("apply err = %v, want multi-document error", err)
	}
	if got := readText(t, a.modelsPath()); got != multi {
		t.Fatalf("models.yml modified: %q", got)
	}
	notExist(t, a.configPath())
}

// TestEmptyDocumentsTreatedAsMapping proves an empty file, a comment-only
// file and a null document all parse to an empty mapping and Apply succeeds.
func TestEmptyDocumentsTreatedAsMapping(t *testing.T) {
	cases := []struct{ name, content string }{
		{"empty", ""},
		{"comment only", "# comment only\n"},
		{"null", "~\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := parseDocument("x.yml", []byte(tc.content))
			if err != nil {
				t.Fatalf("parseDocument: %v", err)
			}
			if d.root.Kind != yaml.MappingNode || len(d.root.Content) != 0 {
				t.Fatalf("root = kind %v with %d children, want empty mapping", d.root.Kind, len(d.root.Content))
			}

			a, _ := newAdapter(t)
			installed(a)
			writeFile(t, a.modelsPath(), tc.content)
			writeFile(t, a.configPath(), tc.content)
			if _, err := a.Apply(sampleProfile()); err != nil {
				t.Fatalf("apply: %v", err)
			}
			providerOf(t, readYAML(t, a.modelsPath()))
			if got := roleOf(readYAML(t, a.configPath()), roleDefault); got != selectorPrefix+"gpt-mint" {
				t.Fatalf("modelRoles.default = %v", got)
			}
		})
	}
}

// TestApplyFlowEmptyProvidersBecomesBlock proves a flow-style "providers: {}"
// switches to block style once the provider block is inserted.
func TestApplyFlowEmptyProvidersBecomesBlock(t *testing.T) {
	a, _ := newAdapter(t)
	installed(a)
	writeFile(t, a.modelsPath(), "providers: {}\n")
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	text := readText(t, a.modelsPath())
	if !strings.Contains(text, "  mintrouter:\n") {
		t.Fatalf("provider not rendered in block style:\n%s", text)
	}
	if strings.Contains(text, "{") {
		t.Fatalf("flow style leaked into output:\n%s", text)
	}
}

func TestNodeHelpers(t *testing.T) {
	parse := func(t *testing.T, src string) *document {
		t.Helper()
		d, err := parseDocument("x.yml", []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	t.Run("mapSet keeps position and line comment", func(t *testing.T) {
		d := parse(t, "a: 1\nb: 2 # keep\nc: 3\n")
		mapSet(d.root, "b", strNode("new"))
		if len(d.root.Content) != 6 {
			t.Fatalf("content len = %d, want 6", len(d.root.Content))
		}
		if d.root.Content[2].Value != "b" || d.root.Content[3].Value != "new" {
			t.Fatalf("b not replaced in place: %v", d.root.Content)
		}
		if d.root.Content[3].LineComment != "# keep" {
			t.Fatalf("line comment = %q, want carried over", d.root.Content[3].LineComment)
		}
	})

	t.Run("mapSet appends new key", func(t *testing.T) {
		d := parse(t, "a: 1\n")
		mapSet(d.root, "z", strNode("v"))
		if len(d.root.Content) != 4 || d.root.Content[2].Value != "z" || d.root.Content[3].Value != "v" {
			t.Fatalf("z not appended: %v", d.root.Content)
		}
	})

	t.Run("mapSet clears flow style on empty map", func(t *testing.T) {
		d := parse(t, "m: {}\n")
		m := mapGet(d.root, "m")
		mapSet(m, "k", strNode("v"))
		if m.Style&yaml.FlowStyle != 0 {
			t.Fatal("flow style must be cleared once the map gains a pair")
		}
	})

	t.Run("mapGet", func(t *testing.T) {
		d := parse(t, "a: 1\nseq: [x]\n")
		if mapGet(nil, "a") != nil {
			t.Fatal("mapGet(nil) must be nil")
		}
		if mapGet(mapGet(d.root, "seq"), "a") != nil {
			t.Fatal("mapGet on a sequence must be nil")
		}
		if mapGet(d.root, "missing") != nil {
			t.Fatal("mapGet missing key must be nil")
		}
		if got := scalarString(mapGet(d.root, "a")); got != "1" {
			t.Fatalf("mapGet a = %q", got)
		}
	})

	t.Run("mapDelete", func(t *testing.T) {
		if mapDelete(nil, "x") {
			t.Fatal("mapDelete(nil) must be false")
		}
		d := parse(t, "a: 1\nb: 2\nseq: [x]\n")
		if mapDelete(mapGet(d.root, "seq"), "a") {
			t.Fatal("mapDelete on a sequence must be false")
		}
		if mapDelete(d.root, "missing") {
			t.Fatal("mapDelete missing key must be false")
		}
		if !mapDelete(d.root, "a") {
			t.Fatal("mapDelete present key must be true")
		}
		if mapGet(d.root, "a") != nil || mapGet(d.root, "b") == nil || len(d.root.Content) != 4 {
			t.Fatalf("mapDelete left wrong content: %v", d.root.Content)
		}
	})

	t.Run("ensureMapping creates when absent", func(t *testing.T) {
		d := parse(t, "a: 1\n")
		m, err := ensureMapping(d.root, "providers", "models.yml")
		if err != nil || m == nil || m.Kind != yaml.MappingNode {
			t.Fatalf("ensureMapping = %v, %v", m, err)
		}
		if mapGet(d.root, "providers") != m {
			t.Fatal("created mapping must be attached under the key")
		}
	})

	t.Run("ensureMapping replaces null with mapping", func(t *testing.T) {
		d := parse(t, "# head\nproviders: ~\n")
		m, err := ensureMapping(d.root, "providers", "models.yml")
		if err != nil || m == nil || m.Kind != yaml.MappingNode {
			t.Fatalf("ensureMapping = %v, %v", m, err)
		}
		if got := mapGet(d.root, "providers"); got != m || got.Kind != yaml.MappingNode {
			t.Fatalf("providers = %v, want the new mapping", got)
		}
	})

	t.Run("ensureMapping returns existing mapping", func(t *testing.T) {
		d := parse(t, "providers:\n  own: {}\n")
		m, err := ensureMapping(d.root, "providers", "models.yml")
		if err != nil || m != mapGet(d.root, "providers") || mapGet(m, "own") == nil {
			t.Fatalf("ensureMapping = %v, %v", m, err)
		}
	})

	t.Run("ensureMapping errors on scalar and sequence", func(t *testing.T) {
		for _, src := range []string{"providers: foo\n", "providers:\n  - a\n"} {
			d := parse(t, src)
			m, err := ensureMapping(d.root, "providers", filepath.Join("dir", "models.yml"))
			if err == nil || m != nil {
				t.Fatalf("ensureMapping(%q) = %v, %v, want error", src, m, err)
			}
			if !strings.Contains(err.Error(), `models.yml: "providers" is not a mapping`) {
				t.Fatalf("error = %q, want file-named message", err)
			}
		}
	})
}
