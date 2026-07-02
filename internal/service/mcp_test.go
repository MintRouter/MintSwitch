package service

import (
	"errors"
	"strings"
	"testing"

	"mintswitch/internal/core"
)

// fakeInjector is a configurable core.MCPInjector used to exercise the Service's
// MCP methods without touching real tool config files.
type fakeInjector struct {
	id        string
	installed bool
	status    core.MCPStatus
	detail    string
	statusErr error
	injectRes core.MCPResult
	injectErr error
	removeRes core.MCPResult
	removeErr error
	lastSpec  *core.MCPServerSpec
	injCalls  int
	remCalls  int
	cfgPaths  []string
}

func (f *fakeInjector) ID() string               { return f.id }
func (f *fakeInjector) MCPConfigPaths() []string { return f.cfgPaths }
func (f *fakeInjector) Detect() bool             { return f.installed }
func (f *fakeInjector) MCPStatus(_ core.MCPServerSpec) (core.MCPStatus, string, error) {
	return f.status, f.detail, f.statusErr
}
func (f *fakeInjector) InjectMCP(spec core.MCPServerSpec) (core.MCPResult, error) {
	f.injCalls++
	cp := spec
	f.lastSpec = &cp
	return f.injectRes, f.injectErr
}
func (f *fakeInjector) RemoveMCP() (core.MCPResult, error) {
	f.remCalls++
	return f.removeRes, f.removeErr
}

func newMCPService(t *testing.T, injectors ...core.MCPInjector) *Service {
	t.Helper()
	s := newTestService(t)
	s.mcp = injectors
	return s
}

func TestSetMCPKeyAndGetState(t *testing.T) {
	inj := &fakeInjector{id: "claude-code", installed: true, status: core.MCPConfiguredByMintSwitch, detail: "ok", cfgPaths: []string{"/x/.claude.json"}}
	s := newMCPService(t, inj)

	st, err := s.GetMCPState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.HasKey {
		t.Fatal("expected HasKey false before a key is set")
	}
	if st.Endpoint != core.DefaultMCPEndpoint {
		t.Fatalf("endpoint = %q, want default", st.Endpoint)
	}
	if len(st.Tools) != 1 || st.Tools[0].ID != "claude-code" || st.Tools[0].Status != "configured_by_mintswitch" {
		t.Fatalf("unexpected tools: %+v", st.Tools)
	}

	if err := s.SetMCPKey("  sk-live  "); err != nil {
		t.Fatalf("set key: %v", err)
	}
	st, err = s.GetMCPState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.HasKey {
		t.Fatal("expected HasKey true after set")
	}

	// The persisted key is trimmed and never exposed via the state struct.
	loaded, _ := s.store.Load()
	if loaded.MCPKey != "sk-live" {
		t.Fatalf("stored key = %q, want trimmed sk-live", loaded.MCPKey)
	}
}

func TestSetMCPKeyEmptyClears(t *testing.T) {
	s := newMCPService(t, &fakeInjector{id: "claude-code"})
	if err := s.SetMCPKey("sk-live"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMCPKey("   "); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetMCPState()
	if st.HasKey {
		t.Fatal("expected key cleared by empty SetMCPKey")
	}
}

func TestMCPEnabledDefaultsTrueWhenUnset(t *testing.T) {
	s := newMCPService(t, &fakeInjector{id: "claude-code"})
	st, err := s.GetMCPState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.Enabled {
		t.Fatal("expected Enabled true by default when the flag is unset")
	}
}

func TestSetMCPEnabledRoundTrip(t *testing.T) {
	s := newMCPService(t, &fakeInjector{id: "claude-code"})

	if err := s.SetMCPEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	st, err := s.GetMCPState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Enabled {
		t.Fatal("expected Enabled false after SetMCPEnabled(false)")
	}

	if err := s.SetMCPEnabled(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	st, err = s.GetMCPState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.Enabled {
		t.Fatal("expected Enabled true after SetMCPEnabled(true)")
	}
}

// saveActiveProfile persists an active profile carrying key as its APIKey so
// tests can exercise the profile-sourced MCP key (the primary source).
func saveActiveProfile(t *testing.T, s *Service, key string) {
	t.Helper()
	st, err := s.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	st.ActiveProfile = &core.Profile{Label: "p", APIKey: key, BaseURL: "https://api.example.com/v1", Model: "m"}
	if err := s.store.Save(st); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// TestMCPKeyFromActiveProfile proves the active profile's APIKey is the primary
// source of the MCP key: with a profile key saved and NO legacy MCPKey, HasKey is
// true and injection uses the profile key.
func TestMCPKeyFromActiveProfile(t *testing.T) {
	inj := &fakeInjector{id: "claude-code", injectRes: core.MCPResult{Message: "done"}}
	s := newMCPService(t, inj)

	saveActiveProfile(t, s, "sk-profile")

	st, err := s.GetMCPState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.HasKey {
		t.Fatal("expected HasKey true from active profile key")
	}
	if st.Endpoint != core.DefaultMCPEndpoint {
		t.Fatalf("endpoint = %q, want default", st.Endpoint)
	}

	if _, err := s.InjectMCPOne("claude-code"); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if inj.lastSpec == nil || inj.lastSpec.APIKey != "sk-profile" {
		t.Fatalf("inject did not use profile key: %+v", inj.lastSpec)
	}

	// No legacy key was ever saved.
	loaded, _ := s.store.Load()
	if loaded.MCPKey != "" {
		t.Fatalf("legacy MCPKey unexpectedly set: %q", loaded.MCPKey)
	}
}

// TestActiveProfileKeyTakesPrecedenceOverMCPKey proves the profile key wins over
// a saved legacy MCPKey when both are present.
func TestActiveProfileKeyTakesPrecedenceOverMCPKey(t *testing.T) {
	inj := &fakeInjector{id: "claude-code", injectRes: core.MCPResult{Message: "done"}}
	s := newMCPService(t, inj)

	if err := s.SetMCPKey("sk-legacy"); err != nil {
		t.Fatal(err)
	}
	saveActiveProfile(t, s, "sk-profile")

	if _, err := s.InjectMCPOne("claude-code"); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if inj.lastSpec == nil || inj.lastSpec.APIKey != "sk-profile" {
		t.Fatalf("profile key should take precedence: %+v", inj.lastSpec)
	}
}

// TestMCPKeyFallsBackToLegacyKey proves the legacy MCPKey still works when the
// active profile has no usable key (blank/whitespace), keeping existing setups
// functional.
func TestMCPKeyFallsBackToLegacyKey(t *testing.T) {
	inj := &fakeInjector{id: "claude-code", injectRes: core.MCPResult{Message: "done"}}
	s := newMCPService(t, inj)

	if err := s.SetMCPKey("sk-legacy"); err != nil {
		t.Fatal(err)
	}
	st, err := s.GetMCPState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.HasKey {
		t.Fatal("expected HasKey true from legacy fallback key")
	}
	if _, err := s.InjectMCPOne("claude-code"); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if inj.lastSpec == nil || inj.lastSpec.APIKey != "sk-legacy" {
		t.Fatalf("inject did not use legacy key: %+v", inj.lastSpec)
	}
}

func TestInjectMCPOne(t *testing.T) {
	inj := &fakeInjector{id: "claude-code", injectRes: core.MCPResult{Message: "done"}}
	s := newMCPService(t, inj)

	if _, err := s.InjectMCPOne("claude-code"); err == nil {
		t.Fatal("expected error when no key is saved")
	}
	if inj.injCalls != 0 {
		t.Fatal("inject should not run without a key")
	}
	if err := s.SetMCPKey("sk-live"); err != nil {
		t.Fatal(err)
	}
	res, err := s.InjectMCPOne("claude-code")
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if res.Message != "done" || inj.injCalls != 1 {
		t.Fatalf("unexpected inject result: %+v calls=%d", res, inj.injCalls)
	}
	if inj.lastSpec == nil || inj.lastSpec.APIKey != "sk-live" || inj.lastSpec.Endpoint != core.DefaultMCPEndpoint {
		t.Fatalf("spec not passed through: %+v", inj.lastSpec)
	}
	if _, err := s.InjectMCPOne("nope"); err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestRemoveMCPOne(t *testing.T) {
	inj := &fakeInjector{id: "claude-code", removeRes: core.MCPResult{Message: "gone"}}
	s := newMCPService(t, inj)
	res, err := s.RemoveMCPOne("claude-code")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if res.Message != "gone" || inj.remCalls != 1 {
		t.Fatalf("unexpected remove: %+v calls=%d", res, inj.remCalls)
	}
	if _, err := s.RemoveMCPOne("nope"); err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

// applyCEService builds a Service registered with endpoint adapter a and MCP
// injector inj (sharing the same tool ID) plus a saved active profile carrying
// key, so mcpSpec reports a key. It is the seam for the Apply/Restore Context
// Engine gating tests.
func applyCEService(t *testing.T, a *fakeAdapter, inj *fakeInjector, key string) *Service {
	t.Helper()
	s := newTestService(t, a)
	s.mcp = []core.MCPInjector{inj}
	saveActiveProfile(t, s, key)
	return s
}

// TestApplyOneInjectsMCPWhenContextEngineEnabled: a successful Apply on a
// MCP-capable tool injects the Context Engine using the profile key when the
// master toggle is enabled (its default), and a successful inject leaves the
// apply message unchanged.
func TestApplyOneInjectsMCPWhenContextEngineEnabled(t *testing.T) {
	a := &fakeAdapter{id: "claude-code", name: "Claude", applyRes: core.ApplyResult{Message: "applied"}}
	inj := &fakeInjector{id: "claude-code", injectRes: core.MCPResult{Message: "injected"}}
	s := applyCEService(t, a, inj, "sk-profile")

	res, err := s.ApplyOne("claude-code")
	if err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.applyCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", a.applyCalls)
	}
	if inj.injCalls != 1 {
		t.Fatalf("inject calls = %d, want 1", inj.injCalls)
	}
	if inj.lastSpec == nil || inj.lastSpec.APIKey != "sk-profile" {
		t.Fatalf("inject did not use profile key: %+v", inj.lastSpec)
	}
	if res.Message != "applied" {
		t.Fatalf("message = %q, want unchanged 'applied' on inject success", res.Message)
	}
}

// TestApplyOneSkipsMCPWhenContextEngineDisabled: with the master toggle off, a
// successful Apply performs no MCP work.
func TestApplyOneSkipsMCPWhenContextEngineDisabled(t *testing.T) {
	a := &fakeAdapter{id: "claude-code", name: "Claude", applyRes: core.ApplyResult{Message: "applied"}}
	inj := &fakeInjector{id: "claude-code"}
	s := applyCEService(t, a, inj, "sk-profile")
	if err := s.SetMCPEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	if _, err := s.ApplyOne("claude-code"); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}
	if a.applyCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", a.applyCalls)
	}
	if inj.injCalls != 0 {
		t.Fatalf("inject must not run when Context Engine is disabled: %d", inj.injCalls)
	}
}

// TestApplyOneMCPFailureDoesNotAbort: an injection failure never aborts Apply;
// Apply still succeeds and a display-safe note is folded into the message.
func TestApplyOneMCPFailureDoesNotAbort(t *testing.T) {
	a := &fakeAdapter{id: "claude-code", name: "Claude", applyRes: core.ApplyResult{Message: "applied"}}
	inj := &fakeInjector{id: "claude-code", injectErr: errors.New("inject boom")}
	s := applyCEService(t, a, inj, "sk-profile")

	res, err := s.ApplyOne("claude-code")
	if err != nil {
		t.Fatalf("ApplyOne must not error on MCP failure: %v", err)
	}
	if inj.injCalls != 1 {
		t.Fatalf("inject calls = %d, want 1", inj.injCalls)
	}
	if !strings.Contains(res.Message, "applied") || res.Message == "applied" {
		t.Fatalf("message should keep the base and fold a note: %q", res.Message)
	}
}

// TestInjectMCPIfEnabledNoKeyIsNoOp: when the toggle is on and an injector
// exists but no key is available, the helper is a silent no-op (no inject, no
// note) — the "missing key -> Apply OK, no inject" path.
func TestInjectMCPIfEnabledNoKeyIsNoOp(t *testing.T) {
	inj := &fakeInjector{id: "claude-code"}
	s := newMCPService(t, inj)

	if note := s.injectMCPIfEnabled("claude-code", true); note != "" {
		t.Fatalf("expected empty note without a key, got %q", note)
	}
	if inj.injCalls != 0 {
		t.Fatalf("inject must not run without a key: %d", inj.injCalls)
	}
}

// TestRestoreOneRemovesMCP: a successful Restore removes the Context Engine from
// a capable tool (regardless of the toggle) and, on success, leaves the restore
// message unchanged.
func TestRestoreOneRemovesMCP(t *testing.T) {
	a := &fakeAdapter{id: "claude-code", name: "Claude", restoreRes: core.RestoreResult{Message: "restored"}}
	inj := &fakeInjector{id: "claude-code", removeRes: core.MCPResult{Message: "removed"}}
	s := applyCEService(t, a, inj, "sk-profile")

	res, err := s.RestoreOne("claude-code")
	if err != nil {
		t.Fatalf("RestoreOne: %v", err)
	}
	if inj.remCalls != 1 {
		t.Fatalf("remove calls = %d, want 1", inj.remCalls)
	}
	if res.Message != "restored" {
		t.Fatalf("message = %q, want unchanged 'restored' on remove success", res.Message)
	}
}

// TestApplyAllInjectsOnlyCapableToolsWhenEnabled: ApplyAll injects the Context
// Engine only for tools that have a registered injector, leaving plain tools to
// only write their profile.
func TestApplyAllInjectsOnlyCapableToolsWhenEnabled(t *testing.T) {
	capable := &fakeAdapter{id: "claude-code", name: "Claude", applyRes: core.ApplyResult{Message: "a1"}}
	plain := &fakeAdapter{id: "codex", name: "Codex", applyRes: core.ApplyResult{Message: "a2"}}
	inj := &fakeInjector{id: "claude-code", injectRes: core.MCPResult{Message: "injected"}}
	s := newTestService(t, capable, plain)
	s.mcp = []core.MCPInjector{inj}
	saveActiveProfile(t, s, "sk-profile")

	if _, err := s.ApplyAll(); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	if inj.injCalls != 1 {
		t.Fatalf("inject calls = %d, want 1 (only the capable tool)", inj.injCalls)
	}
}

// TestRestoreAllRemovesOnlyCapableTools: RestoreAll removes the Context Engine
// only for tools that have a registered injector.
func TestRestoreAllRemovesOnlyCapableTools(t *testing.T) {
	capable := &fakeAdapter{id: "claude-code", name: "Claude", restoreRes: core.RestoreResult{Message: "r1"}}
	plain := &fakeAdapter{id: "codex", name: "Codex", restoreRes: core.RestoreResult{Message: "r2"}}
	inj := &fakeInjector{id: "claude-code", removeRes: core.MCPResult{Message: "removed"}}
	s := newTestService(t, capable, plain)
	s.mcp = []core.MCPInjector{inj}

	if _, err := s.RestoreAll(); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if inj.remCalls != 1 {
		t.Fatalf("remove calls = %d, want 1 (only the capable tool)", inj.remCalls)
	}
}
