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
  "autoupdate": false, /* keep pinned */
  "theme": "kilo",
}
`

// TestInjectWritesToStrictJSONC proves a kilo.jsonc whose content is strict
// JSON is managed in place: InjectMCP targets it and never creates kilo.json
// alongside it.
func TestInjectWritesToStrictJSONC(t *testing.T) {
	i, _ := newInjector(t)
	writeFile(t, i.jsoncPath(), `{"theme":"kilo"}`)
	res, err := i.InjectMCP(spec())
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if res.ChangedPath != i.jsoncPath() {
		t.Fatalf("ChangedPath = %q, want kilo.jsonc", res.ChangedPath)
	}
	if _, err := os.Stat(i.jsonPath()); !os.IsNotExist(err) {
		t.Fatalf("kilo.json must not be created, got err=%v", err)
	}
	m := readJSON(t, i.jsoncPath())
	if m["theme"] != "kilo" {
		t.Fatalf("existing key lost: %+v", m)
	}
	entry(t, i.jsoncPath())
}

// TestInjectRefusesJSONCWithComments proves the comment-safety contract:
// InjectMCP must error and leave the file byte-for-byte untouched, and
// MCPStatus must report ConfiguredExternally instead of destroying comments.
func TestInjectRefusesJSONCWithComments(t *testing.T) {
	i, _ := newInjector(t)
	writeFile(t, i.jsoncPath(), jsoncWithComments)

	s := spec()
	if _, err := i.InjectMCP(s); err == nil {
		t.Fatal("expected inject to refuse a comment-carrying kilo.jsonc")
	} else if strings.Contains(err.Error(), s.APIKey) {
		t.Fatalf("error message leaked the api key: %v", err)
	}
	got, err := os.ReadFile(i.jsoncPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != jsoncWithComments {
		t.Fatalf("kilo.jsonc was modified:\n got: %q\nwant: %q", got, jsoncWithComments)
	}

	i.lookPath = func(string) (string, error) { return "/usr/local/bin/kilo", nil }
	st, detail, err := i.MCPStatus(s)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st != core.MCPConfiguredExternally {
		t.Fatalf("expected ConfiguredExternally for comment-carrying jsonc, got %v", st)
	}
	if !strings.Contains(detail, "JSONC") {
		t.Fatalf("detail should explain the JSONC limitation, got %q", detail)
	}

	res, err := i.RemoveMCP()
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected no-op remove, got %+v", res)
	}
	got, err = os.ReadFile(i.jsoncPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != jsoncWithComments {
		t.Fatalf("remove modified kilo.jsonc: %q", got)
	}
}

// TestRemoveRestoresJSONCBackup proves RemoveMCP finds the backup when the
// injection targeted kilo.jsonc, reverting it byte-for-byte.
func TestRemoveRestoresJSONCBackup(t *testing.T) {
	i, _ := newInjector(t)
	pristine := `{"theme":"kilo"}` + "\n"
	writeFile(t, i.jsoncPath(), pristine)
	if _, err := i.InjectMCP(spec()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, err := i.RemoveMCP(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, err := os.ReadFile(i.jsoncPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != pristine {
		t.Fatalf("kilo.jsonc not restored: %q", got)
	}
}
