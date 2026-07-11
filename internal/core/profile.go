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

// DefaultKeyID is the APIKeyEntry ID assigned when a legacy single-key
// profile is upgraded to the multi-key shape (one active entry named
// "Default").
const DefaultKeyID = "default"

// APIKeyEntry is one named API key in a profile's managed key list. The
// Provider name is the only part of an entry that may ever be shown to the
// user or sent over bindings; the Key value must never be logged or returned
// to the frontend, not even masked.
type APIKeyEntry struct {
	// ID uniquely identifies the entry within the profile.
	ID string `json:"id"`
	// Provider is the user-chosen display name for the key (e.g. "OpenAI").
	Provider string `json:"provider"`
	// Key is the secret value. It is persisted like APIKey (keychain-first
	// with a settings-file fallback). Adapters never read it; they consume
	// the resolved Profile.APIKey.
	Key string `json:"key,omitempty"`
}

// Profile is a single OpenAI-compatible endpoint configuration that the user
// wants to apply to one or more AI coding tools.
//
// API keys live in this struct; callers must never write APIKey to logs.
type Profile struct {
	// Label is an optional human-friendly name for the profile.
	Label string `json:"label,omitempty"`
	// APIKey is the secret bearer token for the endpoint. Required. It is the
	// effective key adapters consume; when APIKeys is non-empty it mirrors the
	// entry selected by ActiveKeyID (see [Profile.NormalizeKeys]).
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
	// APIKeys is the managed list of named keys. The entry selected by
	// ActiveKeyID is mirrored into APIKey, which stays the single value
	// adapters consume — mirroring the Models/Model pattern.
	APIKeys []APIKeyEntry `json:"api_keys,omitempty"`
	// ActiveKeyID selects the active member of APIKeys. It must reference a
	// member when APIKeys is non-empty.
	ActiveKeyID string `json:"active_key_id,omitempty"`
}

// KeyEntry returns the API key entry with the given ID and whether it exists.
func (p Profile) KeyEntry(id string) (APIKeyEntry, bool) {
	for _, e := range p.APIKeys {
		if e.ID == id {
			return e, true
		}
	}
	return APIKeyEntry{}, false
}

// NormalizeKeys reconciles the managed key list with the effective APIKey, in
// place. A legacy single-key profile (non-empty APIKey, no APIKeys) is
// upgraded to one active entry labeled "Default" so v1 profiles load without
// user action. When APIKeys is non-empty, a missing or stale ActiveKeyID
// falls back to the first entry, and APIKey is synced to the active entry's
// key value when that value is available (an empty value — e.g. keychain
// unavailable — leaves APIKey untouched so Validate reports the problem).
func (p *Profile) NormalizeKeys() {
	if len(p.APIKeys) == 0 {
		if strings.TrimSpace(p.APIKey) != "" {
			p.APIKeys = []APIKeyEntry{{ID: DefaultKeyID, Provider: "Default", Key: p.APIKey}}
			p.ActiveKeyID = DefaultKeyID
		}
		return
	}
	if _, ok := p.KeyEntry(p.ActiveKeyID); !ok {
		p.ActiveKeyID = p.APIKeys[0].ID
	}
	if e, ok := p.KeyEntry(p.ActiveKeyID); ok && strings.TrimSpace(e.Key) != "" {
		p.APIKey = e.Key
	}
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
	if len(p.APIKeys) > 0 {
		seen := make(map[string]bool, len(p.APIKeys))
		for _, e := range p.APIKeys {
			if strings.TrimSpace(e.ID) == "" {
				return errors.New("core: profile api key entries need an id")
			}
			if seen[e.ID] {
				return errors.New("core: profile api key ids must be unique")
			}
			seen[e.ID] = true
		}
		if !seen[p.ActiveKeyID] {
			return errors.New("core: profile active_key_id must be one of api_keys")
		}
	}
	return nil
}
