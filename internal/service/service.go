// Package service exposes MintSwitch's backend operations to the frontend as a
// single Wails v3 service. It wires the tool adapter registry, MintSwitch's own
// settings store and the backup engine together behind a small, binding-friendly
// API: list tools with per-tool status, manage the Provider list, and
// apply/restore the resolved configuration per tool or across all tools.
//
// The same Service is used by both the desktop build and the `-tags server`
// (web) build; it holds no UI state and returns plain structs and errors so the
// Wails binding generator can produce typed TypeScript for it.
//
// API-key handling over the wire: the stored Providers contain secret key
// values. [Service.ListProviders] deliberately never returns those secrets —
// it returns [ProviderView]s carrying only non-secret fields (HasKey reports
// whether a key is stored, never a value, not even masked) — so no key is
// ever sent to a browser in server mode (and never logged). On
// [Service.UpdateProvider], an empty incoming key value means "keep the
// stored one", letting the UI submit the form without ever round-tripping a
// secret.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"mintswitch/internal/adapters/claudecode"
	"mintswitch/internal/adapters/codex"
	"mintswitch/internal/adapters/droid"
	"mintswitch/internal/adapters/kilo"
	"mintswitch/internal/adapters/opencode"
	"mintswitch/internal/adapters/zed"
	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/installer"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
	"mintswitch/internal/secrets"
	"mintswitch/internal/settings"
)

// Service is the backend façade bound into the Wails application.
type Service struct {
	reg   *core.Registry
	store *settings.Store
	inst  *installer.Installer
	// mu serializes every operation that mutates state — the settings file
	// (save profile, per-tool models) and the managed tool config files
	// (apply/restore) — so concurrent UI calls cannot interleave their
	// load-modify-save cycles. Read-only methods do not take it, so
	// long-running mutations never block the UI's status polling.
	mu sync.Mutex
	// sweepMu guards sweepErrs, which is written by SweepLegacyMarkers (at
	// construction, or when re-invoked) and read by viewFor, which deliberately
	// runs without taking mu.
	sweepMu sync.Mutex
	// sweepErrs records the last legacy-marker sweep failure per tool ID, so
	// the error is surfaced in the tool's Detail via ListTools instead of only
	// being logged.
	sweepErrs map[string]string
}

// ToolView is the per-tool summary returned by [Service.ListTools].
type ToolView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Installed   bool     `json:"installed"`
	Status      string   `json:"status"`
	Detail      string   `json:"detail"`
	ConfigPaths []string `json:"config_paths"`
	// Models is the active profile's selectable model list (with backward-compat
	// seeding from the selected Model), used to populate the per-tool dropdown.
	// It is empty when no profile is saved.
	Models []string `json:"models"`
	// ModelNames maps a member of Models to its optional display name, used only
	// for dropdown labels. Missing entries fall back to the model ID.
	ModelNames map[string]string `json:"model_names"`
	// SelectedModel is the effective model that has been (or would be) applied to
	// this tool: the per-tool override when set, otherwise the profile default.
	// It is empty when no profile is saved.
	SelectedModel string `json:"selected_model"`
	// Providers is the provider list (ID + name + active flag, never key
	// values), used to populate the per-tool provider dropdown. It is empty
	// when no provider is configured.
	Providers []ProviderRef `json:"providers"`
	// SelectedProviderID is the ID of the provider in effect for this tool:
	// the per-tool override when set and still a member, otherwise the active
	// provider. It is empty when no provider is configured.
	SelectedProviderID string `json:"selected_provider_id"`
	// ProviderName is the display name of the provider in effect for this
	// tool. It never carries any part of the key value.
	ProviderName string `json:"provider_name"`
	// ProviderOverridden is true when the provider in effect comes from a
	// per-tool override rather than the active provider.
	ProviderOverridden bool `json:"provider_overridden"`
	// Installable is true when the tool has a whitelisted npm package the
	// installer can install/uninstall. It is false for tools distributed only as
	// a standalone binary, so the UI can hide the Install action for those.
	Installable bool `json:"installable"`
}

// ToolOpResult is the per-tool outcome of a bulk apply/restore operation.
type ToolOpResult struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ProviderRef is the minimal non-secret reference to one provider (for
// per-tool dropdowns): ID, display name and whether it is the globally
// active provider — never any part of the key value, not even masked.
type ProviderRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// ProviderView is the non-secret view of one managed provider returned to
// the frontend. It never carries any API key value; HasKey reports whether
// one is stored.
type ProviderView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Note    string   `json:"note"`
	BaseURL string   `json:"base_url"`
	Models  []string `json:"models"`
	// ModelNames maps a member of Models to its optional display name, used only
	// for UI labels. Missing entries fall back to the model ID.
	ModelNames     map[string]string `json:"model_names"`
	Model          string            `json:"model"`
	SmallFastModel string            `json:"small_fast_model"`
	HasKey         bool              `json:"has_key"`
	Active         bool              `json:"active"`
}

// providerRefs maps the state's providers to their minimal non-secret refs.
func providerRefs(st *settings.State) []ProviderRef {
	if len(st.Providers) == 0 {
		return nil
	}
	out := make([]ProviderRef, 0, len(st.Providers))
	for _, p := range st.Providers {
		out = append(out, ProviderRef{ID: p.ID, Name: p.Name, Active: p.ID == st.ActiveProviderID})
	}
	return out
}

// providerView maps one provider to its non-secret view. Models saved before
// the list existed are seeded from the default Model so the UI always has
// options.
func providerView(p core.Provider, active bool) ProviderView {
	models := p.Models
	if len(models) == 0 && strings.TrimSpace(p.Model) != "" {
		models = []string{p.Model}
	}
	return ProviderView{
		ID:             p.ID,
		Name:           p.Name,
		Note:           p.Note,
		BaseURL:        p.BaseURL,
		Models:         models,
		ModelNames:     p.ModelNames,
		Model:          p.Model,
		SmallFastModel: p.SmallFastModel,
		HasKey:         strings.TrimSpace(p.APIKey) != "",
		Active:         active,
	}
}

// InstallResult is the structured outcome of an Install/Uninstall operation,
// designed to be shown to the user. Command is the exact command line that was
// (or would be) run; Output is npm's combined stdout+stderr.
type InstallResult struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Command string `json:"command"`
	Output  string `json:"output"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// New builds a Service backed by the real environment: a default
// paths.Resolver, a backup.Engine under the user's data dir, and the
// built-in tool adapters. It returns an error only if the home/data directories
// cannot be resolved.
func New() (*Service, error) {
	r, err := paths.NewResolver()
	if err != nil {
		return nil, err
	}
	s := NewWithDeps(r, backup.NewEngine(r.BackupsDir()))
	// Store the providers' API keys in the OS keychain (real environment only —
	// NewWithDeps stays file-only so tests never touch the user's keychain).
	// Migration (legacy shapes + keychain move) is idempotent and never
	// deletes a key from the file before the keychain write succeeded; a
	// failure just keeps the old plaintext behaviour, so it must not abort
	// startup.
	s.store.Secrets = secrets.New()
	if err := s.store.Migrate(); err != nil {
		log.Printf("settings: migration skipped: %v", err)
	}
	return s, nil
}

// NewWithDeps builds a Service from an injected Resolver and backup Engine,
// registering the built-in adapters over a sidecar marker store at
// r.MarkersPath(). Tests can point r.Home at a temp dir. It runs the one-time
// legacy-marker sweep (see [Service.SweepLegacyMarkers]) before returning, so
// tool configs broken by the legacy in-file marker heal at app startup.
func NewWithDeps(r *paths.Resolver, e *backup.Engine) *Service {
	reg := core.NewRegistry()
	mk := markers.NewStore(r.MarkersPath())
	reg.Register(claudecode.New(r, e, mk))
	reg.Register(codex.New(r, e, mk))
	reg.Register(opencode.New(r, e, mk))
	reg.Register(droid.New(r, e, mk))
	reg.Register(zed.New(r, e, mk))
	reg.Register(kilo.New(r, e, mk))
	inst := installer.NewMethodAware(installer.ExecRunner{}, r)
	store := settings.NewStore(r.SettingsPath())
	s := NewWithInstaller(reg, store, inst)
	s.SweepLegacyMarkers()
	return s
}

// SweepLegacyMarkers strips the legacy in-file "mintswitchManaged" key from
// every registered adapter that implements [core.LegacyMarkerStripper]
// (migrating the marker into the sidecar store). It is best-effort: a failure
// on one tool is logged, recorded per tool so ListTools surfaces it in that
// tool's Detail, and never blocks the others or app startup.
func (s *Service) SweepLegacyMarkers() {
	errs := make(map[string]string)
	for _, a := range s.reg.All() {
		stripper, ok := a.(core.LegacyMarkerStripper)
		if !ok {
			continue
		}
		if err := stripper.StripLegacyMarker(); err != nil {
			log.Printf("service: legacy marker sweep for %s (%s): %v",
				a.ID(), strings.Join(a.ConfigPaths(), ", "), err)
			errs[a.ID()] = fmt.Sprintf("Legacy marker cleanup failed: %v", err)
		}
	}
	s.sweepMu.Lock()
	s.sweepErrs = errs
	s.sweepMu.Unlock()
}

// sweepErrFor returns the recorded legacy-marker sweep failure for toolID, or
// "" when the last sweep succeeded for it (or never ran).
func (s *Service) sweepErrFor(toolID string) string {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	return s.sweepErrs[toolID]
}

// NewWithRegistry builds a Service from a pre-built registry and settings store,
// using the real npm-backed installer. It is the seam used by tests to register
// a fake adapter and a temp store.
func NewWithRegistry(reg *core.Registry, store *settings.Store) *Service {
	return NewWithInstaller(reg, store, installer.New(installer.ExecRunner{}))
}

// NewWithInstaller is like [NewWithRegistry] but injects the installer, letting
// tests supply one backed by a fake command runner so no real npm is invoked.
func NewWithInstaller(reg *core.Registry, store *settings.Store, inst *installer.Installer) *Service {
	return &Service{reg: reg, store: store, inst: inst}
}

// ListTools returns one [ToolView] per registered adapter, in registration
// order. Status is evaluated against the per-tool effective provider's
// profile (a zero profile when none is configured). A per-tool Status error
// is surfaced in Detail rather than failing the whole list.
func (s *Service) ListTools() ([]ToolView, error) {
	st, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	adapters := s.reg.All()
	out := make([]ToolView, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, s.viewFor(a, st))
	}
	return out, nil
}

// viewFor builds a [ToolView] for a single adapter. Status is evaluated against
// the EFFECTIVE per-tool profile (so the recomputed fingerprint matches what was
// applied, avoiding a false modified_externally). When no valid provider is
// configured it falls back to the effective provider's raw profile (or a zero
// profile with no providers at all) and reports an empty SelectedModel,
// preserving the prior zero-profile listing behaviour. A per-tool Status error
// is surfaced in Detail rather than failing the caller.
func (s *Service) viewFor(a core.ToolAdapter, st *settings.State) ToolView {
	var p core.Profile
	selectedModel := ""
	selectedProviderID := ""
	providerName := ""
	overridden := false
	if pr, isOverride, ok := resolveProvider(st, a.ID()); ok {
		p = pr.Profile()
		selectedProviderID = pr.ID
		providerName = pr.Name
		overridden = isOverride
	}
	if eff, err := s.effectiveProfileFor(a.ID()); err == nil {
		p = eff
		selectedModel = eff.Model
	}
	installed, _ := a.Detect()
	status, detail, serr := a.Status(p)
	if serr != nil {
		detail = serr.Error()
	}
	if msg := s.sweepErrFor(a.ID()); msg != "" {
		detail = joinMessage(detail, msg)
	}
	// Backward compat for providers saved before Models existed: surface the
	// single default Model as a one-element list so the UI always has options.
	models := p.Models
	if len(models) == 0 && strings.TrimSpace(p.Model) != "" {
		models = []string{p.Model}
	}
	_, installable := installer.Spec(a.ID())
	return ToolView{
		ID:                 a.ID(),
		Name:               a.Name(),
		Installed:          installed,
		Status:             status.String(),
		Detail:             detail,
		ConfigPaths:        a.ConfigPaths(),
		Models:             models,
		ModelNames:         p.ModelNames,
		SelectedModel:      selectedModel,
		Providers:          providerRefs(st),
		SelectedProviderID: selectedProviderID,
		ProviderName:       providerName,
		ProviderOverridden: overridden,
		Installable:        installable,
	}
}

// ListProviders returns the non-secret views of every managed provider, in
// stored order. With no providers configured it returns an empty list and no
// error.
func (s *Service) ListProviders() ([]ProviderView, error) {
	st, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	out := make([]ProviderView, 0, len(st.Providers))
	for _, p := range st.Providers {
		out = append(out, providerView(p, p.ID == st.ActiveProviderID))
	}
	return out, nil
}

// AddProvider validates and persists a new provider, returning its generated
// ID. The name, API key value, base URL and default model are required
// ([core.Provider.Validate] rules). The first provider added becomes active.
// The key value is only persisted (keychain-first), never echoed back.
func (s *Service) AddProvider(p core.Provider) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(st.Providers))
	for _, ex := range st.Providers {
		taken[ex.ID] = true
	}
	p.ID = newProviderID(taken)
	normalizeProvider(&p)
	if err := p.Validate(); err != nil {
		return "", err
	}
	st.Providers = append(st.Providers, p)
	if len(st.Providers) == 1 {
		st.ActiveProviderID = p.ID
	}
	if err := s.store.Save(st); err != nil {
		return "", err
	}
	return p.ID, nil
}

// UpdateProvider validates and persists changes to the provider identified by
// p.ID. An empty incoming APIKey means "keep the stored one", so the UI can
// submit the form without ever round-tripping a secret. Stale per-tool model
// selections that pointed at models this provider no longer offers are pruned
// (for tools whose effective provider is this one) so they fall back to the
// provider default.
func (s *Service) UpdateProvider(p core.Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i, ex := range st.Providers {
		if ex.ID == p.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("service: unknown provider %q", p.ID)
	}
	if strings.TrimSpace(p.APIKey) == "" {
		p.APIKey = st.Providers[idx].APIKey
	}
	normalizeProvider(&p)
	if err := p.Validate(); err != nil {
		return err
	}
	st.Providers[idx] = p
	// Prune per-tool model selections that this provider no longer offers, for
	// tools whose effective provider is this one, so they fall back to the
	// provider default.
	for tid, m := range st.ToolModels {
		if pr, _, ok := resolveProvider(st, tid); ok && pr.ID == p.ID && !p.HasModel(m) {
			delete(st.ToolModels, tid)
		}
	}
	return s.store.Save(st)
}

// RemoveProvider deletes the provider with the given ID. Removing the active
// provider promotes the first remaining one (none when the list empties).
// Per-tool overrides pointing at the removed provider are pruned so those
// tools fall back to the active provider.
func (s *Service) RemoveProvider(providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	if _, ok := st.Provider(providerID); !ok {
		return fmt.Errorf("service: unknown provider %q", providerID)
	}
	kept := make([]core.Provider, 0, len(st.Providers)-1)
	for _, p := range st.Providers {
		if p.ID != providerID {
			kept = append(kept, p)
		}
	}
	st.Providers = kept
	if st.ActiveProviderID == providerID {
		st.ActiveProviderID = ""
		if len(kept) > 0 {
			st.ActiveProviderID = kept[0].ID
		}
	}
	for tid, pid := range st.ToolProviders {
		if pid == providerID {
			delete(st.ToolProviders, tid)
		}
	}
	return s.store.Save(st)
}

// SetActiveProvider selects the provider with the given ID as the globally
// active one, used by every tool without a per-tool override.
func (s *Service) SetActiveProvider(providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	if _, ok := st.Provider(providerID); !ok {
		return fmt.Errorf("service: unknown provider %q", providerID)
	}
	st.ActiveProviderID = providerID
	return s.store.Save(st)
}

// normalizeProvider trims the provider's text fields, normalizes the base URL
// and reconciles the model list (trim, de-dupe, seed from the default Model),
// in place.
func normalizeProvider(p *core.Provider) {
	p.Name = strings.TrimSpace(p.Name)
	p.Note = strings.TrimSpace(p.Note)
	p.APIKey = strings.TrimSpace(p.APIKey)
	p.BaseURL, _ = core.NormalizeBaseURL(p.BaseURL)
	p.Model = strings.TrimSpace(p.Model)
	p.SmallFastModel = strings.TrimSpace(p.SmallFastModel)
	p.Models = normalizeModels(p.Models, p.Model)
	p.ModelNames = normalizeModelNames(p.ModelNames, p.Models)
}

// newProviderID returns a fresh provider ID not present in taken.
func newProviderID(taken map[string]bool) string {
	for i := 1; ; i++ {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			if id := fmt.Sprintf("prov-%d", len(taken)+i); !taken[id] {
				return id
			}
			continue
		}
		if id := "prov-" + hex.EncodeToString(b[:]); !taken[id] {
			return id
		}
	}
}

// SetToolModel records (or clears) the per-tool model selection for toolID.
// An empty model deletes the selection so the tool uses the effective
// provider's default. A non-empty model must be a member of the EFFECTIVE
// provider's Models (the tool's provider override when set, else the active
// provider), otherwise a clear error is returned. The toolID must be a
// registered tool. The selection is persisted via the settings store.
func (s *Service) SetToolModel(toolID, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.reg.Get(toolID); !ok {
		return fmt.Errorf("service: unknown tool %q", toolID)
	}
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		delete(st.ToolModels, toolID)
		return s.store.Save(st)
	}
	pr, _, ok := resolveProvider(st, toolID)
	if !ok {
		return errors.New("service: no provider configured; add a provider before selecting models")
	}
	if !pr.HasModel(model) {
		return fmt.Errorf("service: model %q is not one of the provider's models", model)
	}
	if st.ToolModels == nil {
		st.ToolModels = make(map[string]string)
	}
	st.ToolModels[toolID] = model
	return s.store.Save(st)
}

// SetToolProvider records (or clears) the per-tool provider selection for
// toolID. An empty providerID deletes the selection so the tool uses the
// active provider. A non-empty providerID must reference a managed provider,
// otherwise a clear error is returned. The toolID must be a registered tool.
// The selection is persisted via the settings store.
func (s *Service) SetToolProvider(toolID, providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.reg.Get(toolID); !ok {
		return fmt.Errorf("service: unknown tool %q", toolID)
	}
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		delete(st.ToolProviders, toolID)
		return s.store.Save(st)
	}
	if _, ok := st.Provider(providerID); !ok {
		return fmt.Errorf("service: unknown provider %q", providerID)
	}
	if st.ToolProviders == nil {
		st.ToolProviders = make(map[string]string)
	}
	st.ToolProviders[toolID] = providerID
	return s.store.Save(st)
}

// normalizeModels trims and de-duplicates the saved model list, preserving
// first-seen order and dropping empties. It then guarantees the selected model
// is a member: a non-empty selected model that is absent is prepended (which
// also turns an otherwise-empty list into [selected]).
func normalizeModels(models []string, selected string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	if selected != "" && !seen[selected] {
		out = append([]string{selected}, out...)
	}
	return out
}

// normalizeModelNames trims display names and keeps only entries whose (trimmed)
// model ID is a member of models and whose name is non-empty, so stale or blank
// aliases never persist. It returns nil when nothing remains.
func normalizeModelNames(names map[string]string, models []string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	member := make(map[string]bool, len(models))
	for _, m := range models {
		member[m] = true
	}
	out := make(map[string]string, len(names))
	for id, name := range names {
		id = strings.TrimSpace(id)
		name = strings.TrimSpace(name)
		if name == "" || !member[id] {
			continue
		}
		out[id] = name
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveProvider returns the provider in effect for toolID: the per-tool
// override when set and still a managed member, otherwise the active
// provider. overridden reports whether the result came from a valid per-tool
// override that differs from the active provider; ok=false means no provider
// could be resolved (none configured).
func resolveProvider(st *settings.State, toolID string) (pr core.Provider, overridden bool, ok bool) {
	if sel := st.ToolProviders[toolID]; sel != "" {
		if p, found := st.Provider(sel); found {
			return p, p.ID != st.ActiveProviderID, true
		}
	}
	p, found := st.ActiveProvider()
	return p, false, found
}

// activeProfile loads the active provider's profile and validates it. It
// returns a clear error when no provider is configured or the active provider
// is invalid, so Apply operations never run with missing/bad configuration.
func (s *Service) activeProfile() (core.Profile, error) {
	st, err := s.store.Load()
	if err != nil {
		return core.Profile{}, err
	}
	pr, ok := st.ActiveProvider()
	if !ok {
		return core.Profile{}, errors.New("service: no provider configured; add a valid provider before applying")
	}
	p := pr.Profile()
	// Auto-upgrade legacy http base URLs stored before normalization existed so
	// remote endpoints behind an https redirect keep their Authorization header.
	p.BaseURL, _ = core.NormalizeBaseURL(p.BaseURL)
	if err := p.Validate(); err != nil {
		return core.Profile{}, fmt.Errorf("service: saved provider is invalid: %w", err)
	}
	return p, nil
}

// effectiveProfileFor resolves the profile to apply to toolID: the tool's
// provider override when set and still a managed member (else the active
// provider), with the provider's default Model overridden by the per-tool
// model selection when set and still one of that provider's Models. It
// returns a clear error when no provider is configured or the resolved
// provider is invalid. A stale or absent selection is never an error: the
// provider defaults are left in place. This single helper is used by Apply
// and by status computation so the fingerprint stays consistent across both.
func (s *Service) effectiveProfileFor(toolID string) (core.Profile, error) {
	st, err := s.store.Load()
	if err != nil {
		return core.Profile{}, err
	}
	pr, _, ok := resolveProvider(st, toolID)
	if !ok {
		return core.Profile{}, errors.New("service: no provider configured; add a valid provider before applying")
	}
	p := pr.Profile()
	// Auto-upgrade legacy http base URLs stored before normalization existed so
	// remote endpoints behind an https redirect keep their Authorization header.
	p.BaseURL, _ = core.NormalizeBaseURL(p.BaseURL)
	if err := p.Validate(); err != nil {
		return core.Profile{}, fmt.Errorf("service: saved provider is invalid: %w", err)
	}
	if sel := st.ToolModels[toolID]; sel != "" && pr.HasModel(sel) {
		p.Model = sel
	}
	return p, nil
}

// joinMessage folds an optional note onto a base message, separating them
// with a space so both remain readable. Either side may be empty.
func joinMessage(base, note string) string {
	switch {
	case note == "":
		return base
	case base == "":
		return note
	default:
		return base + " " + note
	}
}

// ApplyOne applies the active profile to the single tool identified by toolID,
// honoring the per-tool model selection. It first validates the saved profile
// and returns an error for an unknown tool or an invalid/missing profile.
func (s *Service) ApplyOne(toolID string) (core.ApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.reg.Get(toolID)
	if !ok {
		return core.ApplyResult{}, fmt.Errorf("service: unknown tool %q", toolID)
	}
	p, err := s.effectiveProfileFor(toolID)
	if err != nil {
		return core.ApplyResult{}, err
	}
	return a.Apply(p)
}

// RestoreOne restores the single tool identified by toolID to its pre-apply
// state. It returns an error for an unknown tool; a tool with nothing to restore
// is a safe no-op handled by the adapter.
func (s *Service) RestoreOne(toolID string) (core.RestoreResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.reg.Get(toolID)
	if !ok {
		return core.RestoreResult{}, fmt.Errorf("service: unknown tool %q", toolID)
	}
	return a.Restore()
}

// ApplyAll applies the active profile to every registered tool and returns a
// per-tool outcome. It validates the saved profile once up front and fails fast
// (returning an error, no partial results) when no valid profile is saved.
// Individual adapter failures are captured per tool and do not abort the run.
func (s *Service) ApplyAll() ([]ToolOpResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.activeProfile(); err != nil {
		return nil, err
	}
	adapters := s.reg.All()
	out := make([]ToolOpResult, 0, len(adapters))
	for _, a := range adapters {
		p, perr := s.effectiveProfileFor(a.ID())
		if perr != nil {
			out = append(out, ToolOpResult{ID: a.ID(), OK: false, Error: perr.Error()})
			continue
		}
		res, aerr := a.Apply(p)
		r := ToolOpResult{ID: a.ID(), OK: aerr == nil}
		if aerr != nil {
			r.Error = aerr.Error()
		} else {
			r.Message = res.Message
		}
		out = append(out, r)
	}
	return out, nil
}

// Install installs the tool identified by toolID globally via npm and returns a
// structured [InstallResult] carrying the exact command and npm's output. An
// unknown toolID returns an error. When npm is missing the result is returned
// (not an error) with a clear, user-facing message so the UI can show it.
func (s *Service) Install(toolID string) (InstallResult, error) {
	args, out, err := s.inst.Install(context.Background(), toolID)
	return s.installResult(toolID, "install", args, out, err)
}

// Uninstall removes the tool identified by toolID using the method it was
// actually installed with (npm, Homebrew, or a standalone binary), surfacing the
// exact command (or delete action) and its output. Its return contract matches
// [Service.Install]; when the install method cannot be determined it returns a
// non-OK result carrying a clear, user-facing message instead of throwing.
func (s *Service) Uninstall(toolID string) (InstallResult, error) {
	args, out, err := s.inst.Uninstall(context.Background(), toolID)
	return s.installResult(toolID, "uninstall", args, out, err)
}

// installResult maps the installer's (args, output, error) into an
// [InstallResult]. Unknown tools become a hard error; missing tooling
// (npm/brew), an indeterminate install method, and command failures all become a
// non-OK result with a clear message so the UI can show the command and its
// output instead of throwing.
func (s *Service) installResult(toolID, action string, args []string, out string, err error) (InstallResult, error) {
	res := InstallResult{
		ID:      toolID,
		Action:  action,
		Command: strings.Join(args, " "),
		Output:  out,
	}
	switch {
	case errors.Is(err, installer.ErrUnknownTool):
		return InstallResult{}, fmt.Errorf("service: unknown tool %q", toolID)
	case errors.Is(err, installer.ErrNpmMissing):
		res.Error = "Node.js / npm is required. Install Node.js, then retry."
		return res, nil
	case errors.Is(err, installer.ErrBrewMissing):
		res.Error = "Homebrew (brew) is required to uninstall this tool. Install Homebrew, then retry."
		return res, nil
	case errors.Is(err, installer.ErrUnknownMethod):
		// The installer puts the clear, user-facing message in out; surface it as
		// the error so the UI shows why nothing was removed.
		res.Error = out
		res.Output = ""
		return res, nil
	case err != nil:
		res.Error = err.Error()
		return res, nil
	default:
		res.OK = true
		return res, nil
	}
}

// RestoreAll restores every registered tool to its pre-apply state and returns a
// per-tool outcome. It needs no profile; individual adapter failures are
// captured per tool and do not abort the run.
func (s *Service) RestoreAll() ([]ToolOpResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	adapters := s.reg.All()
	out := make([]ToolOpResult, 0, len(adapters))
	for _, a := range adapters {
		res, aerr := a.Restore()
		r := ToolOpResult{ID: a.ID(), OK: aerr == nil}
		if aerr != nil {
			r.Error = aerr.Error()
		} else {
			r.Message = res.Message
		}
		out = append(out, r)
	}
	return out, nil
}
