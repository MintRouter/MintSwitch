package core

import (
	"errors"
	"strings"
)

// DefaultProviderID is the Provider ID assigned when a legacy single-key
// state is migrated to the provider shape (one active provider named
// "Default").
const DefaultProviderID = "default"

// Provider is one named OpenAI-compatible endpoint the user manages: an API
// key plus the endpoint fields adapters need, under a user-chosen display
// name. The application holds a list of Providers and resolves one per tool
// into the [Profile] adapters consume.
//
// Name and Note are non-secret display text (persisted in the settings file
// and shown over bindings); the APIKey value must never be logged or sent to
// the frontend, not even masked.
type Provider struct {
	// ID uniquely identifies the provider within the state.
	ID string `json:"id"`
	// Name is the user-chosen display name (e.g. "OpenAI").
	Name string `json:"name"`
	// Note is optional free-text shown alongside the name. Never secret.
	Note string `json:"note,omitempty"`
	// APIKey is the secret bearer token. Required. Persisted keychain-first;
	// never logged or returned to the frontend.
	APIKey string `json:"api_key,omitempty"`
	// BaseURL is the OpenAI-compatible base URL (http or https). Required.
	BaseURL string `json:"base_url"`
	// Models is the provider's saved set of selectable model identifiers. The
	// default selection is Model, which must be a member when Models is
	// non-empty.
	Models []string `json:"models,omitempty"`
	// ModelNames optionally maps a member of Models to a human-friendly
	// display name shown by the UI.
	ModelNames map[string]string `json:"model_names,omitempty"`
	// Model is the provider's default model identifier. Required.
	Model string `json:"model"`
	// SmallFastModel is an optional secondary model used by some tools for
	// lightweight/background tasks. It need not be a member of Models.
	SmallFastModel string `json:"small_fast_model,omitempty"`
	// OpusModel, SonnetModel, HaikuModel and FableModel optionally pin Claude
	// Code's model tiers (opus/sonnet/haiku/fable aliases). Empty tiers fall
	// back to Model (Haiku prefers SmallFastModel). They need not be members
	// of Models.
	OpusModel   string `json:"opus_model,omitempty"`
	SonnetModel string `json:"sonnet_model,omitempty"`
	HaikuModel  string `json:"haiku_model,omitempty"`
	FableModel  string `json:"fable_model,omitempty"`
}

// Profile returns the provider's endpoint fields as the [Profile] adapters
// consume, labeled with the provider's display name.
func (pr Provider) Profile() Profile {
	return Profile{
		Label:          pr.Name,
		APIKey:         pr.APIKey,
		BaseURL:        pr.BaseURL,
		Models:         pr.Models,
		ModelNames:     pr.ModelNames,
		Model:          pr.Model,
		SmallFastModel: pr.SmallFastModel,
		OpusModel:      pr.OpusModel,
		SonnetModel:    pr.SonnetModel,
		HaikuModel:     pr.HaikuModel,
		FableModel:     pr.FableModel,
	}
}

// HasModel reports whether m is one of the provider's selectable models. An
// empty Models list (saved before Models existed) offers exactly the default
// Model.
func (pr Provider) HasModel(m string) bool {
	if len(pr.Models) == 0 {
		return m != "" && m == pr.Model
	}
	for _, x := range pr.Models {
		if x == m {
			return true
		}
	}
	return false
}

// Validate reports whether the provider carries the minimum information
// needed to configure a tool: an ID, a display name, and endpoint fields that
// satisfy [Profile.Validate] (key, base URL, model/models rules).
func (pr Provider) Validate() error {
	if strings.TrimSpace(pr.ID) == "" {
		return errors.New("core: provider id is required")
	}
	if strings.TrimSpace(pr.Name) == "" {
		return errors.New("core: provider name is required")
	}
	return pr.Profile().Validate()
}
