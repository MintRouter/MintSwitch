package enhance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
	"mintswitch/internal/settings"
)

func newManager(t *testing.T) (*Manager, *paths.Resolver) {
	t.Helper()
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	e := backup.NewEngine(r.BackupsDir())
	return New(r, e, "/Applications/Mint Switch.app/Contents/MacOS/MintSwitch"), r
}

func TestPathsPerTool(t *testing.T) {
	m, r := newManager(t)
	want := map[string]string{
		"claude-code": filepath.Join(r.Home, ".claude", "commands", "enhance.md"),
		"codex":       filepath.Join(r.Home, ".codex", "prompts", "enhance.md"),
		"opencode":    filepath.Join(r.Home, ".config", "opencode", "commands", "enhance.md"),
		"pi":          filepath.Join(r.Home, ".pi", "agent", "prompts", "enhance.md"),
	}
	for id, p := range want {
		got, ok := m.Path(id)
		if !ok || got != p {
			t.Errorf("%s: path = %q, %v; want %q", id, got, ok, p)
		}
	}
	if m.Supports("claude-desktop") {
		t.Error("claude-desktop must not be supported")
	}
	if _, ok := m.Path("nope"); ok {
		t.Error("unknown tool must be unsupported")
	}
}

func TestRenderEmbedsQuotedBinaryAndTool(t *testing.T) {
	m, _ := newManager(t)
	for _, id := range []string{"claude-code", "codex", "opencode", "pi"} {
		out := string(m.Render(id))
		if !strings.HasPrefix(out, "---\n") {
			t.Errorf("%s: missing frontmatter", id)
		}
		if !strings.Contains(out, marker) {
			t.Errorf("%s: missing marker", id)
		}
		if !strings.Contains(out, "'/Applications/Mint Switch.app/Contents/MacOS/MintSwitch' enhance-prompt --tool "+id) {
			t.Errorf("%s: binary path not quoted/embedded:\n%s", id, out)
		}
		if !strings.Contains(out, "$ARGUMENTS") {
			t.Errorf("%s: missing $ARGUMENTS", id)
		}
	}
	// Tools with shell substitution call it inline; the others instruct the agent.
	if !strings.Contains(string(m.Render("claude-code")), "!`printf") {
		t.Error("claude-code must use inline !`cmd` substitution")
	}
	if !strings.Contains(string(m.Render("claude-code")), "allowed-tools: Bash(") {
		t.Error("claude-code must pre-allow the Bash call")
	}
	if strings.Contains(string(m.Render("codex")), "!`") {
		t.Error("codex has no shell substitution; must not use !`cmd`")
	}
	if m.Render("claude-desktop") != nil {
		t.Error("unsupported tool must render nil")
	}
}

func TestInstallStatusRemoveRoundTrip(t *testing.T) {
	m, _ := newManager(t)
	p, _ := m.Path("codex")
	if st, _, _ := m.Status("codex"); st != StatusNotInstalled {
		t.Fatalf("initial status = %s", st)
	}
	res, err := m.Install("codex")
	if err != nil {
		t.Fatal(err)
	}
	if res.ChangedPath != p || res.BackupPath == "" || !strings.HasSuffix(res.BackupPath, ".absent") {
		t.Errorf("install result = %+v", res)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o", fi.Mode().Perm())
	}
	if st, _, _ := m.Status("codex"); st != StatusInstalled {
		t.Fatalf("status after install = %s", st)
	}
	// Idempotent: no second backup, same content.
	res2, err := m.Install("codex")
	if err != nil || res2.BackupPath != "" {
		t.Fatalf("second install = %+v, %v", res2, err)
	}
	// Moving the binary makes the file outdated.
	m2 := New(m.r, m.e, "/usr/local/bin/MintSwitch")
	if st, _, _ := m2.Status("codex"); st != StatusOutdated {
		t.Fatalf("status with moved binary = %s", st)
	}
	if _, err := m2.Install("codex"); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := m2.Status("codex"); st != StatusInstalled {
		t.Fatalf("status after reinstall = %s", st)
	}
	// Remove restores the pristine (absent) state.
	rr, err := m2.Remove("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("file still present after remove: %v (%+v)", err, rr)
	}
	if st, _, _ := m2.Status("codex"); st != StatusNotInstalled {
		t.Fatalf("status after remove = %s", st)
	}
	// Remove again is a no-op.
	if _, err := m2.Remove("codex"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestInstallBacksUpForeignFileAndRemoveRestoresIt(t *testing.T) {
	m, _ := newManager(t)
	p, _ := m.Path("opencode")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("my own enhance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := m.Status("opencode"); st != StatusForeign {
		t.Fatalf("status = %s", st)
	}
	if _, err := m.Install("opencode"); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := m.Status("opencode"); st != StatusInstalled {
		t.Fatalf("status = %s", st)
	}
	if _, err := m.Remove("opencode"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "my own enhance\n" {
		t.Fatalf("foreign file not restored: %q, %v", got, err)
	}
}

func TestRemoveRefusesForeignFileWithoutBackup(t *testing.T) {
	m, _ := newManager(t)
	p, _ := m.Path("pi")
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("hand written\n"), 0o600)
	if _, err := m.Remove("pi"); err == nil {
		t.Fatal("expected error removing a foreign file with no backup")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("foreign file must be left in place")
	}
}

func TestUnsupportedToolErrors(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Install("claude-desktop"); err == nil {
		t.Error("Install must fail for claude-desktop")
	}
	if _, err := m.Remove("claude-desktop"); err == nil {
		t.Error("Remove must fail for claude-desktop")
	}
	if _, err := New(m.r, m.e, "").Install("codex"); err == nil {
		t.Error("Install must fail without an executable path")
	}
}

func TestEndpointURL(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8318/v1":  "http://localhost:8318/v1/enhance-prompt",
		"http://localhost:8318/v1/": "http://localhost:8318/v1/enhance-prompt",
		"http://localhost:8318":     "http://localhost:8318/v1/enhance-prompt",
		"https://r.example/api/v1":  "https://r.example/api/v1/enhance-prompt",
		"https://r.example/v1beta":  "https://r.example/v1beta/v1/enhance-prompt",
	}
	for in, want := range cases {
		if got := EndpointURL(in); got != want {
			t.Errorf("EndpointURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunCLIUsesToolProviderAndKey(t *testing.T) {
	var gotAuth, gotPath, gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		gotPrompt = body["prompt"]
		json.NewEncoder(w).Encode(map[string]string{"enhanced_prompt": "ENHANCED"})
	}))
	defer srv.Close()

	store := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	st := &settings.State{
		Providers: []core.Provider{
			{ID: "a", Name: "A", APIKey: "key-a", BaseURL: "http://127.0.0.1:1/v1", Model: "m"},
			{ID: "b", Name: "B", APIKey: "key-b", BaseURL: srv.URL + "/v1", Model: "m"},
		},
		ActiveProviderID: "a",
		ToolProviders:    map[string]string{"codex": "b"},
	}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err := RunCLI(context.Background(), store, "codex", strings.NewReader("  fix the bug \n"), &out, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "ENHANCED\n" {
		t.Errorf("stdout = %q", out.String())
	}
	if gotAuth != "Bearer key-b" || gotPath != "/v1/enhance-prompt" || gotPrompt != "fix the bug" {
		t.Errorf("request: auth=%q path=%q prompt=%q", gotAuth, gotPath, gotPrompt)
	}
}

func TestRunCLIErrors(t *testing.T) {
	store := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	ctx := context.Background()
	if err := RunCLI(ctx, store, "", strings.NewReader("x"), io.Discard, nil); err == nil {
		t.Error("missing --tool must fail")
	}
	if err := RunCLI(ctx, store, "claude-desktop", strings.NewReader("x"), io.Discard, nil); err == nil {
		t.Error("unsupported tool must fail")
	}
	if err := RunCLI(ctx, store, "codex", strings.NewReader("  \n"), io.Discard, nil); err == nil {
		t.Error("empty prompt must fail")
	}
	if err := RunCLI(ctx, store, "codex", strings.NewReader("x"), io.Discard, nil); err == nil || !strings.Contains(err.Error(), "no provider") {
		t.Errorf("no provider: %v", err)
	}
	store.Save(&settings.State{Providers: []core.Provider{{ID: "a", Name: "A", BaseURL: "http://127.0.0.1:1/v1", Model: "m"}}, ActiveProviderID: "a"})
	if err := RunCLI(ctx, store, "codex", strings.NewReader("x"), io.Discard, nil); err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Errorf("no key: %v", err)
	}
}

func TestEnhanceSurfacesServerErrorWithoutKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"message":"scope prompt:enhance required"}}`)
	}))
	defer srv.Close()
	_, err := Enhance(context.Background(), srv.Client(), srv.URL+"/v1", "secret-key", "p")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "scope prompt:enhance required") {
		t.Errorf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Error("error must not carry the key")
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv2.Close()
	if _, err := Enhance(context.Background(), srv2.Client(), srv2.URL, "k", "p"); err == nil {
		t.Error("missing enhanced_prompt must fail")
	}
}
