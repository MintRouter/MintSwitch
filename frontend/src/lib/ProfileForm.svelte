<script lang="ts">
  import type { ProfileView } from "../../bindings/mintswitch/internal/service";
  import type { Profile } from "../../bindings/mintswitch/internal/core";
  import { isHttpUrl } from "./ui";

  interface Props {
    profile: ProfileView;
    saving: boolean;
    onSave: (p: Profile) => Promise<boolean>;
  }
  let { profile, saving, onSave }: Props = $props();

  let label = $state("");
  let baseUrl = $state("");
  let apiKey = $state("");
  let model = $state("");
  let smallFastModel = $state("");
  let errors = $state<Record<string, string>>({});

  // Seed the editable fields from the saved (non-secret) profile. Re-runs only
  // when the parent reassigns `profile` (initial load / post-save refresh), so
  // in-progress edits are never clobbered. The API key is never seeded — the
  // backend never sends it — so the field starts blank and an empty submit keeps
  // the stored key.
  $effect(() => {
    label = profile.label ?? "";
    baseUrl = profile.base_url ?? "";
    model = profile.model ?? "";
    smallFastModel = profile.small_fast_model ?? "";
    apiKey = "";
  });

  function validate(): boolean {
    const next: Record<string, string> = {};
    if (!isHttpUrl(baseUrl)) {
      next.baseUrl = "Enter a valid http(s) URL.";
    }
    if (!model.trim()) {
      next.model = "Model is required.";
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
      base_url: baseUrl.trim(),
      model: model.trim(),
      small_fast_model: smallFastModel.trim(),
    };
    const ok = await onSave(payload);
    if (ok) apiKey = "";
  }

  const keyPlaceholder = $derived(
    profile.has_key ? "•••• key saved — leave blank to keep" : "Enter your API key",
  );
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
      aria-describedby={errors.baseUrl ? "err-base" : undefined} />
    {#if errors.baseUrl}<p class="field-error" id="err-base">{errors.baseUrl}</p>{/if}
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
    <label class="field-label" for="pf-model">Model</label>
    <input class="field-input" id="pf-model" type="text" bind:value={model}
      placeholder="gpt-5.5" autocomplete="off" spellcheck="false"
      aria-invalid={!!errors.model}
      aria-describedby={errors.model ? "err-model" : undefined} />
    {#if errors.model}<p class="field-error" id="err-model">{errors.model}</p>{/if}
  </div>

  <div class="field">
    <label class="field-label" for="pf-small">Small / fast model <span class="opt">(optional)</span></label>
    <input class="field-input" id="pf-small" type="text" bind:value={smallFastModel}
      placeholder="gpt-5.5-mini" autocomplete="off" spellcheck="false" />
  </div>

  <div class="profile-actions">
    <button class="btn-primary" type="submit" disabled={saving}>
      {saving ? "Saving…" : "Save profile"}
    </button>
  </div>
</form>

<style>
  .profile { display: flex; flex-direction: column; gap: var(--s-1); }
  .card-head { margin-bottom: 0.25rem; }
  .opt { color: var(--muted); font-weight: 400; }
  .profile-actions { display: flex; justify-content: flex-end; margin-top: 0.25rem; }
</style>
