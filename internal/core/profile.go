// Package core defines the shared domain types and the ToolAdapter contract
// that every MintSwitch tool adapter and backend service builds on.
//
// The central abstraction is [ToolAdapter]: each supported AI coding tool
// (Claude Code, Codex, OpenCode, Pi, ...) implements it so the
// rest of the application can detect, inspect, apply and restore that tool's
// OpenAI-compatible endpoint configuration in a uniform way.
package core

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// NormalizeBaseURL canonicalizes a profile base URL so tools never receive an
// http endpoint that a remote host silently 301-redirects to https — a redirect
// that makes many HTTP clients drop the Authorization header and fail with a
// spurious "missing API key" error. It trims surrounding whitespace, strips
// trailing slashes from the path (/v1/ -> /v1, root / -> empty), and upgrades an
// http scheme to https for remote hosts, reporting upgraded=true when it does.
// Local and private hosts (localhost, *.local, *.localhost, loopback,
// RFC1918/link-local addresses) are left on http so local model servers keep
// working. The trimmed input is returned unchanged (upgraded=false) when it is
// empty, fails to parse, or has no host, leaving Validate to report errors. It
// never inspects or logs secrets.
func NormalizeBaseURL(raw string) (normalized string, upgraded bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return trimmed, false
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return trimmed, false
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Scheme == "http" && !isLocalOrPrivateHost(u.Hostname()) {
		u.Scheme = "https"
		upgraded = true
	}
	return u.String(), upgraded
}

// isLocalOrPrivateHost reports whether host refers to the local machine or a
// private/link-local network, in which case an http base URL must be preserved
// (no https upgrade). host is the URL hostname with any port already stripped.
func isLocalOrPrivateHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" ||
		strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return false
}

// Profile is a single OpenAI-compatible endpoint configuration, resolved from
// a [Provider], that adapters consume when configuring a tool.
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
	// ModelNames optionally maps a member of Models to a human-friendly display
	// name shown by the UI. Adapters never read it; tool configs always receive
	// the canonical model identifier. Missing entries fall back to the ID.
	ModelNames map[string]string `json:"model_names,omitempty"`
	// Model is the currently selected model identifier and the single value
	// adapters write to tool configs. Required.
	Model string `json:"model"`
	// SmallFastModel is an optional secondary model used by some tools for
	// lightweight/background tasks. It need not be a member of Models.
	SmallFastModel string `json:"small_fast_model,omitempty"`
	// OpusModel, SonnetModel and HaikuModel optionally pin Claude Code's model
	// tiers (the opus/sonnet/haiku aliases used by subagents, plan mode and
	// background tasks) to specific models. Only the claudecode adapter reads
	// them; an empty tier falls back to Model (Haiku additionally prefers
	// SmallFastModel). They need not be members of Models.
	OpusModel   string `json:"opus_model,omitempty"`
	SonnetModel string `json:"sonnet_model,omitempty"`
	HaikuModel  string `json:"haiku_model,omitempty"`
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
