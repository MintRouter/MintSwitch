package codex

import (
	_ "embed"

	"mintswitch/internal/core"
)

// catalogFileName is the model-catalog file MintSwitch writes under the Codex
// home dir in "All models" mode and points config.toml's model_catalog_json
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
// defaultContextWindow). A pinned ReviewModel not already among the applied
// models is appended last, so Codex has context-window metadata for it too.
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
		models = append(models, map[string]any{
			"slug":                         m,
			"display_name":                 display,
			"description":                  nil,
			"supported_reasoning_levels":   []any{},
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
		})
	}
	return map[string]any{"models": models}
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
