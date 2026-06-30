package core

import "testing"

// fakeAdapter is a minimal ToolAdapter for registry tests.
type fakeAdapter struct{ id, name string }

func (f fakeAdapter) ID() string             { return f.id }
func (f fakeAdapter) Name() string           { return f.name }
func (f fakeAdapter) ConfigPaths() []string  { return nil }
func (f fakeAdapter) Detect() (bool, string) { return false, "" }
func (f fakeAdapter) Status(Profile) (ToolStatus, string, error) {
	return StatusNotInstalled, "", nil
}
func (f fakeAdapter) Apply(Profile) (ApplyResult, error) { return ApplyResult{}, nil }
func (f fakeAdapter) Restore() (RestoreResult, error)    { return RestoreResult{}, nil }

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if got := r.All(); len(got) != 0 {
		t.Fatalf("expected empty registry, got %d", len(got))
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("Get on empty registry returned ok")
	}

	r.Register(fakeAdapter{id: "b", name: "B"})
	r.Register(fakeAdapter{id: "a", name: "A"})
	r.Register(fakeAdapter{id: "c", name: "C"})

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 adapters, got %d", len(all))
	}
	wantOrder := []string{"b", "a", "c"}
	for i, w := range wantOrder {
		if all[i].ID() != w {
			t.Fatalf("order[%d] = %q, want %q", i, all[i].ID(), w)
		}
	}

	got, ok := r.Get("a")
	if !ok || got.Name() != "A" {
		t.Fatalf("Get(a) = %v, %v", got, ok)
	}

	r.Register(fakeAdapter{id: "a", name: "A2"})
	if got, _ := r.Get("a"); got.Name() != "A2" {
		t.Fatalf("re-register did not replace: %v", got.Name())
	}
	if len(r.All()) != 3 {
		t.Fatalf("re-register changed count: %d", len(r.All()))
	}

	r.Register(nil)
	r.Register(fakeAdapter{id: ""})
	if len(r.All()) != 3 {
		t.Fatalf("nil/empty register changed count: %d", len(r.All()))
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeAdapter{id: "a", name: "A"})
	r.Register(fakeAdapter{id: "b", name: "B"})
	r.Register(fakeAdapter{id: "c", name: "C"})

	r.Unregister("b")
	all := r.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 after unregister, got %d", len(all))
	}
	if all[0].ID() != "a" || all[1].ID() != "c" {
		t.Fatalf("order of remaining not preserved: %q %q", all[0].ID(), all[1].ID())
	}
	if _, ok := r.Get("b"); ok {
		t.Fatal("Get(b) still ok after Unregister")
	}

	// Unregistering a missing id is a no-op.
	r.Unregister("missing")
	if len(r.All()) != 2 {
		t.Fatalf("unregister missing changed count: %d", len(r.All()))
	}

	// A re-registered id appends at the end (order semantics preserved).
	r.Register(fakeAdapter{id: "b", name: "B2"})
	all = r.All()
	if all[len(all)-1].ID() != "b" {
		t.Fatalf("re-registered id not appended at end: %v", all)
	}
}

func TestToolStatusStrings(t *testing.T) {
	cases := map[ToolStatus]string{
		StatusNotInstalled:        "not_installed",
		StatusDefault:             "default",
		StatusAppliedByMintSwitch: "applied_by_mintswitch",
		StatusModifiedExternally:  "modified_externally",
		ToolStatus(99):            "unknown",
	}
	for s, want := range cases {
		if s.String() != want {
			t.Errorf("String(%d) = %q, want %q", s, s.String(), want)
		}
		if s.Detail() == "" {
			t.Errorf("Detail(%d) empty", s)
		}
	}
}
