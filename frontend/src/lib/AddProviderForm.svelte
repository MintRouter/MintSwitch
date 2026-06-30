<script lang="ts">
  // Modal for adding a user-defined custom provider. Collects Name, Config file
  // path, an optional Binary name, and three optional "key path" inputs that say
  // WHERE each profile value is written in the config file using dot-notation
  // (e.g. `apiKey`, `baseURL`, `model`, or `provider.acme.options.apiKey`). On
  // submit the form builds the JSON `template` itself: each filled path becomes a
  // nested object whose leaf value is the matching placeholder token
  // (${API_KEY}/${BASE_URL}/${MODEL}), which the backend (AddCustomTool) leaves
  // unchanged and substitutes from the saved profile on Apply. Validation is
  // client-side (name + config path required, at least one key path, no empty
  // segments, no conflicting paths); any backend error is surfaced read-only via
  // `backendError`. Secrets are never logged — only placeholder tokens are built.
  interface Props {
    open: boolean;
    busy: boolean;
    backendError: string;
    onSubmit: (data: { name: string; configPath: string; binaryName: string; template: string }) => void;
    onCancel: () => void;
  }
  let { open, busy, backendError, onSubmit, onCancel }: Props = $props();

  // Placeholder tokens recognised by the backend (internal/core/customtool.go).
  // Defined as plain (non-template) strings so the `${…}` stays literal.
  const PLACEHOLDER_API_KEY = "${API_KEY}";
  const PLACEHOLDER_BASE_URL = "${BASE_URL}";
  const PLACEHOLDER_MODEL = "${MODEL}";

  const DEFAULT_API_KEY_PATH = "apiKey";
  const DEFAULT_BASE_URL_PATH = "baseURL";
  const DEFAULT_MODEL_PATH = "model";

  let name = $state("");
  let configPath = $state("");
  let binaryName = $state("");
  let apiKeyPath = $state(DEFAULT_API_KEY_PATH);
  let baseUrlPath = $state(DEFAULT_BASE_URL_PATH);
  let modelPath = $state(DEFAULT_MODEL_PATH);
  let errors = $state<Record<string, string>>({});

  let dialogEl = $state<HTMLDivElement | null>(null);
  let nameEl = $state<HTMLInputElement | null>(null);

  // Reset to a clean, prefilled form each time the modal opens and move focus to
  // the first field so keyboard users land in the form.
  $effect(() => {
    if (open) {
      name = "";
      configPath = "";
      binaryName = "";
      apiKeyPath = DEFAULT_API_KEY_PATH;
      baseUrlPath = DEFAULT_BASE_URL_PATH;
      modelPath = DEFAULT_MODEL_PATH;
      errors = {};
      queueMicrotask(() => nameEl?.focus());
    }
  });

  // Build the nested template object from the filled key-paths, recording any
  // per-field path errors (empty segments or conflicts) into `next`. Returns null
  // when no path is filled (caller records the "at least one path" error).
  function buildTemplateObject(next: Record<string, string>): Record<string, unknown> | null {
    const root: Record<string, unknown> = {};
    const entries: Array<{ raw: string; token: string; field: string }> = [
      { raw: apiKeyPath.trim(), token: PLACEHOLDER_API_KEY, field: "apiKeyPath" },
      { raw: baseUrlPath.trim(), token: PLACEHOLDER_BASE_URL, field: "baseUrlPath" },
      { raw: modelPath.trim(), token: PLACEHOLDER_MODEL, field: "modelPath" },
    ];
    let anyFilled = false;
    for (const { raw, token, field } of entries) {
      if (!raw) continue;
      anyFilled = true;
      const segs = raw.split(".");
      if (segs.some((s) => s.trim() === "")) {
        next[field] = "Remove empty path segments (no leading, trailing or doubled dots).";
        continue;
      }
      let node = root;
      let conflict = false;
      for (let i = 0; i < segs.length - 1; i++) {
        const k = segs[i];
        const existing = node[k];
        if (existing === undefined) {
          node[k] = {};
        } else if (typeof existing !== "object" || existing === null) {
          conflict = true;
          break;
        }
        node = node[k] as Record<string, unknown>;
      }
      if (conflict) {
        next[field] = "This path conflicts with another field's path.";
        continue;
      }
      const leaf = segs[segs.length - 1];
      if (leaf in node) {
        next[field] = "This path conflicts with another field's path.";
        continue;
      }
      node[leaf] = token;
    }
    if (!anyFilled) {
      next.paths = "Fill at least one key path (API key, Base URL or Model).";
      return null;
    }
    return root;
  }

  let pendingTemplate = "";

  function validate(): boolean {
    const next: Record<string, string> = {};
    if (!name.trim()) next.name = "A name is required.";
    if (!configPath.trim()) next.configPath = "A config file path is required.";
    const obj = buildTemplateObject(next);
    if (obj && !next.apiKeyPath && !next.baseUrlPath && !next.modelPath) {
      pendingTemplate = JSON.stringify(obj, null, 2);
    } else {
      pendingTemplate = "";
    }
    errors = next;
    return Object.keys(next).length === 0;
  }

  function submit(e: SubmitEvent): void {
    e.preventDefault();
    if (busy || !validate()) return;
    onSubmit({
      name: name.trim(),
      configPath: configPath.trim(),
      binaryName: binaryName.trim(),
      template: pendingTemplate,
    });
  }

  function onKeydown(e: KeyboardEvent): void {
    if (!open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      if (!busy) onCancel();
      return;
    }
    if (e.key !== "Tab" || !dialogEl) return;
    const focusables = dialogEl.querySelectorAll<HTMLElement>(
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
</script>

<svelte:window onkeydown={onKeydown} />

{#if open}
  <div class="backdrop" role="presentation"
    onclick={(e) => e.target === e.currentTarget && !busy && onCancel()}>
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="add-provider-title"
      tabindex="-1" bind:this={dialogEl}>
      <h2 class="title" id="add-provider-title">Add a custom provider</h2>
      <form class="add-body" onsubmit={submit} novalidate>
        <div class="field">
          <label class="field-label" for="ap-name">Name</label>
          <input class="field-input" id="ap-name" type="text" bind:value={name} bind:this={nameEl}
            placeholder="e.g. Acme CLI" autocomplete="off"
            aria-invalid={!!errors.name} aria-describedby={errors.name ? "ap-err-name" : undefined} />
          {#if errors.name}<p class="field-error" id="ap-err-name">{errors.name}</p>{/if}
        </div>

        <div class="field">
          <label class="field-label" for="ap-path">Config file path</label>
          <input class="field-input" id="ap-path" type="text" bind:value={configPath}
            placeholder="~/.config/acme/config.json" autocomplete="off" spellcheck="false"
            aria-invalid={!!errors.configPath} aria-describedby={errors.configPath ? "ap-err-path" : undefined} />
          {#if errors.configPath}<p class="field-error" id="ap-err-path">{errors.configPath}</p>{/if}
        </div>

        <div class="field">
          <label class="field-label" for="ap-bin">Binary name <span class="opt">(optional)</span></label>
          <input class="field-input" id="ap-bin" type="text" bind:value={binaryName}
            placeholder="acme" autocomplete="off" spellcheck="false" />
          <p class="field-hint">Used to detect if the tool is installed. Leave blank to always treat as installed.</p>
        </div>

        <div class="field">
          <label class="field-label" for="ap-apikey">API key path <span class="opt">(optional)</span></label>
          <input class="field-input" id="ap-apikey" type="text" bind:value={apiKeyPath}
            placeholder="apiKey" autocomplete="off" spellcheck="false"
            aria-invalid={!!errors.apiKeyPath} aria-describedby={errors.apiKeyPath ? "ap-err-apikey" : undefined} />
          {#if errors.apiKeyPath}<p class="field-error" id="ap-err-apikey">{errors.apiKeyPath}</p>{/if}
        </div>

        <div class="field">
          <label class="field-label" for="ap-baseurl">Base URL path <span class="opt">(optional)</span></label>
          <input class="field-input" id="ap-baseurl" type="text" bind:value={baseUrlPath}
            placeholder="baseURL" autocomplete="off" spellcheck="false"
            aria-invalid={!!errors.baseUrlPath} aria-describedby={errors.baseUrlPath ? "ap-err-baseurl" : undefined} />
          {#if errors.baseUrlPath}<p class="field-error" id="ap-err-baseurl">{errors.baseUrlPath}</p>{/if}
        </div>

        <div class="field">
          <label class="field-label" for="ap-model">Model path <span class="opt">(optional)</span></label>
          <input class="field-input" id="ap-model" type="text" bind:value={modelPath}
            placeholder="model" autocomplete="off" spellcheck="false"
            aria-invalid={!!errors.modelPath} aria-describedby={errors.modelPath ? "ap-err-model" : undefined} />
          {#if errors.modelPath}<p class="field-error" id="ap-err-model">{errors.modelPath}</p>{/if}
        </div>

        <p class="field-hint">Mỗi ô là nơi giá trị từ hồ sơ được ghi vào file cấu hình khi Apply. Dùng dấu chấm để lồng: <code>provider.acme.options.apiKey</code>.</p>
        {#if errors.paths}<p class="field-error">{errors.paths}</p>{/if}

        {#if backendError}
          <p class="field-error backend-error" role="alert">{backendError}</p>
        {/if}

        <div class="actions">
          <button class="btn-ghost" type="button" onclick={onCancel} disabled={busy}>Cancel</button>
          <button class="btn-primary" type="submit" disabled={busy}>
            {busy ? "Adding…" : "Add provider"}
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

<style>
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
  /* The dialog is capped to the viewport; the form body scrolls within it so a
     tall form never breaks the no-scroll app shell. */
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
    font-size: 1.15rem;
    font-weight: 700;
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
  .opt { color: var(--muted); font-weight: 400; }
  .field-hint code {
    font-size: 0.74rem;
    padding: 0.05rem 0.25rem;
    border-radius: 4px;
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  .backend-error {
    margin: 0;
    padding: 0.5rem 0.65rem;
    border: 1px solid var(--danger);
    border-radius: 8px;
    background: var(--surface-2);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .actions {
    flex: 0 0 auto;
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 0.25rem;
  }
</style>
