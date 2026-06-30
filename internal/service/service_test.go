package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"mintswitch/internal/core"
	"mintswitch/internal/installer"
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
}

func (f *fakeAdapter) ID() string            { return f.id }
func (f *fakeAdapter) Name() string          { return f.name }
func (f *fakeAdapter) ConfigPaths() []string { return f.paths }
func (f *fakeAdapter) Detect() (bool, string) {
	return f.installed, f.active
}
func (f *fakeAdapter) Status(core.Profile) (core.ToolStatus, string, error) {
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

// validProfile is a saved-and-valid profile for apply tests.
func validProfile() core.Profile {
	return core.Profile{
		Label:   "test",
		APIKey:  "sk-test",
		BaseURL: "https://api.example.com/v1",
		Model:   "gpt-test",
	}
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

func TestSaveProfileValidation(t *testing.T) {
	tests := []struct {
		name    string
		in      core.Profile
		wantErr bool
	}{
		{"valid", validProfile(), false},
		{"missing model", core.Profile{APIKey: "k", BaseURL: "https://x.test"}, true},
		{"missing key", core.Profile{Model: "m", BaseURL: "https://x.test"}, true},
		{"bad url", core.Profile{APIKey: "k", Model: "m", BaseURL: "://nope"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			err := svc.SaveProfile(tt.in)
			if tt.wantErr != (err != nil) {
				t.Fatalf("SaveProfile(%+v) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestSaveProfilePreservesKeyAndGetProfileHidesIt(t *testing.T) {
	svc := newTestService(t)
	if err := svc.SaveProfile(validProfile()); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	// Re-save with an empty key (masked UI submit): the stored key is kept and
	// the changed fields are persisted.
	update := validProfile()
	update.APIKey = ""
	update.Model = "gpt-2"
	if err := svc.SaveProfile(update); err != nil {
		t.Fatalf("update save: %v", err)
	}
	view, err := svc.GetProfile()
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !view.HasKey {
		t.Fatal("HasKey=false after key should have been preserved")
	}
	if view.Model != "gpt-2" {
		t.Fatalf("model not updated: %q", view.Model)
	}
	// ApplyOne must receive the preserved secret, proving it was kept on disk.
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc2 := NewWithRegistry(reg(a), storeFrom(svc))
	if _, err := svc2.ApplyOne("alpha"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.lastApplied == nil || a.lastApplied.APIKey != "sk-test" {
		t.Fatalf("preserved key not applied: %+v", a.lastApplied)
	}
}

func TestGetProfileEmpty(t *testing.T) {
	svc := newTestService(t)
	view, err := svc.GetProfile()
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if view.HasKey || view.Model != "" || len(view.Models) != 0 {
		t.Fatalf("expected zero ProfileView, got %+v", view)
	}
}

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

// TestSaveProfileBackwardCompatModels: a profile saved with only a Model (no
// Models) is read back with Models seeded from the selected Model.
func TestSaveProfileBackwardCompatModels(t *testing.T) {
	svc := newTestService(t)
	// Save through the store directly to simulate pre-Models on-disk state.
	st := &settings.State{ActiveProfile: &core.Profile{
		APIKey: "sk-test", BaseURL: "https://api.example.com/v1", Model: "gpt-old",
	}}
	if err := storeFrom(svc).Save(st); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	view, err := svc.GetProfile()
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !equalStrings(view.Models, []string{"gpt-old"}) {
		t.Fatalf("backward-compat Models = %v, want [gpt-old]", view.Models)
	}
	if view.Model != "gpt-old" {
		t.Fatalf("Model = %q, want gpt-old", view.Model)
	}
}

// TestSaveProfileNormalizesModels exercises trim, drop-empty, de-dupe (first
// seen order) and prepending the selected model when it is missing.
func TestSaveProfileNormalizesModels(t *testing.T) {
	svc := newTestService(t)
	p := validProfile()
	p.Model = "sel"
	p.Models = []string{" a ", "b", "a", "", "  ", "b"}
	if err := svc.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	view, err := svc.GetProfile()
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !equalStrings(view.Models, []string{"sel", "a", "b"}) {
		t.Fatalf("normalized Models = %v, want [sel a b]", view.Models)
	}
	if view.Model != "sel" {
		t.Fatalf("Model = %q, want sel", view.Model)
	}
}

// TestSaveProfileModelAlreadyInModels: when the selected model is already a
// member it is not duplicated and order is preserved.
func TestSaveProfileModelAlreadyInModels(t *testing.T) {
	svc := newTestService(t)
	p := validProfile()
	p.Model = "b"
	p.Models = []string{"a", "b", "c"}
	if err := svc.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	view, err := svc.GetProfile()
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !equalStrings(view.Models, []string{"a", "b", "c"}) {
		t.Fatalf("Models = %v, want [a b c]", view.Models)
	}
}

// TestSaveProfileEmptyModelsSeededFromModel: an empty Models list with a
// selected Model becomes [Model].
func TestSaveProfileEmptyModelsSeededFromModel(t *testing.T) {
	svc := newTestService(t)
	p := validProfile()
	p.Model = "only"
	p.Models = []string{"  ", ""}
	if err := svc.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	view, err := svc.GetProfile()
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !equalStrings(view.Models, []string{"only"}) {
		t.Fatalf("Models = %v, want [only]", view.Models)
	}
}

// TestSaveProfileRoundTripModels: a multi-model profile round-trips through
// save+reload with Models and the selected Model intact.
func TestSaveProfileRoundTripModels(t *testing.T) {
	svc := newTestService(t)
	p := validProfile()
	p.Model = "m2"
	p.Models = []string{"m1", "m2", "m3"}
	p.SmallFastModel = " small "
	if err := svc.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	view, err := svc.GetProfile()
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !equalStrings(view.Models, []string{"m1", "m2", "m3"}) {
		t.Fatalf("Models = %v, want [m1 m2 m3]", view.Models)
	}
	if view.Model != "m2" {
		t.Fatalf("Model = %q, want m2", view.Model)
	}
	if view.SmallFastModel != "small" {
		t.Fatalf("SmallFastModel = %q, want trimmed 'small'", view.SmallFastModel)
	}
}

// TestSaveProfileRejectsModelNotInModels: when normalization cannot make the
// selected model a member, Validate must reject it. This is reached by saving a
// profile whose Models has entries but whose (already-present-after-prepend)
// invariant is deliberately broken via a direct on-disk state, proving Validate
// guards reads/applies. Here we test the SaveProfile path indirectly: since
// normalization always prepends the selected model, SaveProfile cannot produce
// this state, so we assert Validate (the guard) rejects it directly.
func TestValidateRejectsModelNotInModels(t *testing.T) {
	p := core.Profile{APIKey: "k", BaseURL: "https://h", Model: "m", Models: []string{"x", "y"}}
	if err := p.Validate(); err == nil {
		t.Fatal("Validate accepted Model not present in non-empty Models")
	}
}

func TestApplyOneAndRestoreOne(t *testing.T) {
	a := &fakeAdapter{
		id: "alpha", name: "Alpha",
		applyRes:   core.ApplyResult{Message: "applied", ChangedPath: "/c"},
		restoreRes: core.RestoreResult{Message: "restored"},
	}
	svc := newTestService(t, a)
	if err := svc.SaveProfile(validProfile()); err != nil {
		t.Fatalf("save: %v", err)
	}

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

func TestApplyOneNoProfile(t *testing.T) {
	a := &fakeAdapter{id: "alpha", name: "Alpha"}
	svc := newTestService(t, a)
	if _, err := svc.ApplyOne("alpha"); err == nil {
		t.Fatal("ApplyOne without saved profile want error")
	}
	if a.applyCalls != 0 {
		t.Fatalf("adapter applied despite missing profile: %d", a.applyCalls)
	}
}

func TestApplyAllRestoreAllAggregation(t *testing.T) {
	ok := &fakeAdapter{id: "ok", name: "OK", applyRes: core.ApplyResult{Message: "did ok"}}
	bad := &fakeAdapter{id: "bad", name: "Bad", applyErr: errors.New("apply failed"),
		restoreErr: errors.New("restore failed")}
	svc := newTestService(t, ok, bad)
	if err := svc.SaveProfile(validProfile()); err != nil {
		t.Fatalf("save: %v", err)
	}

	results, err := svc.ApplyAll()
	if err != nil {
		t.Fatalf("ApplyAll: %v", err)
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

func TestApplyAllNoProfile(t *testing.T) {
	svc := newTestService(t, &fakeAdapter{id: "alpha", name: "Alpha"})
	if results, err := svc.ApplyAll(); err == nil || results != nil {
		t.Fatalf("ApplyAll without profile = %+v, %v; want nil + error", results, err)
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

func TestUninstallSuccess(t *testing.T) {
	fr := &fakeRunner{out: "removed 1 package"}
	svc := newInstallService(t, fr, okLook)
	res, err := svc.Uninstall("pi")
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if !res.OK || res.Action != "uninstall" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Command != "npm uninstall -g @earendil-works/pi-coding-agent" {
		t.Fatalf("command = %q", res.Command)
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
