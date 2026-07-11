<script lang="ts">
  import { Service } from "../../bindings/mintswitch/internal/service";
  import type { ProviderView, ToolView } from "../../bindings/mintswitch/internal/service";
  import type { Provider } from "../../bindings/mintswitch/internal/core";
  import { errMsg, isHttpUrl, normalizeBaseUrl } from "./ui";

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

  // Builds the full UpdateProvider payload for one provider with patch applied
  // on top. api_key defaults to "" which the backend treats as "keep the
  // stored key" — a key value only rides along when the user typed a new one.
  function fullProvider(p: ProviderView, patch: Partial<Provider>): Provider {
    return {
      id: p.id,
      name: p.name,
      note: p.note,
      api_key: "",
      base_url: p.base_url,
      models: p.models ?? [],
      model_names: p.model_names ?? {},
      model: p.model,
      small_fast_model: p.small_fast_model,
      ...patch,
    };
  }

  // ---- Manage dialog (provider list) ----
  let manageOpen = $state(false);
  let manageDialogEl = $state<HTMLDivElement | null>(null);
  let addNameEl = $state<HTMLInputElement | null>(null);
  let dialogError = $state("");
  let toolProviderError = $state("");

  function openManage(): void {
    dialogError = "";
    toolProviderError = "";
    autoFetchNotice = "";
    manageOpen = true;
  }

  function closeManage(): void {
    manageOpen = false;
    cancelEdit();
  }

  // Add form. The backend requires name + key + base URL + a default model
  // (core.Provider.Validate), so all four gate the Add button; the note is
  // optional non-secret text shown in the list row.
  let newName = $state("");
  let newBaseUrl = $state("");
  let newKey = $state("");
  let newModel = $state("");
  let newNote = $state("");
  const newBase = $derived(normalizeBaseUrl(newBaseUrl));
  const canAdd = $derived(
    !!newName.trim() && isHttpUrl(newBase.url) && !!newKey.trim() && !!newModel.trim(),
  );

  async function addProvider(): Promise<void> {
    if (!canAdd || saving) return;
    dialogError = "";
    autoFetchNotice = "";
    const addedName = newName.trim();
    const addedBase = newBase.url;
    const err = await onAdd({
      id: "",
      name: addedName,
      note: newNote.trim(),
      api_key: newKey,
      base_url: addedBase,
      models: [],
      model_names: {},
      model: newModel.trim(),
      small_fast_model: "",
    });
    if (err != null) {
      dialogError = err;
      return;
    }
    newName = "";
    newBaseUrl = "";
    newKey = "";
    newModel = "";
    newNote = "";
    addNameEl?.focus();
    void autoFetchFor(addedName, addedBase);
  }

  function onAddKeydown(e: KeyboardEvent): void {
    if (e.key === "Enter") {
      e.preventDefault();
      void addProvider();
    }
  }

  // Inline per-provider edit of name / base URL / key / note. The key input
  // stays empty for "keep the stored key"; typing a value replaces it.
  let editingId = $state("");
  let editName = $state("");
  let editBaseUrl = $state("");
  let editKey = $state("");
  let editNote = $state("");
  const editBase = $derived(normalizeBaseUrl(editBaseUrl));
  const canSaveEdit = $derived(!!editName.trim() && isHttpUrl(editBase.url));

  function startEdit(p: ProviderView): void {
    editingId = p.id;
    editName = p.name;
    editBaseUrl = p.base_url;
    editKey = "";
    editNote = p.note;
    dialogError = "";
  }

  function cancelEdit(): void {
    editingId = "";
    editKey = "";
  }

  async function saveEdit(): Promise<void> {
    const p = providers.find((x) => x.id === editingId);
    if (!p || !canSaveEdit || saving) return;
    dialogError = "";
    const err = await onUpdate(fullProvider(p, {
      name: editName.trim(),
      note: editNote.trim(),
      base_url: editBase.url,
      api_key: editKey,
    }));
    if (err != null) {
      dialogError = err;
      return;
    }
    cancelEdit();
  }

  async function removeProvider(id: string): Promise<void> {
    dialogError = "";
    if (editingId === id) cancelEdit();
    const err = await onRemove(id);
    if (err != null) dialogError = err;
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
  async function changeToolProvider(toolID: string, providerID: string): Promise<void> {
    toolProviderError = "";
    const err = await onToolProviderChange(toolID, providerID);
    if (err != null) toolProviderError = err;
  }

  // ---- Models dialog (one provider's model list) ----
  // Opened from a provider row; it temporarily replaces the Manage dialog and
  // Esc / backdrop / Done all return to it. Model mutations persist
  // immediately via UpdateProvider (api_key "" = keep the stored key).
  let modelsProviderId = $state("");
  let modelsDialogEl = $state<HTMLDivElement | null>(null);
  let modelInputEl = $state<HTMLInputElement | null>(null);
  let mModels = $state<string[]>([]);
  let mModelNames = $state<Record<string, string>>({});
  let mModel = $state("");
  let newModelId = $state("");
  let newModelName = $state("");
  let modelsError = $state("");

  // ---- Fetch models from the provider's endpoint ----
  // Read-only fetch of GET {base_url}/models via the backend; results feed a
  // checkbox picker for bulk-add. Errors are display-safe and never blocking:
  // the manual add-model input keeps working regardless.
  let fetching = $state(false);
  let fetchAttempted = $state(false);
  let fetchError = $state("");
  let fetchedModels = $state<string[]>([]);
  let fetchedSelected = $state<Record<string, boolean>>({});
  // Gentle notice on the provider list for the auto-fetch that runs right
  // after a provider is added (progress, or its non-blocking failure).
  let autoFetchNotice = $state("");

  const modelsProvider = $derived(providers.find((p) => p.id === modelsProviderId) ?? null);
  // A provider always needs one model (the backend refuses an empty list), so
  // the last entry can't be removed.
  const canRemoveModel = $derived(mModels.length > 1);

  function openModels(p: ProviderView): void {
    modelsProviderId = p.id;
    mModels = [...(p.models ?? [])];
    const seeded: Record<string, string> = {};
    for (const [id, name] of Object.entries(p.model_names ?? {})) {
      if (name) seeded[id] = name;
    }
    mModelNames = seeded;
    mModel = p.model;
    newModelId = "";
    newModelName = "";
    modelsError = "";
    fetching = false;
    fetchAttempted = false;
    fetchError = "";
    fetchedModels = [];
    fetchedSelected = {};
  }

  function closeModels(): void {
    modelsProviderId = "";
  }

  // What the UI shows for a model: its optional display name, falling back to
  // the canonical ID.
  function displayName(m: string): string {
    return mModelNames[m] || m;
  }

  async function persistModels(): Promise<void> {
    const p = providers.find((x) => x.id === modelsProviderId);
    if (!p) return;
    if (mModels.length === 0 || !mModels.includes(mModel)) return;
    modelsError = "";
    const err = await onUpdate(fullProvider(p, {
      models: mModels,
      model_names: mModelNames,
      model: mModel,
    }));
    if (err != null) modelsError = err;
  }

  // Add the typed model to the list (trimmed, deduped by ID). The optional
  // display name is stored alongside; re-adding an existing ID never
  // duplicates it but a newly typed name updates its alias. The first model
  // added becomes the default automatically.
  function addModel(): void {
    const id = newModelId.trim();
    if (!id) return;
    const name = newModelName.trim();
    if (!mModels.includes(id)) {
      mModels = [...mModels, id];
      if (!mModel) mModel = id;
    }
    if (name) {
      mModelNames = { ...mModelNames, [id]: name };
    }
    newModelId = "";
    newModelName = "";
    modelInputEl?.focus();
    void persistModels();
  }

  function onModelKeydown(e: KeyboardEvent): void {
    if (e.key === "Enter") {
      e.preventDefault();
      addModel();
    }
  }

  // Remove a model (and its display name); if it was the default, fall back
  // to the first remaining model so the default is never orphaned.
  function removeModel(m: string): void {
    if (!canRemoveModel) return;
    mModels = mModels.filter((x) => x !== m);
    if (m in mModelNames) {
      const next = { ...mModelNames };
      delete next[m];
      mModelNames = next;
    }
    if (mModel === m) {
      mModel = mModels[0] ?? "";
    }
    void persistModels();
  }

  function setDefault(m: string): void {
    if (mModel === m) return;
    mModel = m;
    void persistModels();
  }

  // Fetch (or refetch) the model IDs the provider's endpoint advertises. The
  // result replaces the previous picker; already-added models render marked
  // and disabled so bulk-add can never duplicate.
  async function fetchModels(): Promise<void> {
    const p = modelsProvider;
    if (!p || fetching) return;
    fetching = true;
    fetchError = "";
    try {
      fetchedModels = (await Service.FetchProviderModels(p.id)) ?? [];
      fetchedSelected = {};
      fetchAttempted = true;
    } catch (e) {
      fetchError = errMsg(e);
    } finally {
      fetching = false;
    }
  }

  const selectedFetchCount = $derived(
    fetchedModels.filter((m) => fetchedSelected[m] && !mModels.includes(m)).length,
  );

  // Bulk-add the checked fetched models (dedupe against the existing list);
  // the first model ever added becomes the default, same as manual add.
  function addSelectedModels(): void {
    const picked = fetchedModels.filter((m) => fetchedSelected[m] && !mModels.includes(m));
    if (picked.length === 0) return;
    mModels = [...mModels, ...picked];
    if (!mModel) mModel = mModels[0];
    fetchedSelected = {};
    void persistModels();
  }

  // Auto-fetch right after a provider is added: locate the new provider in
  // the refreshed list (by name, preferring an exact base URL match), then on
  // success open its Models dialog pre-populated with the fetched picker. Any
  // failure degrades to a gentle notice — the manual flow stays untouched.
  async function autoFetchFor(name: string, base: string): Promise<void> {
    const byName = providers.filter((x) => x.name === name);
    const added = byName.filter((x) => x.base_url === base).pop() ?? byName.pop();
    if (!added) return;
    autoFetchNotice = `Checking ${added.name} for available models…`;
    let ids: string[];
    try {
      ids = (await Service.FetchProviderModels(added.id)) ?? [];
    } catch {
      autoFetchNotice = `Couldn't fetch models from ${added.name} — you can add them manually via Models.`;
      return;
    }
    autoFetchNotice = "";
    if (!manageOpen || modelsProviderId) return;
    if (ids.length === 0) {
      autoFetchNotice = `${added.name} didn't advertise any models — you can add them manually via Models.`;
      return;
    }
    openModels(providers.find((x) => x.id === added.id) ?? added);
    fetchedModels = ids;
    fetchAttempted = true;
  }

  // Focus the first input whenever a dialog (or dialog view) opens; runs
  // after render so the element refs exist.
  $effect(() => {
    if (manageOpen && !modelsProviderId) {
      queueMicrotask(() => addNameEl?.focus());
    }
  });

  $effect(() => {
    if (modelsProviderId) {
      queueMicrotask(() => modelInputEl?.focus());
    }
  });

  // Esc closes the open dialog view (Models falls back to the provider list);
  // Tab is trapped inside the visible dialog while open.
  function onDialogKeydown(e: KeyboardEvent): void {
    if (!manageOpen) return;
    const inModels = !!modelsProviderId;
    const dialogEl = inModels ? modelsDialogEl : manageDialogEl;
    if (e.key === "Escape") {
      e.preventDefault();
      if (inModels) closeModels();
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

<div class="card providers" aria-labelledby="providers-h">
  <h2 class="card-title providers-title" id="providers-h">Providers</h2>

  <div class="field">
    <span class="micro-label" id="pv-list-label">Providers</span>
    <div class="providers-summary">
      <span class="providers-summary-text" class:is-empty={providers.length === 0}
        aria-describedby="pv-list-label">{providersSummary}</span>
      <button class="btn-ghost sm" type="button" onclick={openManage}>Manage</button>
    </div>
    {#if providers.length === 0}
      <p class="field-hint">Add at least one provider to configure your tools.</p>
    {/if}
  </div>

  {#if active}
    <div class="field">
      <span class="micro-label">Active provider</span>
      <p class="active-name">{active.name}</p>
      {#if active.note}
        <p class="active-note">{active.note}</p>
      {/if}
      <p class="active-meta">{active.base_url}</p>
      <p class="active-meta">{modelsSummary(active)}</p>
    </div>
  {/if}
</div>

<svelte:window onkeydown={onDialogKeydown} />

{#if manageOpen && !modelsProviderId}
  <div class="backdrop" role="presentation"
    onclick={(e) => e.target === e.currentTarget && closeManage()}>
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="pv-manage-title"
      tabindex="-1" bind:this={manageDialogEl}>
      <h2 class="title" id="pv-manage-title">Providers</h2>
      <div class="add-body">
        <div class="field">
          <span class="micro-label">Add a provider</span>
          <div class="add-grid">
            <div class="add-field">
              <label class="add-label" for="pv-add-name">Name</label>
              <input class="field-input" id="pv-add-name" type="text" bind:value={newName}
                bind:this={addNameEl}
                placeholder="MintRouter.AI" autocomplete="off" spellcheck="false"
                onkeydown={onAddKeydown} />
            </div>
            <div class="add-field">
              <label class="add-label" for="pv-add-base">Base URL</label>
              <input class="field-input" id="pv-add-base" type="url" bind:value={newBaseUrl}
                placeholder="https://api.mintrouter.ai/v1" autocomplete="off" spellcheck="false"
                onkeydown={onAddKeydown} />
            </div>
            <div class="add-field">
              <label class="add-label" for="pv-add-key">API key</label>
              <input class="field-input" id="pv-add-key" type="password" bind:value={newKey}
                placeholder="Enter the API key" autocomplete="off"
                onkeydown={onAddKeydown} />
            </div>
            <div class="add-field">
              <label class="add-label" for="pv-add-model">Default model</label>
              <input class="field-input" id="pv-add-model" type="text" bind:value={newModel}
                placeholder="anthropic/claude-opus-4.8" autocomplete="off" spellcheck="false"
                onkeydown={onAddKeydown} />
            </div>
          </div>
          <div class="add-field">
            <label class="add-label" for="pv-add-note">Note <span class="opt">Optional</span></label>
            <textarea class="field-input note-input" id="pv-add-note" bind:value={newNote}
              placeholder="e.g. team account, EU endpoint" rows="2" autocomplete="off"></textarea>
          </div>
          {#if newBase.upgraded}
            <p class="field-notice">
              Will be saved as <code>{newBase.url}</code> — http endpoints can drop the API key on redirect.
            </p>
          {/if}
          <div class="add-actions">
            <button class="btn-primary sm" type="button" onclick={() => void addProvider()}
              disabled={!canAdd || saving}>Add provider</button>
          </div>
          {#if autoFetchNotice}
            <p class="field-hint" role="status">{autoFetchNotice}</p>
          {/if}
        </div>

        {#if providers.length}
          <div class="provider-list" role="group" aria-label="Providers">
            {#each providers as p (p.id)}
              <div class="provider-row" class:selected={p.active}>
                <div class="provider-line">
                  <button class="provider-select" type="button" aria-pressed={p.active}
                    onclick={() => void setActive(p.id)}
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
                    <button class="btn-ghost sm" type="button" onclick={() => openModels(p)}
                      title={`Manage ${p.name} models`}>Models</button>
                    <button class="btn-ghost sm" type="button"
                      onclick={() => (editingId === p.id ? cancelEdit() : startEdit(p))}>
                      {editingId === p.id ? "Cancel" : "Edit"}
                    </button>
                    <button class="provider-remove" type="button" disabled={saving}
                      onclick={() => void removeProvider(p.id)}
                      aria-label={`Remove ${p.name}`} title={`Remove ${p.name}`}>×</button>
                  </div>
                </div>
                {#if editingId === p.id}
                  <div class="provider-edit">
                    <div class="add-grid">
                      <div class="add-field">
                        <label class="add-label" for={`pv-edit-name-${p.id}`}>Name</label>
                        <input class="field-input" id={`pv-edit-name-${p.id}`} type="text"
                          bind:value={editName} autocomplete="off" spellcheck="false" />
                      </div>
                      <div class="add-field">
                        <label class="add-label" for={`pv-edit-base-${p.id}`}>Base URL</label>
                        <input class="field-input" id={`pv-edit-base-${p.id}`} type="url"
                          bind:value={editBaseUrl} autocomplete="off" spellcheck="false" />
                      </div>
                    </div>
                    <div class="add-field">
                      <label class="add-label" for={`pv-edit-key-${p.id}`}>API key</label>
                      <input class="field-input" id={`pv-edit-key-${p.id}`} type="password"
                        bind:value={editKey} autocomplete="off"
                        placeholder="Leave blank to keep the current key" />
                    </div>
                    <div class="add-field">
                      <label class="add-label" for={`pv-edit-note-${p.id}`}>Note <span class="opt">Optional</span></label>
                      <textarea class="field-input note-input" id={`pv-edit-note-${p.id}`}
                        bind:value={editNote} rows="2" autocomplete="off"></textarea>
                    </div>
                    {#if editBase.upgraded}
                      <p class="field-notice">
                        Will be saved as <code>{editBase.url}</code> — http endpoints can drop the API key on redirect.
                      </p>
                    {/if}
                    <div class="add-actions">
                      <button class="btn-primary sm" type="button" onclick={() => void saveEdit()}
                        disabled={!canSaveEdit || saving}>Save changes</button>
                    </div>
                  </div>
                {/if}
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
                    onchange={(e) => void changeToolProvider(t.id, e.currentTarget.value)}>
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

{#if manageOpen && modelsProviderId}
  <div class="backdrop" role="presentation"
    onclick={(e) => e.target === e.currentTarget && closeModels()}>
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="pv-models-title"
      tabindex="-1" bind:this={modelsDialogEl}>
      <h2 class="title" id="pv-models-title">Models — {modelsProvider?.name ?? ""}</h2>
      <div class="add-body">
        <div class="field">
          <span class="micro-label">Add a model</span>
          <div class="model-add">
            <div class="add-field">
              <label class="add-label" for="pv-model-add">Model ID</label>
              <input class="field-input" id="pv-model-add" type="text" bind:value={newModelId}
                bind:this={modelInputEl}
                placeholder="anthropic/claude-opus-4.8" autocomplete="off" spellcheck="false"
                onkeydown={onModelKeydown} />
            </div>
            <div class="add-field">
              <label class="add-label" for="pv-model-add-name">Display name <span class="opt">Optional</span></label>
              <input class="field-input" id="pv-model-add-name" type="text" bind:value={newModelName}
                placeholder="opus4.8" autocomplete="off" spellcheck="false"
                onkeydown={onModelKeydown} />
            </div>
            <button class="btn-primary sm" type="button" onclick={addModel}
              disabled={!newModelId.trim() || saving}>Add</button>
          </div>
        </div>
        <div class="field">
          <div class="fetch-head">
            <span class="micro-label">From the endpoint</span>
            <button class="btn-ghost sm" type="button" onclick={() => void fetchModels()}
              disabled={fetching || saving} aria-busy={fetching}
              title="List the models the provider's /models endpoint advertises">
              {fetching ? "Fetching…" : fetchAttempted || fetchError ? "Refetch models" : "Fetch models"}
            </button>
          </div>
          {#if fetchError}
            <p class="field-notice" role="alert">
              Couldn't fetch models: {fetchError} — you can still add models manually above.
            </p>
          {/if}
          {#if fetchAttempted && !fetchError}
            {#if fetchedModels.length === 0}
              <p class="field-hint">The endpoint didn't advertise any models.</p>
            {:else}
              <div class="fetch-list" role="group" aria-label="Fetched models">
                {#each fetchedModels as m (m)}
                  {#if mModels.includes(m)}
                    <label class="fetch-item added">
                      <input type="checkbox" checked disabled />
                      <span class="fetch-id">{m}</span>
                      <span class="badge tone-success">Added</span>
                    </label>
                  {:else}
                    <label class="fetch-item">
                      <input type="checkbox" bind:checked={fetchedSelected[m]} />
                      <span class="fetch-id">{m}</span>
                    </label>
                  {/if}
                {/each}
              </div>
              <div class="add-actions">
                <button class="btn-primary sm" type="button" onclick={addSelectedModels}
                  disabled={selectedFetchCount === 0 || saving}>
                  Add selected{selectedFetchCount > 0 ? ` (${selectedFetchCount})` : ""}
                </button>
              </div>
            {/if}
          {/if}
        </div>
        {#if mModels.length}
          <div class="seg-group" role="group" aria-label="Default model">
            {#each mModels as m (m)}
              <div class="seg" class:selected={m === mModel}>
                <button class="seg-select" type="button" aria-pressed={m === mModel}
                  onclick={() => setDefault(m)} title={`Set ${displayName(m)} as default`}>
                  <span class="seg-name">{displayName(m)}</span>
                  {#if mModelNames[m]}
                    <span class="seg-id">{m}</span>
                  {/if}
                </button>
                <button class="seg-remove" type="button" disabled={!canRemoveModel}
                  onclick={(e) => { e.preventDefault(); e.stopPropagation(); removeModel(m); }}
                  aria-label={`Remove ${displayName(m)}`}
                  title={canRemoveModel ? `Remove ${displayName(m)}` : "A provider needs at least one model"}>×</button>
              </div>
            {/each}
          </div>
        {:else}
          <p class="field-hint">No models yet — add your first one above.</p>
        {/if}
        {#if modelsError}
          <p class="field-error" role="alert">Couldn't save: {modelsError}</p>
        {/if}
      </div>
      <div class="actions">
        <button class="btn-primary" type="button" onclick={closeModels}>Done</button>
      </div>
    </div>
  </div>
{/if}

<style>
  /* Same rhythm as the old profile card: tight local gap, and the card grows
     to fill the left column so its bottom edge lines up with the tools panel. */
  .providers { display: flex; flex-direction: column; gap: 10px; flex: 1 0 auto; }
  .providers-title { margin: 0; }
  .opt {
    margin-left: 0.3rem;
    text-transform: none;
    letter-spacing: 0;
    font-weight: var(--fw-medium);
    color: var(--muted);
  }

  /* Compact card row: a muted one-line summary on the left and a quiet-accent
     Manage action on the right — plain text + button, no inset. */
  .providers-summary {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--s-2);
    padding: 2px 0;
    background: transparent;
    border: none;
  }
  .providers-summary-text {
    flex: 1 1 auto;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: var(--fs-sm);
    line-height: var(--lh);
    color: var(--muted);
  }
  .providers-summary-text.is-empty { color: var(--muted); }
  .providers-summary .btn-ghost {
    flex: 0 0 auto;
    background: transparent;
    border-color: transparent;
    color: var(--accent-soft-text);
  }
  .providers-summary .btn-ghost:hover:not(:disabled) {
    background: var(--accent-soft);
    border-color: transparent;
  }

  /* Active-provider block on the card: name (bold), optional muted note line,
     then quiet meta lines (base URL, models summary). Never any key value. */
  .active-name {
    margin: 0;
    font-size: var(--fs-sm);
    font-weight: var(--fw-semibold);
    color: var(--text);
    line-height: var(--lh);
    overflow-wrap: anywhere;
  }
  .active-note {
    margin: 0;
    font-size: var(--fs-micro);
    color: var(--muted);
    line-height: 1.35;
    overflow-wrap: anywhere;
  }
  .active-meta {
    margin: 0;
    font-size: var(--fs-micro);
    color: var(--muted);
    line-height: 1.35;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
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
    background: rgba(0, 0, 0, 0.4);
    -webkit-backdrop-filter: blur(4px);
    backdrop-filter: blur(4px);
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

  /* Add/edit form fields: labelled inputs in a 2-col grid (stacking on narrow
     dialogs), with the Note textarea full-width below. */
  .add-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
    gap: var(--s-1);
  }
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
  .add-actions { display: flex; justify-content: flex-end; margin-top: 0.1rem; }

  /* Provider list: one bordered row per provider — the main area is a button
     that sets it active (accent ring when active), with quiet Models / Edit /
     remove actions on the right; the inline edit form expands below. */
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
    width: 26px;
    min-height: 26px;
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
  .provider-edit {
    display: flex;
    flex-direction: column;
    gap: var(--s-1);
    padding: 0 0.6rem 0.6rem;
    border-top: 1px solid var(--border);
    padding-top: 0.6rem;
    margin: 0 0 0;
  }

  /* Fetch-from-endpoint block: quiet header row (label + ghost fetch button),
     then a scroll-capped checkbox list of the advertised model IDs; rows for
     already-added models are muted with an "Added" badge and can't be
     re-checked, so bulk-add never duplicates. */
  .fetch-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--s-2);
  }
  .fetch-head .btn-ghost {
    background: transparent;
    border-color: transparent;
    color: var(--accent-soft-text);
  }
  .fetch-head .btn-ghost:hover:not(:disabled) {
    background: var(--accent-soft);
    border-color: transparent;
  }
  .fetch-list {
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 14rem;
    overflow-y: auto;
    padding: 4px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .fetch-item {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    padding: 0.28rem 0.4rem;
    border-radius: 6px;
    font-size: var(--fs-sm);
    color: var(--text);
    line-height: var(--lh-tight);
    cursor: pointer;
  }
  .fetch-item:hover { background: var(--surface); }
  .fetch-item.added { color: var(--muted); cursor: default; }
  .fetch-item.added:hover { background: transparent; }
  .fetch-item input { flex: 0 0 auto; margin: 0; accent-color: var(--accent); }
  .fetch-item .fetch-id { min-width: 0; overflow-wrap: anywhere; }
  .fetch-item .badge { flex: 0 0 auto; margin-left: auto; }

  /* Models add row: Model ID + optional Display name side by side with the Add
     button pinned level to the input boxes. */
  .model-add { display: flex; gap: var(--s-1); align-items: flex-end; }
  .model-add .field-input,
  .model-add .btn-primary { height: 36px; }
  .model-add .add-field { flex: 1 1 0; }
  .model-add .btn-primary { flex: 0 0 auto; }

  /* Segmented default picker (same language as the old Models dialog): models
     render as connected segments; the selected one is accent-filled, the rest
     are clickable to become the default; each carries its own isolated ×. */
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
    min-height: 36px;
    border: 1px solid transparent;
    border-radius: 7px;
    background: transparent;
    transition: background-color 0.15s ease, border-color 0.15s ease;
  }
  .seg.selected { background: var(--accent); }
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
  .seg-remove:disabled { opacity: 0.4; cursor: default; }
  .seg-remove:disabled:hover { color: var(--muted); }
  .seg.selected .seg-remove:disabled:hover { color: var(--accent-text); }

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
