// Package omp implements the core.ToolAdapter for oh-my-pi
// (https://github.com/can1357/oh-my-pi), the @oh-my-pi/pi-coding-agent CLI
// ("omp"). omp is a Pi fork with its own config directory and YAML config
// files, so it is managed as a tool separate from the Pi adapter. It applies
// and restores a MintSwitch-managed OpenAI-compatible provider across omp's
// two global config files under ~/.omp/agent: models.yml — upserting a custom
// provider "mintrouter" of the form { baseUrl, api: "openai-completions",
// apiKey, models: [{id, name, input, reasoning, contextWindow, maxTokens}] }
// under the top-level "providers" map — and config.yml — setting modelRoles.default to
// the "mintrouter/<model>" selector (plus modelRoles.smol when the profile
// pins a small/fast model). Both files are edited as yaml.v3 node trees so
// the user's comments, key order and every other key survive each rewrite.
// The managed marker lives in the sidecar marker store, never in omp's files.
//
// Schema reference (verified 2026-09-28 against github.com/can1357/oh-my-pi
// main: packages/coding-agent settings.ts / model-selector.ts / docs):
// models.yml carries { providers: { <id>: { baseUrl, api, apiKey, models:
// [{ id, name, input?, reasoning?, contextWindow?, maxTokens? }] } } } — the
// same provider shape as Pi's models.json (docs/models.md, re-verified
// 2026-09-30: input defaults to [text] so images are silently dropped,
// reasoning defaults to false so the /thinking selector stays hidden — hence
// both are always written per model); config.yml carries modelRoles: { default:
// "<provider>/<modelId>[:<thinking>]", smol, slow, ... } in place of Pi's
// flat defaultProvider/defaultModel. omp prefers the .yml spelling and falls
// back to .yaml for both files; a legacy models.json / settings.json is
// migrated by omp only while the .yml file does not exist, so when MintSwitch
// creates the .yml first it seeds it from the legacy JSON (see
// readWithLegacy). A project-level .omp/config.yml may still override
// modelRoles for one repository — omp's normal precedence, out of scope here.
package omp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/markers"
	"mintswitch/internal/paths"
)

// id is the stable adapter identifier, also the tool's key in the marker store.
const id = "omp"

// providerID is the custom provider key MintSwitch writes under "providers" in
// models.yml and as the provider half of every selector in config.yml.
const providerID = "mintrouter"

// apiType is omp's API type for OpenAI-compatible Chat Completions endpoints.
const apiType = "openai-completions"

// selectorPrefix is the provider half of a "<provider>/<modelId>" selector.
const selectorPrefix = providerID + "/"

// Model roles MintSwitch manages under config.yml's modelRoles map: default is
// the primary model, smol the lightweight model used for background tasks.
const (
	roleDefault = "default"
	roleSmol    = "smol"
)

// orphanDetail explains the orphan-remnant state: models.yml still carries
// the MintSwitch provider but the managed marker is gone (e.g. a previous
// restore was interrupted after clearing the marker).
const orphanDetail = "The MintSwitch provider is still present but the managed marker is missing " +
	"(a previous restore may have been interrupted). Restore Default will remove it."

// configDriftDetail explains the config-drift state: models.yml still carries
// the MintSwitch provider, but config.yml's modelRoles.default no longer
// selects it — typically because the user picked another provider inside omp
// (/model rewrites modelRoles.default). omp then routes traffic elsewhere, so
// the profile must be re-applied.
const configDriftDetail = "config.yml no longer selects the MintSwitch provider as the default model role " +
	"(omp's /model picker likely changed it), so oh-my-pi bypasses the configured endpoint. Apply the profile again to fix this."

// modelDriftDetail explains the milder drift where modelRoles.default still
// points at the MintSwitch provider but its model was changed inside omp (the
// /model picker, e.g. between models applied in "All models" mode). Requests
// still go through the configured endpoint — only the default model differs.
const modelDriftDetail = "oh-my-pi's default model was changed inside omp (the /model picker), but requests " +
	"still go through the MintSwitch endpoint. Apply the profile again to reset the default model."

// Ensure Adapter satisfies the shared adapter contract.
var _ core.ToolAdapter = (*Adapter)(nil)

// Adapter manages omp's configuration on behalf of MintSwitch. The managed
// marker lives in the sidecar marker store, never in models.yml/config.yml.
type Adapter struct {
	r *paths.Resolver
	e *backup.Engine
	m *markers.Store
	// lookPath resolves a binary on PATH; overridable in tests. Defaults to
	// exec.LookPath.
	lookPath func(string) (string, error)
	// writeConfig writes config.yml; overridable in tests to inject write
	// failures into the second half of Apply's two-file write. Defaults to
	// writeDocument.
	writeConfig func(string, *document) error
}

// New constructs an omp adapter that resolves paths via r, backs up via e, and
// records its managed marker in m. All filesystem locations derive from the
// injected dependencies so tests can point HOME at a temp dir.
func New(r *paths.Resolver, e *backup.Engine, m *markers.Store) *Adapter {
	return &Adapter{r: r, e: e, m: m, lookPath: exec.LookPath, writeConfig: writeDocument}
}

// ID returns the stable adapter identifier.
func (a *Adapter) ID() string { return id }

// Name returns the display name.
func (a *Adapter) Name() string { return "oh-my-pi" }

// agentDir returns omp's global agent directory (~/.omp/agent).
func (a *Adapter) agentDir() string { return a.r.Join(".omp", "agent") }

// resolveYAML returns <base>.yml under the agent dir, or <base>.yaml when
// only that spelling exists — the same preference order omp uses. A missing
// file resolves to the .yml spelling, which is what Apply then creates.
func (a *Adapter) resolveYAML(base string) string {
	yml := filepath.Join(a.agentDir(), base+".yml")
	if _, err := os.Stat(yml); err == nil {
		return yml
	}
	if alt := filepath.Join(a.agentDir(), base+".yaml"); fileExists(alt) {
		return alt
	}
	return yml
}

// modelsPath returns the absolute path to omp's global custom-models file
// (~/.omp/agent/models.yml).
func (a *Adapter) modelsPath() string { return a.resolveYAML("models") }

// configPath returns the absolute path to omp's global settings file
// (~/.omp/agent/config.yml).
func (a *Adapter) configPath() string { return a.resolveYAML("config") }

// legacyModelsPath / legacySettingsPath are the Pi-era JSON files omp migrates
// from when the YAML file is absent; Apply seeds from them (see readWithLegacy).
func (a *Adapter) legacyModelsPath() string   { return filepath.Join(a.agentDir(), "models.json") }
func (a *Adapter) legacySettingsPath() string { return filepath.Join(a.agentDir(), "settings.json") }

// ConfigPaths returns the config files this adapter manages.
func (a *Adapter) ConfigPaths() []string {
	return []string{a.modelsPath(), a.configPath()}
}

// fileExists reports whether path exists (any file type).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Detect reports whether omp is installed, defined solely as the "omp" CLI
// binary being resolvable (via PATH or a curated set of common bin dirs,
// including ~/.bun/bin for bun-global installs). A leftover ~/.omp dir is not
// an installed signal, so an uninstall is reflected. The active path is
// always models.yml and is returned even when not installed, since
// Status/Apply rely on it.
func (a *Adapter) Detect() (bool, string) {
	return a.r.BinaryResolvable(a.lookPath, "omp"), a.modelsPath()
}

// Status inspects models.yml and config.yml relative to the given profile.
// The marker is read from the sidecar store: no entry means Default — unless
// models.yml still carries the MintSwitch provider block (see orphanRemnant),
// which reports ModifiedExternally so the UI offers Restore even after the
// marker was lost (e.g. an interrupted restore). An entry whose managed
// provider block (providers.mintrouter) has been removed from the file also
// means Default (the file is back to an unmanaged state, e.g. after an
// external restore/wipe); otherwise the marker fingerprint decides Applied vs
// ModifiedExternally. Even with a matching fingerprint, config.yml must still
// select the MintSwitch provider and model as modelRoles.default: omp's
// /model picker rewrites it behind MintSwitch's back, so that state reports
// ModifiedExternally instead of a false Applied — with a detail
// distinguishing a repointed provider (configDriftDetail) from a mere model
// change still routed through the endpoint (modelDriftDetail); see configDrift.
func (a *Adapter) Status(p core.Profile) (core.ToolStatus, string, error) {
	installed, path := a.Detect()
	if !installed {
		return core.StatusNotInstalled, core.StatusNotInstalled.Detail(), nil
	}
	marker, ok, err := a.m.Get(id)
	if err != nil {
		return core.StatusDefault, "", err
	}
	if !ok || !marker.Managed {
		if a.orphanRemnant() {
			return core.StatusModifiedExternally, orphanDetail, nil
		}
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	models, err := readDocument(path)
	if err != nil {
		return core.StatusDefault, "", err
	}
	if managedProvider(models) == nil {
		return core.StatusDefault, core.StatusDefault.Detail(), nil
	}
	if marker.Fingerprint != core.Fingerprint(p) {
		return core.StatusModifiedExternally, core.StatusModifiedExternally.Detail(), nil
	}
	if detail := a.configDrift(p); detail != "" {
		return core.StatusModifiedExternally, detail, nil
	}
	return core.StatusAppliedByMintSwitch, core.StatusAppliedByMintSwitch.Detail(), nil
}

// managedProvider returns the providers.mintrouter node of a models document,
// or nil when the file carries no MintSwitch provider block.
func managedProvider(d *document) *yaml.Node {
	return mapGet(mapGet(d.root, "providers"), providerID)
}

// configDrift reports whether config.yml no longer selects the MintSwitch
// provider and model that Apply wrote for the given profile, returning the
// matching drift detail ("" when nothing drifted). modelRoles.default
// pointing at another provider — or missing, malformed, or an
// unreadable/corrupt file — means omp bypasses the endpoint entirely
// (configDriftDetail); provider intact but model changed means only the
// default model drifted while traffic still flows through the endpoint
// (modelDriftDetail). It is only meaningful when models.yml is confirmed
// MintSwitch-managed with a matching fingerprint, so any mismatch here is by
// definition an external change.
func (a *Adapter) configDrift(p core.Profile) string {
	cfg, err := readDocument(a.configPath())
	if err != nil {
		return configDriftDetail
	}
	sel := mapGet(mapGet(cfg.root, "modelRoles"), roleDefault)
	if sel == nil || sel.Kind != yaml.ScalarNode {
		return configDriftDetail
	}
	prov, model, ok := splitSelector(sel.Value)
	if !ok || prov != providerID {
		return configDriftDetail
	}
	if !modelMatches(model, p.Model) {
		return modelDriftDetail
	}
	return ""
}

// splitSelector splits a "<provider>/<modelId>" selector at its first slash,
// mirroring omp's parseModelString: the model ID may itself contain slashes.
func splitSelector(s string) (provider, model string, ok bool) {
	provider, model, ok = strings.Cut(strings.TrimSpace(s), "/")
	return provider, model, ok && provider != "" && model != ""
}

// modelMatches reports whether a selector's model part names want. omp's
// /model picker may append a thinking level (":high", ":max", ...) to the
// selector; that still routes to the same model through the endpoint, so a
// suffixed form counts as a match — but only when stripping the suffix yields
// exactly want, so a model ID that itself ends in ":<word>" is never
// mistaken for a suffixed different model.
func modelMatches(got, want string) bool {
	if got == want {
		return true
	}
	i := strings.LastIndex(got, ":")
	return i > 0 && got[:i] == want
}
