<script lang="ts">
  // Modal for adding a user-defined custom provider. Collects Name, Config file
  // path, an optional Binary name, and a JSON template that uses the placeholders
  // ${API_KEY}/${BASE_URL}/${MODEL}. Validation is client-side (name + path
  // required, template must be a JSON object); any backend error from
  // AddCustomTool is surfaced read-only via `backendError`. Secrets are never
  // logged — the template only ever holds placeholder tokens, not real values.
  interface Props {
    open: boolean;
    busy: boolean;
    backendError: string;
    onSubmit: (data: { name: string; configPath: string; binaryName: string; template: string }) => void;
    onCancel: () => void;
  }
  let { open, busy, backendError, onSubmit, onCancel }: Props = $props();

  // A helpful, opencode/claude-style example wiring all three placeholders, so a
  // user can edit rather than start from a blank textarea.
  const DEFAULT_TEMPLATE = `{
  "model": "\${MODEL}",
  "provider": {
    "mintswitch": {
      "name": "MintSwitch",
      "options": {
        "baseURL": "\${BASE_URL}",
        "apiKey": "\${API_KEY}"
      },
      "models": {
        "\${MODEL}": {}
      }
    }
  }
}`;

  let name = $state("");
  let configPath = $state("");
  let binaryName = $state("");
  let template = $state(DEFAULT_TEMPLATE);
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
      template = DEFAULT_TEMPLATE;
      errors = {};
      queueMicrotask(() => nameEl?.focus());
    }
  });

  function validate(): boolean {
    const next: Record<string, string> = {};
    if (!name.trim()) next.name = "A name is required.";
    if (!configPath.trim()) next.configPath = "A config file path is required.";
    const t = template.trim();
    if (!t) {
      next.template = "A JSON template is required.";
    } else {
      let parsed: unknown;
      try {
        parsed = JSON.parse(t);
      } catch {
        next.template = "Template must be valid JSON.";
      }
      if (!("template" in next) && (parsed === null || typeof parsed !== "object" || Array.isArray(parsed))) {
        next.template = "Template must be a JSON object (e.g. { … }).";
      }
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
      template: template,
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
      'button:not([disabled]), input:not([disabled]), textarea:not([disabled])',
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
          <label class="field-label" for="ap-template">JSON template</label>
          <textarea class="field-input field-textarea" id="ap-template" bind:value={template}
            spellcheck="false" autocapitalize="off" autocomplete="off" rows="10"
            aria-invalid={!!errors.template} aria-describedby={errors.template ? "ap-err-template" : "ap-hint-template"}
          ></textarea>
          {#if errors.template}
            <p class="field-error" id="ap-err-template">{errors.template}</p>
          {:else}
            <p class="field-hint" id="ap-hint-template">Use <code>{"${API_KEY}"}</code>, <code>{"${BASE_URL}"}</code> and <code>{"${MODEL}"}</code> — they're filled from your saved profile on Apply.</p>
          {/if}
        </div>

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
     long template never breaks the no-scroll app shell. */
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
  .field-textarea {
    min-height: 9rem;
    max-height: 22rem;
    resize: vertical;
    font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
    font-size: 0.82rem;
    line-height: 1.5;
    white-space: pre;
    overflow: auto;
  }
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
