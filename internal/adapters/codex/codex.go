// Package codex implements the core.ToolAdapter for OpenAI Codex (the Codex
// CLI and IDE extension), which share configuration under ~/.codex.
//
// Since the July 2026 unification the standalone Codex desktop app ships as
// the unified ChatGPT app (macOS bundle ID com.openai.codex), but the CLI is
// still the "codex" binary (npm @openai/codex) and its configuration is
// unchanged: $CODEX_HOME (default ~/.codex) with config.toml and auth.json.
// OpenAI documents that no configuration migration is needed, so the managed
// keys below remain the correct contract. Detect covers both surfaces: the
// "codex" CLI binary being resolvable, or the ChatGPT desktop app being
// present — on macOS an .app bundle in /Applications or ~/Applications whose
// Info.plist carries the com.openai.codex bundle ID (the legacy chat-only
// ChatGPT.app / "ChatGPT Classic.app" keep com.openai.chat and must NOT
// count), on Windows the MSIX package data dir %LOCALAPPDATA%\Packages\
// OpenAI.Codex_* or the app's runtime dir %LOCALAPPDATA%\OpenAI\Codex (the
// binary itself sits in the protected WindowsApps dir and cannot be stat'ed
// from user space), and on Linux the "chatgpt" launcher binary or the .deb/
// .rpm payload dir /usr/lib/chatgpt. All probes are filesystem-only — no
// subprocesses. The desktop app shares ~/.codex with the CLI, so a
// desktop-only install still applies/restores through the same files.
//
// Codex stores user configuration in ~/.codex/config.toml (TOML) and file-based
// credentials in ~/.codex/auth.json (JSON). To point the built-in "openai"
// provider at an OpenAI-compatible proxy/router, MintSwitch sets the top-level
// openai_base_url and model keys in config.toml (leaving model_provider at its
// default "openai") and writes the API key to auth.json as OPENAI_API_KEY.
// Apply also sets auth_mode="apikey" in auth.json: when a ChatGPT session also
// exists (auth_mode="chatgpt" with tokens present), Codex uses the OAuth token
// and ignores OPENAI_API_KEY, returning 401 against a proxy openai_base_url.
// The unified ChatGPT app shares ~/.codex, so signing in with ChatGPT can
// rewrite auth.json (flipping auth_mode back to "chatgpt") behind MintSwitch's
// back — Status therefore verifies auth.json as well as config.toml.
// See https://developers.openai.com/codex/config-advanced and
// https://developers.openai.com/codex/auth.
package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

// authKeyName is the JSON key Codex reads the API key from in auth.json.
const authKeyName = "OPENAI_API_KEY"

// desktopBundleID is the macOS bundle identifier of the unified ChatGPT
// desktop app (and of the pre-unification Codex.app). The legacy chat-only
// app uses com.openai.chat under the same ChatGPT.app name, so bundle
// presence alone is not an installed signal — the Info.plist must carry this
// ID (see bundleIsCodexApp).
const desktopBundleID = "com.openai.codex"

// msixPackageGlob matches the ChatGPT desktop app's MSIX package data dir
// under %LOCALAPPDATA%\Packages. The published family is
// OpenAI.Codex_2p2nqsd0c76g0 (the publisher-hash suffix is stable across
// versions); the glob keys off the OpenAI.Codex_ name prefix so a re-signed
// package with a different suffix is still detected.
const msixPackageGlob = "OpenAI.Codex_*"

// linuxDesktopLibDir is the payload dir of the official Linux .deb/.rpm
// packages (holding the ChatGPT binary and codex-launcher, with the
// /usr/bin/chatgpt symlink pointing into it).
const linuxDesktopLibDir = "/usr/lib/chatgpt"

// authModeKey is the auth.json field selecting Codex's credential source, and
// authModeAPIKey is the value forcing it to use OPENAI_API_KEY (matching the
// AuthMode enum's lowercase serialization in OpenAI codex).
const (
	authModeKey    = "auth_mode"
	authModeAPIKey = "apikey"
)

// reviewModelKey is the top-level config.toml key selecting the model Codex
// uses for its code-review flow; unset, reviews follow the session model.
const reviewModelKey = "review_model"

// windowKey is the top-level config.toml key overriding the context window
// Codex assumes for the session model (applied as min(override, the model's
// max window), so it only ever lowers; auto-compact follows at 90% of the
// resolved window). Apply writes it in single-model mode when the endpoint
// advertised a window for the selected model and no catalog is written (see
// needsCatalog for why the catalog is not used for this) — never alongside a
// catalog, whose per-model context_window entries a global override would be
// min'd onto (including the review model's).
const windowKey = "model_context_window"

// orphanDetail explains the orphan-remnant state: the config files still carry
// the MintSwitch-injected settings but the managed marker is gone (e.g. a
// previous restore was interrupted after clearing the marker).
const orphanDetail = "MintSwitch settings are still present but the managed marker is missing " +
	"(a previous restore may have been interrupted). Restore Default will remove them."

// authDriftDetail explains the auth-drift state: config.toml still routes to
// the MintSwitch provider, but auth.json no longer selects the MintSwitch API
// key — typically because a ChatGPT sign-in (CLI or the unified ChatGPT app,
// which shares ~/.codex) rewrote it. Codex then ignores OPENAI_API_KEY and
// bypasses the configured endpoint, so the profile must be re-applied.
const authDriftDetail = "auth.json no longer uses the MintSwitch API key (a ChatGPT sign-in " +
	"likely replaced it), so Codex bypasses the configured endpoint. Apply the profile again to fix this."

// catalogStaleDetail explains the stale-metadata state: the profile needs
// MintSwitch's model catalog (see needsCatalog) but the file is missing,
// unreferenced, or carries outdated model metadata (e.g. reasoning levels
// fetched after the last Apply) — or the profile needs none but config.toml
// still references it — or, in the single-model override case, the
// model_context_window key (see windowKey) is missing or differs from the
// window the endpoint advertised (e.g. fetched after the last Apply).
const catalogStaleDetail = "Codex's model metadata (reasoning-effort levels, context windows) is out of date. " +
	"Apply the profile again to update it."

// Adapter applies/restores a MintSwitch profile to the Codex configuration.
// The managed marker lives in the sidecar marker store, never in config.toml,
// so Codex configs stay free of the legacy [mintswitchManaged] table.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	m *markers.Store
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
	// writeConfig writes config.toml; overridable in tests to inject write
	// failures into the second half of Apply's two-file write. Defaults to
	// writeTOML.
	writeConfig func(string, map[string]any) error
	// goos selects the OS-specific desktop-app probe; overridable in tests so
	// every branch is exercisable from any host OS. Defaults to runtime.GOOS.
	goos string
	// macAppBundles are the macOS .app bundle dirs probed for the unified
	// ChatGPT desktop app; overridable in tests. Defaults to
	// defaultMacAppBundles.
	macAppBundles []string
	// linuxLibDir is the Linux .deb/.rpm payload dir probed for the desktop
	// app; overridable in tests. Defaults to linuxDesktopLibDir.
	linuxLibDir string
}

// New constructs a Codex adapter using the injected resolver, backup engine,
// and sidecar marker store.
func New(r *paths.Resolver, e *backup.Engine, m *markers.Store) *Adapter {
	return &Adapter{
		r: r, e: e, m: m,
		lookPath:      exec.LookPath,
		writeConfig:   writeTOML,
		goos:          runtime.GOOS,
		macAppBundles: defaultMacAppBundles(r),
		linuxLibDir:   linuxDesktopLibDir,
	}
}

// defaultMacAppBundles returns the macOS .app bundle dirs desktopInstalled
// probes: the unified ChatGPT.app plus the pre-unification Codex.app (same
// com.openai.codex bundle ID), each in /Applications and ~/Applications.
func defaultMacAppBundles(r *paths.Resolver) []string {
	return []string{
		"/Applications/ChatGPT.app",
		r.Join("Applications", "ChatGPT.app"),
		"/Applications/Codex.app",
		r.Join("Applications", "Codex.app"),
	}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return "codex" }

// Name returns the display name for the installed surface(s). The UI leads
// with ChatGPT and splits the parenthetical off as the card subtitle, so the
// subtitle reflects what is actually present: the Codex CLI/IDE, the ChatGPT
// desktop app, or both. With nothing installed it falls back to the CLI + IDE
// form, matching the uninstalled card's generic copy.
func (a *Adapter) Name() string {
	cli := a.r.BinaryResolvable(a.lookPath, "codex")
	switch {
	case cli && a.desktopInstalled():
		return "ChatGPT (Codex CLI + Desktop app)"
	case !cli && a.desktopInstalled():
		return "ChatGPT (Desktop app)"
	default:
		return "ChatGPT (Codex CLI + IDE)"
	}
}

// configPath returns the absolute path to config.toml under the Codex home
// dir ($CODEX_HOME, default ~/.codex).
func (a *Adapter) configPath() string { return filepath.Join(a.r.CodexDir(), "config.toml") }

// authPath returns the absolute path to auth.json under the Codex home dir
// ($CODEX_HOME, default ~/.codex).
func (a *Adapter) authPath() string { return filepath.Join(a.r.CodexDir(), "auth.json") }

// catalogPath returns the absolute path to MintSwitch's model-catalog file
// under the Codex home dir ($CODEX_HOME, default ~/.codex), written when
// needsCatalog holds and referenced by config.toml's model_catalog_json.
func (a *Adapter) catalogPath() string { return filepath.Join(a.r.CodexDir(), catalogFileName) }

// ConfigPaths returns the config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.configPath(), a.authPath()}
}

// Detect reports whether Codex is installed: either the "codex" CLI binary is
// resolvable (via PATH or a curated set of common bin dirs) or the ChatGPT
// desktop app is present (see desktopInstalled). The desktop app shares
// ~/.codex with the CLI, so a desktop-only install is fully manageable. A
// leftover ~/.codex dir is not an installed signal, so an uninstall is
// reflected. The active path is always config.toml and is returned even when not
// installed, since Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	installed := a.r.BinaryResolvable(a.lookPath, "codex") || a.desktopInstalled()
	return installed, a.configPath()
}

// desktopInstalled reports whether the ChatGPT desktop app is present, using
// only filesystem probes (no subprocesses, matching the claudecode extension
// probe) so it is safe to call on every Detect/ListTools. Per OS: macOS
// checks the .app bundles in macAppBundles for the com.openai.codex bundle ID
// (see bundleIsCodexApp); Windows checks the MSIX package data dir
// %LOCALAPPDATA%\Packages\OpenAI.Codex_* and the app's runtime dir
// %LOCALAPPDATA%\OpenAI\Codex — the binary lives in the protected WindowsApps
// dir and cannot be stat'ed from user space; Linux checks for the "chatgpt"
// launcher binary or the /usr/lib/chatgpt package payload dir.
func (a *Adapter) desktopInstalled() bool {
	switch a.goos {
	case "darwin":
		for _, app := range a.macAppBundles {
			if bundleIsCodexApp(app) {
				return true
			}
		}
	case "windows":
		if matches, err := filepath.Glob(filepath.Join(a.r.PackagesDir(), msixPackageGlob)); err == nil {
			for _, m := range matches {
				if fi, err := os.Stat(m); err == nil && fi.IsDir() {
					return true
				}
			}
		}
		if fi, err := os.Stat(filepath.Join(a.r.LocalAppDataDir(), "OpenAI", "Codex")); err == nil && fi.IsDir() {
			return true
		}
	case "linux":
		if a.r.BinaryResolvable(a.lookPath, "chatgpt") {
			return true
		}
		if fi, err := os.Stat(a.linuxLibDir); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// bundleIsCodexApp reports whether the macOS .app bundle at dir is the
// unified ChatGPT/Codex desktop app, by checking its Contents/Info.plist for
// the com.openai.codex bundle identifier. The legacy chat-only app ships
// under the same ChatGPT.app name (and as "ChatGPT Classic.app") with bundle
// ID com.openai.chat, so the name alone must never count as installed.
// Info.plist may be XML or binary; a byte-substring check covers both without
// a plist parser. A missing or unreadable plist is never a match.
func bundleIsCodexApp(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "Contents", "Info.plist"))
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte(desktopBundleID))
}

// Status inspects config.toml and auth.json relative to the given profile.
// The marker is read from the sidecar store: no entry means Default — unless
// the config files still carry the full MintSwitch injection signature (see
// orphanRemnant), which reports ModifiedExternally so the UI offers Restore
// even after the marker was lost (e.g. an interrupted restore). An entry
// whose managed key (openai_base_url) has been removed from the file also
// means Default (the file is back to an unmanaged state, e.g. after an
// external restore/wipe); otherwise the marker fingerprint decides Applied vs
// ModifiedExternally, exactly as with the legacy in-file marker. Even with a
// matching fingerprint, auth.json must still select the MintSwitch key
// (auth_mode="apikey" with the profile's OPENAI_API_KEY): a ChatGPT sign-in —
// via the CLI or the unified ChatGPT app, which shares ~/.codex — rewrites
// auth.json and makes Codex bypass the configured endpoint, so that state
// reports ModifiedExternally (authDriftDetail) instead of a false Applied.
func (a *Adapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	installed, _ := a.Detect()
	if !installed {
		return core.StatusNotInstalled, core.StatusNotInstalled.Detail(), nil
	}
	marker, ok, err := a.m.Get(a.ID())
	if err != nil {
		return core.StatusDefault, "", err
	}
	if !ok || !marker.Managed {
		if a.orphanRemnant() {
			return core.StatusModifiedExternally, orphanDetail, nil
		}
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	cfg, err := readTOML(a.configPath())
	if err != nil {
		return core.StatusDefault, "", err
	}
	if _, present := cfg["openai_base_url"]; !present {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint != core.Fingerprint(p) {
		return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
	}
	if a.authDrifted(p) {
		return core.StatusModifiedExternally, authDriftDetail, nil
	}
	if needsCatalog(p) {
		if a.catalogStale(cfg, p) {
			return core.StatusModifiedExternally, catalogStaleDetail, nil
		}
	} else if managedCatalogRef(cfg, a.catalogPath()) {
		// Apply removes the catalog reference when the profile needs none,
		// so a leftover one still feeds Codex outdated model metadata.
		return core.StatusModifiedExternally, catalogStaleDetail, nil
	} else if w := p.ContextWindow(p.Model); w > 0 && windowStale(cfg, w) {
		// The override case (see windowKey): the window is kept out of the
		// fingerprint, so a missing or changed key is how a window fetched or
		// refreshed after the last Apply surfaces as "apply again". With no
		// advertised window nothing is checked — a leftover key cannot be
		// told apart from a user's own, and the next Apply on a managed
		// config removes it.
		return core.StatusModifiedExternally, catalogStaleDetail, nil
	}
	return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
}

// windowStale reports whether config.toml's model_context_window is not the
// integer w: missing, a different number, or not a number at all. TOML
// integers decode as int64 (and a hand-edited value may be a float), so every
// numeric representation is normalized before comparing.
func windowStale(cfg map[string]any, w int) bool {
	switch v := cfg[windowKey].(type) {
	case int64:
		return v != int64(w)
	case int:
		return v != w
	case float64:
		return v != float64(w)
	default:
		return true
	}
}

// catalogStale reports whether MintSwitch's model catalog does not match what
// Apply would write for p now: config.toml no longer references it, the file
// is missing or unreadable, or its contents differ. The catalog carries data
// deliberately kept out of [core.Fingerprint] (context windows, reasoning
// levels), so this is how a refreshed model list surfaces as "apply again".
// Both sides are compared as decoded JSON, so formatting never matters.
func (a *Adapter) catalogStale(cfg map[string]any, p core.Profile) bool {
	catalogPath := a.catalogPath()
	if cfg[catalogKey] != catalogPath {
		return true
	}
	got, err := core.ReadJSONObject(catalogPath)
	if err != nil || len(got) == 0 {
		return true
	}
	raw, err := json.Marshal(catalogObject(p))
	if err != nil {
		return true
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		return true
	}
	return !reflect.DeepEqual(got, want)
}

// authDrifted reports whether auth.json no longer selects the MintSwitch API
// key that Apply wrote for the given profile: auth_mode flipped away from
// "apikey" (e.g. a ChatGPT sign-in set "chatgpt"), the OPENAI_API_KEY was
// removed or replaced, or the file is unreadable/corrupt. It is only
// meaningful when config.toml is confirmed MintSwitch-managed with a matching
// fingerprint, so any mismatch here is by definition an external change.
func (a *Adapter) authDrifted(p core.Profile) bool {
	auth, err := core.ReadJSONObject(a.authPath())
	if err != nil {
		return true
	}
	if mode, _ := auth[authModeKey].(string); mode != authModeAPIKey {
		return true
	}
	key, _ := auth[authKeyName].(string)
	return key != p.APIKey
}

// Apply backs up both files (only when config.toml is not already
// MintSwitch-managed), then injects openai_base_url + model into config.toml
// and OPENAI_API_KEY plus auth_mode="apikey" into auth.json, preserving all
// other existing keys in each file. A non-empty ReviewModel is written as the
// top-level review_model key; when empty the key is removed, but only from an
// already-managed config, so a user's own review_model is never deleted on a
// first Apply. In "All models" mode — or whenever ReviewModel is set, or the
// selected model has endpoint-advertised reasoning levels, so Codex has
// context-window and reasoning-effort metadata (see needsCatalog) — it
// additionally writes the mintswitch-models.json catalog under the Codex home dir and sets
// model_catalog_json to it (otherwise it removes both again). In single-model
// mode with no catalog, an endpoint-advertised context window for the
// selected model is written as the top-level model_context_window override
// (see windowKey); otherwise that key is removed under the same managed-only
// rule as review_model. The managed
// marker is recorded in the sidecar store — never in config.toml — and a
// leftover legacy in-file marker table is stripped in the same write.
//
// "Already managed" (the backup gate) means a store entry OR a legacy in-file
// marker, so upgrading from a legacy-marker install never snapshots a managed
// file. The backups are created only on the first Apply over a
// pristine/unmanaged (or absent) config, so the pristine pre-MintSwitch
// snapshots are what Restore reverts to even after repeated Applies. auth.json
// carries no marker but is gated by the same check so both files snapshot the
// same pre-MintSwitch point in time. Backing up an already-managed config
// would snapshot a managed state (prior profile's key) and hide the pristine
// original. If config.toml is already managed but no backup exists (e.g. the
// backups dir was deleted), no new backup is taken — we cannot safely snapshot
// a managed file; Restore then falls back to stripping the managed keys.
//
// Write order matters: auth.json is written first, config.toml second (with a
// best-effort rollback of auth.json when the config.toml write fails). If the
// process dies between the two writes, config.toml — the file that redirects
// traffic — is still pristine, so Codex never sends requests to the proxy
// without a matching key configured (the reverse order left exactly that
// half-state behind, and orphanRemnant, which requires the full two-file
// signature, could not detect it). The leftover auth.json key never routes
// traffic anywhere new, and the next Apply or Restore overwrites or strips it.
func (a *Adapter) Apply(p core.Profile) (core.ApplyResult, error) {
	if err := p.Validate(); err != nil {
		return core.ApplyResult{}, err
	}
	cfgPath, authPath := a.configPath(), a.authPath()

	// Read both files up front so a corrupt file fails the Apply before
	// anything is written.
	cfg, err := readTOML(cfgPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	auth, err := core.ReadJSONObject(authPath)
	if err != nil {
		return core.ApplyResult{}, err
	}
	origAuth, readErr := os.ReadFile(authPath)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return core.ApplyResult{}, readErr
	}
	authExisted := readErr == nil

	_, inStore, err := a.m.Get(a.ID())
	if err != nil {
		return core.ApplyResult{}, err
	}
	var cfgBackup string
	legacy, hasLegacy := core.ExtractLegacyMarker(cfg)
	managed := inStore || (hasLegacy && legacy.Managed)
	if !managed {
		cfgBackup, err = a.e.Backup(cfgPath)
		if err != nil {
			return core.ApplyResult{}, err
		}
		if _, err := a.e.Backup(authPath); err != nil {
			return core.ApplyResult{}, err
		}
	}

	auth[authKeyName] = p.APIKey
	auth[authModeKey] = authModeAPIKey
	if err := core.WriteJSONObjectAtomic(authPath, auth); err != nil {
		return core.ApplyResult{}, err
	}
	// rollbackAuth best-effort reverts auth.json to its pre-Apply bytes when
	// the config.toml half of the two-file write fails, so a failed Apply
	// never leaves the MintSwitch key (and auth_mode="apikey") behind in
	// auth.json while config.toml was left untouched.
	rollbackAuth := func() {
		if authExisted {
			_ = core.WriteFileAtomic(authPath, origAuth, 0o600)
		} else {
			_ = os.Remove(authPath)
		}
	}

	// "All models" mode — and any profile pinning a review model or whose
	// selected model has endpoint-advertised reasoning levels, both of which
	// need catalog metadata (see needsCatalog) — writes MintSwitch's model
	// catalog and points
	// model_catalog_json at it (before config.toml, so the reference never
	// lands ahead of the file); otherwise both are removed again, gated on
	// managedCatalogRef so a user's own catalog reference is never touched. A
	// catalog file left behind by a failed config.toml write routes no traffic
	// and is overwritten or removed by the next Apply/Restore.
	catalogPath := a.catalogPath()
	catalog := needsCatalog(p)
	if catalog {
		if err := core.WriteJSONObjectAtomic(catalogPath, catalogObject(p)); err != nil {
			rollbackAuth()
			return core.ApplyResult{}, err
		}
		cfg[catalogKey] = catalogPath
	} else {
		if managedCatalogRef(cfg, catalogPath) {
			delete(cfg, catalogKey)
		}
		if err := os.Remove(catalogPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			rollbackAuth()
			return core.ApplyResult{}, err
		}
	}

	cfg["openai_base_url"] = p.BaseURL
	cfg["model"] = p.Model
	if p.ReviewModel != "" {
		cfg[reviewModelKey] = p.ReviewModel
	} else if managed {
		// Only an already-managed config can carry a MintSwitch-written
		// review_model, so a user's own pin survives a first Apply.
		delete(cfg, reviewModelKey)
	}
	// The context-window override (see windowKey) is written only when no
	// catalog carries per-model windows; otherwise it is removed under the
	// same managed-only rule as review_model, so a user's own override
	// survives a first Apply.
	if w := p.ContextWindow(p.Model); !catalog && w > 0 {
		cfg[windowKey] = w
	} else if managed {
		delete(cfg, windowKey)
	}
	delete(cfg, core.MarkerKey)
	if err := a.writeConfig(cfgPath, cfg); err != nil {
		rollbackAuth()
		return core.ApplyResult{}, err
	}

	if err := a.m.Put(a.ID(), core.NewMarker(p, p.Label)); err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{
		ChangedPath: cfgPath,
		BackupPath:  cfgBackup,
		Message:     "Applied MintSwitch profile to Codex config.toml and auth.json.",
	}, nil
}

// Restore reverts config.toml and auth.json to their pristine pre-MintSwitch
// state via the backup engine (oldest snapshots; all entries are pruned after
// a successful restore). Both restores are attempted best-effort even when one
// fails, so an error on config.toml never silently skips auth.json (or vice
// versa); failures are joined into a single error naming each file. When a
// file has no backup but Codex is still MintSwitch-managed (marker in store,
// or — with the marker lost — the full injection signature still in the
// files, see orphanRemnant), Restore falls back to stripping the managed keys
// from it — openai_base_url, model, review_model, model_context_window and
// MintSwitch's model_catalog_json in config.toml, OPENAI_API_KEY and
// auth_mode in auth.json — preserving every other key. The mintswitch-models.json catalog file is MintSwitch's own
// creation (never part of any pre-apply backup), so it is simply removed.
func (a *Adapter) Restore() (core.RestoreResult, error) {
	cfgPath, authPath := a.configPath(), a.authPath()
	_, inStore, err := a.m.Get(a.ID())
	if err != nil {
		return core.RestoreResult{}, err
	}
	orphan := !inStore && a.orphanRemnant()
	cfgRestored, cfgEntry, cfgErr := a.e.RestorePristine(cfgPath)
	authRestored, _, authErr := a.e.RestorePristine(authPath)
	if cfgErr != nil {
		cfgErr = fmt.Errorf("restore config.toml: %w", cfgErr)
	}
	if authErr != nil {
		authErr = fmt.Errorf("restore auth.json: %w", authErr)
	}
	if err := errors.Join(cfgErr, authErr); err != nil {
		return core.RestoreResult{}, err
	}
	var cfgStripped, authStripped bool
	if !cfgRestored && (inStore || orphan) {
		cfgStripped, err = stripManagedConfig(cfgPath)
		if err != nil {
			return core.RestoreResult{}, err
		}
	}
	if !authRestored && (inStore || orphan) {
		authStripped, err = stripManagedAuth(authPath)
		if err != nil {
			return core.RestoreResult{}, err
		}
	}
	if inStore || orphan {
		if err := os.Remove(a.catalogPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return core.RestoreResult{}, err
		}
	}
	if err := a.m.Delete(a.ID()); err != nil {
		return core.RestoreResult{}, err
	}
	var msg string
	switch {
	case cfgRestored && authRestored:
		msg = "Restored Codex config.toml and auth.json from backup."
	case cfgRestored && authStripped:
		msg = "Restored Codex config.toml from backup; no backup found for auth.json, so the MintSwitch API key was removed from it."
	case cfgRestored:
		msg = "Restored Codex config.toml from backup; no backup found for auth.json, so the MintSwitch API key may still be present there."
	case authRestored && cfgStripped:
		msg = "Restored Codex auth.json from backup; no backup found for config.toml, so the MintSwitch-managed keys were removed from it."
	case authRestored:
		msg = "Restored Codex auth.json from backup; no backup found for config.toml."
	case cfgStripped || authStripped:
		msg = "No backup found; removed the MintSwitch-managed keys from the Codex config files."
	default:
		msg = "No backup found; nothing to restore."
	}
	return core.RestoreResult{
		ChangedPath: cfgPath,
		BackupPath:  cfgEntry,
		Message:     msg,
	}, nil
}

// orphanRemnant reports whether the Codex config files still carry the FULL
// MintSwitch injection signature without requiring a marker: config.toml
// holding both managed keys (openai_base_url and model) AND auth.json holding
// OPENAI_API_KEY with auth_mode="apikey" — exactly the shape Apply writes. It
// is deliberately stricter than the strip helpers' gates because each key on
// its own is a state a user could configure themselves (a hand-rolled proxy
// URL, an API-key login): only the complete signature is treated as a
// MintSwitch remnant, so a hand-written config never shows Restore or gets
// stripped by mistake. A missing or corrupt file is never a remnant.
func (a *Adapter) orphanRemnant() bool {
	cfg, err := readTOML(a.configPath())
	if err != nil {
		return false
	}
	if _, ok := cfg["openai_base_url"]; !ok {
		return false
	}
	if _, ok := cfg["model"]; !ok {
		return false
	}
	auth, err := core.ReadJSONObject(a.authPath())
	if err != nil {
		return false
	}
	if _, ok := auth[authKeyName]; !ok {
		return false
	}
	mode, _ := auth[authModeKey].(string)
	return mode == authModeAPIKey
}

// stripManagedConfig removes the MintSwitch-managed keys (openai_base_url,
// model, review_model, model_context_window, and model_catalog_json when it
// points at MintSwitch's own catalog file) from config.toml, preserving every
// other key. It is the
// Restore fallback when no pristine backup exists. Gated on the managed
// signal (openai_base_url present) so an unmanaged file is never rewritten;
// it never creates the file.
func stripManagedConfig(path string) (bool, error) {
	cfg, err := readTOML(path)
	if err != nil {
		return false, err
	}
	if _, present := cfg["openai_base_url"]; !present {
		return false, nil
	}
	delete(cfg, "openai_base_url")
	delete(cfg, "model")
	delete(cfg, reviewModelKey)
	delete(cfg, windowKey)
	if v, _ := cfg[catalogKey].(string); hasCatalogBase(v) {
		delete(cfg, catalogKey)
	}
	return true, writeTOML(path, cfg)
}

// stripManagedAuth removes OPENAI_API_KEY and auth_mode from auth.json,
// preserving every other key. Apply always overwrites both, so with the
// managed marker still in the store the current values are MintSwitch's.
// Gated on OPENAI_API_KEY being present so an unmanaged file is never
// rewritten; it never creates the file.
func stripManagedAuth(path string) (bool, error) {
	auth, err := core.ReadJSONObject(path)
	if err != nil {
		return false, err
	}
	if _, present := auth[authKeyName]; !present {
		return false, nil
	}
	delete(auth, authKeyName)
	delete(auth, authModeKey)
	return true, core.WriteJSONObjectAtomic(path, auth)
}

// StripLegacyMarker removes the legacy [mintswitchManaged] table from
// config.toml, migrating its value into the sidecar store when the store has
// no entry for this tool yet. The TOML round-trip preserves every other key
// (including [mcp_servers], [projects], ...). It is a no-op when the file is
// absent or carries no legacy marker; it never creates the file.
func (a *Adapter) StripLegacyMarker() error {
	path := a.configPath()
	cfg, err := readTOML(path)
	if err != nil {
		return err
	}
	legacy, ok := core.ExtractLegacyMarker(cfg)
	if !ok {
		if _, present := cfg[core.MarkerKey]; !present {
			return nil
		}
	}
	if ok && legacy.Managed {
		if _, inStore, err := a.m.Get(a.ID()); err == nil && !inStore {
			if err := a.m.Put(a.ID(), legacy); err != nil {
				return err
			}
		}
	}
	delete(cfg, core.MarkerKey)
	return writeTOML(path, cfg)
}

var (
	_ core.ToolAdapter          = (*Adapter)(nil)
	_ core.LegacyMarkerStripper = (*Adapter)(nil)
)
