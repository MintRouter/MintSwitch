// Package service exposes MintSwitch's backend operations to the frontend as a
// single Wails v3 service. It wires the tool adapter registry, MintSwitch's own
// settings store and the backup engine together behind a small, binding-friendly
// API: list tools with per-tool status, get/save the active profile, and
// apply/restore a profile per tool or across all tools.
//
// The same Service is used by both the desktop build and the `-tags server`
// (web) build; it holds no UI state and returns plain structs and errors so the
// Wails binding generator can produce typed TypeScript for it.
//
// API-key handling over the wire: the stored Profile contains a secret APIKey.
// [Service.GetProfile] deliberately never returns that secret — it returns a
// [ProfileView] carrying only non-secret fields plus a HasKey flag — so the key
// is never sent to a browser in server mode (and never logged). The UI shows a
// masked field. On [Service.SaveProfile], an empty incoming APIKey means "keep
// the existing key", letting the UI submit the form without ever round-tripping
// the secret.
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
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
	mcpclaudecode "mintswitch/internal/injectors/claudecode"
	mcpcodex "mintswitch/internal/injectors/codex"
	mcpdroid "mintswitch/internal/injectors/droid"
	mcpkilo "mintswitch/internal/injectors/kilo"
	mcpopencode "mintswitch/internal/injectors/opencode"
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
	// (save profile, per-tool models, MCP toggles/key) and the managed tool
	// config files (apply/restore/inject) — so concurrent UI calls cannot
	// interleave their load-modify-save cycles. Read-only methods do not take
	// it, so long-running mutations never block the UI's status polling.
	mu sync.Mutex
	// mcp holds the registered MCP injectors (separate from the endpoint tool
	// registry) in registration order.
	mcp []core.MCPInjector
	// mcpClient is the HTTP client used by TestMCPConnection. It is nil for the
	// production constructors (a 10s-timeout client is built on demand); tests
	// may set it to an httptest client.
	mcpClient *http.Client
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

// ProfileView is the non-secret view of the active profile returned to the
// frontend. It never carries the API key; HasKey reports whether one is stored.
type ProfileView struct {
	Label   string   `json:"label"`
	BaseURL string   `json:"base_url"`
	Models  []string `json:"models"`
	// ModelNames maps a member of Models to its optional display name, used only
	// for UI labels. Missing entries fall back to the model ID.
	ModelNames     map[string]string `json:"model_names"`
	Model          string            `json:"model"`
	SmallFastModel string            `json:"small_fast_model"`
	HasKey         bool              `json:"has_key"`
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
	// Store the profile API key in the OS keychain (real environment only —
	// NewWithDeps stays file-only so tests never touch the user's keychain).
	// Migration is idempotent and never deletes the key from the file before
	// the keychain write succeeded; a failure just keeps the old plaintext
	// behaviour, so it must not abort startup.
	s.store.Secrets = secrets.New()
	if err := s.store.MigrateAPIKey(); err != nil {
		log.Printf("settings: api_key keychain migration skipped: %v", err)
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
	// Register the MCP injectors. This is a distinct registry from the endpoint
	// tool adapters above: MCP injection is independent of the active profile.
	// The injectors get their OWN backup engine (rooted at r.MCPBackupsDir())
	// so their snapshots never mix with the adapters' — an adapter and an
	// injector touching the same file each keep their own pristine original.
	// Zed deliberately has NO injector: its context_servers settings schema is
	// not yet verified, and Zed forbids writing secrets (the API key) into
	// settings.json, so there is no safe place to inject the MCP entry.
	me := backup.NewEngine(r.MCPBackupsDir())
	s.mcp = []core.MCPInjector{
		mcpclaudecode.New(r, me),
		mcpopencode.New(r, me),
		mcpcodex.New(r, me),
		mcpdroid.New(r, me),
		mcpkilo.New(r, me),
	}
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
// order. Status is evaluated against the saved active profile (a zero profile
// when none is saved). A per-tool Status error is surfaced in Detail rather than
// failing the whole list.
func (s *Service) ListTools() ([]ToolView, error) {
	st, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	var p core.Profile
	if st.ActiveProfile != nil {
		p = *st.ActiveProfile
	}
	adapters := s.reg.All()
	out := make([]ToolView, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, s.viewFor(a, p))
	}
	return out, nil
}

// viewFor builds a [ToolView] for a single adapter. Status is evaluated against
// the EFFECTIVE per-tool profile (so the recomputed fingerprint matches what was
// applied, avoiding a false modified_externally). When no valid profile is saved
// it falls back to the supplied zero profile and reports an empty SelectedModel,
// preserving the prior zero-profile listing behaviour. A per-tool Status error
// is surfaced in Detail rather than failing the caller.
func (s *Service) viewFor(a core.ToolAdapter, fallback core.Profile) ToolView {
	p := fallback
	selectedModel := ""
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
	// Backward compat for profiles saved before Models existed: surface the
	// single selected Model as a one-element list so the UI always has options.
	models := p.Models
	if len(models) == 0 && strings.TrimSpace(p.Model) != "" {
		models = []string{p.Model}
	}
	_, installable := installer.Spec(a.ID())
	return ToolView{
		ID:            a.ID(),
		Name:          a.Name(),
		Installed:     installed,
		Status:        status.String(),
		Detail:        detail,
		ConfigPaths:   a.ConfigPaths(),
		Models:        models,
		ModelNames:    p.ModelNames,
		SelectedModel: selectedModel,
		Installable:   installable,
	}
}

// GetProfile returns the non-secret view of the saved active profile. When no
// profile is saved it returns a zero ProfileView (HasKey=false) and no error.
func (s *Service) GetProfile() (ProfileView, error) {
	st, err := s.store.Load()
	if err != nil {
		return ProfileView{}, err
	}
	if st.ActiveProfile == nil {
		return ProfileView{}, nil
	}
	p := st.ActiveProfile
	// Backward compat for profiles saved before Models existed: surface the
	// single selected Model as a one-element list so the UI always has options.
	models := p.Models
	if len(models) == 0 && strings.TrimSpace(p.Model) != "" {
		models = []string{p.Model}
	}
	return ProfileView{
		Label:          p.Label,
		BaseURL:        p.BaseURL,
		Models:         models,
		ModelNames:     p.ModelNames,
		Model:          p.Model,
		SmallFastModel: p.SmallFastModel,
		HasKey:         strings.TrimSpace(p.APIKey) != "",
	}, nil
}

// SaveProfile validates and persists p as the active profile. An empty incoming
// APIKey is treated as "keep the existing key" so the masked UI can submit the
// form without re-sending the secret; the merged profile is then validated via
// [core.Profile.Validate] and an invalid profile is rejected with an error.
func (s *Service) SaveProfile(p core.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	p.BaseURL, _ = core.NormalizeBaseURL(p.BaseURL)
	p.Model = strings.TrimSpace(p.Model)
	p.SmallFastModel = strings.TrimSpace(p.SmallFastModel)
	p.Models = normalizeModels(p.Models, p.Model)
	p.ModelNames = normalizeModelNames(p.ModelNames, p.Models)
	if strings.TrimSpace(p.APIKey) == "" && st.ActiveProfile != nil {
		p.APIKey = st.ActiveProfile.APIKey
	}
	if err := p.Validate(); err != nil {
		return err
	}
	// Prune stale per-tool model selections: drop any entry whose chosen model is
	// no longer in the new Models list so it falls back to the profile default.
	if len(st.ToolModels) > 0 {
		allowed := make(map[string]bool, len(p.Models))
		for _, m := range p.Models {
			allowed[m] = true
		}
		for tid, m := range st.ToolModels {
			if !allowed[m] {
				delete(st.ToolModels, tid)
			}
		}
	}
	st.ActiveProfile = &p
	return s.store.Save(st)
}

// SetToolModel records (or clears) the per-tool model selection for toolID. An
// empty model deletes the selection so the tool uses the profile default. A
// non-empty model must be a member of the active profile's Models, otherwise a
// clear error is returned. The toolID must be a registered tool. The selection
// is persisted via the settings store.
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
	p, err := s.activeProfile()
	if err != nil {
		return err
	}
	member := false
	for _, m := range p.Models {
		if m == model {
			member = true
			break
		}
	}
	if !member {
		return fmt.Errorf("service: model %q is not one of the profile's models", model)
	}
	if st.ToolModels == nil {
		st.ToolModels = make(map[string]string)
	}
	st.ToolModels[toolID] = model
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

// activeProfile loads the saved active profile and validates it. It returns a
// clear error when no profile is saved or the saved profile is invalid, so
// Apply operations never run with missing/bad configuration.
func (s *Service) activeProfile() (core.Profile, error) {
	st, err := s.store.Load()
	if err != nil {
		return core.Profile{}, err
	}
	if st.ActiveProfile == nil {
		return core.Profile{}, errors.New("service: no profile saved; save a valid profile before applying")
	}
	p := *st.ActiveProfile
	// Auto-upgrade legacy http base URLs stored before normalization existed so
	// remote endpoints behind an https redirect keep their Authorization header.
	p.BaseURL, _ = core.NormalizeBaseURL(p.BaseURL)
	if err := p.Validate(); err != nil {
		return core.Profile{}, fmt.Errorf("service: saved profile is invalid: %w", err)
	}
	return p, nil
}

// effectiveProfileFor returns the active profile with its selected Model
// overridden by the per-tool selection for toolID, when one is set and is still
// a member of the profile's Models. It reuses [Service.activeProfile] (which
// normalizes the base URL and validates), so it returns that helper's error when
// no valid profile is saved. A stale or absent selection is never an error: the
// profile default Model is left in place. This single helper is used by Apply
// and by status computation so the fingerprint stays consistent across both.
func (s *Service) effectiveProfileFor(toolID string) (core.Profile, error) {
	p, err := s.activeProfile()
	if err != nil {
		return core.Profile{}, err
	}
	st, err := s.store.Load()
	if err != nil {
		return core.Profile{}, err
	}
	if sel := st.ToolModels[toolID]; sel != "" {
		for _, m := range p.Models {
			if m == sel {
				p.Model = sel
				break
			}
		}
	}
	return p, nil
}

// contextEngineEnabled reports the persisted Context Engine master toggle,
// defaulting to false (no MCP work) when the store cannot be read so a settings
// error never turns a successful Apply into an unexpected MCP mutation.
func (s *Service) contextEngineEnabled() bool {
	st, err := s.store.Load()
	if err != nil {
		return false
	}
	return st.ContextEngineEnabled()
}

// injectMCPIfEnabled is the best-effort Context Engine step run after a
// successful Apply. It injects the MintRouter MCP server into toolID only when
// the master toggle is enabled, the tool has a registered injector, and a key is
// available. It never returns an error and never aborts Apply: an injection
// failure is reported as a display-safe note to fold into the Apply message, and
// an empty string means nothing noteworthy happened (silent no-op or success).
func (s *Service) injectMCPIfEnabled(toolID string, enabled bool) string {
	if !enabled {
		return ""
	}
	inj, ok := s.mcpInjector(toolID)
	if !ok {
		return ""
	}
	spec, hasKey, err := s.mcpSpec()
	if err != nil || !hasKey {
		return ""
	}
	if _, err := inj.InjectMCP(spec); err != nil {
		return fmt.Sprintf("Context Engine not injected: %v", err)
	}
	return ""
}

// removeMCPIfCapable is the best-effort Context Engine step run after a
// successful Restore. It removes the MintRouter MCP server from toolID when the
// tool has a registered injector (a safe no-op when nothing was injected). It
// never returns an error and never aborts Restore: a removal failure is reported
// as a display-safe note to fold into the Restore message.
func (s *Service) removeMCPIfCapable(toolID string) string {
	inj, ok := s.mcpInjector(toolID)
	if !ok {
		return ""
	}
	if _, err := inj.RemoveMCP(); err != nil {
		return fmt.Sprintf("Context Engine not removed: %v", err)
	}
	return ""
}

// joinMessage folds an optional MCP note onto a base message, separating them
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
// and returns an error for an unknown tool or an invalid/missing profile. After
// a successful Apply it injects the Context Engine MCP server when the master
// toggle is enabled; an MCP failure never aborts Apply (it is folded into the
// result message).
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
	res, err := a.Apply(p)
	if err != nil {
		return res, err
	}
	if note := s.injectMCPIfEnabled(toolID, s.contextEngineEnabled()); note != "" {
		res.Message = joinMessage(res.Message, note)
	}
	return res, nil
}

// RestoreOne restores the single tool identified by toolID to its pre-apply
// state. It returns an error for an unknown tool; a tool with nothing to restore
// is a safe no-op handled by the adapter. After a successful Restore it removes
// the Context Engine MCP server for capable tools; an MCP failure never aborts
// Restore (it is folded into the result message).
func (s *Service) RestoreOne(toolID string) (core.RestoreResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.reg.Get(toolID)
	if !ok {
		return core.RestoreResult{}, fmt.Errorf("service: unknown tool %q", toolID)
	}
	res, err := a.Restore()
	if err != nil {
		return res, err
	}
	if note := s.removeMCPIfCapable(toolID); note != "" {
		res.Message = joinMessage(res.Message, note)
	}
	return res, nil
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
	enabled := s.contextEngineEnabled()
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
			r.Message = joinMessage(res.Message, s.injectMCPIfEnabled(a.ID(), enabled))
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
			r.Message = joinMessage(res.Message, s.removeMCPIfCapable(a.ID()))
		}
		out = append(out, r)
	}
	return out, nil
}
