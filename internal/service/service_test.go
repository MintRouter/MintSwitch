package service

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/installer"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
	"mintswitch/internal/settings"
)

// fakeRunner is a fake installer.CommandRunner: it records the npm invocation
// and returns a canned result so service tests never run real npm.
type fakeRunner struct {
	out  string
	err  error
	runs int
}

func (f *fakeRunner) Run(_ context.Context, _ string, _ ...string) (string, error) {
	f.runs++
	return f.out, f.err
}

func okLook(string) (string, error)   { return "/usr/bin/npm", nil }
func missLook(string) (string, error) { return "", errors.New("not found") }

// newInstallService builds a Service whose installer uses the given fake runner
// and npm-lookup behaviour, over a temp settings store and the given adapters.
func newInstallService(t *testing.T, runner *fakeRunner, look func(string) (string, error), adapters ...*fakeAdapter) *Service {
	t.Helper()
	r := core.NewRegistry()
	for _, a := range adapters {
		r.Register(a)
	}
	store := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	return NewWithInstaller(r, store, installer.NewWithLookPath(runner, look))
}

// fakeAdapter is a configurable core.ToolAdapter used to exercise the Service
// without touching real tool config files. It records the profile last applied
// and how many times Apply/Restore were called.
type fakeAdapter struct {
	id, name    string
	paths       []string
	installed   bool
	active      string
	status      core.ToolStatus
	detail      string
	statusErr   error
	applyErr    error
	restoreErr  error
	applyRes    core.ApplyResult
	restoreRes  core.RestoreResult
	lastApplied *core.Profile
	applyCalls  int
	restCalls   int
	// statusModel, when non-empty, makes Status simulate a Model-bearing
	// fingerprint: it reports applied_by_mintswitch only when the profile it is
	// evaluated against carries this Model, else modified_externally. This proves
	// viewFor evaluates status with the EFFECTIVE per-tool model.
	statusModel string
}

func (f *fakeAdapter) ID() string            { return f.id }
func (f *fakeAdapter) Name() string          { return f.name }
func (f *fakeAdapter) ConfigPaths() []string { return f.paths }
func (f *fakeAdapter) Detect() (bool, string) {
	return f.installed, f.active
}
func (f *fakeAdapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	if f.statusModel != "" {
		if p.Model == f.statusModel {
			return core.StatusAppliedByMintSwitch, "applied", nil
		}
		return core.StatusModifiedExternally, "modified", nil
	}
	return f.status, f.detail, f.statusErr
}
func (f *fakeAdapter) Apply(p core.Profile) (core.ApplyResult, error) {
	f.applyCalls++
	cp := p
	f.lastApplied = &cp
	return f.applyRes, f.applyErr
}
func (f *fakeAdapter) Restore() (core.RestoreResult, error) {
	f.restCalls++
	return f.restoreRes, f.restoreErr
}

// newTestService builds a Service over the given fake adapters and a settings
// store rooted at a temp dir, so no real ~/.claude etc. is ever touched.
func newTestService(t *testing.T, adapters ...*fakeAdapter) *Service {
	t.Helper()
	reg := core.NewRegistry()
	for _, a := range adapters {
		reg.Register(a)
	}
	store := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	return NewWithRegistry(reg, store)
}

// validProvider is a valid provider (no ID: AddProvider assigns one) for
// provider-management and apply tests.
func validProvider() core.Provider {
	return core.Provider{
		Name:    "Test",
		APIKey:  "sk-test",
		BaseURL: "https://api.example.com/v1",
		Model:   "gpt-test",
	}
}

// addProvider adds p via the service and returns its generated ID.
func addProvider(t *testing.T, svc *Service, p core.Provider) string {
	t.Helper()
	id, err := svc.AddProvider(p)
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	return id
}

// reg builds a registry from adapters for ad-hoc service construction in tests.
func reg(adapters ...*fakeAdapter) *core.Registry {
	r := core.NewRegistry()
	for _, a := range adapters {
		r.Register(a)
	}
	return r
}

// storeFrom returns the settings store backing an existing test Service so a
// second Service can read the same persisted state.
func storeFrom(s *Service) *settings.Store { return s.store }

// equalStrings reports whether two string slices are element-wise equal.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListTools(t *testing.T) {
	a := &fakeAdapter{
		id: "alpha", name: "Alpha", installed: true,
		status: core.StatusAppliedByMintSwitch, detail: "applied",
		paths: []string{"/tmp/a.json"},
	}
	b := &fakeAdapter{
		id: "beta", name: "Beta", installed: false,
		status: core.StatusDefault, detail: "ignored",
		statusErr: errors.New("status boom"),
	}
	svc := newTestService(t, a, b)

	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("want 2 views, got %d", len(views))
	}
	if views[0].ID != "alpha" || views[1].ID != "beta" {
		t.Fatalf("registration order not preserved: %+v", views)
	}
	if !views[0].Installed || views[0].Status != "applied_by_mintswitch" ||
		views[0].Detail != "applied" || len(views[0].ConfigPaths) != 1 {
		t.Fatalf("alpha view mapped wrong: %+v", views[0])
	}
	// A Status error must surface in Detail, not abort the whole list.
	if views[1].Detail != "status boom" {
		t.Fatalf("beta detail = %q, want status error", views[1].Detail)
	}
}

// TestRemovedToolOverridesIgnored: settings persisted by older builds may
// still carry per-tool overrides for tools whose adapters were removed
// (droid, zed, kilo). Those stale entries must be ignored: Load and ListTools
// succeed, and the remaining tools are unaffected.
func TestRemovedToolOverridesIgnored(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha", installed: true, status: core.StatusDefault}
	store := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	p := validProvider()
	p.ID = "p1"
	if err := store.Save(&settings.State{
		Providers:        []core.Provider{p},
		ActiveProviderID: "p1",
		ToolModels:       map[string]string{"droid": "gpt-test", "zed": "gpt-test", "kilo": "gpt-test"},
		ToolProviders:    map[string]string{"droid": "p1", "zed": "p1", "kilo": "p1"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	svc := NewWithRegistry(reg(a), store)

	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(views) != 1 || views[0].ID != "alpha" {
		t.Fatalf("want only alpha, got %+v", views)
	}
	if views[0].ProviderName != "Test" || views[0].ProviderOverridden {
		t.Fatalf("alpha must use the active provider, got %+v", views[0])
	}
}

func TestAddProviderValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*core.Provider)
		wantErr bool
	}{
		{"valid", func(*core.Provider) {}, false},
		{"missing name", func(p *core.Provider) { p.Name = "  " }, true},
		{"missing model", func(p *core.Provider) { p.Model = "" }, true},
		{"missing key", func(p *core.Provider) { p.APIKey = "" }, true},
		{"bad url", func(p *core.Provider) { p.BaseURL = "://nope" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			p := validProvider()
			tt.mutate(&p)
			_, err := svc.AddProvider(p)
			if tt.wantErr != (err != nil) {
				t.Fatalf("AddProvider(%+v) err=%v, wantErr=%v", p, err, tt.wantErr)
			}
		})
	}
}

// TestAddProviderFirstBecomesActive: the first provider added is active; a
// second one is not, and views never carry key values.
func TestAddProviderFirstBecomesActive(t *testing.T) {
	svc := newTestService(t)
	id1 := addProvider(t, svc, validProvider())
	p2 := validProvider()
	p2.Name = "Second"
	p2.Note = "backup endpoint"
	id2 := addProvider(t, svc, p2)

	views, err := svc.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("want 2 providers, got %+v", views)
	}
	if views[0].ID != id1 || !views[0].Active || views[1].ID != id2 || views[1].Active {
		t.Fatalf("active flags wrong: %+v", views)
	}
	if views[1].Note != "backup endpoint" {
		t.Fatalf("note not surfaced: %+v", views[1])
	}
	if !views[0].HasKey {
		t.Fatal("HasKey=false after key was stored")
	}
	if id1 == id2 {
		t.Fatal("generated provider IDs must be unique")
	}
}

// TestUpdateProviderKeepsStoredKey: an empty incoming key keeps the stored
// one (the UI never round-trips secrets); changed fields persist.
func TestUpdateProviderKeepsStoredKey(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	id := addProvider(t, svc, validProvider())

	update := validProvider()
	update.ID = id
	update.APIKey = ""
	update.Model = "gpt-2"
	if err := svc.UpdateProvider(update); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	views, err := svc.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if !views[0].HasKey || views[0].Model != "gpt-2" {
		t.Fatalf("update lost key or model: %+v", views[0])
	}
	// ApplyOne must receive the preserved secret, proving it was kept on disk.
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.APIKey != "sk-test" {
		t.Fatalf("preserved key not applied: %+v", a.lastApplied)
	}

	unknown := validProvider()
	unknown.ID = "nope"
	if err := svc.UpdateProvider(unknown); err == nil {
		t.Fatal("UpdateProvider unknown ID want error")
	}
}

// TestSaveProviderNormalizesBaseURL: a remote http base URL with a trailing
// slash is stored as https without the slash, while a localhost http base URL
// is preserved on http so local model servers keep working.
func TestSaveProviderNormalizesBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"remote http upgraded and trimmed", "http://api.example.com/v1/", "https://api.example.com/v1"},
		{"localhost kept http", "http://localhost:1234/v1", "http://localhost:1234/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			p := validProvider()
			p.BaseURL = tt.in
			addProvider(t, svc, p)
			views, err := svc.ListProviders()
			if err != nil {
				t.Fatalf("ListProviders: %v", err)
			}
			if views[0].BaseURL != tt.want {
				t.Fatalf("stored BaseURL = %q, want %q", views[0].BaseURL, tt.want)
			}
		})
	}
}

// TestAddProviderNormalizesModels exercises trim, drop-empty, de-dupe (first
// seen order), prepending the selected model, and model-name cleanup.
func TestAddProviderNormalizesModels(t *testing.T) {
	svc := newTestService(t)
	p := validProvider()
	p.Model = "sel"
	p.Models = []string{" a ", "b", "a", "", "  ", "b"}
	p.ModelNames = map[string]string{"a": "  nice  ", "b": "   ", "ghost": "gone"}
	addProvider(t, svc, p)
	views, err := svc.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if !equalStrings(views[0].Models, []string{"sel", "a", "b"}) {
		t.Fatalf("normalized Models = %v, want [sel a b]", views[0].Models)
	}
	if len(views[0].ModelNames) != 1 || views[0].ModelNames["a"] != "nice" {
		t.Fatalf("ModelNames = %v, want map[a:nice]", views[0].ModelNames)
	}
}

// TestRemoveProvider: removing the active provider promotes the first
// remaining one and prunes per-tool overrides pointing at the removed one;
// removing the last provider leaves an empty state.
func TestRemoveProvider(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	id1 := addProvider(t, svc, validProvider())
	p2 := validProvider()
	p2.Name = "Second"
	id2 := addProvider(t, svc, p2)
	if err := svc.SetToolProvider("alpha", id2); err != nil {
		t.Fatalf("SetToolProvider: %v", err)
	}

	if err := svc.RemoveProvider(id2); err != nil {
		t.Fatalf("RemoveProvider: %v", err)
	}
	st, err := storeFrom(svc).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(st.Providers) != 1 || st.ActiveProviderID != id1 {
		t.Fatalf("state after remove wrong: %+v", st)
	}
	if _, ok := st.ToolProviders["alpha"]; ok {
		t.Fatalf("tool override on removed provider must be pruned: %v", st.ToolProviders)
	}

	// Removing the active provider promotes nothing when it is the last one.
	if err := svc.RemoveProvider(id1); err != nil {
		t.Fatalf("RemoveProvider last: %v", err)
	}
	if st, _ := storeFrom(svc).Load(); len(st.Providers) != 0 || st.ActiveProviderID != "" {
		t.Fatalf("state after removing last provider: %+v", st)
	}
	if err := svc.RemoveProvider("nope"); err == nil {
		t.Fatal("RemoveProvider unknown ID want error")
	}
}

// TestSetActiveProvider: switching the active provider changes what ApplyOne
// writes; an unknown ID errors.
func TestSetActiveProvider(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	addProvider(t, svc, validProvider())
	p2 := validProvider()
	p2.Name = "Second"
	p2.APIKey = "sk-second"
	id2 := addProvider(t, svc, p2)

	if err := svc.SetActiveProvider(id2); err != nil {
		t.Fatalf("SetActiveProvider: %v", err)
	}
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.APIKey != "sk-second" {
		t.Fatalf("active provider not applied: %+v", a.lastApplied)
	}
	if err := svc.SetActiveProvider("nope"); err == nil {
		t.Fatal("SetActiveProvider unknown ID want error")
	}
}

// multiModelProvider is a valid provider carrying a Models list for per-tool
// model tests. The default Model is the first entry.
func multiModelProvider(models ...string) core.Provider {
	p := validProvider()
	p.Model = models[0]
	p.Models = models
	return p
}

func TestApplyOneAndRestoreOne(t *testing.T) {
	a := &fakeAdapter{
		id: "alpha", name: "Alpha",
		applyRes:   core.ApplyResult{Message: "applied", ChangedPath: "/c"},
		restoreRes: core.RestoreResult{Message: "restored"},
	}
	svc := newTestService(t, a)
	addProvider(t, svc, validProvider())

	res, err := svc.ApplyOne("alpha")
	if err != nil || res.Message != "applied" {
		t.Fatalf("ApplyOne = %+v, %v", res, err)
	}
	if a.applyCalls != 1 || a.lastApplied == nil || a.lastApplied.Model != "gpt-test" {
		t.Fatalf("adapter not applied with profile: calls=%d last=%+v", a.applyCalls, a.lastApplied)
	}
	rres, err := svc.RestoreOne("alpha")
	if err != nil || rres.Message != "restored" {
		t.Fatalf("RestoreOne = %+v, %v", rres, err)
	}

	if _, err := svc.ApplyOne("missing"); err == nil {
		t.Fatal("ApplyOne(missing) want error")
	}
	if _, err := svc.RestoreOne("missing"); err == nil {
		t.Fatal("RestoreOne(missing) want error")
	}
}

func TestApplyOneNoProvider(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	if _, err := svc.ApplyOne("alpha"); err == nil {
		t.Fatal("ApplyOne without provider want error")
	}
	if a.applyCalls != 0 {
		t.Fatalf("adapter applied despite missing provider: %d", a.applyCalls)
	}
}

func TestApplyAllRestoreAllAggregation(t *testing.T) {
	ok := &fakeAdapter{id: "ok", name: "OK", installed: true, applyRes: core.ApplyResult{Message: "did ok"}}
	bad := &fakeAdapter{id: "bad", name: "Bad", installed: true, applyErr: errors.New("apply failed"),
		restoreErr: errors.New("restore failed")}
	// absent is not installed: ApplyAll must skip it entirely — applying would
	// create its config file (API key included) for software not on the machine.
	absent := &fakeAdapter{id: "absent", name: "Absent"}
	svc := newTestService(t, ok, bad, absent)
	addProvider(t, svc, validProvider())

	results, err := svc.ApplyAll()
	if err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("ApplyAll results = %+v, want the 2 installed tools only", results)
	}
	if absent.applyCalls != 0 {
		t.Fatalf("ApplyAll applied to a not-installed tool %d times", absent.applyCalls)
	}
	byID := map[string]ToolOpResult{}
	for _, r := range results {
		byID[r.ID] = r
	}
	if !byID["ok"].OK || byID["ok"].Message != "did ok" {
		t.Fatalf("ok result wrong: %+v", byID["ok"])
	}
	if byID["bad"].OK || byID["bad"].Error != "apply failed" {
		t.Fatalf("bad result wrong: %+v", byID["bad"])
	}

	rresults, err := svc.RestoreAll()
	if err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	byID = map[string]ToolOpResult{}
	for _, r := range rresults {
		byID[r.ID] = r
	}
	if !byID["ok"].OK || byID["bad"].OK || byID["bad"].Error != "restore failed" {
		t.Fatalf("restore aggregation wrong: %+v", rresults)
	}
}

func TestApplyAllNoProvider(t *testing.T) {
	svc := newTestService(t, &fakeAdapter{id: "alpha", name: "Alpha"})
	if results, err := svc.ApplyAll(); err == nil || results != nil {
		t.Fatalf("ApplyAll without provider = %+v, %v; want nil + error", results, err)
	}
}

// TestSetToolModelPersistsAndValidates: a member model persists, a non-member
// is rejected, "" clears the entry, and an unknown tool errors.
func TestSetToolModelPersistsAndValidates(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	addProvider(t, svc, multiModelProvider("gpt-test", "m2"))

	if err := svc.SetToolModel("alpha", "m2"); err != nil {
		t.Fatalf("SetToolModel member: %v", err)
	}
	st, err := storeFrom(svc).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.ToolModels["alpha"] != "m2" {
		t.Fatalf("ToolModels = %v, want alpha=m2", st.ToolModels)
	}

	if err := svc.SetToolModel("alpha", "nope"); err == nil {
		t.Fatal("SetToolModel non-member want error")
	}
	// The rejected selection must not overwrite the previously stored one.
	if st, _ := storeFrom(svc).Load(); st.ToolModels["alpha"] != "m2" {
		t.Fatalf("rejected model mutated state: %v", st.ToolModels)
	}

	if err := svc.SetToolModel("alpha", ""); err != nil {
		t.Fatalf("SetToolModel clear: %v", err)
	}
	if st, _ := storeFrom(svc).Load(); st.ToolModels["alpha"] != "" {
		t.Fatalf("clear did not delete entry: %v", st.ToolModels)
	}

	if err := svc.SetToolModel("missing", "m2"); err == nil {
		t.Fatal("SetToolModel unknown tool want error")
	}
}

// TestSetToolModelValidatesAgainstEffectiveProvider: with a per-tool provider
// override in place, SetToolModel validates against THAT provider's models,
// not the active provider's.
func TestSetToolModelValidatesAgainstEffectiveProvider(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	addProvider(t, svc, multiModelProvider("gpt-test", "m2"))
	p2 := multiModelProvider("other-default", "other-2")
	p2.Name = "Other"
	id2 := addProvider(t, svc, p2)
	if err := svc.SetToolProvider("alpha", id2); err != nil {
		t.Fatalf("SetToolProvider: %v", err)
	}

	if err := svc.SetToolModel("alpha", "other-2"); err != nil {
		t.Fatalf("SetToolModel against override provider: %v", err)
	}
	// The active provider's model is NOT valid for this tool anymore.
	if err := svc.SetToolModel("alpha", "m2"); err == nil {
		t.Fatal("SetToolModel must validate against the effective provider")
	}
}

// TestApplyOneUsesPerToolModel: ApplyOne writes the per-tool model when one is
// selected, and falls back to the provider default otherwise.
func TestApplyOneUsesPerToolModel(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	addProvider(t, svc, multiModelProvider("gpt-test", "m2"))

	// No selection yet: provider default.
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne default: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.Model != "gpt-test" {
		t.Fatalf("default apply model = %+v, want gpt-test", a.lastApplied)
	}

	// With a selection: the chosen model is applied.
	if err := svc.SetToolModel("alpha", "m2"); err != nil {
		t.Fatalf("SetToolModel: %v", err)
	}
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne selected: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.Model != "m2" {
		t.Fatalf("selected apply model = %+v, want m2", a.lastApplied)
	}
}

// TestSetToolApplyModePersistsAndValidates: "all" persists, "one" (the
// default) removes the entry, an unknown mode or tool errors, ListTools
// surfaces the mode, and the effective profile carries ApplyAllModels.
func TestSetToolApplyModePersistsAndValidates(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha", installed: true}
	svc := newTestService(t, a)
	addProvider(t, svc, multiModelProvider("gpt-test", "m2"))

	if err := svc.SetToolApplyMode("alpha", ApplyModeAll); err != nil {
		t.Fatalf("SetToolApplyMode all: %v", err)
	}
	if st, _ := storeFrom(svc).Load(); st.ToolApplyModes["alpha"] != ApplyModeAll {
		t.Fatalf("ToolApplyModes = %v, want alpha=all", st.ToolApplyModes)
	}
	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if views[0].ApplyMode != ApplyModeAll {
		t.Fatalf("ApplyMode = %q, want all", views[0].ApplyMode)
	}
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || !a.lastApplied.ApplyAllModels {
		t.Fatalf("effective profile missing ApplyAllModels: %+v", a.lastApplied)
	}

	if err := svc.SetToolApplyMode("alpha", ApplyModeOne); err != nil {
		t.Fatalf("SetToolApplyMode one: %v", err)
	}
	if st, _ := storeFrom(svc).Load(); st.ToolApplyModes["alpha"] != "" {
		t.Fatalf("one did not delete entry: %v", st.ToolApplyModes)
	}
	views, _ = svc.ListTools()
	if views[0].ApplyMode != ApplyModeOne {
		t.Fatalf("ApplyMode = %q, want one (default)", views[0].ApplyMode)
	}
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.ApplyAllModels {
		t.Fatalf("single-model profile must not carry ApplyAllModels: %+v", a.lastApplied)
	}

	if err := svc.SetToolApplyMode("alpha", "everything"); err == nil {
		t.Fatal("SetToolApplyMode unknown mode want error")
	}
	if err := svc.SetToolApplyMode("missing", ApplyModeAll); err == nil {
		t.Fatal("SetToolApplyMode unknown tool want error")
	}
}

// TestSetToolProviderPersistsAndValidates: a managed provider persists, an
// unknown one is rejected, "" clears the entry, and an unknown tool errors.
func TestSetToolProviderPersistsAndValidates(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	addProvider(t, svc, validProvider())
	p2 := validProvider()
	p2.Name = "Second"
	id2 := addProvider(t, svc, p2)

	if err := svc.SetToolProvider("alpha", id2); err != nil {
		t.Fatalf("SetToolProvider member: %v", err)
	}
	if st, _ := storeFrom(svc).Load(); st.ToolProviders["alpha"] != id2 {
		t.Fatalf("ToolProviders = %v, want alpha=%s", st.ToolProviders, id2)
	}

	if err := svc.SetToolProvider("alpha", "nope"); err == nil {
		t.Fatal("SetToolProvider unknown provider want error")
	}
	if st, _ := storeFrom(svc).Load(); st.ToolProviders["alpha"] != id2 {
		t.Fatalf("rejected provider mutated state: %v", st.ToolProviders)
	}

	if err := svc.SetToolProvider("alpha", ""); err != nil {
		t.Fatalf("SetToolProvider clear: %v", err)
	}
	if st, _ := storeFrom(svc).Load(); st.ToolProviders["alpha"] != "" {
		t.Fatalf("clear did not delete entry: %v", st.ToolProviders)
	}

	if err := svc.SetToolProvider("missing", id2); err == nil {
		t.Fatal("SetToolProvider unknown tool want error")
	}
}

// TestApplyOneUsesPerToolProvider: ApplyOne resolves the tool's provider
// override (endpoint + key), falls back to the active provider otherwise, and
// ListTools surfaces the override.
func TestApplyOneUsesPerToolProvider(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha", installed: true}
	svc := newTestService(t, a)
	addProvider(t, svc, validProvider())
	p2 := validProvider()
	p2.Name = "Second"
	p2.APIKey = "sk-second"
	p2.BaseURL = "https://second.example.com/v1"
	id2 := addProvider(t, svc, p2)

	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne default: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.APIKey != "sk-test" {
		t.Fatalf("default apply key = %+v, want sk-test", a.lastApplied)
	}

	if err := svc.SetToolProvider("alpha", id2); err != nil {
		t.Fatalf("SetToolProvider: %v", err)
	}
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne override: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.APIKey != "sk-second" ||
		a.lastApplied.BaseURL != "https://second.example.com/v1" {
		t.Fatalf("override provider not applied: %+v", a.lastApplied)
	}

	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	tv := views[0]
	if tv.SelectedProviderID != id2 || tv.ProviderName != "Second" || !tv.ProviderOverridden {
		t.Fatalf("override not surfaced: %+v", tv)
	}
	if len(tv.Providers) != 2 {
		t.Fatalf("provider refs missing: %+v", tv.Providers)
	}
}

// TestStaleToolModelFallsBackAcrossProviders: a model selected under one
// provider silently falls back to the new provider's default when the tool is
// switched to a provider that does not offer it — never an error.
func TestStaleToolModelFallsBackAcrossProviders(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	addProvider(t, svc, multiModelProvider("gpt-test", "m2"))
	p2 := multiModelProvider("other-default")
	p2.Name = "Other"
	id2 := addProvider(t, svc, p2)

	if err := svc.SetToolModel("alpha", "m2"); err != nil {
		t.Fatalf("SetToolModel: %v", err)
	}
	if err := svc.SetToolProvider("alpha", id2); err != nil {
		t.Fatalf("SetToolProvider: %v", err)
	}
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.Model != "other-default" {
		t.Fatalf("stale model must fall back to provider default: %+v", a.lastApplied)
	}
}

// TestListToolsModelsAndSelectedModel: ListTools surfaces the effective
// provider's model list and the effective per-tool selected model.
func TestListToolsModelsAndSelectedModel(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha", installed: true}
	svc := newTestService(t, a)
	addProvider(t, svc, multiModelProvider("gpt-test", "m2"))

	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if !equalStrings(views[0].Models, []string{"gpt-test", "m2"}) {
		t.Fatalf("Models = %v, want [gpt-test m2]", views[0].Models)
	}
	if views[0].SelectedModel != "gpt-test" {
		t.Fatalf("SelectedModel = %q, want default gpt-test", views[0].SelectedModel)
	}

	if err := svc.SetToolModel("alpha", "m2"); err != nil {
		t.Fatalf("SetToolModel: %v", err)
	}
	views, err = svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools 2: %v", err)
	}
	if views[0].SelectedModel != "m2" {
		t.Fatalf("SelectedModel = %q, want m2", views[0].SelectedModel)
	}
}

// TestListToolsNoProviderEmptyFields: with no provider configured, the
// listing still works and reports empty model/provider fields.
func TestListToolsNoProviderEmptyFields(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha", status: core.StatusDefault}
	svc := newTestService(t, a)
	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	v := views[0]
	if len(v.Models) != 0 || v.SelectedModel != "" || len(v.Providers) != 0 ||
		v.SelectedProviderID != "" || v.ProviderName != "" || v.ProviderOverridden {
		t.Fatalf("expected empty model/provider fields, got %+v", v)
	}
}

// TestApplyThenListToolsStaysApplied: after applying a non-default per-tool
// model, ListTools must recompute status against the SAME effective model so
// the badge stays applied_by_mintswitch (no false modified_externally).
func TestApplyThenListToolsStaysApplied(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha", installed: true}
	svc := newTestService(t, a)
	addProvider(t, svc, multiModelProvider("gpt-test", "m2"))
	if err := svc.SetToolModel("alpha", "m2"); err != nil {
		t.Fatalf("SetToolModel: %v", err)
	}
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.Model != "m2" {
		t.Fatalf("applied model = %+v, want m2", a.lastApplied)
	}
	// The adapter persisted a fingerprint over the model it actually wrote.
	a.statusModel = a.lastApplied.Model
	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if views[0].Status != "applied_by_mintswitch" {
		t.Fatalf("status = %q, want applied_by_mintswitch (effective model mismatch)", views[0].Status)
	}
}

// TestUpdateProviderPrunesToolModels: shrinking a provider's Models drops
// per-tool selections that point to a now-absent model for tools whose
// effective provider is that one, while valid ones are kept.
func TestUpdateProviderPrunesToolModels(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	b := &fakeAdapter{id: "beta", name: "Beta"}
	svc := newTestService(t, a, b)
	id := addProvider(t, svc, multiModelProvider("a", "b", "c"))
	if err := svc.SetToolModel("alpha", "b"); err != nil {
		t.Fatalf("SetToolModel alpha: %v", err)
	}
	if err := svc.SetToolModel("beta", "c"); err != nil {
		t.Fatalf("SetToolModel beta: %v", err)
	}

	// Shrink Models to [a, b]; "c" is gone so beta's selection must be pruned.
	shrunk := multiModelProvider("a", "b")
	shrunk.ID = id
	if err := svc.UpdateProvider(shrunk); err != nil {
		t.Fatalf("UpdateProvider shrink: %v", err)
	}
	st, err := storeFrom(svc).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.ToolModels["alpha"] != "b" {
		t.Fatalf("alpha selection should survive: %v", st.ToolModels)
	}
	if _, ok := st.ToolModels["beta"]; ok {
		t.Fatalf("beta selection should be pruned: %v", st.ToolModels)
	}
}

// TestModelNamesPersistenceRoundtrip: display names survive an app restart.
// AddProvider must write model_names into the settings file on disk, and a
// brand-new Service over a brand-new Store at the same path (simulating a
// relaunch/refresh) must read them back intact via ListProviders AND surface
// them per tool via ListTools — including when a per-tool model override is
// set (the effectiveProfileFor path used by viewFor).
func TestModelNamesPersistenceRoundtrip(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha", installed: true}
	svc := newTestService(t, a)
	p := multiModelProvider("opus-id", "gpt-id")
	p.ModelNames = map[string]string{"opus-id": "opus4.8", "gpt-id": "gpt5.5"}
	addProvider(t, svc, p)
	if err := svc.SetToolModel("alpha", "gpt-id"); err != nil {
		t.Fatalf("SetToolModel: %v", err)
	}

	// The names must be present in the persisted settings file itself.
	raw, err := os.ReadFile(storeFrom(svc).Path)
	if err != nil {
		t.Fatalf("read settings file: %v", err)
	}
	if !strings.Contains(string(raw), `"model_names"`) ||
		!strings.Contains(string(raw), `"opus4.8"`) ||
		!strings.Contains(string(raw), `"gpt5.5"`) {
		t.Fatal("settings file does not contain the saved model_names")
	}

	// Simulate a restart: fresh Service + fresh Store over the same file.
	reloaded := NewWithRegistry(reg(a), settings.NewStore(storeFrom(svc).Path))
	want := map[string]string{"opus-id": "opus4.8", "gpt-id": "gpt5.5"}
	pviews, err := reloaded.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders after reload: %v", err)
	}
	if !maps.Equal(pviews[0].ModelNames, want) {
		t.Fatalf("ProviderView.ModelNames after reload = %v, want %v", pviews[0].ModelNames, want)
	}
	views, err := reloaded.ListTools()
	if err != nil {
		t.Fatalf("ListTools after reload: %v", err)
	}
	if !maps.Equal(views[0].ModelNames, want) {
		t.Fatalf("ToolView.ModelNames after reload = %v, want %v", views[0].ModelNames, want)
	}
	if views[0].SelectedModel != "gpt-id" {
		t.Fatalf("SelectedModel after reload = %q, want gpt-id", views[0].SelectedModel)
	}
}

// TestTierModelsPersistenceRoundtrip: the Claude Code tier pins survive a
// restart, are trimmed on save, and round-trip through ProviderView so the
// Edit dialog can re-populate them.
func TestTierModelsPersistenceRoundtrip(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha", installed: true}
	svc := newTestService(t, a)
	p := multiModelProvider("opus-id", "gpt-id")
	p.OpusModel = " opus-id "
	p.SonnetModel = "gpt-id"
	p.HaikuModel = " haiku-id "
	addProvider(t, svc, p)

	reloaded := NewWithRegistry(reg(a), settings.NewStore(storeFrom(svc).Path))
	pviews, err := reloaded.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders after reload: %v", err)
	}
	pv := pviews[0]
	if pv.OpusModel != "opus-id" || pv.SonnetModel != "gpt-id" || pv.HaikuModel != "haiku-id" {
		t.Fatalf("tier models after reload = %q/%q/%q, want opus-id/gpt-id/haiku-id",
			pv.OpusModel, pv.SonnetModel, pv.HaikuModel)
	}

	// The tier pins reach the adapter through the effective profile on Apply.
	if _, err := reloaded.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.OpusModel != "opus-id" ||
		a.lastApplied.SonnetModel != "gpt-id" || a.lastApplied.HaikuModel != "haiku-id" {
		t.Fatalf("applied profile tier models wrong: %+v", a.lastApplied)
	}
}

// TestV1StateMigratesToDefaultProvider: a v1 single-key on-disk state
// surfaces as one active Provider named "Default" with zero user action, and
// still applies.
func TestV1StateMigratesToDefaultProvider(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	v1 := `{"active_profile": {"api_key": "sk-v1", "base_url": "https://api.example.com/v1", "model": "m"}}`
	if err := os.WriteFile(storeFrom(svc).Path, []byte(v1), 0o600); err != nil {
		t.Fatalf("seed v1 file: %v", err)
	}
	pviews, err := svc.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(pviews) != 1 || pviews[0].ID != core.DefaultProviderID ||
		pviews[0].Name != "Default" || !pviews[0].Active || !pviews[0].HasKey {
		t.Fatalf("v1 migration view wrong: %+v", pviews)
	}
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.APIKey != "sk-v1" {
		t.Fatalf("v1 key not applied: %+v", a.lastApplied)
	}
}

// TestWave2StateMigratesToProviders: a Wave 2 multi-key on-disk state
// surfaces as one provider per key entry, the active key's provider is
// active, tool_keys become per-tool provider overrides, and apply resolves
// them.
func TestWave2StateMigratesToProviders(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha", installed: true}
	svc := newTestService(t, a)
	w2 := `{
  "active_profile": {"base_url": "https://api.example.com/v1", "model": "m", "models": ["m", "m2"],
    "api_keys": [
      {"id": "k1", "provider": "OpenAI", "key": "sk-one"},
      {"id": "k2", "provider": "MintRouter", "key": "sk-two"}
    ],
    "active_key_id": "k2"},
  "tool_keys": {"alpha": "k1"},
  "tool_models": {"alpha": "m2"}
}`
	if err := os.WriteFile(storeFrom(svc).Path, []byte(w2), 0o600); err != nil {
		t.Fatalf("seed wave2 file: %v", err)
	}
	pviews, err := svc.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(pviews) != 2 || pviews[0].Name != "OpenAI" || pviews[1].Name != "MintRouter" || !pviews[1].Active {
		t.Fatalf("wave2 migration views wrong: %+v", pviews)
	}
	// alpha's old key override resolves as a provider override, and its old
	// model override stays valid.
	if _, err := svc.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.APIKey != "sk-one" || a.lastApplied.Model != "m2" {
		t.Fatalf("wave2 overrides not resolved: %+v", a.lastApplied)
	}
	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if views[0].SelectedProviderID != "k1" || views[0].ProviderName != "OpenAI" || !views[0].ProviderOverridden {
		t.Fatalf("wave2 override not surfaced: %+v", views[0])
	}
}

func TestInstallSuccess(t *testing.T) {
	fr := &fakeRunner{out: "added 1 package"}
	svc := newInstallService(t, fr, okLook)
	res, err := svc.Install("codex")
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if !res.OK || res.Action != "install" || res.ID != "codex" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Command != "npm install -g @openai/codex" {
		t.Fatalf("command = %q", res.Command)
	}
	if res.Output != "added 1 package" || fr.runs != 1 {
		t.Fatalf("output/runs wrong: %+v runs=%d", res, fr.runs)
	}
}

// TestUninstallStandaloneSurfacing: a method-aware uninstall of a standalone
// binary surfaces the delete action as the command plus a message, and routes
// through the injected remove func (no real fs/brew/npm).
func TestUninstallStandaloneSurfacing(t *testing.T) {
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
	inst := installer.NewWithResolver(fr, okLook,
		func(string) (string, bool) { return bin, true },
		[]string{binDir},
		func(p string) error { removed = append(removed, p); return nil })
	store := settings.NewStore(filepath.Join(home, "settings.json"))
	svc := NewWithInstaller(reg(), store, inst)

	res, err := svc.Uninstall("opencode")
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if !res.OK || res.Action != "uninstall" || res.ID != "opencode" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Command != "rm "+bin {
		t.Fatalf("command = %q", res.Command)
	}
	if res.Output == "" {
		t.Fatal("expected a delete message in Output")
	}
	if !equalStrings(removed, []string{bin}) {
		t.Fatalf("removed = %v, want [%s]", removed, bin)
	}
	if fr.runs != 0 {
		t.Fatalf("standalone delete must not run a command: %d", fr.runs)
	}
}

// TestUninstallUnknownMethodMessage: when the install method cannot be
// determined, Uninstall returns a non-OK result carrying the clear message
// instead of throwing, and nothing destructive happens.
func TestUninstallUnknownMethodMessage(t *testing.T) {
	fr := &fakeRunner{}
	var removed []string
	inst := installer.NewWithResolver(fr, okLook,
		func(string) (string, bool) { return "", false }, nil,
		func(p string) error { removed = append(removed, p); return nil })
	store := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	svc := NewWithInstaller(reg(), store, inst)

	res, err := svc.Uninstall("opencode")
	if err != nil {
		t.Fatalf("Uninstall should not throw on unknown method: %v", err)
	}
	if res.OK || res.Error == "" {
		t.Fatalf("expected non-OK result with a clear message: %+v", res)
	}
	if fr.runs != 0 || len(removed) != 0 {
		t.Fatalf("unknown method must be a no-op: runs=%d removed=%v", fr.runs, removed)
	}
}

func TestInstallUnknownTool(t *testing.T) {
	fr := &fakeRunner{}
	svc := newInstallService(t, fr, okLook)
	if _, err := svc.Install("nope"); err == nil {
		t.Fatal("Install(nope) want error")
	}
	if fr.runs != 0 {
		t.Fatalf("runner should not run for unknown tool: %d", fr.runs)
	}
}

func TestInstallNpmMissing(t *testing.T) {
	fr := &fakeRunner{}
	svc := newInstallService(t, fr, missLook)
	res, err := svc.Install("opencode")
	if err != nil {
		t.Fatalf("Install should not error on npm-missing: %v", err)
	}
	if res.OK || res.Error == "" {
		t.Fatalf("expected non-OK result with message: %+v", res)
	}
	if fr.runs != 0 {
		t.Fatalf("runner should not run when npm missing: %d", fr.runs)
	}
	// The intended command is still shown to the user.
	if res.Command != "npm install -g opencode-ai" {
		t.Fatalf("command = %q", res.Command)
	}
}

func TestInstallCommandFailureReportsOutput(t *testing.T) {
	fr := &fakeRunner{out: "npm ERR! boom", err: errors.New("exit status 1")}
	svc := newInstallService(t, fr, okLook)
	res, err := svc.Install("claude-code")
	if err != nil {
		t.Fatalf("Install should not throw on command failure: %v", err)
	}
	if res.OK || res.Output != "npm ERR! boom" || res.Error == "" {
		t.Fatalf("expected failed result carrying output: %+v", res)
	}
}

// TestNewWithDepsSweepsLegacyMarkers is the startup-sweep integration test: a
// user's settings.json broken by the legacy in-file marker is healed (key
// removed, marker migrated to the sidecar store) just by constructing the
// Service, without any Apply.
func TestNewWithDepsSweepsLegacyMarkers(t *testing.T) {
	home := t.TempDir()
	r := &paths.Resolver{Home: home, DataDir: filepath.Join(home, "data")}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"theme":"dark","env":{"ANTHROPIC_BASE_URL":"https://x.example.com"},` +
		`"mintswitchManaged":{"managed":true,"fingerprint":"legacyfp","version":1}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	NewWithDeps(r, backup.NewEngine(r.BackupsDir()))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings after sweep: %v", err)
	}
	if strings.Contains(string(data), core.MarkerKey) {
		t.Fatalf("legacy marker not swept from settings.json: %s", data)
	}
	if !strings.Contains(string(data), "ANTHROPIC_BASE_URL") || !strings.Contains(string(data), "dark") {
		t.Fatalf("sweep dropped user/env keys: %s", data)
	}
	marker, ok, err := markers.NewStore(r.MarkersPath()).Get("claude-code")
	if err != nil || !ok {
		t.Fatalf("store entry after sweep = ok=%v err=%v", ok, err)
	}
	if marker.Fingerprint != "legacyfp" {
		t.Fatalf("migrated fingerprint = %q, want legacyfp", marker.Fingerprint)
	}
}

// strippingAdapter wraps fakeAdapter with a configurable StripLegacyMarker so
// tests can drive SweepLegacyMarkers failures.
type strippingAdapter struct {
	*fakeAdapter
	stripErr error
}

func (s *strippingAdapter) StripLegacyMarker() error { return s.stripErr }

// TestSweepFailureSurfacesInToolDetail pins the sweep-error surfacing fix: a
// StripLegacyMarker failure recorded by SweepLegacyMarkers shows up in that
// tool's ListTools Detail (appended to the adapter's own detail), and a later
// clean sweep clears it.
func TestSweepFailureSurfacesInToolDetail(t *testing.T) {
	a := &strippingAdapter{
		fakeAdapter: &fakeAdapter{id: "alpha", name: "Alpha", detail: "status detail"},
		stripErr:    errors.New("boom"),
	}
	r := core.NewRegistry()
	r.Register(a)
	store := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	svc := NewWithRegistry(r, store)

	svc.SweepLegacyMarkers()
	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := "Legacy marker cleanup failed: boom"
	if len(views) != 1 || !strings.Contains(views[0].Detail, want) {
		t.Fatalf("Detail = %q, want it to contain %q", views[0].Detail, want)
	}
	if !strings.Contains(views[0].Detail, "status detail") {
		t.Fatalf("Detail = %q must keep the adapter's own detail", views[0].Detail)
	}

	a.stripErr = nil
	svc.SweepLegacyMarkers()
	views, err = svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools after clean sweep: %v", err)
	}
	if strings.Contains(views[0].Detail, want) {
		t.Fatalf("Detail = %q must clear after a clean sweep", views[0].Detail)
	}
}

// newModelsService builds a Service with one provider pointed at the given
// httptest server (loopback http URLs are preserved by normalization) and
// wires the server's client in as the injected models HTTP client. It returns
// the service and the provider's ID.
func newModelsService(t *testing.T, srv *httptest.Server) (*Service, string) {
	t.Helper()
	svc := newTestService(t)
	p := validProvider()
	p.BaseURL = srv.URL
	id := addProvider(t, svc, p)
	svc.modelsClient = srv.Client()
	return svc, id
}

// TestFetchProviderModels: a well-behaved OpenAI-style endpoint yields the
// sorted, de-duplicated model IDs; the key travels only in the Authorization
// header; the call mutates no settings.
func TestFetchProviderModels(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"id":"zeta"},{"id":"alpha"},{"id":"alpha"},{"id":""}]}`))
	}))
	defer srv.Close()
	svc, id := newModelsService(t, srv)

	before, err := os.ReadFile(storeFrom(svc).Path)
	if err != nil {
		t.Fatalf("read settings before: %v", err)
	}
	models, err := svc.FetchProviderModels(id)
	if err != nil {
		t.Fatalf("FetchProviderModels: %v", err)
	}
	if !equalStrings(models, []string{"alpha", "zeta"}) {
		t.Fatalf("models = %v, want [alpha zeta]", models)
	}
	if gotPath != "/models" {
		t.Fatalf("path = %q, want /models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want the bearer key", gotAuth)
	}
	after, err := os.ReadFile(storeFrom(svc).Path)
	if err != nil {
		t.Fatalf("read settings after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("FetchProviderModels must not mutate settings:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestFetchProviderModelsVariants: bare-string arrays, a "models" key and a
// top-level array all parse.
func TestFetchProviderModelsVariants(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"data of strings", `{"data":["b","a"]}`, []string{"a", "b"}},
		{"models key", `{"models":[{"id":"m1"},{"name":"m0"}]}`, []string{"m0", "m1"}},
		{"top-level array", `[{"id":"x"},"y"]`, []string{"x", "y"}},
		{"empty data", `{"data":[]}`, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			svc, id := newModelsService(t, srv)
			models, err := svc.FetchProviderModels(id)
			if err != nil {
				t.Fatalf("FetchProviderModels: %v", err)
			}
			if !equalStrings(models, tt.want) {
				t.Fatalf("models = %v, want %v", models, tt.want)
			}
		})
	}
}

// TestFetchProviderModelsNon200: a non-200 status yields a display-safe error
// carrying the status but never the key or the response body.
func TestFetchProviderModelsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad key sk-test junk-echo"}`))
	}))
	defer srv.Close()
	svc, id := newModelsService(t, srv)
	_, err := svc.FetchProviderModels(id)
	if err == nil {
		t.Fatal("FetchProviderModels: want error for HTTP 401")
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("error = %q, want it to name HTTP 401", err)
	}
	if strings.Contains(err.Error(), "sk-test") || strings.Contains(err.Error(), "junk-echo") {
		t.Fatalf("error = %q must not echo the key or response body", err)
	}
}

// TestFetchProviderModelsInvalidJSON: an unparsable or unrecognizable body
// yields a display-safe error that never echoes the body.
func TestFetchProviderModelsInvalidJSON(t *testing.T) {
	for _, body := range []string{"not json at all", `{"object":"list"}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(body))
		}))
		svc, id := newModelsService(t, srv)
		_, err := svc.FetchProviderModels(id)
		srv.Close()
		if err == nil {
			t.Fatalf("FetchProviderModels(%q): want error", body)
		}
		if strings.Contains(err.Error(), "not json") {
			t.Fatalf("error = %q must not echo the response body", err)
		}
	}
}

// TestFetchProviderModelsTimeout: a hung endpoint fails with a timeout
// message instead of blocking, and the error stays display-safe.
func TestFetchProviderModelsTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()
	svc, id := newModelsService(t, srv)
	svc.modelsClient = &http.Client{Timeout: 50 * time.Millisecond}
	_, err := svc.FetchProviderModels(id)
	if err == nil {
		t.Fatal("FetchProviderModels: want timeout error")
	}
	if !strings.Contains(err.Error(), "did not respond") {
		t.Fatalf("error = %q, want the timeout message", err)
	}
	if strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error = %q must not embed the raw transport error", err)
	}
}

// TestFetchProviderModelsUnknownProvider: an unknown ID is a clear error and
// no request is attempted.
func TestFetchProviderModelsUnknownProvider(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.FetchProviderModels("nope")
	if err == nil || !strings.Contains(err.Error(), `unknown provider "nope"`) {
		t.Fatalf("FetchProviderModels = %v, want unknown provider error", err)
	}
}

// TestFetchEndpointModelsDisplayNames: the Add/Edit dialog entry point
// returns each model's optional display name ("display_name", else "name"
// unless it was consumed as the ID; names equal to the ID are dropped).
func TestFetchEndpointModelsDisplayNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[
			{"id":"gpt-x","display_name":"GPT X"},
			{"id":"claude-y","name":"Claude Y"},
			{"name":"bare-name"},
			{"id":"plain","name":"plain"}
		]}`))
	}))
	defer srv.Close()
	svc, id := newModelsService(t, srv)
	options, err := svc.FetchEndpointModels(srv.URL, "", id)
	if err != nil {
		t.Fatalf("FetchEndpointModels: %v", err)
	}
	want := []ModelOption{
		{ID: "bare-name"},
		{ID: "claude-y", DisplayName: "Claude Y"},
		{ID: "gpt-x", DisplayName: "GPT X"},
		{ID: "plain"},
	}
	if len(options) != len(want) {
		t.Fatalf("options = %+v, want %+v", options, want)
	}
	for i := range want {
		if options[i] != want[i] {
			t.Fatalf("options[%d] = %+v, want %+v", i, options[i], want[i])
		}
	}
}

// countingTransport wraps an http.RoundTripper and counts every request made
// through it, so tests can prove a code path performs no network I/O at all.
type countingTransport struct {
	inner http.RoundTripper
	calls *int
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*c.calls++
	return c.inner.RoundTrip(r)
}

// TestFetchEndpointModelsStoredKeyURLGuard pins the defense-in-depth guard:
// the provider's stored key is attached only when the normalized baseURL
// matches the provider's stored base URL. Any other URL fails with a
// display-safe error and makes no HTTP request, so a stored key can never be
// sent to an attacker-chosen endpoint.
func TestFetchEndpointModelsStoredKeyURLGuard(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()
	svc, id := newModelsService(t, srv)
	calls := 0
	svc.modelsClient = &http.Client{Transport: countingTransport{inner: srv.Client().Transport, calls: &calls}}

	// Matching URL (trailing slash proves both sides are normalized): the
	// stored key is used.
	if _, err := svc.FetchEndpointModels(srv.URL+"/", "", id); err != nil {
		t.Fatalf("FetchEndpointModels matching URL: %v", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want the stored bearer key", gotAuth)
	}
	if calls != 1 {
		t.Fatalf("matching URL made %d request(s), want 1", calls)
	}

	// Different URL: a display-safe error that never carries the key, and no
	// request at all.
	calls = 0
	_, err := svc.FetchEndpointModels("https://attacker.example.com/v1", "", id)
	if err == nil {
		t.Fatal("FetchEndpointModels mismatched URL: want error")
	}
	if !strings.Contains(err.Error(), "enter the API key for the new endpoint") {
		t.Fatalf("error = %q, want the enter-the-key message", err)
	}
	if strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("error = %q must not include the stored key", err)
	}
	if calls != 0 {
		t.Fatalf("mismatched URL made %d request(s), want none", calls)
	}
}

// TestPlanUninstallContract covers every UI-visible classification and proves
// the service preview is read-only: neither commands nor deletes are performed.
func TestPlanUninstallContract(t *testing.T) {
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
		name        string
		toolID      string
		resolved    string
		found       bool
		wantMethod  string
		wantAction  string
		wantCommand string
		wantTarget  string
		wantCan     bool
		wantWarning bool
	}{
		{
			name: "npm", toolID: "codex", resolved: filepath.Join(root, ".npm-global", "bin", "codex"), found: true,
			wantMethod: "npm", wantAction: "run_command", wantCommand: "npm uninstall -g @openai/codex",
			wantTarget: "@openai/codex", wantCan: true,
		},
		{
			name: "homebrew", toolID: "opencode", resolved: brewLink, found: true,
			wantMethod: "homebrew", wantAction: "run_command", wantCommand: "brew uninstall opencode",
			wantTarget: "opencode", wantCan: true,
		},
		{
			name: "standalone", toolID: "opencode", resolved: standalone, found: true,
			wantMethod: "standalone", wantAction: "delete_file", wantCommand: "rm " + standalone,
			wantTarget: standalone, wantCan: true, wantWarning: true,
		},
		{
			name: "unknown", toolID: "codex", resolved: "/usr/bin/codex", found: true,
			wantMethod: "unknown", wantAction: "manual", wantTarget: "/usr/bin/codex", wantWarning: true,
		},
		{
			name: "unresolvable", toolID: "opencode", found: false,
			wantMethod: "unknown", wantAction: "manual", wantTarget: "opencode", wantWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &fakeRunner{}
			var removed []string
			inst := installer.NewWithResolver(fr, okLook,
				func(string) (string, bool) { return tt.resolved, tt.found },
				[]string{binDir}, func(p string) error { removed = append(removed, p); return nil })
			svc := NewWithInstaller(reg(), settings.NewStore(filepath.Join(t.TempDir(), "settings.json")), inst)

			plan, err := svc.PlanUninstall(tt.toolID)
			if err != nil {
				t.Fatalf("PlanUninstall: %v", err)
			}
			if plan.Method != tt.wantMethod || plan.Action != tt.wantAction ||
				plan.Command != tt.wantCommand || plan.Target != tt.wantTarget || plan.CanExecute != tt.wantCan {
				t.Fatalf("plan = %+v", plan)
			}
			if (plan.Warning != "") != tt.wantWarning {
				t.Fatalf("warning = %q, want non-empty=%v", plan.Warning, tt.wantWarning)
			}
			if fr.runs != 0 || len(removed) != 0 {
				t.Fatalf("preview must be read-only: runs=%d removed=%v", fr.runs, removed)
			}
		})
	}
}

func TestPlanUninstallMissingDependencyAndUnknownTool(t *testing.T) {
	fr := &fakeRunner{}
	inst := installer.NewWithResolver(fr, missLook,
		func(string) (string, bool) { return "/home/u/.npm-global/bin/codex", true }, nil, nil)
	svc := NewWithInstaller(reg(), settings.NewStore(filepath.Join(t.TempDir(), "settings.json")), inst)

	plan, err := svc.PlanUninstall("codex")
	if err != nil {
		t.Fatalf("missing npm should be a displayable plan, got %v", err)
	}
	if plan.Method != "npm" || plan.Action != "run_command" || plan.CanExecute || plan.Warning == "" {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := svc.PlanUninstall("nope"); err == nil {
		t.Fatal("unknown tool should return an error")
	}
	if fr.runs != 0 {
		t.Fatalf("preview ran a command: %d", fr.runs)
	}
}

func TestUninstallPlanJSONContract(t *testing.T) {
	plan := UninstallPlan{
		Method: "npm", Action: "run_command", Command: "npm uninstall -g pkg",
		Target: "pkg", Warning: "warning", CanExecute: true,
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"method", "action", "command", "target", "warning", "can_execute"}
	if len(fields) != len(want) {
		t.Fatalf("JSON fields = %v", fields)
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing JSON field %q in %s", key, data)
		}
	}
}
