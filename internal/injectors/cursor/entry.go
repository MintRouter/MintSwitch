package cursor

import (
	"errors"
	"strings"

	"mintswitch/internal/core"
)

// errMissingKey is returned by InjectMCP when no API key is present. Its message
// never contains the key.
var errMissingKey = errors.New("cursor: a MintRouter API key is required to inject the MCP server")

// serverEntry builds the JSON object written under mcpServers.<name> for spec.
// Cursor auto-detects the remote streamable-HTTP transport from "url", so no
// "type" field is written.
func serverEntry(spec core.MCPServerSpec) map[string]any {
	return map[string]any{
		"url": spec.Endpoint,
		"headers": map[string]any{
			authHeader: bearerPrefix + spec.APIKey,
		},
	}
}

// entryMatchesSpec reports whether entry is exactly our MintSwitch-owned shape
// for spec: url == endpoint and the Authorization header equal to "Bearer
// <key>". A mismatch on any field means the entry was configured outside
// MintSwitch (or with a different key/endpoint).
func entryMatchesSpec(entry any, spec core.MCPServerSpec) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	if u, _ := obj["url"].(string); u != spec.Endpoint {
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
