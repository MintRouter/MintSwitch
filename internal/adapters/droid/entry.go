package droid

import (
	"encoding/json"

	"mintswitch/internal/core"
)

// customModelEntry builds the MintSwitch-owned object written into the
// "customModels" array for the given profile, using Droid's camelCase BYOK
// schema.
func customModelEntry(p core.Profile) map[string]any {
	return map[string]any{
		"model":           p.Model,
		"displayName":     entryDisplayName,
		"baseUrl":         p.BaseURL,
		"apiKey":          p.APIKey,
		"provider":        providerType,
		"maxOutputTokens": defaultMaxOutputTokens,
	}
}

// upsertCustomModel replaces the existing MintSwitch-owned entry (identified by
// its displayName) in root's customModels array, or appends one when absent.
// Every other entry in the array is preserved as-is.
func upsertCustomModel(root map[string]any, entry map[string]any) {
	models, _ := root[customModelsKey].([]any)
	for i, v := range models {
		if isOurEntry(v) {
			models[i] = entry
			root[customModelsKey] = models
			return
		}
	}
	root[customModelsKey] = append(models, entry)
}

// isOurEntry reports whether v is the MintSwitch-owned customModels entry,
// identified solely by the reserved displayName.
func isOurEntry(v any) bool {
	obj, ok := v.(map[string]any)
	if !ok {
		return false
	}
	name, _ := obj["displayName"].(string)
	return name == entryDisplayName
}

// hasManagedEntry reports whether root's customModels array still contains
// the MintSwitch-owned entry that Apply writes.
func hasManagedEntry(root map[string]any) bool {
	models, _ := root[customModelsKey].([]any)
	for _, v := range models {
		if isOurEntry(v) {
			return true
		}
	}
	return false
}

// extractMarker decodes the MintSwitch marker from the parsed config, if present.
func extractMarker(root map[string]any) (core.Marker, bool) {
	raw, ok := root[core.MarkerKey]
	if !ok {
		return core.Marker{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return core.Marker{}, false
	}
	var m core.Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return core.Marker{}, false
	}
	if !m.Managed {
		return core.Marker{}, false
	}
	return m, true
}
