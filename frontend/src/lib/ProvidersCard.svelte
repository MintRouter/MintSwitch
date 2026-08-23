<script lang="ts">
  import { Service } from "../../bindings/mintswitch/internal/service";
  import type { ProviderView, ToolView } from "../../bindings/mintswitch/internal/service";
  import type { Provider } from "../../bindings/mintswitch/internal/core";
  import { errMsg, isHttpUrl, normalizeBaseUrl } from "./ui";
  import ConfirmDialog from "./ConfirmDialog.svelte";

  interface Props {
    providers: ProviderView[];
    tools: ToolView[];
    saving: boolean;
    onAdd: (p: Provider) => Promise<string | null>;
    onUpdate: (p: Provider) => Promise<string | null>;
    onRemove: (id: string) => Promise<string | null>;
    onSetActive: (id: string) => Promise<string | null>;
    onToolProviderChange: (toolID: string, providerID: string) => Promise<string | null>;
  }
  let {
    providers, tools, saving,
    onAdd, onUpdate, onRemove, onSetActive, onToolProviderChange,
  }: Props = $props();

  const active = $derived(providers.find((p) => p.active) ?? null);
  const installedTools = $derived(tools.filter((t) => t.installed));

  // One-line card summary: count (with correct plural) plus the active
  // provider's name — never any part of a key value.
  const providersSummary = $derived(
    providers.length === 0
      ? "No providers yet"
      : `${providers.length} provider${providers.length > 1 ? "s" : ""}${active ? ` · ${active.name} active` : ""}`,
  );

  // Same one-line summary for a provider's model list (count + default by
  // display name), shown on the card for the active provider.
  function modelsSummary(p: ProviderView): string {
    const models = p.models ?? [];
    if (models.length === 0) return "No models yet";
    const name = (p.model_names ?? {})[p.model] || p.model;
    return `${models.length} model${models.length > 1 ? "s" : ""}${p.model ? ` · default ${name}` : ""}`;
  }

  // ---- Manage dialog (provider list) ----
  let manageOpen = $state(false);
  let manageDialogEl = $state<HTMLDivElement | null>(null);
  let addBtnEl = $state<HTMLButtonElement | null>(null);
  let dialogError = $state("");
  let toolProviderError = $state("");

  // Focus restore: remember what was focused before the Manage dialog opened
  // so closing it puts keyboard users back where they were. (The form dialog
  // needs no equivalent — closing it re-mounts the Manage dialog, whose
  // open-effect below focuses the Add button.) Plain variable, not $state —
  // only read inside handlers/microtasks.
  let manageReturnFocus: HTMLElement | null = null;

  function openManage(): void {
    manageReturnFocus = document.activeElement as HTMLElement | null;
    dialogError = "";
    toolProviderError = "";
    manageOpen = true;
  }

  function closeManage(): void {
    manageOpen = false;
    closeForm();
    const target = manageReturnFocus;
    manageReturnFocus = null;
    if (target?.isConnected) queueMicrotask(() => target.focus());
  }

  // Exported for tool cards' "Add provider…" CTA: jumps straight to the Add
  // form (skipping the Manage list view). openManage() still captures the
  // caller's focused element, so closing the dialog restores focus to it.
  export function openAddProvider(): void {
    openManage();
    openForm(null);
  }

  // Removing a provider permanently deletes its stored key, so it always goes
  // through an explicit confirmation dialog first. On failure the dialog
  // STAYS OPEN with the error inline — closing it would read as "removed".
  let removeTarget = $state<ProviderView | null>(null);
  let removeBusy = $state(false);
  let removeError = $state("");

  async function confirmRemove(): Promise<void> {
    if (!removeTarget || removeBusy) return;
    removeBusy = true;
    removeError = "";
    const err = await onRemove(removeTarget.id);
    removeBusy = false;
    if (err != null) {
      removeError = err;
      return;
    }
    removeTarget = null;
    // ConfirmDialog restores focus to the row's "×" button, but on success
    // that row is gone — fall back to the Add button so keyboard focus (and
    // the manage dialog's Tab trap) never lands on <body>. setTimeout runs
    // after the dialog's DOM removal and its own focus-restore attempt.
    setTimeout(() => {
      if (manageOpen && document.activeElement === document.body) addBtnEl?.focus();
    }, 0);
  }

  async function setActive(id: string): Promise<void> {
    if (active?.id === id) return;
    dialogError = "";
    const err = await onSetActive(id);
    if (err != null) dialogError = err;
  }

  // Per-tool provider override: persisted immediately via the parent's
  // SetToolProvider call (an empty selection clears the override so the tool
  // follows the active provider). Failures surface inline in the dialog.
  // The <select> is snapped back to the state value before the async call:
  // if the backend rejects the change, the refresh leaves the state
  // unchanged and Svelte would otherwise skip re-applying `value=`, leaving
  // the DOM stuck on the rejected option. On success the refresh re-renders
  // the select with the new value.
  async function changeToolProvider(t: ToolView, e: Event & { currentTarget: HTMLSelectElement }): Promise<void> {
    const chosen = e.currentTarget.value;
    e.currentTarget.value = t.provider_overridden ? t.selected_provider_id : "";
    toolProviderError = "";
    const err = await onToolProviderChange(t.id, chosen);
    if (err != null) toolProviderError = err;
  }

  // ---- Unified Add/Edit dialog ----
  // One form serves both flows: formId "" means Add, otherwise it edits that
  // provider. It temporarily replaces the Manage dialog and Esc / backdrop /
  // Cancel all return to it. The key input stays empty on Edit for "keep the
  // stored key"; typing a value replaces it. Nothing persists until Save.
  let formOpen = $state(false);
  let formId = $state("");
  let formDialogEl = $state<HTMLDivElement | null>(null);
  let formNameEl = $state<HTMLInputElement | null>(null);
  let formKeyEl = $state<HTMLInputElement | null>(null);
  let formName = $state("");
  let formNote = $state("");
  let formBaseUrl = $state("");
  let formKey = $state("");
  let fModels = $state<string[]>([]);
  let fModelNames = $state<Record<string, string>>({});
  let fModel = $state("");
  let modelInput = $state("");
  let modelInputEl = $state<HTMLInputElement | null>(null);
  let formError = $state("");

  const formBase = $derived(normalizeBaseUrl(formBaseUrl));
  const isEdit = $derived(!!formId);
  const editing = $derived(providers.find((p) => p.id === formId) ?? null);

  // Models can be fetched once there is a usable endpoint + key. On Edit the
  // stored key counts (the backend uses it when the key input is blank, so
  // the key value never round-trips to the frontend) — but ONLY while the
  // endpoint still matches the stored one: once the URL is edited, the
  // stored key must not travel to the new host, so a typed key is required.
  const storedKeyUsable = $derived(
    isEdit && !!editing?.has_key &&
    formBase.url === normalizeBaseUrl(editing?.base_url ?? "").url,
  );
  const canFetch = $derived(
    isHttpUrl(formBase.url) && (!!formKey.trim() || storedKeyUsable),
  );

  // The backend requires name + key + base URL + a default model
  // (core.Provider.Validate); on Edit a blank key keeps the stored one.
  const canSave = $derived(
    !!formName.trim() && isHttpUrl(formBase.url) &&
    (isEdit || !!formKey.trim()) &&
    fModels.length > 0 && fModels.includes(fModel),
  );

  // Dirty tracking for the form: a snapshot of the NON-SECRET fields taken on
  // open (the key never enters the snapshot — any typed key marks the form
  // dirty on its own). Used to guard Esc/Cancel against losing typed input.
  let formInitial = $state("");
  const formSnapshot = () =>
    JSON.stringify([formName, formNote, formBaseUrl, fModels, fModelNames, fModel]);
  const formDirty = $derived(formOpen && (!!formKey || formSnapshot() !== formInitial));
  // Confirmation shown when Esc/Cancel would discard a dirty form.
  let discardOpen = $state(false);

  // ---- Preset chips (Add only) ----
  // Logo chips for common OpenAI-compatible endpoints: picking one prefills
  // name + base URL and moves focus to the API key. A preset only overwrites
  // a field that is blank or still holds exactly what a chip prefilled earlier
  // in this form session — the same rule for every chip (including Custom,
  // whose prefill is blank) so hand-typed input is never thrown away, even
  // when it happens to match a preset string. Never shown on Edit.
  const presets = [
    { id: "mintrouter", label: "MintRouter.AI", name: "MintRouter.AI", baseUrl: "https://api.mintrouter.ai/v1" },
    { id: "openrouter", label: "OpenRouter", name: "OpenRouter", baseUrl: "https://openrouter.ai/api/v1" },
    { id: "openai", label: "OpenAI", name: "OpenAI", baseUrl: "https://api.openai.com/v1" },
    { id: "custom", label: "Custom", name: "", baseUrl: "" },
  ];
  let selectedPreset = $state("");
  // What the last chip actually wrote into each field (reset when the form
  // opens). Plain variables, not $state — only touched inside handlers.
  let prefilledName = "";
  let prefilledUrl = "";

  // Pressing a chip while the key (or URL) field is focused fires that
  // field's blur BEFORE the click applies the new URL — unguarded, the blur
  // auto-fetch would send the typed key to the pre-click URL. pointerdown
  // drops any pending debounce and raises this flag so onAutoFetchBlur skips
  // the stale fetch; the flag clears on pointerup/pointercancel (click, when
  // it completes, fires after pointerup), and applyPreset re-arms the
  // auto-fetch against the URL the chip actually applied.
  let presetPointerDown = false;

  function onPresetPointerdown(): void {
    cancelAutoFetch();
    presetPointerDown = true;
    const reset = () => {
      presetPointerDown = false;
      window.removeEventListener("pointerup", reset, true);
      window.removeEventListener("pointercancel", reset, true);
    };
    window.addEventListener("pointerup", reset, true);
    window.addEventListener("pointercancel", reset, true);
  }

  function onAutoFetchBlur(): void {
    if (presetPointerDown) return;
    tryAutoFetch();
  }

  function applyPreset(id: string): void {
    const p = presets.find((q) => q.id === id);
    if (!p) return;
    selectedPreset = id;
    if (!formName.trim() || formName === prefilledName) {
      formName = p.name;
      prefilledName = p.name;
    }
    if (!formBaseUrl.trim() || formBaseUrl === prefilledUrl) {
      formBaseUrl = p.baseUrl;
      prefilledUrl = p.baseUrl;
    }
    if (id === "custom") formNameEl?.focus();
    else formKeyEl?.focus();
    // One-shot auto-fetch against the URL now in the form (no-op while the
    // URL is invalid, the key is blank, or the pair already ran).
    tryAutoFetch();
  }

  function openForm(p: ProviderView | null): void {
    formId = p?.id ?? "";
    formName = p?.name ?? "";
    formNote = p?.note ?? "";
    formBaseUrl = p?.base_url ?? "";
    formKey = "";
    fModels = [...(p?.models ?? [])];
    const seeded: Record<string, string> = {};
    for (const [id, name] of Object.entries(p?.model_names ?? {})) {
      if (name) seeded[id] = name;
    }
    fModelNames = seeded;
    fModel = p?.model ?? "";
    modelInput = "";
    formError = "";
    fetching = false;
    fetchAttempted = false;
    fetchError = "";
    fetchedModels = [];
    fetchedNames = {};
    dropdownOpen = false;
    activeIndex = -1;
    discardOpen = false;
    selectedPreset = "";
    prefilledName = "";
    prefilledUrl = "";
    autoFetchDone = new Set();
    cancelAutoFetch();
    formInitial = formSnapshot();
    formOpen = true;
    // Edit opens with a stored endpoint + key: fetching here sends a blank
    // key (the backend substitutes the stored one) to the stored URL only,
    // so the model list can appear with zero extra clicks without any risk
    // of leaking a key to a half-typed endpoint.
    if (p) void fetchModels();
  }

  function closeForm(): void {
    formOpen = false;
    formId = "";
    formKey = "";
    discardOpen = false;
    cancelAutoFetch();
    autoFetchDone = new Set();
    fetchSeq++;
    fetching = false;
    closeDropdown();
  }

  // Esc / Cancel on the form: close immediately when pristine, otherwise ask
  // before throwing typed input away (backdrop clicks never close the form).
  function requestCloseForm(): void {
    if (formDirty) discardOpen = true;
    else closeForm();
  }

  // ---- Fetching the endpoint's advertised models ----
  // Runs on an explicit user action — the "Fetch models" button, or opening
  // Edit (stored endpoint + stored key) — or on the one-shot auto-fetch below,
  // via the read-only FetchEndpointModels binding: a typed key travels only
  // for that one request and is never stored or echoed back. Failures degrade
  // to a quiet notice — manual entry and Save keep working regardless.
  let fetching = $state(false);
  let fetchAttempted = $state(false);
  let fetchError = $state("");
  let fetchedModels = $state<string[]>([]);
  // Display names the endpoint advertised alongside the fetched IDs. They are
  // copied into fModelNames when a model is ADDED, and a successful fetch also
  // seeds them for models already in the form (never overwriting a user-set
  // name). When that seeding lands on a pristine form — the auto-fetch on
  // Edit — the initial snapshot is retaken so an auto-fetch never dirties the
  // form by itself; Save still persists the seeded names.
  let fetchedNames = $state<Record<string, string>>({});
  // Monotonic token so a stale (slow) response can never clobber the state of
  // a newer fetch or a reopened form.
  let fetchSeq = 0;

  async function fetchModels(): Promise<void> {
    if (!canFetch || fetching) return;
    const seq = ++fetchSeq;
    // Any fetch with a typed key marks its URL+key pair as attempted, so the
    // one-shot auto-fetch never repeats a pair that already ran (auto or
    // manual) — editing either field arms it again.
    if (formKey.trim()) autoFetchDone.add(`${formBase.url}\u0000${formKey}`);
    fetching = true;
    fetchError = "";
    try {
      const options = (await Service.FetchEndpointModels(formBase.url, formKey, formId)) ?? [];
      if (seq !== fetchSeq) return;
      fetchedModels = options.map((o) => o.id);
      const names: Record<string, string> = {};
      for (const o of options) {
        if (o.display_name) names[o.id] = o.display_name;
      }
      fetchedNames = names;
      // Seed advertised names for models already selected (chips added before
      // this fetch, or stored on the provider being edited). A user-set name
      // always wins. If the form was pristine, re-snapshot so the seeding
      // alone does not trigger the discard-confirmation flow.
      const pristine = formSnapshot() === formInitial;
      const merged = { ...fModelNames };
      let seededAny = false;
      for (const m of fModels) {
        const n = names[m];
        if (n && !merged[m]) {
          merged[m] = n;
          seededAny = true;
        }
      }
      if (seededAny) {
        fModelNames = merged;
        if (pristine) formInitial = formSnapshot();
      }
      fetchAttempted = true;
      fetching = false;
      // Fresh suggestions open the dropdown so the checkbox list is visible
      // without an extra click.
      if (options.length) dropdownOpen = true;
    } catch (e) {
      if (seq !== fetchSeq) return;
      fetchError = errMsg(e);
      fetchAttempted = true;
      fetching = false;
    }
  }

  // ---- Auto-fetch on a typed key ----
  // Once the endpoint URL is valid and a key has been TYPED (never the stored
  // key — that path stays behind storedKeyUsable and explicit actions), models
  // fetch on key blur or ~600ms after typing pauses. Each URL+key pair
  // auto-fetches at most once (autoFetchDone) so a failing endpoint never
  // loops; the Fetch button remains the manual retry. Plain variables, not
  // $state — only touched inside handlers.
  let autoFetchDone = new Set<string>();
  let autoFetchTimer: ReturnType<typeof setTimeout> | null = null;

  function cancelAutoFetch(): void {
    if (autoFetchTimer != null) {
      clearTimeout(autoFetchTimer);
      autoFetchTimer = null;
    }
  }

  function tryAutoFetch(): void {
    cancelAutoFetch();
    if (!formOpen || fetching) return;
    if (!formKey.trim() || !isHttpUrl(formBase.url)) return;
    if (autoFetchDone.has(`${formBase.url}\u0000${formKey}`)) return;
    void fetchModels();
  }

  function scheduleAutoFetch(): void {
    cancelAutoFetch();
    autoFetchTimer = setTimeout(tryAutoFetch, 600);
  }

  // ---- Models combobox (chips-in-field + checkbox dropdown) ----
  // The dropdown lists every fetched model with a checkbox that mirrors
  // selection; the inline input live-filters the list and Enter adds a manual
  // ID when nothing matches. Esc / outside click close it.
  let dropdownOpen = $state(false);
  let activeIndex = $state(-1);
  let comboEl = $state<HTMLDivElement | null>(null);

  const modelQuery = $derived(modelInput.trim().toLowerCase());
  const filteredModels = $derived(
    modelQuery ? fetchedModels.filter((m) => m.toLowerCase().includes(modelQuery)) : fetchedModels,
  );
  // "Select all" mirrors the visible rows, so with a filter active it applies
  // to the matches only.
  const allFilteredSelected = $derived(
    filteredModels.length > 0 && filteredModels.every((m) => fModels.includes(m)),
  );

  function openDropdown(): void {
    dropdownOpen = true;
  }

  function closeDropdown(): void {
    dropdownOpen = false;
    activeIndex = -1;
  }

  function toggleModel(m: string): void {
    if (fModels.includes(m)) removeModel(m);
    else addModelId(m);
    modelInputEl?.focus();
  }

  // The "Select all" row: ticks every visible model, or unticks them all when
  // they are already all selected.
  function toggleAllFiltered(): void {
    if (allFilteredSelected) {
      for (const m of filteredModels) removeModel(m);
    } else {
      for (const m of filteredModels) addModelId(m);
    }
    modelInputEl?.focus();
  }

  // Clicking bare space inside the field behaves like clicking the input.
  function onFieldPointerdown(e: PointerEvent): void {
    if (e.target !== e.currentTarget) return;
    e.preventDefault();
    modelInputEl?.focus();
    openDropdown();
  }

  function onModelInput(e: Event): void {
    dropdownOpen = true;
    const q = (e.currentTarget as HTMLInputElement).value.trim().toLowerCase();
    activeIndex = q && fetchedModels.some((m) => m.toLowerCase().includes(q)) ? 0 : -1;
  }

  // ArrowUp/Down wrap through the visible rows and keep the row in view.
  function moveActive(delta: number): void {
    dropdownOpen = true;
    const n = filteredModels.length;
    if (n === 0) return;
    activeIndex = activeIndex < 0 ? (delta > 0 ? 0 : n - 1) : (activeIndex + delta + n) % n;
    const rowId = `pv-model-opt-${activeIndex}`;
    requestAnimationFrame(() => document.getElementById(rowId)?.scrollIntoView({ block: "nearest" }));
  }

  // What the UI shows for a model: its optional display name, falling back to
  // the canonical ID.
  function displayName(m: string): string {
    return fModelNames[m] || m;
  }

  // Add one model ID (trimmed, deduped); the first model added becomes the
  // default automatically. Adding also seeds the endpoint's display name into
  // fModelNames (never overwriting a name the user already has) so the chip —
  // and the saved ModelNames — get the friendly label.
  function addModelId(id: string): void {
    const v = id.trim();
    if (!v) return;
    if (!fModels.includes(v)) {
      fModels = [...fModels, v];
      if (!fModel) fModel = v;
      const name = fetchedNames[v];
      if (name && !fModelNames[v]) fModelNames = { ...fModelNames, [v]: name };
    }
  }

  function addTypedModel(): void {
    addModelId(modelInput);
    modelInput = "";
    modelInputEl?.focus();
  }

  function onModelKeydown(e: KeyboardEvent): void {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      moveActive(e.key === "ArrowDown" ? 1 : -1);
      return;
    }
    if (e.key !== "Enter") return;
    e.preventDefault();
    if (dropdownOpen && activeIndex >= 0 && activeIndex < filteredModels.length) {
      toggleModel(filteredModels[activeIndex]);
      modelInput = "";
      activeIndex = -1;
      return;
    }
    if (modelInput.trim()) addTypedModel();
  }

  // Remove a chip (and its display name); if it was the default, fall back to
  // the first remaining model so the default is never orphaned.
  function removeModel(m: string): void {
    fModels = fModels.filter((x) => x !== m);
    if (m in fModelNames) {
      const next = { ...fModelNames };
      delete next[m];
      fModelNames = next;
    }
    if (fModel === m) fModel = fModels[0] ?? "";
  }

  function setDefault(m: string): void {
    fModel = m;
  }

  async function saveForm(): Promise<void> {
    if (!canSave || saving) return;
    formError = "";
    // Tier pins are edited from the Claude Code tool card, not this form —
    // pass the stored values through unchanged so saving here never wipes them
    // (the backend replaces the whole provider on update).
    const payload: Provider = {
      id: formId,
      name: formName.trim(),
      note: formNote.trim(),
      api_key: formKey,
      base_url: formBase.url,
      models: fModels,
      model_names: fModelNames,
      model: fModel,
      small_fast_model: editing?.small_fast_model ?? "",
      opus_model: editing?.opus_model ?? "",
      sonnet_model: editing?.sonnet_model ?? "",
      haiku_model: editing?.haiku_model ?? "",
      fable_model: editing?.fable_model ?? "",
    };
    const err = isEdit ? await onUpdate(payload) : await onAdd(payload);
    if (err != null) {
      formError = err;
      return;
    }
    closeForm();
  }

  // Focus the natural first control whenever a dialog view opens; runs after
  // render so the element refs exist.
  $effect(() => {
    if (manageOpen && !formOpen) {
      queueMicrotask(() => addBtnEl?.focus());
    }
  });

  $effect(() => {
    if (formOpen) {
      queueMicrotask(() => formNameEl?.focus());
    }
  });

  // Close the models dropdown on any pointer press outside the combobox.
  $effect(() => {
    if (!dropdownOpen) return;
    const onPointerDown = (e: PointerEvent) => {
      if (comboEl && !comboEl.contains(e.target as Node)) closeDropdown();
    };
    document.addEventListener("pointerdown", onPointerDown, true);
    return () => document.removeEventListener("pointerdown", onPointerDown, true);
  });

  // Esc closes the open dialog view (the form falls back to the provider
  // list, asking first if it has unsaved input); Tab is trapped inside the
  // visible dialog while open. While a ConfirmDialog (remove / discard) is
  // stacked on top, IT owns Esc and the Tab trap — bail out here so one Esc
  // press can't close both layers.
  function onDialogKeydown(e: KeyboardEvent): void {
    if (!manageOpen || e.defaultPrevented) return;
    if (removeTarget || discardOpen) return;
    const inForm = formOpen;
    const dialogEl = inForm ? formDialogEl : manageDialogEl;
    if (e.key === "Escape") {
      e.preventDefault();
      if (inForm && dropdownOpen) closeDropdown();
      else if (inForm) requestCloseForm();
      else closeManage();
      return;
    }
    if (e.key !== "Tab" || !dialogEl) return;
    const focusables = dialogEl.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled])',
    );
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const activeEl = document.activeElement as HTMLElement | null;
    if (e.shiftKey && activeEl === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && activeEl === last) {
      e.preventDefault();
      first.focus();
    }
  }
</script>

<section class="providers" aria-labelledby="providers-h">
  <div class="providers-head">
    <div>
      <span class="micro-label">Routing profile</span>
      <h2 id="providers-h">Active provider</h2>
    </div>
    <button class="manage-button" type="button" onclick={openManage}>Manage</button>
  </div>

  {#if active}
    <div class="active-provider">
      <div class="provider-avatar" aria-hidden="true">{active.name.trim().charAt(0).toUpperCase()}</div>
      <div class="active-copy">
        <div class="active-title"><strong>{active.name}</strong><span><i aria-hidden="true"></i>Live</span></div>
        {#if active.note}<p>{active.note}</p>{/if}
      </div>
      <svg class="chevron" viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="m9 18 6-6-6-6"/></svg>
    </div>
    <div class="provider-details">
      <div class="detail-row"><span>Endpoint</span><strong title={active.base_url}>{active.base_url.replace(/^https?:\/\//, "")}</strong></div>
      <div class="detail-row"><span>Models</span><strong>{modelsSummary(active)}</strong></div>
      <div class="detail-row"><span>API key</span><strong class="secure"><i aria-hidden="true"></i>{active.has_key ? "Secured" : "Missing"}</strong></div>
    </div>
  {:else}
    <button class="empty-provider" type="button" onclick={openManage}>
      <span class="empty-plus" aria-hidden="true">+</span>
      <span><strong>Add your first provider</strong><small>Connect an OpenAI-compatible endpoint</small></span>
    </button>
  {/if}
  <p class="provider-count">{providersSummary}</p>
</section>

<svelte:window onkeydown={onDialogKeydown} />

{#if manageOpen && !formOpen}
  <div class="backdrop" role="presentation"
    onclick={(e) => e.target === e.currentTarget && closeManage()}>
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="pv-manage-title"
      tabindex="-1" bind:this={manageDialogEl}>
      <h2 class="title" id="pv-manage-title">Providers</h2>
      <div class="add-body">
        <div class="list-head">
          <span class="micro-label">Providers</span>
          <button class="btn-ghost sm" type="button" bind:this={addBtnEl}
            onclick={() => openForm(null)}>Add provider</button>
        </div>

        {#if providers.length}
          <div class="provider-list" role="group" aria-label="Providers">
            {#each providers as p (p.id)}
              <div class="provider-row" class:selected={p.active}>
                <div class="provider-line">
                  <button class="provider-select" type="button" aria-pressed={p.active}
                    disabled={saving} onclick={() => void setActive(p.id)}
                    title={p.active ? `${p.name} is the active provider` : `Set ${p.name} as the active provider`}>
                    <span class="provider-name">
                      {p.name}
                      {#if p.active}<span class="badge tone-success">Active</span>{/if}
                    </span>
                    {#if p.note}
                      <span class="provider-note">{p.note}</span>
                    {/if}
                    <span class="provider-meta">{p.base_url} · {modelsSummary(p)}</span>
                  </button>
                  <div class="provider-actions">
                    <button class="btn-ghost sm" type="button" disabled={saving}
                      onclick={() => openForm(p)}>
                      Edit
                    </button>
                    <button class="provider-remove" type="button" disabled={saving}
                      onclick={() => (removeTarget = p)}
                      aria-label={`Remove ${p.name}`} title={`Remove ${p.name}`}>×</button>
                  </div>
                </div>
              </div>
            {/each}
          </div>
        {:else}
          <p class="field-hint">No providers yet — add your first one above.</p>
        {/if}

        {#if providers.length && installedTools.length}
          <div class="field">
            <span class="micro-label">Per-tool provider</span>
            <p class="field-hint">Each tool follows the active provider unless you pick a specific one.</p>
            <div class="tool-providers">
              {#each installedTools as t (t.id)}
                <div class="tool-provider-row">
                  <label class="tool-provider-name" for={`pv-tool-${t.id}`}>{t.name}</label>
                  <select class="tool-provider-select" id={`pv-tool-${t.id}`}
                    value={t.provider_overridden ? t.selected_provider_id : ""}
                    onchange={(e) => void changeToolProvider(t, e)}>
                    <option value="">Active provider (default)</option>
                    {#each t.providers ?? [] as pr (pr.id)}
                      <option value={pr.id}>{pr.name}</option>
                    {/each}
                  </select>
                </div>
              {/each}
            </div>
          </div>
        {/if}

        {#if dialogError}
          <p class="field-error" role="alert">Couldn't save: {dialogError}</p>
        {/if}
        {#if toolProviderError}
          <p class="field-error" role="alert">Couldn't save: {toolProviderError}</p>
        {/if}
      </div>
      <div class="actions">
        <button class="btn-primary" type="button" onclick={closeManage}>Done</button>
      </div>
    </div>
  </div>
{/if}

{#if manageOpen && formOpen}
  <!-- No backdrop-close for the form: a stray click must never throw away a
       half-typed provider. Esc and Cancel remain (confirming when dirty). -->
  <div class="backdrop" role="presentation">
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="pv-form-title"
      tabindex="-1" bind:this={formDialogEl}>
      <h2 class="title" id="pv-form-title">{isEdit ? `Edit ${editing?.name ?? "provider"}` : "Add a provider"}</h2>
      <div class="add-body">
        {#if !isEdit}
          <!-- Preset logo chips: one press prefills name + endpoint and jumps
               to the API key. The base URL lives in the tooltip, not in the
               layout. -->
          <div class="preset-row" role="group" aria-label="Provider presets">
            {#each presets as p (p.id)}
              <button class="preset-chip" type="button" aria-pressed={selectedPreset === p.id}
                class:selected={selectedPreset === p.id} title={p.baseUrl || undefined}
                onpointerdown={onPresetPointerdown} onclick={() => applyPreset(p.id)}>
                {#if p.id === "mintrouter"}
                  <svg viewBox="4 4 40 40" aria-hidden="true">
                    <g fill="none" stroke-width="5" stroke-linecap="round">
                      <path stroke="#0d9488" d="M20 24C27 24 27 36 34 36H38"/>
                      <path stroke="#135bec" d="M8.5 24H20C27 24 27 12 34 12H38"/>
                    </g>
                    <circle fill="#135bec" cx="8.5" cy="24" r="4.5"/>
                    <circle fill="#135bec" cx="38" cy="12" r="4.5"/>
                    <circle fill="#0d9488" cx="38" cy="36" r="4.5"/>
                  </svg>
                {:else if p.id === "openrouter"}
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"
                    stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M2 12h4c4 0 5-5.5 9-5.5h3"/>
                    <path d="m15.5 3.5 4.5 3-4.5 3"/>
                    <path d="M2 12h4c4 0 5 5.5 9 5.5h3"/>
                    <path d="m15.5 14.5 4.5 3-4.5 3"/>
                  </svg>
                {:else if p.id === "openai"}
                  <svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="3.2" aria-hidden="true">
                    <g transform="rotate(0 24 24)"><ellipse cx="24" cy="16.5" rx="4.6" ry="8.2"/></g>
                    <g transform="rotate(60 24 24)"><ellipse cx="24" cy="16.5" rx="4.6" ry="8.2"/></g>
                    <g transform="rotate(120 24 24)"><ellipse cx="24" cy="16.5" rx="4.6" ry="8.2"/></g>
                    <g transform="rotate(180 24 24)"><ellipse cx="24" cy="16.5" rx="4.6" ry="8.2"/></g>
                    <g transform="rotate(240 24 24)"><ellipse cx="24" cy="16.5" rx="4.6" ry="8.2"/></g>
                    <g transform="rotate(300 24 24)"><ellipse cx="24" cy="16.5" rx="4.6" ry="8.2"/></g>
                  </svg>
                {:else}
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
                    stroke-linecap="round" aria-hidden="true">
                    <path d="M4 8h8M18 8h2M4 16h2M12 16h8"/>
                    <circle cx="15" cy="8" r="2.6"/>
                    <circle cx="9" cy="16" r="2.6"/>
                  </svg>
                {/if}
                {p.label}
              </button>
            {/each}
          </div>
        {/if}
        <div class="add-field">
          <label class="add-label" for="pv-form-name">Provider name</label>
          <input class="field-input" id="pv-form-name" type="text" bind:value={formName}
            bind:this={formNameEl}
            placeholder="MintRouter.AI" autocomplete="off" spellcheck="false" />
        </div>
        <div class="add-field">
          <label class="add-label" for="pv-form-note">Note <span class="opt">Optional</span></label>
          <textarea class="field-input note-input" id="pv-form-note" bind:value={formNote}
            placeholder="e.g. team account, EU endpoint" rows="2" autocomplete="off"></textarea>
        </div>
        <div class="add-field">
          <label class="add-label" for="pv-form-base">API endpoint</label>
          <input class="field-input" id="pv-form-base" type="url" bind:value={formBaseUrl}
            placeholder="https://api.mintrouter.ai/v1" autocomplete="off" spellcheck="false"
            onblur={onAutoFetchBlur} />
        </div>
        {#if formBase.upgraded}
          <p class="field-notice">
            Will be saved as <code>{formBase.url}</code> — http endpoints can drop the API key on redirect.
          </p>
        {/if}
        <div class="add-field">
          <label class="add-label" for="pv-form-key">API key</label>
          <!-- Typing (debounced) or leaving the field triggers the one-shot
               auto-fetch; the URL field only triggers on blur so a typed key
               never travels to a half-typed endpoint. Blur caused by pressing
               a preset chip is skipped (onAutoFetchBlur) — applyPreset re-runs
               the auto-fetch after the chip's URL is in place. -->
          <input class="field-input" id="pv-form-key" type="password" bind:value={formKey}
            bind:this={formKeyEl}
            placeholder={isEdit ? "Unchanged unless typed" : "Enter the API key"}
            autocomplete="off" oninput={scheduleAutoFetch} onblur={onAutoFetchBlur} />
        </div>

        <div class="add-field">
          <div class="models-head">
            <label class="add-label" for="pv-form-model-input">Models</label>
            <!-- The button stays mounted while fetching (disabled, relabelled)
                 so keyboard focus is never dropped mid-fetch. -->
            <button class="btn-ghost sm" type="button" onclick={() => void fetchModels()}
              disabled={!canFetch || fetching} aria-live="polite"
              title={canFetch
                ? "List the models the endpoint's /models route advertises"
                : isEdit && !!editing?.has_key && isHttpUrl(formBase.url)
                  ? "Endpoint changed — enter the API key for the new endpoint first"
                  : "Enter the API endpoint and key first"}>
              {fetching ? "Fetching models…" : fetchAttempted || fetchError ? "Refetch models" : "Fetch models"}
            </button>
          </div>
          <div class="combo" bind:this={comboEl}>
            <div class="combo-field" role="presentation" onpointerdown={onFieldPointerdown}>
              {#each fModels as m (m)}
                <span class="chip" class:default={m === fModel}>
                  <button class="chip-label" type="button" aria-pressed={m === fModel}
                    onclick={() => setDefault(m)}
                    title={m === fModel ? `${displayName(m)} is the default model` : `Set ${displayName(m)} as default`}>
                    {displayName(m)}
                    {#if m === fModel}<span class="chip-default-tag">default</span>{/if}
                  </button>
                  <button class="chip-remove" type="button" onclick={() => removeModel(m)}
                    aria-label={`Remove ${displayName(m)}`} title={`Remove ${displayName(m)}`}>×</button>
                </span>
              {/each}
              <input class="combo-input" id="pv-form-model-input" type="text" bind:value={modelInput}
                bind:this={modelInputEl}
                placeholder={fModels.length ? "Search or add models" : "Search models or type an ID"}
                autocomplete="off" spellcheck="false"
                role="combobox" aria-expanded={dropdownOpen} aria-controls="pv-model-listbox"
                aria-autocomplete="list"
                aria-activedescendant={dropdownOpen && activeIndex >= 0 && activeIndex < filteredModels.length
                  ? `pv-model-opt-${activeIndex}` : undefined}
                onfocus={openDropdown} oninput={onModelInput} onkeydown={onModelKeydown} />
              <svg class="combo-search" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <circle cx="7" cy="7" r="4.5" stroke="currentColor" stroke-width="1.5" />
                <path d="m10.7 10.7 2.8 2.8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
              </svg>
            </div>
            {#if dropdownOpen}
              <div class="combo-pop" id="pv-model-listbox" role="listbox" aria-label="Available models"
                aria-multiselectable="true">
                {#if fetching}
                  <div class="combo-note" role="status">
                    <span class="combo-spinner" aria-hidden="true"></span>Fetching models…
                  </div>
                {:else if filteredModels.length === 0}
                  <div class="combo-note">
                    {modelQuery
                      ? `No matches — press Enter to add “${modelInput.trim()}”`
                      : "No models from endpoint — type to add manually"}
                  </div>
                {:else}
                  <button class="combo-option combo-all" type="button" role="option"
                    id="pv-model-opt-all" tabindex="-1" aria-selected={allFilteredSelected}
                    class:checked={allFilteredSelected}
                    onmousedown={(e) => e.preventDefault()} onclick={toggleAllFiltered}
                    title={allFilteredSelected ? "Unselect all listed models" : "Select all listed models"}>
                    <span class="combo-check" aria-hidden="true">
                      {#if allFilteredSelected}
                        <svg viewBox="0 0 10 8" fill="none">
                          <path d="M1 4.2 3.8 7 9 1" stroke="currentColor" stroke-width="1.8"
                            stroke-linecap="round" stroke-linejoin="round" />
                        </svg>
                      {/if}
                    </span>
                    <span class="combo-id">
                      {modelQuery
                        ? `Select all matches (${filteredModels.length})`
                        : `Select all (${filteredModels.length})`}
                    </span>
                  </button>
                  {#each filteredModels as m, i (m)}
                    <button class="combo-option" type="button" role="option" id={`pv-model-opt-${i}`}
                      tabindex="-1" aria-selected={fModels.includes(m)}
                      class:checked={fModels.includes(m)} class:active={i === activeIndex}
                      onmousedown={(e) => e.preventDefault()} onclick={() => toggleModel(m)}
                      title={fModels.includes(m) ? `Remove ${m}` : `Add ${m}`}>
                      <span class="combo-check" aria-hidden="true">
                        {#if fModels.includes(m)}
                          <svg viewBox="0 0 10 8" fill="none">
                            <path d="M1 4.2 3.8 7 9 1" stroke="currentColor" stroke-width="1.8"
                              stroke-linecap="round" stroke-linejoin="round" />
                          </svg>
                        {/if}
                      </span>
                      <span class="combo-id">{m}</span>
                      {#if fetchedNames[m]}
                        <span class="combo-name">{fetchedNames[m]}</span>
                      {/if}
                    </button>
                  {/each}
                {/if}
              </div>
            {/if}
          </div>
          {#if fetchError}
            <p class="models-error" role="status">
              <strong>Model list unavailable.</strong>
              <span>{fetchError} You can add model IDs manually.</span>
            </p>
          {/if}
          {#if fModels.length && !fModels.includes(fModel)}
            <p class="field-hint">Pick a default model by clicking one of the chips above.</p>
          {:else if !fModels.length}
            <p class="field-hint">At least one model is required; the first one added becomes the default.</p>
          {/if}
        </div>

        {#if formError}
          <p class="field-error" role="alert">Couldn't save: {formError}</p>
        {/if}
      </div>
      <div class="actions">
        <button class="btn-ghost" type="button" onclick={requestCloseForm}>Cancel</button>
        <button class="btn-primary" type="button" onclick={() => void saveForm()}
          disabled={!canSave || saving}>{isEdit ? "Save changes" : "Add provider"}</button>
      </div>
    </div>
  </div>
{/if}

<!-- Stacked confirmations. ConfirmDialog restores focus to the trigger on
     close by itself; while one is open, onDialogKeydown yields Esc/Tab to it. -->
<ConfirmDialog
  open={removeTarget != null}
  title={`Remove ${removeTarget?.name ?? "provider"}?`}
  message={`This permanently deletes “${removeTarget?.name ?? ""}” and its stored API key. This cannot be undone.`}
  confirmLabel={removeError ? "Try again" : "Remove provider"}
  danger
  busy={removeBusy || saving}
  error={removeError ? `Couldn't remove: ${removeError}` : ""}
  onConfirm={() => void confirmRemove()}
  onCancel={() => { removeTarget = null; removeError = ""; }} />

<ConfirmDialog
  open={discardOpen}
  title="Discard unsaved changes?"
  message="This provider form has unsaved changes. Closing it will discard them."
  confirmLabel="Discard changes"
  danger
  onConfirm={closeForm}
  onCancel={() => (discardOpen = false)} />

<style>
  /* Sidebar card. Type scale is deliberately larger than the old 9–11px set:
     12px is the floor for body copy so the panel stays readable at a glance. */
  .providers{display:flex;flex-direction:column;gap:14px;padding:16px;border:1px solid var(--border);border-radius:14px;background:var(--surface);box-shadow:var(--shadow-card)}
  .providers-head{display:flex;align-items:flex-end;justify-content:space-between;gap:8px}
  .providers-head .micro-label{font-size:11px}
  .providers-head h2{margin:5px 0 0;color:var(--text);font-size:15.5px;line-height:1.2;font-weight:720;letter-spacing:-.01em}
  .manage-button{padding:5px 4px;color:var(--accent-soft-text);background:transparent;border:0;cursor:pointer;font-size:12.5px;font-weight:650}.manage-button:hover{color:var(--accent)}
  .active-provider{display:flex;align-items:center;gap:11px;padding:12px;border:1px solid color-mix(in srgb,var(--accent) 18%,var(--border));border-radius:12px;background:linear-gradient(135deg,var(--accent-soft),color-mix(in srgb,var(--accent-soft) 35%,var(--surface)))}
  .provider-avatar{width:38px;height:38px;display:grid;place-items:center;flex:0 0 auto;border-radius:10px;color:var(--accent-text);background:var(--accent);font-size:16px;font-weight:750;box-shadow:0 3px 9px color-mix(in srgb,var(--accent) 22%,transparent)}
  .active-copy{min-width:0;flex:1}.active-title{display:flex;align-items:center;gap:8px}
  .active-title strong{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--text);font-size:13.5px}
  .active-title span{display:inline-flex;align-items:center;gap:4px;color:var(--ok-strong);font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.05em}
  .active-title i{width:6px;height:6px;border-radius:50%;background:var(--ok)}
  .active-copy p{margin:4px 0 0;color:var(--muted);font-size:11.5px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .chevron{flex:0 0 auto;color:var(--muted)}
  .provider-details{display:flex;flex-direction:column;padding:0 2px}
  .detail-row{display:flex;align-items:center;justify-content:space-between;gap:10px;min-height:32px;border-bottom:1px solid var(--border)}.detail-row:last-child{border-bottom:0}
  .detail-row>span{color:var(--muted);font-size:12px}
  .detail-row>strong{min-width:0;max-width:62%;color:var(--text);font-size:12px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .detail-row .secure{display:inline-flex;align-items:center;gap:6px;color:var(--ok-strong)}.secure i{width:6px;height:6px;border-radius:50%;background:var(--ok)}
  .provider-count{margin:-3px 1px 0;color:var(--muted);font-size:11px}
  .empty-provider{display:flex;align-items:center;gap:11px;width:100%;padding:13px;text-align:left;border:1px dashed var(--border-strong);border-radius:12px;background:var(--surface-2);cursor:pointer}.empty-provider:hover{border-color:var(--accent)}
  .empty-plus{width:34px;height:34px;display:grid;place-items:center;border-radius:9px;background:var(--accent-soft);color:var(--accent-soft-text);font-size:20px}
  .empty-provider strong,.empty-provider small{display:block}.empty-provider strong{color:var(--text);font-size:12.5px}.empty-provider small{margin-top:3px;color:var(--muted);font-size:11px}
  /* Stretch to fill the sidebar column so the card reaches down instead of
     leaving a stubby gap; the provider count anchors to the bottom edge. */
  .providers { flex: 1 1 auto; min-height: 0; }
  .provider-details { flex: 1 1 auto; justify-content: flex-start; }
  .detail-row { min-height: 38px; }
  .provider-count { margin-top: auto; padding-top: 8px; }
  .opt {
    margin-left: 0.3rem;
    text-transform: none;
    letter-spacing: 0;
    font-weight: var(--fw-medium);
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

  /* Dialogs — same language as the rest of the app: blurred backdrop centering
     a viewport-capped dialog whose body scrolls so the shell never does. */
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 55;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--s-2);
    background: rgba(10, 13, 20, 0.48);
    -webkit-backdrop-filter: blur(8px);
    backdrop-filter: blur(8px);
    --wails-draggable: no-drag;
  }
  .dialog {
    width: 100%;
    max-width: 34rem;
    max-height: min(90dvh, 44rem);
    display: flex;
    flex-direction: column;
    padding: var(--s-3);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
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

  /* Add/edit form fields: stacked labelled inputs, top to bottom. */
  .add-field {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .add-label {
    font-size: 0.72rem;
    font-weight: var(--fw-medium);
    color: var(--muted);
    line-height: var(--lh-tight);
  }
  .add-field .field-input { width: 100%; }
  .note-input { resize: vertical; min-height: 2.4rem; font-family: inherit; }

  /* Preset logo chips (Add only): one 30px row of pill buttons, logo + label,
     base URL in the tooltip. */
  .preset-row { display: flex; flex-wrap: nowrap; gap: 6px; min-height: 0; overflow-x: auto; }
  .preset-chip {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    height: 30px;
    padding: 0 0.65rem;
    font-size: var(--fs-sm);
    font-weight: var(--fw-medium);
    color: var(--text);
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    cursor: pointer;
    white-space: nowrap;
    transition: border-color 0.15s ease, background-color 0.15s ease, color 0.15s ease;
  }
  .preset-chip svg { flex: 0 0 auto; width: 15px; height: 15px; }
  .preset-chip:hover { border-color: var(--border-strong); }
  .preset-chip:focus-visible { outline: none; border-color: var(--accent); box-shadow: var(--focus); }
  .preset-chip.selected {
    color: var(--accent-soft-text);
    background: var(--accent-soft);
    border-color: var(--accent);
  }

  /* Manage dialog header row: the list label left, quiet-accent Add action
     right — the single entry point into the unified Add/Edit form. */
  .list-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--s-2);
  }
  .list-head .btn-ghost {
    background: transparent;
    border-color: transparent;
    color: var(--accent-soft-text);
  }
  .list-head .btn-ghost:hover:not(:disabled) {
    background: var(--accent-soft);
    border-color: transparent;
  }

  /* Provider list: one bordered row per provider — the main area is a button
     that sets it active (accent ring when active), with quiet Edit / remove
     actions on the right. */
  .provider-list { display: flex; flex-direction: column; gap: 6px; }
  .provider-row {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    transition: border-color 0.15s ease;
  }
  .provider-row.selected { border-color: var(--accent); }
  .provider-row:not(.selected):hover { border-color: var(--muted); }
  .provider-line {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.45rem 0.45rem 0.45rem 0.6rem;
  }
  .provider-select {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    padding: 0;
    background: transparent;
    border: none;
    text-align: left;
    cursor: pointer;
  }
  .provider-name {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    max-width: 100%;
    font-size: var(--fs-sm);
    font-weight: var(--fw-semibold);
    color: var(--text);
    line-height: var(--lh-tight);
    overflow-wrap: anywhere;
  }
  .provider-note {
    max-width: 100%;
    font-size: var(--fs-micro);
    color: var(--muted);
    line-height: 1.35;
    overflow-wrap: anywhere;
  }
  .provider-meta {
    max-width: 100%;
    font-size: var(--fs-micro);
    color: var(--muted);
    line-height: 1.35;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .provider-actions {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    gap: 0.15rem;
  }
  .provider-actions .btn-ghost {
    background: transparent;
    border-color: transparent;
    color: var(--muted);
  }
  .provider-actions .btn-ghost:hover:not(:disabled) {
    color: var(--text);
    background: var(--surface);
    border-color: transparent;
  }
  .provider-remove {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    min-height: 32px;
    padding: 0;
    border: none;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--muted);
    font-size: 1.05rem;
    line-height: 1;
    cursor: pointer;
    transition: color 0.15s ease;
  }
  .provider-remove:hover:not(:disabled) { color: var(--danger-strong); }
  .provider-remove:disabled { opacity: 0.4; cursor: default; }

  /* Models field header: label left, and either a quiet-accent Fetch button or
     a muted "Fetching…" status on the right. */
  .models-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--s-2);
  }
  .models-head .btn-ghost {
    background: transparent;
    border-color: transparent;
    color: var(--accent-soft-text);
  }
  .models-head .btn-ghost:hover:not(:disabled) {
    background: var(--accent-soft);
    border-color: transparent;
  }
  .models-error {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin: 0;
    padding: 8px 10px;
    border: 1px solid color-mix(in srgb, var(--warn) 24%, var(--border));
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--warn) 7%, var(--surface));
    color: var(--muted);
    font-size: var(--fs-micro);
    line-height: var(--lh);
  }
  .models-error strong { color: var(--warn); font-weight: var(--fw-semibold); }

  /* AionUI-style combobox: selected models live as chips inside one bordered
     field next to an inline filter input, with a search glyph at the right
     edge and an attached checkbox dropdown below. */
  .combo { display: flex; flex-direction: column; min-width: 0; }
  .combo-field {
    position: relative;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    min-height: 38px;
    padding: 4px 30px 4px 5px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    cursor: text;
    transition: border-color 0.15s ease, box-shadow 0.15s ease;
  }
  .combo-field:hover { border-color: var(--border-strong); }
  .combo-field:focus-within { border-color: var(--accent); box-shadow: var(--focus); }
  .combo-input {
    flex: 1 1 7rem;
    min-width: 5rem;
    padding: 0.15rem 0.2rem;
    font-size: 16px; /* matches .field-input (guards against iOS focus-zoom) */
    line-height: var(--lh);
    color: var(--text);
    background: transparent;
    border: none;
    outline: none;
  }
  .combo-input::placeholder { color: var(--muted); opacity: 0.8; }
  .combo-search {
    position: absolute;
    top: 50%;
    right: 9px;
    width: 15px;
    height: 15px;
    transform: translateY(-50%);
    color: var(--muted);
    pointer-events: none;
  }

  /* Chips: click the label to make it the default (accent fill + "default"
     tag), click × to remove. */
  .chip {
    display: inline-flex;
    align-items: stretch;
    min-width: 0;
    max-width: 100%;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface-2);
    transition: background-color 0.15s ease, border-color 0.15s ease;
  }
  .chip:not(.default):hover { border-color: var(--muted); }
  .chip.default { background: var(--accent); border-color: var(--accent); }
  .chip-label {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    min-width: 0;
    padding: 0.22rem 0.15rem 0.22rem 0.65rem;
    font-size: var(--fs-sm);
    font-weight: var(--fw-medium);
    color: var(--text);
    background: transparent;
    border: none;
    border-radius: 999px 0 0 999px;
    cursor: pointer;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    transition: color 0.15s ease;
  }
  .chip.default .chip-label { color: var(--accent-text); }
  .chip-default-tag {
    flex: 0 0 auto;
    font-size: 0.62rem;
    font-weight: var(--fw-semibold);
    text-transform: uppercase;
    letter-spacing: 0.03em;
    opacity: 0.8;
  }
  .chip-remove {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0 0.5rem 0 0.3rem;
    border: none;
    border-radius: 0 999px 999px 0;
    background: transparent;
    color: var(--muted);
    font-size: 1rem;
    line-height: 1;
    cursor: pointer;
    transition: color 0.15s ease, opacity 0.15s ease;
  }
  .chip-remove:hover { color: var(--danger-strong); }
  .chip.default .chip-remove { color: var(--accent-text); }
  .chip.default .chip-remove:hover { color: var(--accent-text); opacity: 0.75; }

  /* Attached dropdown: a scroll-capped listbox of the endpoint's advertised
     models — the checkbox mirrors selection, the full row toggles it, and
     hover/keyboard rows highlight; quiet rows cover the fetching / empty /
     no-match states. */
  .combo-pop {
    display: flex;
    flex-direction: column;
    gap: 1px;
    margin-top: 4px;
    max-height: 280px;
    overflow-y: auto;
    padding: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-card);
  }
  .combo-option {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.32rem 0.45rem;
    border: none;
    border-radius: var(--radius-xs);
    background: transparent;
    text-align: left;
    font-size: var(--fs-sm);
    color: var(--text);
    line-height: var(--lh-tight);
    cursor: pointer;
    transition: background-color 0.1s ease;
  }
  .combo-option:hover, .combo-option.active { background: var(--surface-2); }
  .combo-check {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 15px;
    height: 15px;
    border: 1px solid var(--border-strong);
    border-radius: 4px;
    background: var(--surface);
    color: var(--accent-text);
    transition: background-color 0.15s ease, border-color 0.15s ease;
  }
  .combo-option.checked .combo-check { background: var(--accent); border-color: var(--accent); }
  .combo-check svg { width: 9px; height: 8px; }
  .combo-id { min-width: 0; overflow-wrap: anywhere; }
  .combo-name {
    flex: 0 1 auto;
    margin-left: auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--muted);
  }
  .combo-all { font-weight: 600; }
  .combo-note {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    padding: 0.4rem 0.45rem;
    font-size: var(--fs-sm);
    color: var(--muted);
    line-height: var(--lh);
  }
  .combo-spinner {
    flex: 0 0 auto;
    width: 12px;
    height: 12px;
    border: 2px solid var(--border-strong);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: combo-spin 0.7s linear infinite;
  }
  @keyframes combo-spin { to { transform: rotate(360deg); } }

  /* Per-tool provider overrides: one compact row per installed tool — tool
     name left, a small select right listing providers by name only. */
  .tool-providers { display: flex; flex-direction: column; gap: 6px; }
  .tool-provider-row { display: flex; align-items: center; gap: 0.5rem; }
  .tool-provider-name {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--fs-sm);
    color: var(--text);
  }
  .tool-provider-select {
    flex: 0 0 auto;
    max-width: 55%;
    height: var(--control-h-sm);
    padding: 0 1.8rem 0 0.7rem;
    font-size: var(--fs-sm);
    line-height: 1.2;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text);
    background-color: var(--surface-2);
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='8' viewBox='0 0 12 8' fill='none'%3E%3Cpath d='M1 1.5 6 6.5 11 1.5' stroke='%236e6e73' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
    background-repeat: no-repeat;
    background-position: right 0.6rem center;
    background-size: 12px 8px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    outline: none;
    cursor: pointer;
    appearance: none;
    -webkit-appearance: none;
    transition: border-color 0.15s ease, box-shadow 0.15s ease;
  }
  :global([data-theme="dark"]) .tool-provider-select {
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='8' viewBox='0 0 12 8' fill='none'%3E%3Cpath d='M1 1.5 6 6.5 11 1.5' stroke='%2398989d' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  }
  .tool-provider-select:hover { border-color: var(--border-strong); }
  .tool-provider-select:focus-visible { border-color: var(--accent); box-shadow: var(--focus); }

</style>
