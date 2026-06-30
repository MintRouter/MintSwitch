// Package core defines the shared domain types and the ToolAdapter contract
// that every MintSwitch tool adapter and backend service builds on.
//
// The central abstraction is [ToolAdapter]: each supported AI coding tool
// (Claude Code, Codex, OpenCode, Factory Droid, Pi, ...) implements it so the
// rest of the application can detect, inspect, apply and restore that tool's
// OpenAI-compatible endpoint configuration in a uniform way.
package core

import (
	"errors"
	"net/url"
	"strings"
)

// Profile is a single OpenAI-compatible endpoint configuration that the user
// wants to apply to one or more AI coding tools.
//
// API keys live in this struct; callers must never write APIKey to logs.
type Profile struct {
	// Label is an optional human-friendly name for the profile.
	Label string `json:"label,omitempty"`
	// APIKey is the secret bearer token for the endpoint. Required.
	APIKey string `json:"api_key"`
	// BaseURL is the OpenAI-compatible base URL (http or https). Required.
	BaseURL string `json:"base_url"`
	// Models is the user's saved set of selectable model identifiers. The
	// currently selected one is Model, which must be a member when Models is
	// non-empty. Adapters never read Models; they consume only Model.
	Models []string `json:"models,omitempty"`
	// Model is the currently selected model identifier and the single value
	// adapters write to tool configs. Required.
	Model string `json:"model"`
	// SmallFastModel is an optional secondary model used by some tools for
	// lightweight/background tasks. It need not be a member of Models.
	SmallFastModel string `json:"small_fast_model,omitempty"`
}

// Validate reports whether the profile carries the minimum information needed
// to configure a tool: a non-empty API key, a non-empty model, and a base URL
// that parses as an absolute http or https URL.
func (p Profile) Validate() error {
	if strings.TrimSpace(p.APIKey) == "" {
		return errors.New("core: profile api_key is required")
	}
	if strings.TrimSpace(p.Model) == "" {
		return errors.New("core: profile model is required")
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return errors.New("core: profile base_url is required")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return errors.New("core: profile base_url is not a valid URL: " + err.Error())
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("core: profile base_url must use http or https scheme")
	}
	if u.Host == "" {
		return errors.New("core: profile base_url must include a host")
	}
	if len(p.Models) > 0 {
		found := false
		for _, m := range p.Models {
			if m == p.Model {
				found = true
				break
			}
		}
		if !found {
			return errors.New("core: profile model must be one of models")
		}
	}
	return nil
}
