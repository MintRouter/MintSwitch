package kilo

import (
	"os"
	"strings"
	"testing"

	"mintswitch/internal/core"
)

// jsoncWithComments is a realistic kilo.jsonc carrying JSONC-only syntax
// (comments + a trailing comma) that a strict JSON round-trip would destroy.
const jsoncWithComments = `{
  // my kilo settings
  "$schema": "https://app.kilo.ai/config.json",
  "autoupdate": false, /* keep pinned */
  "theme": "kilo",
}
`

// TestApplyWritesToStrictJSONC proves a kilo.jsonc whose content is strict JSON
// is managed in place (valid JSON is valid JSONC): Apply targets it, preserves
// its keys, and never creates kilo.json alongside it.
func TestApplyWritesToStrictJSONC(t *testing.T) {
	a, _ := newAdapter(t)
	writeFile(t, a.jsoncPath(), `{"theme":"kilo"}`)
	res, err := a.Apply(sampleProfile())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ChangedPath != a.jsoncPath() {
		t.Fatalf("ChangedPath = %q, want kilo.jsonc", res.ChangedPath)
	}
	if _, err := os.Stat(a.jsonPath()); !os.IsNotExist(err) {
		t.Fatalf("kilo.json must not be created, got err=%v", err)
	}
	root := readJSON(t, a.jsoncPath())
	if root["theme"] != "kilo" {
		t.Fatalf("existing key not preserved: %v", root)
	}
	if root["model"] != "openai-compatible/gpt-mint" {
		t.Fatalf("model = %v", root["model"])
	}
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	if st, _, _ := a.Status(sampleProfile()); st != core.StatusAppliedByMintSwitch {
		t.Fatalf("expected AppliedByMintSwitch, got %v", st)
	}
}

// TestApplyRefusesJSONCWithComments proves the comment-safety contract: Apply
// must error and leave the file byte-for-byte untouched, and Status must report
// ModifiedExternally instead of destroying the user's comments.
func TestApplyRefusesJSONCWithComments(t *testing.T) {
	a, _ := newAdapter(t)
	writeFile(t, a.jsoncPath(), jsoncWithComments)

	p := sampleProfile()
	if _, err := a.Apply(p); err == nil {
		t.Fatal("expected apply to refuse a comment-carrying kilo.jsonc")
	} else if strings.Contains(err.Error(), p.APIKey) {
		t.Fatalf("error message leaked the api key: %v", err)
	}
	got, err := os.ReadFile(a.jsoncPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != jsoncWithComments {
		t.Fatalf("kilo.jsonc was modified:\n got: %q\nwant: %q", got, jsoncWithComments)
	}
	if _, err := os.Stat(a.jsonPath()); !os.IsNotExist(err) {
		t.Fatalf("kilo.json must not be created, got err=%v", err)
	}

	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	st, detail, err := a.Status(p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != core.StatusModifiedExternally {
		t.Fatalf("expected ModifiedExternally for comment-carrying jsonc, got %v", st)
	}
	if !strings.Contains(detail, "JSONC") {
		t.Fatalf("detail should explain the JSONC limitation, got %q", detail)
	}
}

// TestStatusCorruptJSONErrors proves a broken kilo.json surfaces the parse
// error instead of being silently treated as JSONC.
func TestStatusCorruptJSONErrors(t *testing.T) {
	a, _ := newAdapter(t)
	a.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	writeFile(t, a.jsonPath(), `{"broken":`)
	if _, _, err := a.Status(sampleProfile()); err == nil {
		t.Fatal("expected parse error for corrupt kilo.json")
	}
	if _, err := a.Apply(sampleProfile()); err == nil {
		t.Fatal("expected apply to fail on corrupt kilo.json")
	}
}
