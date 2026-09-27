package enhance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mintswitch/internal/core"
	"mintswitch/internal/settings"
)

// CLIName is the sub-command the rendered slash commands invoke on the
// MintSwitch binary: `MintSwitch enhance-prompt --tool <id>`.
const CLIName = "enhance-prompt"

// requestTimeout bounds one enhance-prompt round trip.
const requestTimeout = 90 * time.Second

// maxPromptBytes bounds the prompt read from stdin so a runaway pipe cannot
// exhaust memory.
const maxPromptBytes = 4 << 20

// ProviderResolver resolves the provider in effect for a tool. It is
// satisfied by a loaded [settings.State].
type ProviderResolver interface {
	ProviderForTool(toolID string) (core.Provider, bool, bool)
}

// RunCLI implements the `enhance-prompt` mode. It reads the rough prompt from
// stdin, resolves toolID's effective provider through the settings store
// (keychain-first, exactly like the desktop app), posts to
// {base}/v1/enhance-prompt and writes the enhanced prompt to stdout. Errors
// are returned (the caller prints them to stderr prefixed "enhance-prompt:")
// and never contain the key value. client may be nil (a default client with
// a timeout is used).
func RunCLI(ctx context.Context, store *settings.Store, toolID string, stdin io.Reader, stdout io.Writer, client *http.Client) error {
	if strings.TrimSpace(toolID) == "" {
		return errors.New("--tool is required")
	}
	if !Supported(toolID) {
		return fmt.Errorf("unknown tool %q", toolID)
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, maxPromptBytes+1))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	if len(raw) > maxPromptBytes {
		return errors.New("prompt is too large")
	}
	prompt := strings.TrimSpace(string(raw))
	if prompt == "" {
		return errors.New("empty prompt")
	}
	st, err := store.Load()
	if err != nil {
		return fmt.Errorf("load MintSwitch settings: %w", err)
	}
	pr, _, ok := st.ProviderForTool(toolID)
	if !ok {
		return errors.New("no provider configured in MintSwitch; add one and apply it to this tool")
	}
	if strings.TrimSpace(pr.APIKey) == "" {
		return fmt.Errorf("provider %q has no API key stored in MintSwitch", pr.Name)
	}
	base, _ := core.NormalizeBaseURL(pr.BaseURL)
	if base == "" {
		return fmt.Errorf("provider %q has no base URL", pr.Name)
	}
	enhanced, err := Enhance(ctx, client, base, pr.APIKey, prompt)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, enhanced+"\n")
	return err
}

// EndpointURL returns the enhance-prompt URL for base. Providers store the
// OpenAI-style base with a trailing "/v1" (e.g. http://localhost:8318/v1);
// the endpoint is served under /v1, so exactly one trailing "/v1" is folded
// rather than doubled.
func EndpointURL(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	b = strings.TrimSuffix(b, "/v1")
	return b + "/v1/enhance-prompt"
}

// Enhance posts prompt to base's /v1/enhance-prompt with a bearer key and
// returns the enhanced_prompt field. Error messages carry the HTTP status and
// the server's error.message when present, never the key.
func Enhance(ctx context.Context, client *http.Client, base, apiKey, prompt string) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	body, err := json.Marshal(map[string]string{"prompt": prompt})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, EndpointURL(base), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "MintSwitch/"+CLIName)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("server unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := serverErrorMessage(data)
		if msg == "" {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
		}
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, msg)
	}
	var out struct {
		EnhancedPrompt string `json:"enhanced_prompt"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("invalid JSON response: %w", err)
	}
	if strings.TrimSpace(out.EnhancedPrompt) == "" {
		return "", errors.New("response carried no enhanced_prompt")
	}
	return out.EnhancedPrompt, nil
}

// serverErrorMessage extracts error.message (OpenAI-style) or a top-level
// message from an error body, or "" when neither is present.
func serverErrorMessage(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &e) != nil {
		return ""
	}
	if e.Error.Message != "" {
		return e.Error.Message
	}
	return e.Message
}
