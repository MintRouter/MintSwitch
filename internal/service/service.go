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
// API-key handling over the wire: the stored Profile contains secret key
// values (APIKey and the managed APIKeys entries). [Service.GetProfile]
// deliberately never returns those secrets — it returns a [ProfileView]
// carrying only non-secret fields (key entries are reduced to provider name +
// active flag, never a value, not even masked) — so no key is ever sent to a
// browser in server mode (and never logged). On [Service.SaveProfile], an
// empty incoming key value means "keep the stored one", letting the UI submit
// the form without ever round-tripping a secret.
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
	// Keys is the profile's key list (provider names + active flag, never key
	// values), used to populate the per-tool key dropdown. It is empty when no
	// profile is saved.
	Keys []APIKeyView `json:"keys"`
	// SelectedKeyID is the ID of the key entry in effect for this tool: the
	// per-tool override when set and still a member, otherwise the profile's
	// active key. It is empty when no valid profile is saved.
	SelectedKeyID string `json:"selected_key_id"`
	// KeyProvider is the Provider display name of the key entry in effect for
	// this tool. It never carries any part of the key value.
	KeyProvider string `json:"key_provider"`
	// KeyOverridden is true when the key in effect comes from a per-tool
	// override rather than the profile's active key.
	KeyOverridden bool `json:"key_overridden"`
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

// APIKeyView is the non-secret view of one managed API key entry. It exposes
// only the entry ID, the user-chosen provider name and whether the entry is
// the profile's active key — never any part of the key value, not even
// masked.
type APIKeyView struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Active   bool   `json:"active"`
}

// ProfileView is the non-secret view of the active profile returned to the
// frontend. It never carries any API key value; HasKey reports whether one is
// stored and Keys lists the managed entries by provider name only.
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
	// Keys is the managed key list (provider names + active flag only).
	Keys []APIKeyView `json:"keys"`
}

// keyViews maps the profile's key entries to their non-secret views.
func keyViews(p core.Profile) []APIKeyView {
	if len(p.APIKeys) == 0 {
		return nil
	}
	out := make([]APIKeyView, 0, len(p.APIKeys))
	for _, e := range p.APIKeys {
		out = append(out, APIKeyView{ID: e.ID, Provider: e.Provider, Active: e.ID == p.ActiveKeyID})
	}
	return out
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
	selectedKeyID := ""
	keyProvider := ""
	keyOverridden := false
	if eff, err := s.effectiveProfileFor(a.ID()); err == nil {
		p = eff
		selectedModel = eff.Model
		selectedKeyID = eff.ActiveKeyID
		if e, ok := eff.KeyEntry(eff.ActiveKeyID); ok {
			keyProvider = e.Provider
		}
		keyOverridden = eff.ActiveKeyID != fallback.ActiveKeyID
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
		Keys:          keyViews(fallback),
		SelectedKeyID: selectedKeyID,
		KeyProvider:   keyProvider,
		KeyOverridden: keyOverridden,
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
		Keys:           keyViews(*p),
	}, nil
}

// SaveProfile validates and persists p as the active profile. Empty incoming
// key values mean "keep the stored ones" so the UI can submit the form without
// ever round-tripping a secret: an APIKeys entry with an empty Key inherits
// the stored entry's value (matched by ID), and a legacy submission with no
// APIKeys and an empty APIKey keeps the stored key material entirely. The
// merged profile is then validated via [core.Profile.Validate] and an invalid
// profile is rejected with an error.
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
	mergeKeys(&p, st.ActiveProfile)
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
	// Prune stale per-tool key selections likewise, so a removed key entry
	// falls back to the profile's active key.
	for tid, kid := range st.ToolKeys {
		if _, ok := p.KeyEntry(kid); !ok {
			delete(st.ToolKeys, tid)
		}
	}
	st.ActiveProfile = &p
	return s.store.Save(st)
}

// mergeKeys reconciles the incoming profile's key material with the stored
// profile so secrets never have to round-trip through the UI. Entries are
// trimmed, entries without an ID (newly added in the UI) get a fresh unique
// one, and an entry with an empty Key inherits the stored entry's value by
// ID. A legacy submission (no APIKeys) inherits the stored managed list when
// one exists — a non-empty incoming APIKey then updates the active entry —
// or falls back to the v1 "empty means keep" single-key behaviour. The
// result is normalized via [core.Profile.NormalizeKeys], so APIKey always
// mirrors the active entry.
func mergeKeys(p *core.Profile, stored *core.Profile) {
	if len(p.APIKeys) > 0 {
		taken := make(map[string]bool, len(p.APIKeys))
		for i := range p.APIKeys {
			p.APIKeys[i].ID = strings.TrimSpace(p.APIKeys[i].ID)
			p.APIKeys[i].Provider = strings.TrimSpace(p.APIKeys[i].Provider)
			p.APIKeys[i].Key = strings.TrimSpace(p.APIKeys[i].Key)
			taken[p.APIKeys[i].ID] = true
		}
		for i := range p.APIKeys {
			if p.APIKeys[i].ID == "" {
				p.APIKeys[i].ID = newKeyID(taken)
				taken[p.APIKeys[i].ID] = true
			}
			if p.APIKeys[i].Key == "" && stored != nil {
				if e, ok := stored.KeyEntry(p.APIKeys[i].ID); ok {
					p.APIKeys[i].Key = e.Key
				}
			}
		}
		// APIKey is a computed mirror of the active entry: never trust the
		// incoming value, resync it from the merged list.
		p.APIKey = ""
		p.ActiveKeyID = strings.TrimSpace(p.ActiveKeyID)
		p.NormalizeKeys()
		return
	}
	if stored != nil && len(stored.APIKeys) > 0 {
		p.APIKeys = make([]core.APIKeyEntry, len(stored.APIKeys))
		copy(p.APIKeys, stored.APIKeys)
		if strings.TrimSpace(p.ActiveKeyID) == "" {
			p.ActiveKeyID = stored.ActiveKeyID
		}
		if k := strings.TrimSpace(p.APIKey); k != "" {
			for i := range p.APIKeys {
				if p.APIKeys[i].ID == p.ActiveKeyID {
					p.APIKeys[i].Key = k
				}
			}
		}
		p.APIKey = ""
		p.NormalizeKeys()
		return
	}
	if strings.TrimSpace(p.APIKey) == "" && stored != nil {
		p.APIKey = stored.APIKey
	}
	p.NormalizeKeys()
}

// newKeyID returns a fresh API key entry ID not present in taken.
func newKeyID(taken map[string]bool) string {
	for i := 1; ; i++ {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			if id := fmt.Sprintf("key-%d", len(taken)+i); !taken[id] {
				return id
			}
			continue
		}
		if id := "key-" + hex.EncodeToString(b[:]); !taken[id] {
			return id
		}
	}
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

// SetToolKey records (or clears) the per-tool API key selection for toolID. An
// empty keyID deletes the selection so the tool uses the profile's active key.
// A non-empty keyID must reference a member of the active profile's APIKeys,
// otherwise a clear error is returned. The toolID must be a registered tool.
// The selection is persisted via the settings store.
func (s *Service) SetToolKey(toolID, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.reg.Get(toolID); !ok {
		return fmt.Errorf("service: unknown tool %q", toolID)
	}
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		delete(st.ToolKeys, toolID)
		return s.store.Save(st)
	}
	p, err := s.activeProfile()
	if err != nil {
		return err
	}
	if _, ok := p.KeyEntry(keyID); !ok {
		return fmt.Errorf("service: key %q is not one of the profile's api keys", keyID)
	}
	if st.ToolKeys == nil {
		st.ToolKeys = make(map[string]string)
	}
	st.ToolKeys[toolID] = keyID
	return s.store.Save(st)
}

// AddAPIKey appends a new named key entry to the active profile's managed
// list and returns its generated ID. The provider name and key value are
// required; when the profile has no keys yet the new entry becomes active.
// The key value is only persisted (keychain-first), never echoed back.
func (s *Service) AddAPIKey(provider, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return "", err
	}
	if st.ActiveProfile == nil {
		return "", errors.New("service: no profile saved; save a profile before adding keys")
	}
	provider = strings.TrimSpace(provider)
	key = strings.TrimSpace(key)
	if provider == "" {
		return "", errors.New("service: key provider name is required")
	}
	if key == "" {
		return "", errors.New("service: key value is required")
	}
	p := *st.ActiveProfile
	taken := make(map[string]bool, len(p.APIKeys))
	for _, e := range p.APIKeys {
		taken[e.ID] = true
	}
	id := newKeyID(taken)
	p.APIKeys = append(append([]core.APIKeyEntry{}, p.APIKeys...), core.APIKeyEntry{ID: id, Provider: provider, Key: key})
	p.NormalizeKeys()
	st.ActiveProfile = &p
	if err := s.store.Save(st); err != nil {
		return "", err
	}
	return id, nil
}

// RemoveAPIKey deletes the key entry with the given ID from the active
// profile's managed list. Removing the last key is rejected (a profile always
// needs one); removing the active key promotes the first remaining entry.
// Per-tool selections pointing at the removed entry are pruned so those tools
// fall back to the active key.
func (s *Service) RemoveAPIKey(keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	if st.ActiveProfile == nil {
		return errors.New("service: no profile saved")
	}
	p := *st.ActiveProfile
	if _, ok := p.KeyEntry(keyID); !ok {
		return fmt.Errorf("service: key %q is not one of the profile's api keys", keyID)
	}
	if len(p.APIKeys) == 1 {
		return errors.New("service: cannot remove the last api key")
	}
	kept := make([]core.APIKeyEntry, 0, len(p.APIKeys)-1)
	for _, e := range p.APIKeys {
		if e.ID != keyID {
			kept = append(kept, e)
		}
	}
	p.APIKeys = kept
	if p.ActiveKeyID == keyID {
		p.ActiveKeyID = ""
		p.APIKey = ""
	}
	p.NormalizeKeys()
	for tid, kid := range st.ToolKeys {
		if kid == keyID {
			delete(st.ToolKeys, tid)
		}
	}
	st.ActiveProfile = &p
	return s.store.Save(st)
}

// SetActiveAPIKey selects the key entry with the given ID as the profile's
// active key, mirroring it into the effective APIKey adapters consume.
func (s *Service) SetActiveAPIKey(keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return err
	}
	if st.ActiveProfile == nil {
		return errors.New("service: no profile saved")
	}
	p := *st.ActiveProfile
	if _, ok := p.KeyEntry(keyID); !ok {
		return fmt.Errorf("service: key %q is not one of the profile's api keys", keyID)
	}
	p.ActiveKeyID = keyID
	p.NormalizeKeys()
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

// effectiveProfileFor returns the active profile with its selected Model and
// API key overridden by the per-tool selections for toolID, when set and
// still members of the profile's Models/APIKeys. It reuses
// [Service.activeProfile] (which normalizes the base URL and validates), so it
// returns that helper's error when no valid profile is saved. A stale or
// absent selection is never an error: the profile defaults are left in place
// (a key override whose entry has no loadable value also falls back, so an
// unavailable keychain never blanks the applied key). This single helper is
// used by Apply and by status computation so the fingerprint stays consistent
// across both.
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
	if sel := st.ToolKeys[toolID]; sel != "" {
		if e, ok := p.KeyEntry(sel); ok && strings.TrimSpace(e.Key) != "" {
			p.ActiveKeyID = e.ID
			p.APIKey = e.Key
		}
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
