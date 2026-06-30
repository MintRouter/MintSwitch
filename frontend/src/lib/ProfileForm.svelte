<script lang="ts">
  import type { ProfileView } from "../../bindings/mintswitch/internal/service";
  import type { Profile } from "../../bindings/mintswitch/internal/core";
  import { isHttpUrl, normalizeBaseUrl } from "./ui";

  interface Props {
    profile: ProfileView;
    saving: boolean;
    onSave: (p: Profile) => Promise<boolean>;
  }
  let { profile, saving, onSave }: Props = $props();

  let label = $state("");
  let baseUrl = $state("");
  let apiKey = $state("");
  let models = $state<string[]>([]);
  let model = $state("");
  let newModel = $state("");
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
    model = profile.model ?? "";
    apiKey = "";
  });

  // Add the typed model to the list (trimmed, deduped). The first model added
  // becomes the default automatically so a valid default is always present.
  function addModel(): void {
    const m = newModel.trim();
    if (!m) return;
    if (!models.includes(m)) {
      models = [...models, m];
      if (!model) model = m;
    }
    newModel = "";
  }

  function onModelKeydown(e: KeyboardEvent): void {
    if (e.key === "Enter") {
      e.preventDefault();
      addModel();
    }
  }

  // Remove a model; if it was the default, fall back to the first remaining
  // model (or clear the default when none are left) so it's never orphaned.
  function removeModel(m: string): void {
    models = models.filter((x) => x !== m);
    if (model === m) {
      model = models[0] ?? "";
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
    const payload: Profile = {
      label: label.trim(),
      api_key: apiKey,
      base_url: normalizedBase.url,
      models: models,
      model: model,
      // The Small / fast model field was removed from the UI; always send "" so
      // the Profile/binding shape stays unchanged (equivalent to the old "None").
      small_fast_model: "",
    };
    const ok = await onSave(payload);
    if (ok) apiKey = "";
  }

  const keyPlaceholder = $derived(
    profile.has_key ? "•••• key saved — leave blank to keep" : "Enter your API key",
  );

  // Live preview of the backend normalization. When a public http endpoint is
  // upgraded to https we surface a non-blocking notice so the user sees the
  // saved value up front (http endpoints often drop the API key on redirect).
  const normalizedBase = $derived(normalizeBaseUrl(baseUrl));
</script>

<form class="card profile" onsubmit={submit} novalidate aria-labelledby="profile-h">
  <div class="card-head">
    <h2 class="card-title" id="profile-h">Endpoint profile</h2>
    <p class="card-sub">Shared by every tool you apply it to.</p>
  </div>

  <div class="field">
    <label class="field-label" for="pf-label">Label <span class="opt">(optional)</span></label>
    <input class="field-input" id="pf-label" type="text" bind:value={label}
      placeholder="MintRouter" autocomplete="off" />
  </div>

  <div class="field">
    <label class="field-label" for="pf-base">Base URL</label>
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
    <label class="field-label" for="pf-key">API key</label>
    <input class="field-input" id="pf-key" type="password" bind:value={apiKey}
      placeholder={keyPlaceholder} autocomplete="off"
      aria-invalid={!!errors.apiKey}
      aria-describedby={errors.apiKey ? "err-key" : "hint-key"} />
    {#if errors.apiKey}
      <p class="field-error" id="err-key">{errors.apiKey}</p>
    {:else}
      <p class="field-hint" id="hint-key">Stored locally and never shown again after saving.</p>
    {/if}
  </div>

  <div class="field">
    <label class="field-label" for="pf-model-add">Models</label>
    <div class="model-add">
      <input class="field-input" id="pf-model-add" type="text" bind:value={newModel}
        placeholder="gpt-5.5" autocomplete="off" spellcheck="false"
        onkeydown={onModelKeydown}
        aria-invalid={!!errors.models}
        aria-describedby={errors.models ? "err-models" : errors.model ? "err-model" : "hint-model"} />
      <button class="btn-ghost sm" type="button" onclick={addModel} disabled={!newModel.trim()}>Add</button>
    </div>
    {#if models.length}
      <ul class="model-list" aria-label="Models">
        {#each models as m (m)}
          <li class="model-chip" class:selected={m === model}>
            <label class="model-default">
              <input type="radio" name="pf-default-model" value={m}
                checked={m === model} onchange={() => (model = m)} />
              <span class="model-name">{m}</span>
              {#if m === model}<span class="model-tag">default</span>{/if}
            </label>
            <button class="model-remove" type="button" onclick={() => removeModel(m)}
              aria-label={`Remove ${m}`} title={`Remove ${m}`}>×</button>
          </li>
        {/each}
      </ul>
    {/if}
    {#if errors.models}
      <p class="field-error" id="err-models">{errors.models}</p>
    {:else if errors.model}
      <p class="field-error" id="err-model">{errors.model}</p>
    {:else}
      <p class="field-hint" id="hint-model">Add the models you use, then pick one as the default.</p>
    {/if}
  </div>

  <div class="profile-actions">
    <button class="btn-primary" type="submit" disabled={saving}>
      {saving ? "Saving…" : "Save profile"}
    </button>
  </div>
</form>

<style>
  .profile { display: flex; flex-direction: column; gap: var(--s-2); }
  .card-head { margin-bottom: 0.25rem; }
  .opt { color: var(--muted); font-weight: 400; }
  .profile-actions { display: flex; justify-content: flex-end; margin-top: 0.25rem; }
  .field-notice { margin: 0; font-size: 0.78rem; color: var(--warn); }
  .field-notice code {
    font-size: 0.74rem;
    padding: 0.05rem 0.25rem;
    border-radius: 4px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    word-break: break-all;
  }

  .model-add { display: flex; gap: 0.4rem; align-items: stretch; }
  .model-add .field-input { flex: 1 1 auto; }
  .model-add .btn-ghost { flex: 0 0 auto; }
  .model-list {
    margin: 0.1rem 0 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }
  .model-chip {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.3rem 0.35rem 0.3rem 0.5rem;
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    background: var(--surface-2);
  }
  .model-chip.selected { border-color: var(--accent); box-shadow: var(--focus); }
  .model-default {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    flex: 1 1 auto;
    min-width: 0;
    cursor: pointer;
    font-size: 0.86rem;
    color: var(--text);
  }
  .model-default input { flex: 0 0 auto; accent-color: var(--accent); }
  .model-name { overflow-wrap: anywhere; }
  .model-tag {
    flex: 0 0 auto;
    font-size: 0.66rem;
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--accent);
  }
  .model-remove {
    flex: 0 0 auto;
    width: 1.4rem;
    height: 1.4rem;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: none;
    border-radius: 6px;
    background: transparent;
    color: var(--muted);
    font-size: 1.05rem;
    line-height: 1;
    cursor: pointer;
  }
  .model-remove:hover { color: var(--danger); background: var(--surface); }
</style>
