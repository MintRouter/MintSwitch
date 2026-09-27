package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"mintswitch/internal/core"
)

// modelsFetchTimeout bounds the models fetch so a hung endpoint cannot block
// the UI indefinitely.
const modelsFetchTimeout = 10 * time.Second

// modelsFetchMaxBody caps how much of the response body is read, so a
// misbehaving endpoint cannot make the app buffer an unbounded payload.
const modelsFetchMaxBody = 4 << 20

// UninstallPlan is the non-secret, read-only preview returned by
// [Service.PlanUninstall]. Command and Target are display-only; execution
// accepts only a tool ID and resolves a fresh plan.
type UninstallPlan struct {
	Method     string `json:"method"`
	Action     string `json:"action"`
	Command    string `json:"command"`
	Target     string `json:"target"`
	Warning    string `json:"warning"`
	CanExecute bool   `json:"can_execute"`
}

// ModelOption is one advertised model returned by the models fetch: its
// canonical ID plus the optional human-friendly display name and context
// window the endpoint advertises. Never secret — safe to return to the
// frontend.
type ModelOption struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	// ContextWindow is the model's advertised context window in tokens; 0
	// means the endpoint did not advertise one.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens is the model's advertised maximum completion (output)
	// tokens; 0 means the endpoint did not advertise one.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// ReasoningLevels is the model's advertised ordered reasoning-effort
	// levels (e.g. "low", "medium", "high"); empty means none advertised.
	ReasoningLevels []string `json:"reasoning_levels,omitempty"`
}

// codexClientVersion is sent as the ?client_version= query of the
// reasoning-level enrichment request (see [Service.enrichReasoningLevels]).
// Gateways that serve Codex clients (e.g. MintRouter) answer that query with
// Codex's catalog shape, which carries per-model supported_reasoning_levels;
// a version >= 0.144.0 unlocks the extended levels ("max", "ultra").
const codexClientVersion = "0.157.0"

// reasoningFetchTimeout bounds the optional reasoning-level enrichment
// request, shorter than modelsFetchTimeout so an endpoint that stalls on the
// unfamiliar query adds little to the fetch the user is waiting on.
const reasoningFetchTimeout = 4 * time.Second

// FetchProviderModels queries the stored provider's OpenAI-compatible
// endpoint (GET {base_url}/models with a Bearer key) and returns the sorted,
// de-duplicated model IDs it advertises. It is read-only: it never mutates
// settings — the caller decides what to do with the list. Errors are
// display-safe: they never include the API key, the Authorization header, or
// the endpoint's response body.
func (s *Service) FetchProviderModels(providerID string) ([]string, error) {
	st, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	pr, ok := st.Provider(strings.TrimSpace(providerID))
	if !ok {
		return nil, fmt.Errorf("service: unknown provider %q", providerID)
	}
	// Re-normalize defensively: providers saved before normalization existed
	// may still carry a raw base URL (mirrors resolveProfile).
	base, _ := core.NormalizeBaseURL(pr.BaseURL)
	if base == "" {
		return nil, errors.New("service: the provider has no base URL saved; set one before fetching models")
	}
	options, err := s.fetchModels(base, pr.APIKey)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(options))
	for i, o := range options {
		ids[i] = o.ID
	}
	return ids, nil
}

// FetchEndpointModels queries {baseURL}/models like [Service.FetchProviderModels]
// but for endpoint values that may not be saved yet, so the Add/Edit dialog
// can list models before the provider is persisted. It returns each model's
// ID plus the display name, context window and reasoning-effort levels the
// endpoint advertises (when any, see [Service.enrichReasoningLevels]), so the
// dialog can seed them. The API key is transient: it is used only for this
// one request and is never stored, logged, or included in errors. When apiKey
// is blank and providerID names a stored provider, that provider's stored key
// is used instead (the Edit flow, where the key never round-trips to the
// frontend) — but only when the normalized baseURL matches the provider's
// stored base URL, so a stored key can never be sent to an arbitrary
// endpoint. Read-only: it never mutates settings, and errors stay
// display-safe.
func (s *Service) FetchEndpointModels(baseURL, apiKey, providerID string) ([]ModelOption, error) {
	base, _ := core.NormalizeBaseURL(baseURL)
	if base == "" {
		return nil, errors.New("service: enter a valid base URL before fetching models")
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		if id := strings.TrimSpace(providerID); id != "" {
			st, err := s.store.Load()
			if err != nil {
				return nil, err
			}
			pr, ok := st.Provider(id)
			if !ok {
				return nil, fmt.Errorf("service: unknown provider %q", id)
			}
			stored, _ := core.NormalizeBaseURL(pr.BaseURL)
			if stored == "" || stored != base {
				return nil, errors.New("service: enter the API key for the new endpoint before fetching models")
			}
			key = strings.TrimSpace(pr.APIKey)
		}
	}
	options, err := s.fetchModels(base, key)
	if err != nil {
		return nil, err
	}
	s.enrichReasoningLevels(base, key, options)
	return options, nil
}

// fetchModels performs the actual GET {base}/models request with an optional
// Bearer key and parses the advertised models (ID + optional display name).
// Shared by the stored-provider and transient-endpoint entry points; errors
// never include the key, the Authorization header, or the endpoint's
// response body.
func (s *Service) fetchModels(base, key string) ([]ModelOption, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, errors.New("service: could not build the models request; check the provider's base URL")
	}
	req.Header.Set("Accept", "application/json")
	if key = strings.TrimSpace(key); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := s.modelsClient
	if client == nil {
		client = &http.Client{Timeout: modelsFetchTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		// Never propagate the raw transport error: it can embed the request
		// URL and driver detail. The key is only in a header, but keep the
		// message fully display-safe regardless.
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return nil, fmt.Errorf("service: the endpoint did not respond within %s", modelsFetchTimeout)
		}
		return nil, errors.New("service: could not reach the endpoint; check the base URL and your network")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("service: the endpoint returned HTTP %d%s", resp.StatusCode, httpStatusHint(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, modelsFetchMaxBody))
	if err != nil {
		return nil, errors.New("service: the endpoint's response could not be read")
	}
	models, ok := parseModelOptions(body)
	if !ok {
		return nil, errors.New("service: the endpoint's response was not a recognizable model list")
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// enrichReasoningLevels best-effort fills ReasoningLevels for models the
// plain listing advertised none for, from a second GET
// {base}/models?client_version=... request. Plain OpenAI /models entries
// carry no reasoning metadata, but gateways that serve Codex clients answer
// that query with Codex's catalog shape ({"models":[{"slug":...,
// "supported_reasoning_levels":[{"effort":...}]}]}); endpoints that ignore
// the query simply return the same plain listing. Any failure (transport,
// non-200, unparseable body) is silently ignored — the plain listing already
// succeeded and the levels are optional metadata. Only IDs present in the
// plain listing are enriched, so the query can never add models.
func (s *Service) enrichReasoningLevels(base, key string, models []ModelOption) {
	missing := false
	for _, m := range models {
		if len(m.ReasoningLevels) == 0 {
			missing = true
			break
		}
	}
	if !missing {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), reasoningFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models?client_version="+codexClientVersion, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/json")
	if key = strings.TrimSpace(key); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := s.modelsClient
	if client == nil {
		client = &http.Client{Timeout: modelsFetchTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, modelsFetchMaxBody))
	if err != nil {
		return
	}
	extra, ok := parseModelOptions(body)
	if !ok {
		return
	}
	levels := make(map[string][]string, len(extra))
	for _, o := range extra {
		if len(o.ReasoningLevels) > 0 {
			levels[o.ID] = o.ReasoningLevels
		}
	}
	for i := range models {
		if len(models[i].ReasoningLevels) == 0 {
			models[i].ReasoningLevels = levels[models[i].ID]
		}
	}
}

// reasoningBackfillTools are the tools whose Apply writes per-model
// reasoning-effort levels (Codex's model catalog), and so benefit from
// [Service.backfillReasoningLevels].
var reasoningBackfillTools = map[string]bool{"codex": true}

// backfillReasoningLevels best-effort fills in toolID's effective provider's
// missing ModelReasoningLevels from the endpoint before an Apply, and
// persists them. Levels are otherwise only captured when the provider form
// fetches models, so a provider saved before levels existed (or never
// re-fetched) would apply a catalog with empty effort pickers until the user
// re-opened and re-saved it. Only tools in reasoningBackfillTools trigger
// it, only models already listed get levels, existing levels are never
// replaced, and every failure (no key, transport, non-Codex endpoint) leaves
// settings untouched. The caller holds s.mu.
func (s *Service) backfillReasoningLevels(toolID string) {
	if !reasoningBackfillTools[toolID] {
		return
	}
	st, err := s.store.Load()
	if err != nil {
		return
	}
	pr, _, ok := resolveProvider(st, toolID)
	if !ok {
		return
	}
	base, _ := core.NormalizeBaseURL(pr.BaseURL)
	if base == "" {
		return
	}
	options := make([]ModelOption, 0, len(pr.Models))
	for _, m := range pr.Models {
		options = append(options, ModelOption{ID: m, ReasoningLevels: pr.ModelReasoningLevels[m]})
	}
	s.enrichReasoningLevels(base, pr.APIKey, options)
	levels := make(map[string][]string, len(options))
	for m, l := range pr.ModelReasoningLevels {
		levels[m] = l
	}
	added := false
	for _, o := range options {
		if len(o.ReasoningLevels) > 0 && len(levels[o.ID]) == 0 {
			levels[o.ID] = o.ReasoningLevels
			added = true
		}
	}
	if !added {
		return
	}
	for i := range st.Providers {
		if st.Providers[i].ID == pr.ID {
			st.Providers[i].ModelReasoningLevels = normalizeModelReasoningLevels(levels, pr.Models)
			_ = s.store.Save(st)
			return
		}
	}
}

// limitsBackfillTools are the tools whose Apply writes per-model context
// windows and/or max output tokens, and so benefit from
// [Service.backfillModelLimits].
var limitsBackfillTools = map[string]bool{"claude-code": true, "codex": true, "opencode": true, "pi": true}

// backfillModelLimits best-effort fills in toolID's effective provider's
// missing ModelContextWindows and ModelMaxOutputTokens from the endpoint's
// plain /models listing before an Apply, and persists them. Limits are
// otherwise only captured when the provider form fetches models, so a
// provider saved before max output tokens existed (or whose models were typed
// by hand) would apply tool configs with default limits until the user
// re-opened and re-saved it. Only tools in limitsBackfillTools trigger it,
// only when at least one listed model lacks a window or an output cap, only
// models already listed get values, existing values are never replaced, and
// every failure (no key, transport, non-200, unparseable body) leaves
// settings untouched. The caller holds s.mu.
func (s *Service) backfillModelLimits(toolID string) {
	if !limitsBackfillTools[toolID] {
		return
	}
	st, err := s.store.Load()
	if err != nil {
		return
	}
	pr, _, ok := resolveProvider(st, toolID)
	if !ok {
		return
	}
	missing := false
	for _, m := range pr.Models {
		if pr.ModelContextWindows[m] <= 0 || pr.ModelMaxOutputTokens[m] <= 0 {
			missing = true
			break
		}
	}
	if !missing {
		return
	}
	base, _ := core.NormalizeBaseURL(pr.BaseURL)
	if base == "" {
		return
	}
	options, err := s.fetchModels(base, pr.APIKey)
	if err != nil {
		return
	}
	windows := make(map[string]int, len(pr.Models))
	for m, w := range pr.ModelContextWindows {
		windows[m] = w
	}
	outputs := make(map[string]int, len(pr.Models))
	for m, n := range pr.ModelMaxOutputTokens {
		outputs[m] = n
	}
	added := false
	for _, o := range options {
		if o.ContextWindow > 0 && windows[o.ID] <= 0 {
			windows[o.ID] = o.ContextWindow
			added = true
		}
		if o.MaxOutputTokens > 0 && outputs[o.ID] <= 0 {
			outputs[o.ID] = o.MaxOutputTokens
			added = true
		}
	}
	if !added {
		return
	}
	for i := range st.Providers {
		if st.Providers[i].ID == pr.ID {
			st.Providers[i].ModelContextWindows = normalizeModelContextWindows(windows, pr.Models)
			st.Providers[i].ModelMaxOutputTokens = normalizeModelContextWindows(outputs, pr.Models)
			_ = s.store.Save(st)
			return
		}
	}
}

// httpStatusHint maps common /models failure statuses to a short display-safe
// hint appended to the error. It never includes the response body.
func httpStatusHint(code int) string {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return " — the API key was rejected"
	case code == http.StatusNotFound:
		return " — no /models endpoint at this base URL"
	case code == http.StatusTooManyRequests:
		return " — rate limited; try again later"
	case code >= 500:
		return " — the endpoint had a server error"
	}
	return ""
}

// modelEntry decodes one element of a models listing. It tolerates the OpenAI
// object shape ({"id": ...}, with "slug"/"model"/"name" as fallbacks — "slug"
// is the Codex catalog shape's identifier) as well as a bare string element. DisplayName ("display_name", with "name" as fallback
// when it wasn't consumed as the ID) is the optional human-friendly label.
// The context-window fields (standard OpenAI /models has none, but many
// providers advertise one under varying names) are RawMessage so a quirky
// non-numeric value is simply ignored instead of failing the whole parse.
type modelEntry struct {
	ID               string          `json:"id"`
	Slug             string          `json:"slug"`
	Name             string          `json:"name"`
	Model            string          `json:"model"`
	DisplayName      string          `json:"display_name"`
	ContextWindow    json.RawMessage `json:"context_window"`
	ContextLength    json.RawMessage `json:"context_length"`
	MaxContextLength json.RawMessage `json:"max_context_length"`
	// The max-output fields (OpenAI-compatible gateways advertise the
	// completion cap under varying names) are RawMessage for the same reason.
	MaxCompletionTokens json.RawMessage `json:"max_completion_tokens"`
	MaxOutputTokens     json.RawMessage `json:"max_output_tokens"`
	MaxTokens           json.RawMessage `json:"max_tokens"`
	// SupportedReasoningLevels is the Codex catalog shape's per-model effort
	// list ([{"effort":"low",...}]); bare strings are accepted too. RawMessage
	// so an unexpected shape is ignored instead of failing the whole parse.
	SupportedReasoningLevels json.RawMessage `json:"supported_reasoning_levels"`
}

// reasoningLevelsOf returns the entry's advertised reasoning-effort levels in
// order: each element's "effort" (or a bare string), trimmed, lower-cased,
// de-duplicated, keeping only well-formed level tokens (see
// validReasoningLevel). nil means none advertised or an unusable shape.
func reasoningLevelsOf(e modelEntry) []string {
	if len(e.SupportedReasoningLevels) == 0 {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(e.SupportedReasoningLevels, &raw); err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range raw {
		var level string
		if err := json.Unmarshal(r, &level); err != nil {
			var obj struct {
				Effort string `json:"effort"`
			}
			if err := json.Unmarshal(r, &obj); err != nil {
				continue
			}
			level = obj.Effort
		}
		level = strings.ToLower(strings.TrimSpace(level))
		if !validReasoningLevel(level) || seen[level] {
			continue
		}
		seen[level] = true
		out = append(out, level)
	}
	return out
}

// validReasoningLevel reports whether level is a plausible reasoning-effort
// token: 1-32 characters of [a-z0-9_-]. Anything else (free text, markup) is
// dropped so it can never reach a tool config.
func validReasoningLevel(level string) bool {
	if level == "" || len(level) > 32 {
		return false
	}
	for _, c := range level {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// contextWindowOf returns the entry's advertised context window: the first
// positive integer among context_window, context_length and
// max_context_length. Non-numeric and non-positive values are skipped; 0
// means the entry advertises none.
func contextWindowOf(e modelEntry) int {
	for _, raw := range []json.RawMessage{e.ContextWindow, e.ContextLength, e.MaxContextLength} {
		if n := positiveInt(raw); n > 0 {
			return n
		}
	}
	return 0
}

// maxOutputTokensOf returns the entry's advertised maximum completion
// tokens: the first positive integer among max_completion_tokens,
// max_output_tokens and max_tokens. 0 means the entry advertises none.
func maxOutputTokensOf(e modelEntry) int {
	for _, raw := range []json.RawMessage{e.MaxCompletionTokens, e.MaxOutputTokens, e.MaxTokens} {
		if n := positiveInt(raw); n > 0 {
			return n
		}
	}
	return 0
}

// positiveInt decodes raw as a JSON number and returns it as a positive int,
// or 0 for absent, non-numeric or non-positive values.
func positiveInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0
	}
	if n := int(f); n > 0 {
		return n
	}
	return 0
}

func (m *modelEntry) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		m.ID = s
		return nil
	}
	type plain modelEntry
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*m = modelEntry(p)
	return nil
}

// parseModelOptions extracts models from an OpenAI-style models response
// ({"data":[{"id":...}]}), tolerating a "models" key or a top-level array.
// ok=false means the payload was not a recognizable model list.
func parseModelOptions(body []byte) (options []ModelOption, ok bool) {
	var envelope struct {
		Data   []modelEntry `json:"data"`
		Models []modelEntry `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && (envelope.Data != nil || envelope.Models != nil) {
		entries := envelope.Data
		if entries == nil {
			entries = envelope.Models
		}
		return optionsOf(entries), true
	}
	var list []modelEntry
	if err := json.Unmarshal(body, &list); err == nil && list != nil {
		return optionsOf(list), true
	}
	return nil, false
}

// optionsOf collects the non-empty identifier of each entry (ID, else Slug,
// else Model, else Name), trimmed and de-duplicated, plus its optional
// display name ("display_name", else "name" when Name wasn't consumed as the
// ID), advertised context window and reasoning-effort levels. Display names equal to the ID are dropped as
// noise. Nothing secret is preserved.
func optionsOf(entries []modelEntry) []ModelOption {
	options := make([]ModelOption, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		nameUsedAsID := false
		id := strings.TrimSpace(e.ID)
		if id == "" {
			id = strings.TrimSpace(e.Slug)
		}
		if id == "" {
			id = strings.TrimSpace(e.Model)
		}
		if id == "" {
			id = strings.TrimSpace(e.Name)
			nameUsedAsID = id != ""
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		display := strings.TrimSpace(e.DisplayName)
		if display == "" && !nameUsedAsID {
			display = strings.TrimSpace(e.Name)
		}
		if display == id {
			display = ""
		}
		options = append(options, ModelOption{
			ID:              id,
			DisplayName:     display,
			ContextWindow:   contextWindowOf(e),
			MaxOutputTokens: maxOutputTokensOf(e),
			ReasoningLevels: reasoningLevelsOf(e),
		})
	}
	return options
}
