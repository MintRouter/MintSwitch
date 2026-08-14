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
// canonical ID plus the optional human-friendly display name the endpoint
// advertises. Never secret — safe to return to the frontend.
type ModelOption struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

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
// ID plus the display name the endpoint advertises (when any), so the dialog
// can seed friendly names. The API key is transient: it is used only for this
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
	return s.fetchModels(base, key)
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
// object shape ({"id": ...}, with "name"/"model" as fallbacks) as well as a
// bare string element. DisplayName ("display_name", with "name" as fallback
// when it wasn't consumed as the ID) is the optional human-friendly label.
type modelEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Model       string `json:"model"`
	DisplayName string `json:"display_name"`
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

// optionsOf collects the non-empty identifier of each entry (ID, else Model,
// else Name), trimmed and de-duplicated, plus its optional display name
// ("display_name", else "name" when Name wasn't consumed as the ID). Display
// names equal to the ID are dropped as noise. Nothing secret is preserved.
func optionsOf(entries []modelEntry) []ModelOption {
	options := make([]ModelOption, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		nameUsedAsID := false
		id := strings.TrimSpace(e.ID)
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
		options = append(options, ModelOption{ID: id, DisplayName: display})
	}
	return options
}
