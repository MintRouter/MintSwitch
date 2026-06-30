package service

import (
	"testing"

	"mintswitch/internal/backup"
	"mintswitch/internal/paths"
)

// newCustomService builds a real Service (NewWithDeps) rooted at temp dirs so
// AddCustomTool/RemoveCustomTool, which need a resolver and backup engine, work.
func newCustomService(t *testing.T) (*Service, *paths.Resolver) {
	t.Helper()
	r := &paths.Resolver{Home: t.TempDir(), DataDir: t.TempDir()}
	return NewWithDeps(r, backup.NewEngine(r.BackupsDir())), r
}

const validTemplate = `{"env":{"KEY":"${API_KEY}","URL":"${BASE_URL}"},"model":"${MODEL}"}`

func TestAddCustomToolSuccessRegistersAndPersists(t *testing.T) {
	svc, r := newCustomService(t)
	view, err := svc.AddCustomTool("Acme CLI", "~/.config/acme/config.json", "", validTemplate)
	if err != nil {
		t.Fatalf("AddCustomTool: %v", err)
	}
	if view.ID != "acme-cli" || view.Name != "Acme CLI" || !view.Custom {
		t.Fatalf("unexpected view: %+v", view)
	}

	// Registered after the five built-ins.
	views, err := svc.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	last := views[len(views)-1]
	if last.ID != "acme-cli" || !last.Custom {
		t.Fatalf("custom tool not registered last: %+v", last)
	}
	for _, v := range views[:len(views)-1] {
		if v.Custom {
			t.Fatalf("built-in marked custom: %+v", v)
		}
	}

	// Persisted: a fresh Service over the same dirs re-registers it from settings.
	svc2 := NewWithDeps(r, backup.NewEngine(r.BackupsDir()))
	if _, ok := svc2.reg.Get("acme-cli"); !ok {
		t.Fatal("custom tool not re-registered on restart")
	}
}

func TestAddCustomToolDuplicateName(t *testing.T) {
	svc, _ := newCustomService(t)
	if _, err := svc.AddCustomTool("Acme CLI", "/tmp/a.json", "", validTemplate); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := svc.AddCustomTool("acme cli", "/tmp/b.json", "", validTemplate); err == nil {
		t.Fatal("expected duplicate-id error for colliding slug")
	}
}

func TestAddCustomToolBuiltinCollision(t *testing.T) {
	svc, _ := newCustomService(t)
	if _, err := svc.AddCustomTool("Codex", "/tmp/c.json", "", validTemplate); err == nil {
		t.Fatal("expected built-in collision error for name slugging to 'codex'")
	}
}

func TestAddCustomToolInvalidTemplate(t *testing.T) {
	svc, _ := newCustomService(t)
	for _, tmpl := range []string{`not json`, `["a"]`, `"scalar"`, `123`} {
		if _, err := svc.AddCustomTool("Tool X", "/tmp/x.json", "", tmpl); err == nil {
			t.Fatalf("expected rejection of non-object template %q", tmpl)
		}
	}
}

func TestAddCustomToolValidation(t *testing.T) {
	svc, _ := newCustomService(t)
	cases := []struct{ name, cfg string }{
		{"", "/tmp/a.json"},    // empty name
		{"Acme", ""},           // empty config path
		{"!!!", "/tmp/a.json"}, // name with no alphanumerics => empty slug
	}
	for _, c := range cases {
		if _, err := svc.AddCustomTool(c.name, c.cfg, "", validTemplate); err == nil {
			t.Fatalf("expected validation error for name=%q cfg=%q", c.name, c.cfg)
		}
	}
}

func TestAddCustomToolUnavailableWithoutDeps(t *testing.T) {
	// A Service built without a resolver/engine (the registry-only test seam)
	// must refuse to add custom tools rather than panic.
	svc := newTestService(t)
	if _, err := svc.AddCustomTool("Acme", "/tmp/a.json", "", validTemplate); err == nil {
		t.Fatal("expected unavailable error without resolver/engine")
	}
}

func TestRemoveCustomTool(t *testing.T) {
	svc, _ := newCustomService(t)
	if _, err := svc.AddCustomTool("Acme CLI", "/tmp/a.json", "", validTemplate); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := svc.RemoveCustomTool("acme-cli"); err != nil {
		t.Fatalf("RemoveCustomTool: %v", err)
	}
	if _, ok := svc.reg.Get("acme-cli"); ok {
		t.Fatal("custom tool still registered after removal")
	}
	// Forgotten from settings: a restart does not bring it back.
	st, err := svc.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(st.CustomTools) != 0 {
		t.Fatalf("custom tool not removed from settings: %+v", st.CustomTools)
	}

	// Unknown id and built-in id are both errors.
	if err := svc.RemoveCustomTool("acme-cli"); err == nil {
		t.Fatal("expected error removing unknown custom tool")
	}
	if err := svc.RemoveCustomTool("codex"); err == nil {
		t.Fatal("expected error refusing to remove a built-in")
	}
}
