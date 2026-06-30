package core

// CustomToolDef describes a user-defined tool/provider managed by the generic
// JSON-template adapter (see internal/adapters/custom). It is persisted in
// MintSwitch's own settings and is the sole input, besides a paths.Resolver and
// a backup.Engine, the generic adapter needs to behave like a built-in adapter.
type CustomToolDef struct {
	// ID is the stable slug identifier (derived from Name). It must be unique
	// and must not collide with the built-in tool IDs.
	ID string `json:"id"`
	// Name is the human-friendly display name.
	Name string `json:"name"`
	// ConfigPath is the JSON config file the tool reads. It may be absolute or
	// "~"-relative (expanded against the resolver's Home at apply time).
	ConfigPath string `json:"config_path"`
	// BinaryName is the optional CLI name used for installed-detection. When set,
	// detection resolves it like a built-in; when empty the tool is always
	// reported installed (config-only provider).
	BinaryName string `json:"binary_name,omitempty"`
	// Template is a JSON OBJECT whose string values may be the placeholders
	// PlaceholderAPIKey/PlaceholderBaseURL/PlaceholderModel. On apply the parsed
	// structure is deep-walked and matching string values are substituted with
	// the active profile's fields.
	Template string `json:"template"`
}

// Placeholder tokens recognised inside a [CustomToolDef.Template]. A string
// value is substituted only when it EXACTLY equals one of these tokens; partial
// or embedded occurrences are left untouched.
const (
	PlaceholderAPIKey  = "${API_KEY}"
	PlaceholderBaseURL = "${BASE_URL}"
	PlaceholderModel   = "${MODEL}"
)

// SubstitutePlaceholders deep-walks a value parsed from JSON (json.Unmarshal
// into an any: nested map[string]any and []any) and replaces every string that
// EXACTLY equals a known placeholder with the corresponding profile field. Maps
// and slices are mutated in place; the (possibly replaced) value is returned so
// a root scalar can also be substituted. Operating on the parsed structure
// (rather than the raw template text) means profile values containing quotes,
// backslashes or braces are carried as plain Go strings and re-escaped correctly
// when the result is marshalled, avoiding any JSON-injection or escaping issues.
func SubstitutePlaceholders(v any, p Profile) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = SubstitutePlaceholders(val, p)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = SubstitutePlaceholders(val, p)
		}
		return t
	case string:
		switch t {
		case PlaceholderAPIKey:
			return p.APIKey
		case PlaceholderBaseURL:
			return p.BaseURL
		case PlaceholderModel:
			return p.Model
		default:
			return t
		}
	default:
		return v
	}
}
