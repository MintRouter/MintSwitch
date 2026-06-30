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
	"strings"

	"mintswitch/internal/adapters/claudecode"
	"mintswitch/internal/adapters/codex"
	"mintswitch/internal/adapters/factorydroid"
	"mintswitch/internal/adapters/opencode"
	"mintswitch/internal/adapters/pi"
	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/installer"
	"mintswitch/internal/paths"
	"mintswitch/internal/settings"
)

// Service is the backend façade bound into the Wails application.
type Service struct {
	reg   *core.Registry
	store *settings.Store
	inst  *installer.Installer
}

// ToolView is the per-tool summary returned by [Service.ListTools].
type ToolView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Installed   bool     `json:"installed"`
	Status      string   `json:"status"`
	Detail      string   `json:"detail"`
	ConfigPaths []string `json:"config_paths"`
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
	Label          string `json:"label"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
	SmallFastModel string `json:"small_fast_model"`
	HasKey         bool   `json:"has_key"`
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
	return NewWithRegistry(reg, settings.NewStore(r.SettingsPath()))
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
		installed, _ := a.Detect()
		status, detail, serr := a.Status(p)
		if serr != nil {
			detail = serr.Error()
		}
		out = append(out, ToolView{
			ID:          a.ID(),
			Name:        a.Name(),
			Installed:   installed,
			Status:      status.String(),
			Detail:      detail,
			ConfigPaths: a.ConfigPaths(),
		})
	}
	return out, nil
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
	return ProfileView{
		Label:          p.Label,
		BaseURL:        p.BaseURL,
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
	if strings.TrimSpace(p.APIKey) == "" && st.ActiveProfile != nil {
		p.APIKey = st.ActiveProfile.APIKey
	}
	if err := p.Validate(); err != nil {
		return err
	}
	st.ActiveProfile = &p
	return s.store.Save(st)
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

// Uninstall uninstalls the tool identified by toolID globally via npm. Its
// return contract matches [Service.Install].
func (s *Service) Uninstall(toolID string) (InstallResult, error) {
	args, out, err := s.inst.Uninstall(context.Background(), toolID)
	return s.installResult(toolID, "uninstall", args, out, err)
}

// installResult maps the installer's (args, output, error) into an
// [InstallResult]. Unknown tools become a hard error; npm-missing and command
// failures become a non-OK result with a clear message so the UI can show the
// command and its output instead of throwing.
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
