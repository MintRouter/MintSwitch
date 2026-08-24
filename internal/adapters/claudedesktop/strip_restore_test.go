package claudedesktop

import (
	"os"
	"strings"
	"testing"

	"mintswitch/internal/core"
)

// TestRestoreStripFallbackPreservesUserConfig covers the strip fallback:
// a managed config whose backups are gone must still Restore by stripping
// exactly the managed pieces — deploymentMode from
// claude_desktop_config.json, the MintRouter.AI entry (and appliedId) from
// _meta.json, and the provider file — while every user/foreign key survives.
func TestRestoreStripFallbackPreservesUserConfig(t *testing.T) {
	a, r, appDir := newAdapter(t)
	installApp(t, appDir)
	mustWriteJSON(t, a.configPath(), map[string]any{"locale": "en-US", "scale": 1.5})
	mustWriteJSON(t, a.metaPath(), map[string]any{
		"entries": []any{map[string]any{"id": "other-uuid", "name": "Other Provider"}},
	})
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	uuid := managedUUID(readJSON(t, a.metaPath()))
	if err := os.RemoveAll(r.BackupsDir()); err != nil {
		t.Fatalf("remove backups: %v", err)
	}

	res, err := a.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !strings.Contains(res.Message, "No backup found") {
		t.Fatalf("message = %q, want strip-fallback message", res.Message)
	}
	cfg := readJSON(t, a.configPath())
	if _, ok := cfg["deploymentMode"]; ok {
		t.Fatalf("deploymentMode still present after strip: %v", cfg)
	}
	if cfg["locale"] != "en-US" || cfg["scale"] != 1.5 {
		t.Fatalf("user config keys lost: %v", cfg)
	}
	meta := readJSON(t, a.metaPath())
	if _, ok := meta["appliedId"]; ok {
		t.Fatalf("appliedId still present after strip: %v", meta)
	}
	entries := meta["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["name"] != "Other Provider" {
		t.Fatalf("entries = %v, want only the foreign entry preserved", entries)
	}
	if _, err := os.Stat(a.providerPath(uuid)); !os.IsNotExist(err) {
		t.Fatal("provider file should be removed by strip fallback")
	}
	st, _, err := a.Status(sampleProfile())
	if err != nil || st != core.StatusDefault {
		t.Fatalf("Status after strip Restore = %v, %v; want Default", st, err)
	}
}

// TestRestoreStripFallbackRemovesManagedOnlyMeta covers stripMeta's
// delete-file branch: when _meta.json held nothing but the managed pieces,
// the strip fallback removes the file (and the provider file), leaving the
// configLibrary directory gone; claude_desktop_config.json stays as an empty
// object since stripConfig only removes the managed key.
func TestRestoreStripFallbackRemovesManagedOnlyMeta(t *testing.T) {
	a, r, appDir := newAdapter(t)
	installApp(t, appDir)
	if _, err := a.Apply(sampleProfile()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	uuid := managedUUID(readJSON(t, a.metaPath()))
	if err := os.RemoveAll(r.BackupsDir()); err != nil {
		t.Fatalf("remove backups: %v", err)
	}

	res, err := a.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !strings.Contains(res.Message, "No backup found") {
		t.Fatalf("message = %q, want strip-fallback message", res.Message)
	}
	if _, err := os.Stat(a.metaPath()); !os.IsNotExist(err) {
		t.Fatal("_meta.json should be removed when only managed pieces remained")
	}
	if _, err := os.Stat(a.providerPath(uuid)); !os.IsNotExist(err) {
		t.Fatal("provider file should be removed by strip fallback")
	}
	if _, err := os.Stat(a.libraryDir()); !os.IsNotExist(err) {
		t.Fatal("empty configLibrary dir should be removed")
	}
	cfg := readJSON(t, a.configPath())
	if len(cfg) != 0 {
		t.Fatalf("config = %v, want empty object after strip", cfg)
	}
	st, _, err := a.Status(sampleProfile())
	if err != nil || st != core.StatusDefault {
		t.Fatalf("Status after strip Restore = %v, %v; want Default", st, err)
	}
}
