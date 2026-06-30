package service

import (
	"errors"
	"path/filepath"
	"testing"

	"mintconfig/internal/core"
	"mintconfig/internal/settings"
)

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
		status: core.StatusAppliedByMintConfig, detail: "applied",
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
	if !views[0].Installed || views[0].Status != "applied_by_mintconfig" ||
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
	if view.HasKey || view.Model != "" {
		t.Fatalf("expected zero ProfileView, got %+v", view)
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
