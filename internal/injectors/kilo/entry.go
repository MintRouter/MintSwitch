package kilo

import (
	"errors"
	"strings"

	"mintswitch/internal/core"
)

// errMissingKey is returned by InjectMCP when no API key is present. Its message
// never contains the key.
var errMissingKey = errors.New("kilo: a MintRouter API key is required to inject the MCP server")

// serverEntry builds the JSON object written under mcp.<name> for spec. Kilo
// remote servers carry an explicit "enabled" flag (same schema as OpenCode).
func serverEntry(spec core.MCPServerSpec) map[string]any {
	return map[string]any{
		"type":    typeRemote,
		"url":     spec.Endpoint,
		"enabled": true,
		"headers": map[string]any{
			authHeader: bearerPrefix + spec.APIKey,
		},
	}
}

// entryMatchesSpec reports whether entry is exactly our MintSwitch-owned shape
// for spec: type "remote", url == endpoint, enabled true, and the Authorization
// header equal to "Bearer <key>". A mismatch on any field means the entry was
// configured outside MintSwitch (or with a different key/endpoint).
func entryMatchesSpec(entry any, spec core.MCPServerSpec) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	if s, _ := obj["type"].(string); s != typeRemote {
		return false
	}
	if u, _ := obj["url"].(string); u != spec.Endpoint {
		return false
	}
	if en, _ := obj["enabled"].(bool); !en {
		return false
	}
	headers, ok := obj["headers"].(map[string]any)
	if !ok {
		return false
	}
	auth, _ := headers[authHeader].(string)
	return auth == bearerPrefix+spec.APIKey
}

// isOurShape reports whether entry looks like a MintSwitch-managed entry for the
// given endpoint, independent of the exact key. It is used only for the
// backup-once decision so rotating the key does not re-snapshot a managed file
// (which would hide the pristine pre-inject original).
func isOurShape(entry any, endpoint string) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	if s, _ := obj["type"].(string); s != typeRemote {
		return false
	}
	if u, _ := obj["url"].(string); u != endpoint {
		return false
	}
	headers, ok := obj["headers"].(map[string]any)
	if !ok {
		return false
	}
	auth, _ := headers[authHeader].(string)
	return strings.HasPrefix(auth, bearerPrefix)
}
