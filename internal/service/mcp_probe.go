package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"mintswitch/internal/core"
)

// mcpInitializeBody builds the JSON-RPC "initialize" request body sent by the
// Test-Connection probe. It contains no secret.
func mcpInitializeBody() []byte {
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "mintswitch",
				"version": "1",
			},
		},
	}
	b, _ := json.Marshal(req)
	return b
}

// probeMCP POSTs a JSON-RPC initialize request to spec.Endpoint with the saved
// key as a Bearer token and maps the outcome to an [MCPTestResult]. It drains
// and discards the response body. The key and Authorization header are never
// placed into the result or any error text (the endpoint URL carries no secret).
func probeMCP(ctx context.Context, client *http.Client, spec core.MCPServerSpec) MCPTestResult {
	spec = spec.Normalized()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.Endpoint, bytes.NewReader(mcpInitializeBody()))
	if err != nil {
		return MCPTestResult{OK: false, Meaning: "Could not build the request: invalid endpoint URL."}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+spec.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return MCPTestResult{OK: false, Status: 0, Meaning: transportMeaning(err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	return MCPTestResult{
		OK:      resp.StatusCode/100 == 2,
		Status:  resp.StatusCode,
		Meaning: statusMeaning(resp.StatusCode),
	}
}

// statusMeaning maps an HTTP status code to a user-facing explanation, per the
// MintRouter Remote MCP error contract.
func statusMeaning(code int) string {
	switch {
	case code/100 == 2:
		return "Connected: MintRouter MCP is reachable and your key is valid."
	case code == 401:
		return "Unauthorized: the MintRouter API key is invalid, expired, or revoked."
	case code == 403:
		return "Forbidden: your key is not provisioned for MCP (auth-group / strict mode)."
	case code == 404:
		return "Not found: MCP is not enabled (feature flag off / context-engine opt-in off) or the endpoint path is wrong."
	case code == 429:
		return "Rate limited: MintRouter is reachable but is throttling requests; retry shortly."
	default:
		return fmt.Sprintf("Unexpected response (HTTP %d).", code)
	}
}

// transportMeaning renders a transport-level failure (DNS, connection refused,
// TLS, timeout) into a display-safe message. The underlying error may reference
// the endpoint URL but never the key or Authorization header.
func transportMeaning(err error) string {
	return "Could not reach the MintRouter MCP endpoint: " + err.Error()
}
