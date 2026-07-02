package droid

import (
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

// removeManagedEntry deletes the MintSwitch-owned customModels entry from
// root, dropping the array when it becomes empty and clearing the top-level
// "model" when it still points at the removed entry's model. Every other
// entry and key is preserved. It reports whether root was modified.
func removeManagedEntry(root map[string]any) bool {
	models, _ := root[customModelsKey].([]any)
	kept := make([]any, 0, len(models))
	ourModel := ""
	found := false
	for _, v := range models {
		if isOurEntry(v) {
			found = true
			if obj, ok := v.(map[string]any); ok {
				ourModel, _ = obj["model"].(string)
			}
			continue
		}
		kept = append(kept, v)
	}
	if !found {
		return false
	}
	if len(kept) == 0 {
		delete(root, customModelsKey)
	} else {
		root[customModelsKey] = kept
	}
	if m, _ := root["model"].(string); m != "" && m == ourModel {
		delete(root, "model")
	}
	return true
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
