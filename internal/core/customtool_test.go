package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

func substProfile() Profile {
	return Profile{
		APIKey:  `sk-"q"\x`,
		BaseURL: "https://h/v1",
		Model:   "m-1",
	}
}

func TestSubstitutePlaceholdersDeepWalk(t *testing.T) {
	src := `{
		"k": "${API_KEY}",
		"u": "${BASE_URL}",
		"arr": ["${MODEL}", "plain", {"m": "${MODEL}"}],
		"untouched": "prefix-${API_KEY}-suffix",
		"literal": "API_KEY",
		"num": 7,
		"bool": true,
		"null": null
	}`
	var root any
	if err := json.Unmarshal([]byte(src), &root); err != nil {
		t.Fatal(err)
	}
	p := substProfile()
	out := SubstitutePlaceholders(root, p).(map[string]any)

	if out["k"] != p.APIKey || out["u"] != p.BaseURL {
		t.Fatalf("top-level substitution wrong: %v", out)
	}
	arr := out["arr"].([]any)
	if arr[0] != p.Model || arr[1] != "plain" {
		t.Fatalf("array substitution wrong: %v", arr)
	}
	if arr[2].(map[string]any)["m"] != p.Model {
		t.Fatalf("nested-in-array substitution wrong: %v", arr[2])
	}
	if out["untouched"] != "prefix-${API_KEY}-suffix" {
		t.Fatalf("embedded placeholder must NOT be substituted: %v", out["untouched"])
	}
	if out["literal"] != "API_KEY" {
		t.Fatalf("bare token must NOT be substituted: %v", out["literal"])
	}
	if out["num"] != float64(7) || out["bool"] != true || out["null"] != nil {
		t.Fatalf("non-string scalars must be preserved: %v", out)
	}
}

// TestSubstituteRootScalarAndNoPlaceholders covers a root scalar string and a
// structure with no placeholders (template without ${API_KEY} etc.).
func TestSubstituteRootScalarAndNoPlaceholders(t *testing.T) {
	p := substProfile()
	if got := SubstitutePlaceholders("${MODEL}", p); got != p.Model {
		t.Fatalf("root scalar not substituted: %v", got)
	}
	src := map[string]any{"a": "static", "b": float64(1)}
	got := SubstitutePlaceholders(map[string]any{"a": "static", "b": float64(1)}, p)
	if !reflect.DeepEqual(got, src) {
		t.Fatalf("no-placeholder structure changed: %v", got)
	}
}
