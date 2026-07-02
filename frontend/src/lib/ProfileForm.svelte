<script lang="ts">
  import type { ProfileView } from "../../bindings/mintswitch/internal/service";
  import type { Profile } from "../../bindings/mintswitch/internal/core";
  import { isHttpUrl, normalizeBaseUrl } from "./ui";

  interface Props {
    profile: ProfileView;
    saving: boolean;
    onSave: (p: Profile) => Promise<boolean>;
    onAutoSave: (p: Profile) => Promise<string | null>;
    mcpEnabled: boolean;
    hasMcpKey: boolean;
    onToggleEnabled: (enabled: boolean) => void;
  }
  let { profile, saving, onSave, onAutoSave, mcpEnabled, hasMcpKey, onToggleEnabled }: Props = $props();

  let label = $state("");
  let baseUrl = $state("");
  let apiKey = $state("");
  let models = $state<string[]>([]);
  let modelNames = $state<Record<string, string>>({});
  let model = $state("");
  let newModel = $state("");
  let newModelName = $state("");
  let errors = $state<Record<string, string>>({});

  // Seed the editable fields from the saved (non-secret) profile. Re-runs only
  // when the parent reassigns `profile` (initial load / post-save refresh), so
  // in-progress edits are never clobbered. The API key is never seeded — the
  // backend never sends it — so the field starts blank and an empty submit keeps
  // the stored key.
  $effect(() => {
    label = profile.label ?? "";
    baseUrl = profile.base_url ?? "";
    models = profile.models ? [...profile.models] : [];
    const seeded: Record<string, string> = {};
    for (const [id, name] of Object.entries(profile.model_names ?? {})) {
      if (name) seeded[id] = name;
    }
    modelNames = seeded;
    model = profile.model ?? "";
    apiKey = "";
  });

  // What the UI shows for a model: its optional display name, falling back to
  // the canonical ID (also for pre-display-name profiles with no names saved).
  function displayName(m: string): string {
    return modelNames[m] || m;
  }

  // Builds the SaveProfile payload from the current form state. api_key is the
  // caller's choice: the typed key on explicit Save, "" (= keep the stored key)
  // on auto-save. The Small / fast model field was removed from the UI; always
  // send "" so the Profile/binding shape stays unchanged (the old "None").
  function payload(key: string): Profile {
    return {
      label: label.trim(),
      api_key: key,
      base_url: normalizedBase.url,
      models: models,
      model_names: modelNames,
      model: model,
      small_fast_model: "",
    };
  }

  // Model changes made in the dialog (add / remove / default) persist
  // immediately so a refresh never loses them — no explicit Save needed. Only
  // runs when the profile is persistable as-is: a key is already stored (the
  // auto-save sends api_key "" = keep it) plus a valid base URL and default
  // model. Otherwise (e.g. first-run profile never saved) the change stays
  // local exactly as before — no error — and the explicit Save persists it.
  let autoSaveError = $state("");

  function canAutoSave(): boolean {
    return (
      profile.has_key &&
      isHttpUrl(normalizedBase.url) &&
      models.length > 0 &&
      models.includes(model)
    );
  }

  async function autoSave(): Promise<void> {
    if (!canAutoSave()) return;
    autoSaveError = "";
    const err = await onAutoSave(payload(""));
    if (err) autoSaveError = err;
  }

  // Add the typed model to the list (trimmed, deduped by ID). The optional
  // display name is stored alongside; re-adding an existing ID never duplicates
  // it but a newly typed name updates its alias. The first model added becomes
  // the default automatically so a valid default is always present.
  function addModel(): void {
    const id = newModel.trim();
    if (!id) return;
    const name = newModelName.trim();
    if (!models.includes(id)) {
      models = [...models, id];
      if (!model) model = id;
    }
    if (name) {
      modelNames = { ...modelNames, [id]: name };
    }
    newModel = "";
    newModelName = "";
    modelInputEl?.focus();
    void autoSave();
  }

  function onModelKeydown(e: KeyboardEvent): void {
    if (e.key === "Enter") {
      e.preventDefault();
      addModel();
    }
  }

  // Remove a model (and its display name); if it was the default, fall back to
  // the first remaining model (or clear the default when none are left) so it's
  // never orphaned. Removing the last model cannot auto-save (a profile needs a
  // model), so the stored list only shrinks to empty via the explicit Save.
  function removeModel(m: string): void {
    models = models.filter((x) => x !== m);
    if (m in modelNames) {
      const next = { ...modelNames };
      delete next[m];
      modelNames = next;
    }
    if (model === m) {
      model = models[0] ?? "";
    }
    void autoSave();
  }

  // Changing the default model in the dialog also persists immediately.
  function setDefault(m: string): void {
    if (model === m) return;
    model = m;
    void autoSave();
  }

  // Models are managed in a popup so the card shows only a compact summary.
  // Esc / backdrop / Done all close it.
  let modelsOpen = $state(false);
  let modelInputEl = $state<HTMLInputElement | null>(null);
  let modelsDialogEl = $state<HTMLDivElement | null>(null);

  function openModels(): void {
    autoSaveError = "";
    modelsOpen = true;
  }

  function closeModels(): void {
    modelsOpen = false;
  }

  // Focus the model input whenever the modal opens; runs after the dialog is
  // rendered so the element ref exists.
  $effect(() => {
    if (modelsOpen) {
      queueMicrotask(() => modelInputEl?.focus());
    }
  });

  // Esc closes the modal; Tab is trapped inside the dialog while it's open.
  function onModelsKeydown(e: KeyboardEvent): void {
    if (!modelsOpen) return;
    if (e.key === "Escape") {
      e.preventDefault();
      closeModels();
      return;
    }
    if (e.key !== "Tab" || !modelsDialogEl) return;
    const focusables = modelsDialogEl.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input:not([disabled])',
    );
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = document.activeElement as HTMLElement | null;
    if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }

  function validate(): boolean {
    const next: Record<string, string> = {};
    if (!isHttpUrl(baseUrl)) {
      next.baseUrl = "Enter a valid http(s) URL.";
    }
    if (models.length === 0) {
      next.models = "Add at least one model.";
    } else if (!model.trim() || !models.includes(model)) {
      next.model = "Choose a default model.";
    }
    if (!profile.has_key && !apiKey.trim()) {
      next.apiKey = "An API key is required to save a new profile.";
    }
    errors = next;
    return Object.keys(next).length === 0;
  }

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (saving || !validate()) return;
    const ok = await onSave(payload(apiKey));
    if (ok) apiKey = "";
  }

  const keyPlaceholder = $derived(
    profile.has_key ? "••••••••" : "Enter your API key",
  );

  // Live preview of the backend normalization. When a public http endpoint is
  // upgraded to https we surface a non-blocking notice so the user sees the
  // saved value up front (http endpoints often drop the API key on redirect).
  const normalizedBase = $derived(normalizeBaseUrl(baseUrl));

  // One-line description of the model list shown in the card: count (with
  // correct plural) plus the current default (by display name), or an
  // empty-state message.
  const modelsSummary = $derived(
    models.length === 0
      ? "No models yet"
      : `${models.length} model${models.length > 1 ? "s" : ""}${model ? ` · default ${displayName(model)}` : ""}`,
  );
</script>

<form class="card profile" onsubmit={submit} novalidate aria-labelledby="profile-h">
  <h2 class="card-title profile-title" id="profile-h">Endpoint profile</h2>

  <div class="field">
    <label class="micro-label" for="pf-label">Label <span class="opt">Optional</span></label>
    <input class="field-input" id="pf-label" type="text" bind:value={label}
      placeholder="MintRouter" autocomplete="off" />
  </div>

  <div class="field">
    <label class="micro-label" for="pf-base">Base URL</label>
    <input class="field-input" id="pf-base" type="url" bind:value={baseUrl}
      placeholder="https://api.mintrouter.ai/v1" autocomplete="off" spellcheck="false"
      aria-invalid={!!errors.baseUrl}
      aria-describedby={errors.baseUrl ? "err-base" : normalizedBase.upgraded ? "notice-base" : undefined} />
    {#if errors.baseUrl}
      <p class="field-error" id="err-base">{errors.baseUrl}</p>
    {:else if normalizedBase.upgraded}
      <p class="field-notice" id="notice-base">
        Will be saved as <code>{normalizedBase.url}</code> — http endpoints can drop the API key on redirect.
      </p>
    {/if}
  </div>

  <div class="field">
    <div class="label-row">
      <label class="micro-label" for="pf-key">API key</label>
      {#if profile.has_key}
        <span class="badge tone-success">Saved</span>
      {/if}
    </div>
    <input class="field-input" id="pf-key" type="password" bind:value={apiKey}
      placeholder={keyPlaceholder} autocomplete="off"
      aria-invalid={!!errors.apiKey}
      aria-describedby={errors.apiKey ? "err-key" : profile.has_key ? "hint-key" : undefined} />
    {#if errors.apiKey}
      <p class="field-error" id="err-key">{errors.apiKey}</p>
    {:else if profile.has_key}
      <p class="field-hint" id="hint-key">Leave blank to keep the saved key.</p>
    {/if}
  </div>

  <div class="field">
    <span class="micro-label" id="pf-models-label">Models</span>
    <div class="models-summary">
      <span class="models-summary-text" class:is-empty={models.length === 0}
        aria-describedby="pf-models-label">{modelsSummary}</span>
      <button class="btn-ghost sm" type="button" onclick={openModels}>Manage</button>
    </div>
    {#if errors.models}
      <p class="field-error" id="err-models">{errors.models}</p>
    {:else if errors.model}
      <p class="field-error" id="err-model">{errors.model}</p>
    {/if}
  </div>

  <div class="ce-intro">
    <span class="micro-label">Context Engine</span>
    <p class="ce-intro-text">
      MintRouter.AI's MCP server helps your AI coding tools understand your
      codebase — semantic code search on a real-time index. Enable the toggle
      below to inject it into your tools when you Apply.
    </p>
  </div>

  <div class="profile-footer">
    <label class="mcp-switch" class:is-disabled={!hasMcpKey}
      title={hasMcpKey ? undefined : "Save your MintRouter API key in the profile first"}>
      <input class="mcp-switch-input" type="checkbox" role="switch"
        checked={mcpEnabled} disabled={!hasMcpKey}
        onchange={(e) => onToggleEnabled(e.currentTarget.checked)} />
      <span class="mcp-switch-track" aria-hidden="true">
        <span class="mcp-switch-thumb"></span>
      </span>
      <span class="mcp-switch-label">Context Engine</span>
    </label>
    <button class="btn-primary" type="submit" disabled={saving}>
      {saving ? "Saving…" : "Save"}
    </button>
  </div>
</form>

<svelte:window onkeydown={onModelsKeydown} />

{#if modelsOpen}
  <div class="backdrop" role="presentation"
    onclick={(e) => e.target === e.currentTarget && closeModels()}>
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="pf-models-title"
      tabindex="-1" bind:this={modelsDialogEl}>
      <h2 class="title" id="pf-models-title">Models</h2>
      <div class="add-body">
        <div class="field">
          <span class="micro-label">Add a model</span>
          <div class="model-add">
            <div class="model-add-field">
              <label class="model-add-label" for="pf-model-add">Model ID</label>
              <input class="field-input" id="pf-model-add" type="text" bind:value={newModel}
                bind:this={modelInputEl}
                placeholder="anthropic/claude-opus-4.8" autocomplete="off" spellcheck="false"
                onkeydown={onModelKeydown}
                aria-invalid={!!errors.models}
                aria-describedby={errors.models ? "err-models" : errors.model ? "err-model" : undefined} />
            </div>
            <div class="model-add-field">
              <label class="model-add-label" for="pf-model-add-name">Display name <span class="opt">Optional</span></label>
              <input class="field-input" id="pf-model-add-name" type="text" bind:value={newModelName}
                placeholder="opus4.8" autocomplete="off" spellcheck="false"
                onkeydown={onModelKeydown} />
            </div>
            <button class="btn-primary sm" type="button" onclick={addModel} disabled={!newModel.trim() || saving}>Add</button>
          </div>
        </div>
        {#if models.length}
          <div class="seg-group" role="group" aria-label="Default model">
            {#each models as m (m)}
              <div class="seg" class:selected={m === model}>
                <button class="seg-select" type="button" aria-pressed={m === model}
                  onclick={() => setDefault(m)} title={`Set ${displayName(m)} as default`}>
                  <span class="seg-name">{displayName(m)}</span>
                  {#if modelNames[m]}
                    <span class="seg-id">{m}</span>
                  {/if}
                </button>
                <button class="seg-remove" type="button"
                  onclick={(e) => { e.preventDefault(); e.stopPropagation(); removeModel(m); }}
                  aria-label={`Remove ${displayName(m)}`} title={`Remove ${displayName(m)}`}>×</button>
              </div>
            {/each}
          </div>
        {:else}
          <p class="field-hint">No models yet — add your first one above.</p>
        {/if}
        {#if autoSaveError}
          <p class="field-error" role="alert">Couldn't save: {autoSaveError}</p>
        {/if}
      </div>
      <div class="actions">
        <button class="btn-primary" type="button" onclick={closeModels}>Done</button>
      </div>
    </div>
  </div>
{/if}

<style>
  /* Tight, even rhythm between field groups — no header/footer divider lines;
     a compact local gap (tighter than the token scale) keeps the card short.
     The card GROWS to fill the left column (feedback #32) so its bottom edge
     lines up with the tools panel; with tall content (many models) it still
     sizes to content and the column scrolls as before. */
  .profile { display: flex; flex-direction: column; gap: 12px; flex: 1 0 auto; }
  .profile-title { margin: 0; }
  /* Label + saved-state badge share a row (e.g. API KEY · Saved). */
  .label-row { display: flex; align-items: center; gap: 0.5rem; }
  .opt {
    margin-left: 0.3rem;
    text-transform: none;
    letter-spacing: 0;
    font-weight: var(--fw-medium);
    color: var(--muted);
  }
  /* The Context Engine toggle and Save action share a footer row. A hairline
     plus extra top space sets the actions apart from the fields above. The
     auto top margin anchors the row to the card's bottom (feedback #32) so
     the stretched card has no dead space mid-card; when content is tall the
     auto margin collapses to 0 and the row follows the fields as before. */
  .profile-footer {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--s-1);
    margin-top: auto;
    border-top: 1px solid var(--border);
    padding-top: 12px;
  }

  /* Master Context Engine ON/OFF switch, shared with the Save row. A native
     checkbox (role="switch") drives an accent track + sliding thumb; the visible
     label is the switch's accessible name via the wrapping <label>. */
  /* Chromeless: just the track + label, structured by the footer hairline. */
  .mcp-switch {
    display: inline-flex;
    align-items: center;
    min-width: 0;
    gap: 0.45rem;
    padding: 0;
    background: transparent;
    border: none;
    cursor: pointer;
    user-select: none;
  }
  .mcp-switch.is-disabled { cursor: not-allowed; opacity: 0.6; }
  /* Visually-hidden but focusable: the ring is drawn on the track instead. */
  .mcp-switch-input {
    position: absolute;
    width: 1px;
    height: 1px;
    margin: -1px;
    padding: 0;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    border: 0;
  }
  .mcp-switch-track {
    position: relative;
    flex: 0 0 auto;
    width: 42px;
    height: 24px;
    border-radius: 999px;
    /* Off state: solid muted fill so the control clears 3:1 against the card. */
    background: var(--muted);
    transition: background-color 0.15s ease;
  }
  .mcp-switch-thumb {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 20px;
    height: 20px;
    border-radius: 50%;
    background: #fff;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.35);
    transition: transform 0.15s ease;
  }
  .mcp-switch-input:checked + .mcp-switch-track { background: var(--accent); }
  .mcp-switch-input:checked + .mcp-switch-track .mcp-switch-thumb {
    transform: translateX(18px);
  }
  .mcp-switch-input:focus-visible + .mcp-switch-track { box-shadow: var(--focus); }
  .mcp-switch-input:disabled + .mcp-switch-track { opacity: 0.5; }
  .mcp-switch-label {
    font-size: var(--fs-sm);
    font-weight: var(--fw-semibold);
    color: var(--text);
    line-height: var(--lh-tight);
    white-space: nowrap;
  }
  .mcp-switch.is-disabled .mcp-switch-label { color: var(--muted); }
  @media (prefers-reduced-motion: reduce) {
    .mcp-switch-track,
    .mcp-switch-thumb { transition: none; }
  }
  /* Context Engine intro (feedback #34): fills the card's former dead zone
     between the Models row and the anchored footer. A hairline (same separator
     language as the footer's) sits right under Models/Manage, then a
     micro-heading + short muted marketing blurb. Small type + roomy leading so
     it reads as supporting copy, not a field. */
  .ce-intro {
    display: flex;
    flex-direction: column;
    gap: 6px;
    border-top: 1px solid var(--border);
    padding-top: 12px;
  }
  .ce-intro-text {
    margin: 0;
    font-size: 0.8rem;
    line-height: 1.5;
    color: var(--muted);
  }

  .field-notice { margin: 0; font-size: 0.78rem; color: var(--warn); }
  .field-notice code {
    font-size: 0.74rem;
    padding: 0.05rem 0.25rem;
    border-radius: 4px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    word-break: break-all;
  }

  /* Compact Models row: a muted summary of count + default on the left and a
     quiet-accent Manage action on the right — plain text + button, no inset. */
  .models-summary {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--s-2);
    padding: 2px 0;
    background: transparent;
    border: none;
  }
  .models-summary-text {
    flex: 1 1 auto;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: var(--fs-sm);
    line-height: var(--lh);
    color: var(--muted);
  }
  .models-summary-text.is-empty { color: var(--muted); }
  .models-summary .btn-ghost {
    flex: 0 0 auto;
    background: transparent;
    border-color: transparent;
    color: var(--accent-soft-text);
  }
  .models-summary .btn-ghost:hover:not(:disabled) {
    background: var(--accent-soft);
    border-color: transparent;
  }

  /* Models management popup — mirrors the Add-provider dialog: a blurred
     backdrop centering a viewport-capped dialog whose body scrolls so the app
     shell never gains a scrollbar. */
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 55;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--s-2);
    background: rgba(0, 0, 0, 0.4);
    -webkit-backdrop-filter: blur(4px);
    backdrop-filter: blur(4px);
    --wails-draggable: no-drag;
  }
  .dialog {
    width: 100%;
    max-width: 32rem;
    max-height: min(90dvh, 44rem);
    display: flex;
    flex-direction: column;
    padding: var(--s-3);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  .title {
    flex: 0 0 auto;
    margin: 0 0 var(--s-2);
    font-size: var(--fs-title);
    font-weight: var(--fw-bold);
    color: var(--text);
  }
  .add-body {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: var(--s-2);
    padding-right: 0.25rem;
  }
  .actions {
    flex: 0 0 auto;
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: var(--s-2);
  }

  /* Add row: Model ID + optional Display name inputs side by side, each with a
     small sub-label. All three controls pin one explicit height so the Add
     button's box exactly equals the input boxes (same px, same bottom edge);
     both already share the 8px --radius-sm corner language. */
  .model-add { display: flex; gap: var(--s-1); align-items: flex-end; }
  .model-add .field-input,
  .model-add .btn-primary { height: 36px; }
  .model-add-field {
    flex: 1 1 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .model-add-label {
    font-size: 0.72rem;
    font-weight: var(--fw-medium);
    color: var(--muted);
    line-height: var(--lh-tight);
  }
  .model-add .field-input { width: 100%; }
  .model-add .btn-primary { flex: 0 0 auto; }

  /* Segmented default picker: models render as connected segments inside a
     grouped container that wraps to a new row in the narrow profile column.
     The selected segment is accent-filled; the rest are neutral and clickable
     to become the default. Each segment carries its own × whose click is
     isolated (preventDefault + stopPropagation) so removing never re-selects. */
  .seg-group {
    display: flex;
    flex-wrap: wrap;
    gap: 3px;
    margin: 0.1rem 0 0;
    padding: 3px;
    width: fit-content;
    max-width: 100%;
    align-self: flex-start;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 10px;
  }
  .seg {
    display: inline-flex;
    align-items: stretch;
    min-width: 0;
    max-width: 100%;
    min-height: 36px; /* matches the pinned add-row control height so chips read level */
    border: 1px solid transparent; /* reserves ring space; shown only on unselected hover */
    border-radius: 7px;
    background: transparent;
    transition: background-color 0.15s ease, border-color 0.15s ease;
  }
  .seg.selected { background: var(--accent); }
  /* Subtle affordance on choosable segments only; never touches the accent-filled selected one. */
  .seg:not(.selected):hover { border-color: var(--muted); }
  .seg-select {
    display: inline-flex;
    flex-direction: column;
    align-items: flex-start;
    justify-content: center;
    gap: 1px;
    min-width: 0;
    padding: 0.32rem 0.4rem 0.32rem 0.6rem;
    font-size: var(--fs-sm);
    font-weight: var(--fw-semibold);
    color: var(--text);
    background: transparent;
    border: none;
    border-radius: 7px 0 0 7px;
    cursor: pointer;
    transition: color 0.15s ease;
  }
  .seg.selected .seg-select { color: var(--accent-text); }
  .seg-name { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* Muted canonical model ID shown under the display name (only when an alias
     exists); inherits the accent text color on the selected segment. */
  .seg-id {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.68rem;
    font-weight: var(--fw-medium);
    line-height: 1.15;
    color: var(--muted);
  }
  .seg.selected .seg-id { color: var(--accent-text); opacity: 0.75; }
  .seg-remove {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0 0.6rem 0 0.4rem;
    border: none;
    border-radius: 0 7px 7px 0;
    background: transparent;
    color: var(--muted);
    font-size: 1.05rem;
    line-height: 1;
    cursor: pointer;
    transition: color 0.15s ease, opacity 0.15s ease;
  }
  .seg-remove:hover { color: var(--danger-strong); }
  .seg.selected .seg-remove { color: var(--accent-text); }
  .seg.selected .seg-remove:hover { color: var(--accent-text); opacity: 0.75; }
</style>
