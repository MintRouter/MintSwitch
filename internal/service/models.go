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
	ctx, cancel := context.WithTimeout(context.Background(), modelsFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, errors.New("service: could not build the models request; check the provider's base URL")
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(pr.APIKey); key != "" {
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
	models, ok := parseModelIDs(body)
	if !ok {
		return nil, errors.New("service: the endpoint's response was not a recognizable model list")
	}
	sort.Strings(models)
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
// bare string element.
type modelEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
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

// parseModelIDs extracts model IDs from an OpenAI-style models response
// ({"data":[{"id":...}]}), tolerating a "models" key or a top-level array.
// ok=false means the payload was not a recognizable model list.
func parseModelIDs(body []byte) (ids []string, ok bool) {
	var envelope struct {
		Data   []modelEntry `json:"data"`
		Models []modelEntry `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && (envelope.Data != nil || envelope.Models != nil) {
		entries := envelope.Data
		if entries == nil {
			entries = envelope.Models
		}
		return idsOf(entries), true
	}
	var list []modelEntry
	if err := json.Unmarshal(body, &list); err == nil && list != nil {
		return idsOf(list), true
	}
	return nil, false
}

// idsOf collects the non-empty identifier of each entry (ID, else Model, else
// Name), trimmed and de-duplicated, preserving nothing secret.
func idsOf(entries []modelEntry) []string {
	ids := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		id := strings.TrimSpace(e.ID)
		if id == "" {
			id = strings.TrimSpace(e.Model)
		}
		if id == "" {
			id = strings.TrimSpace(e.Name)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}
