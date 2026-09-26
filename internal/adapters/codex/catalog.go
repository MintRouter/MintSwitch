package codex

import (
	_ "embed"

	"mintswitch/internal/core"
)

// catalogFileName is the model-catalog file MintSwitch writes under the Codex
// home dir (see needsCatalog) and points config.toml's model_catalog_json
// at. The name is MintSwitch-specific, so its presence (or a
// model_catalog_json value ending in it) is a reliable managed signal.
const catalogFileName = "mintswitch-models.json"

// catalogKey is the config.toml key selecting a custom model catalog. When
// set, Codex replaces its bundled catalog with the file's contents for the
// whole process (models-manager disables remote refreshes).
const catalogKey = "model_catalog_json"

// catalogBaseInstructions is Codex's own default base-instructions prompt,
// snapshotted from github.com/openai/codex (Apache-2.0):
// codex-rs/protocol/src/prompts/base_instructions/default.md (2026-08). Every
// catalog entry must carry base_instructions (or an instructions template) —
// Codex rejects the whole catalog otherwise — and an entry selected from a
// custom catalog uses them verbatim as the session's system prompt, so they
// must be the real Codex instructions, not a placeholder.
//
//go:embed catalog_base_instructions.md
var catalogBaseInstructions string

// defaultContextWindow is the context_window written for a model whose
// provider advertised none: 272k, the window of the gpt-5.x family Codex
// itself defaults to.
const defaultContextWindow = 272_000

// catalogObject builds the mintswitch-models.json contents: a Codex
// ModelsResponse ({"models": [...]}) with one entry per applied model.
// Field shapes verified against codex-rs/protocol/src/openai_models.rs
// (ModelInfo) and the fallback metadata in
// codex-rs/models-manager/src/model_info.rs (2026-08): the required fields
// are written explicitly, visibility "list" puts the model in the /model
// picker, and priority preserves the profile's model order (lower sorts
// first). display_name comes from the profile's ModelNames when set, and
// context_window from the profile's ModelContextWindows (falling back to
// defaultContextWindow). supported_reasoning_levels and
// default_reasoning_level come from [core.Profile.ReasoningLevels] (limited
// to knownReasoningLevels): Codex
// shows its effort picker only for levels listed here, and clamps (or drops)
// any configured model_reasoning_effort to them — an empty list means no
// effort is ever sent. A pinned ReviewModel not already among the applied
// models is appended last, so Codex has metadata for it too.
func catalogObject(p core.Profile) map[string]any {
	slugs := p.ApplyModels()
	if p.ReviewModel != "" {
		present := false
		for _, m := range slugs {
			if m == p.ReviewModel {
				present = true
				break
			}
		}
		if !present {
			slugs = append(slugs, p.ReviewModel)
		}
	}
	models := make([]any, 0)
	for i, m := range slugs {
		display := m
		if label := p.ModelNames[m]; label != "" {
			display = label
		}
		window := defaultContextWindow
		if w := p.ModelContextWindows[m]; w > 0 {
			window = w
		}
		levels := knownReasoningLevels(p.ReasoningLevels(m))
		entry := map[string]any{
			"slug":                         m,
			"display_name":                 display,
			"description":                  nil,
			"supported_reasoning_levels":   reasoningPresets(levels),
			"shell_type":                   "default",
			"visibility":                   "list",
			"supported_in_api":             true,
			"priority":                     i + 1,
			"default_reasoning_summary":    "auto",
			"support_verbosity":            false,
			"default_verbosity":            nil,
			"apply_patch_tool_type":        nil,
			"truncation_policy":            map[string]any{"mode": "bytes", "limit": 10_000},
			"supports_parallel_tool_calls": false,
			"context_window":               window,
			"experimental_supported_tools": []any{},
			"base_instructions":            catalogBaseInstructions,
		}
		if def := core.DefaultReasoningLevel(levels); def != "" {
			entry["default_reasoning_level"] = def
		}
		models = append(models, entry)
	}
	return map[string]any{"models": models}
}

// reasoningLevelDescriptions are the short picker descriptions for the
// effort levels MintSwitch writes, matching the wording of Codex's own
// catalog (codex-rs models.json, 2026-08). Its keys are also the allow-list:
// Codex releases before 0.140 parse ReasoningEffort as a closed enum and
// reject the whole catalog on an unknown value, so any other advertised level
// is dropped (and so are levels a given Codex version may not know yet — the
// safe failure is a shorter picker, never a catalog Codex refuses to load).
var reasoningLevelDescriptions = map[string]string{
	"none":    "No reasoning; fastest responses",
	"minimal": "Minimal reasoning for the simplest tasks",
	"low":     "Fast responses with lighter reasoning",
	"medium":  "Balances speed and reasoning depth for everyday tasks",
	"high":    "Greater reasoning depth for complex problems",
	"xhigh":   "Extra high reasoning depth for complex problems",
	"max":     "Maximum reasoning depth for the hardest problems",
	"ultra":   "Deepest reasoning; slowest and most token-intensive",
}

// knownReasoningLevels filters levels to the reasoningLevelDescriptions
// allow-list, preserving order.
func knownReasoningLevels(levels []string) []string {
	var out []string
	for _, l := range levels {
		if _, ok := reasoningLevelDescriptions[l]; ok {
			out = append(out, l)
		}
	}
	return out
}

// reasoningPresets renders levels as Codex ReasoningEffortPreset objects
// ({"effort", "description"}), in order. It returns an empty (non-nil) list
// for no levels, since supported_reasoning_levels is a required field.
func reasoningPresets(levels []string) []any {
	out := make([]any, 0, len(levels))
	for _, l := range levels {
		out = append(out, map[string]any{"effort": l, "description": reasoningLevelDescriptions[l]})
	}
	return out
}

// needsCatalog reports whether Apply must write MintSwitch's model catalog:
// always in "All models" mode and whenever a review model is pinned (as
// before), and in single-model mode when the endpoint advertised reasoning
// levels for the selected model. Codex in API-key mode never refreshes its
// model list from the endpoint, so without a catalog entry a model missing
// from Codex's bundled/cached catalog gets fallback metadata with no
// reasoning levels — the effort picker is empty and a configured effort Codex
// cannot validate is replaced by its default.
func needsCatalog(p core.Profile) bool {
	return p.ApplyAllModels || p.ReviewModel != "" || len(knownReasoningLevels(p.ReasoningLevels(p.Model))) > 0
}

// managedCatalogRef reports whether the config's model_catalog_json value is
// MintSwitch's own catalog file (matches catalogPath, or — defensively, e.g.
// after a HOME move — still ends in the MintSwitch-specific file name), so a
// user's hand-configured catalog reference is never touched.
func managedCatalogRef(cfg map[string]any, catalogPath string) bool {
	v, _ := cfg[catalogKey].(string)
	if v == "" {
		return false
	}
	return v == catalogPath || hasCatalogBase(v)
}

// hasCatalogBase reports whether path's final segment is catalogFileName,
// accepting both slash styles so configs written on another OS still match.
func hasCatalogBase(path string) bool {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:] == catalogFileName
		}
	}
	return path == catalogFileName
}
