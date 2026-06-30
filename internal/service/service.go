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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mintswitch/internal/adapters/claudecode"
	"mintswitch/internal/adapters/codex"
	"mintswitch/internal/adapters/custom"
	"mintswitch/internal/adapters/factorydroid"
	"mintswitch/internal/adapters/opencode"
	"mintswitch/internal/adapters/pi"
	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/installer"
	"mintswitch/internal/paths"
	"mintswitch/internal/settings"
)

// builtinIDs is the set of reserved built-in tool IDs. A custom tool may not
// claim any of these.
var builtinIDs = map[string]bool{
	"claude-code":   true,
	"codex":         true,
	"opencode":      true,
	"factory-droid": true,
	"pi":            true,
}

// Service is the backend façade bound into the Wails application.
type Service struct {
	reg   *core.Registry
	store *settings.Store
	inst  *installer.Installer
	// r and e are retained so custom tools added at runtime can construct a
	// generic adapter. They are nil for the test seams that inject a pre-built
	// registry; AddCustomTool requires them.
	r *paths.Resolver
	e *backup.Engine
}

// ToolView is the per-tool summary returned by [Service.ListTools].
type ToolView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Installed   bool     `json:"installed"`
	Status      string   `json:"status"`
	Detail      string   `json:"detail"`
	ConfigPaths []string `json:"config_paths"`
	// Custom is true for user-defined tools managed by the generic JSON-template
	// adapter, false for the built-ins. The UI hides Install/Uninstall for these
	// and offers a Remove action instead.
	Custom bool `json:"custom"`
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
	Label          string   `json:"label"`
	BaseURL        string   `json:"base_url"`
	Models         []string `json:"models"`
	Model          string   `json:"model"`
	SmallFastModel string   `json:"small_fast_model"`
	HasKey         bool     `json:"has_key"`
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
// paths.Resolver, a backup.Engine under the user's data dir, and the five
// built-in tool adapters. It returns an error only if the home/data directories
// cannot be resolved.
func New() (*Service, error) {
	r, err := paths.NewResolver()
	if err != nil {
		return nil, err
	}
	return NewWithDeps(r, backup.NewEngine(r.BackupsDir())), nil
}

// NewWithDeps builds a Service from an injected Resolver and backup Engine,
// registering the five built-in adapters. Tests can point r.Home at a temp dir.
func NewWithDeps(r *paths.Resolver, e *backup.Engine) *Service {
	reg := core.NewRegistry()
	reg.Register(claudecode.New(r, e))
	reg.Register(codex.New(r, e))
	reg.Register(opencode.New(r, e))
	reg.Register(factorydroid.New(r, e))
	reg.Register(pi.New(r, e))
	inst := installer.NewMethodAware(installer.ExecRunner{}, r)
	store := settings.NewStore(r.SettingsPath())
	s := NewWithInstaller(reg, store, inst)
	s.r = r
	s.e = e
	// Register user-defined custom tools after the built-ins, in saved order.
	// A load failure here is non-fatal: the built-ins still work and the user
	// can re-add custom tools; it must not prevent the app from starting.
	if st, err := store.Load(); err == nil {
		for _, def := range st.CustomTools {
			reg.Register(custom.New(def, r, e))
		}
	}
	return s
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

// viewFor builds a [ToolView] for a single adapter evaluated against profile p.
// A per-tool Status error is surfaced in Detail rather than failing the caller.
func (s *Service) viewFor(a core.ToolAdapter, p core.Profile) ToolView {
	installed, _ := a.Detect()
	status, detail, serr := a.Status(p)
	if serr != nil {
		detail = serr.Error()
	}
	_, isCustom := a.(*custom.Adapter)
	return ToolView{
		ID:          a.ID(),
		Name:        a.Name(),
		Installed:   installed,
		Status:      status.String(),
		Detail:      detail,
		ConfigPaths: a.ConfigPaths(),
		Custom:      isCustom,
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
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	p.BaseURL, _ = core.NormalizeBaseURL(p.BaseURL)
	p.Model = strings.TrimSpace(p.Model)
	p.SmallFastModel = strings.TrimSpace(p.SmallFastModel)
	p.Models = normalizeModels(p.Models, p.Model)
	if strings.TrimSpace(p.APIKey) == "" && st.ActiveProfile != nil {
		p.APIKey = st.ActiveProfile.APIKey
	}
	if err := p.Validate(); err != nil {
		return err
	}
	st.ActiveProfile = &p
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

// ApplyOne applies the active profile to the single tool identified by toolID.
// It first validates the saved profile and returns an error for an unknown tool
// or an invalid/missing profile.
func (s *Service) ApplyOne(toolID string) (core.ApplyResult, error) {
	p, err := s.activeProfile()
	if err != nil {
		return core.ApplyResult{}, err
	}
	a, ok := s.reg.Get(toolID)
	if !ok {
		return core.ApplyResult{}, fmt.Errorf("service: unknown tool %q", toolID)
	}
	return a.Apply(p)
}

// RestoreOne restores the single tool identified by toolID to its pre-apply
// state. It returns an error for an unknown tool; a tool with nothing to restore
// is a safe no-op handled by the adapter.
func (s *Service) RestoreOne(toolID string) (core.RestoreResult, error) {
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
	p, err := s.activeProfile()
	if err != nil {
		return nil, err
	}
	adapters := s.reg.All()
	out := make([]ToolOpResult, 0, len(adapters))
	for _, a := range adapters {
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

// AddCustomTool validates, persists and registers a new user-defined tool. The
// ID is derived as a slug from name and must be unique and not collide with a
// built-in. The template must parse as a JSON object; name and configPath must
// be non-empty. binaryName is optional. On success the new tool is registered
// and its [ToolView] (Custom=true) is returned. The template/api key are never
// logged. It requires a Service built with NewWithDeps (real resolver/engine).
func (s *Service) AddCustomTool(name, configPath, binaryName, template string) (ToolView, error) {
	if s.r == nil || s.e == nil {
		return ToolView{}, errors.New("service: custom tools are unavailable in this configuration")
	}
	name = strings.TrimSpace(name)
	configPath = strings.TrimSpace(configPath)
	binaryName = strings.TrimSpace(binaryName)
	if name == "" {
		return ToolView{}, errors.New("service: custom tool name is required")
	}
	if configPath == "" {
		return ToolView{}, errors.New("service: custom tool config path is required")
	}
	if err := validateTemplate(template); err != nil {
		return ToolView{}, err
	}
	id := slugID(name)
	if id == "" {
		return ToolView{}, errors.New("service: custom tool name must contain at least one letter or digit")
	}
	if builtinIDs[id] {
		return ToolView{}, fmt.Errorf("service: %q collides with a built-in tool id; choose a different name", id)
	}
	st, err := s.store.Load()
	if err != nil {
		return ToolView{}, err
	}
	for _, def := range st.CustomTools {
		if def.ID == id {
			return ToolView{}, fmt.Errorf("service: a custom tool named %q already exists", name)
		}
	}
	def := core.CustomToolDef{
		ID:         id,
		Name:       name,
		ConfigPath: configPath,
		BinaryName: binaryName,
		Template:   template,
	}
	st.CustomTools = append(st.CustomTools, def)
	if err := s.store.Save(st); err != nil {
		return ToolView{}, err
	}
	a := custom.New(def, s.r, s.e)
	s.reg.Register(a)
	var p core.Profile
	if st.ActiveProfile != nil {
		p = *st.ActiveProfile
	}
	return s.viewFor(a, p), nil
}

// RemoveCustomTool detaches and forgets a previously added custom tool. Built-in
// tools cannot be removed. An unknown id is an error.
func (s *Service) RemoveCustomTool(id string) error {
	if builtinIDs[id] {
		return fmt.Errorf("service: %q is a built-in tool and cannot be removed", id)
	}
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i, def := range st.CustomTools {
		if def.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("service: unknown custom tool %q", id)
	}
	st.CustomTools = append(st.CustomTools[:idx], st.CustomTools[idx+1:]...)
	if err := s.store.Save(st); err != nil {
		return err
	}
	s.reg.Unregister(id)
	return nil
}

// validateTemplate reports whether t parses as a JSON object (the only valid
// custom-tool template root).
func validateTemplate(t string) error {
	var root any
	if err := json.Unmarshal([]byte(t), &root); err != nil {
		return fmt.Errorf("service: custom tool template is not valid JSON: %w", err)
	}
	if _, ok := root.(map[string]any); !ok {
		return errors.New("service: custom tool template must be a JSON object")
	}
	return nil
}

// slugID derives a stable, lower-case, hyphen-separated identifier from name.
// Runs of non-alphanumeric characters collapse to a single hyphen and leading/
// trailing hyphens are trimmed.
func slugID(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
