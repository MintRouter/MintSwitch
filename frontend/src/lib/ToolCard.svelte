<script lang="ts">
  import type { ToolView } from "../../bindings/mintswitch/internal/service";
  import { statusMeta, toolLogoSrc } from "./ui";

  interface Props {
    tool: ToolView;
    busy: boolean;
    onApply: (id: string) => void;
    onRestore: (id: string) => void;
    onInstall: (id: string) => void;
    onUninstall: (id: string) => void;
    onModelChange: (toolID: string, model: string) => void;
  }
  let {
    tool, busy,
    onApply, onRestore, onInstall, onUninstall, onModelChange,
  }: Props = $props();

  // The provider in effect for this tool (per-tool override or the active
  // provider). Empty when no provider is configured yet — Apply then stays
  // disabled since the backend would fail fast anyway.
  const hasProvider = $derived(!!tool.selected_provider_id);

  // Provider logos are self-contained app-icon SVGs under /logos/<id>.svg. If a
  // tool has no asset or it fails to load we fall back to a neutral monogram
  // tile so the card layout never breaks.
  let logoFailed = $state(false);
  const logoSrc = $derived(toolLogoSrc(tool.id));
  const monogram = $derived((tool.name ?? "?").trim().charAt(0).toUpperCase() || "?");

  // Split the display name on its first " (" so the product name stays bold on
  // line one and the parenthetical (e.g. "CLI + IDE") becomes a small muted
  // subtitle — keeping every header tidy at ~2 lines instead of a ragged wrap.
  const nameParts = $derived.by(() => {
    const full = (tool.name ?? "").trim();
    const i = full.indexOf(" (");
    if (i === -1) return { name: full, subtitle: "" };
    let subtitle = full.slice(i + 2).trim();
    if (subtitle.endsWith(")")) subtitle = subtitle.slice(0, -1).trim();
    return { name: full.slice(0, i).trim(), subtitle };
  });

  const meta = $derived(statusMeta(tool.status));
  // ONE compact status line per card (short label + tone dot) under the title;
  // the full statusMeta sentence lives in the tooltip. Only "Applied" carries
  // the success tone — plain "Installed" stays neutral so a card never shows
  // two different greens meaning different things.
  const statusLine = $derived.by(() => {
    if (!tool.installed) return { label: "Not installed", tone: "neutral", full: meta.label };
    if (tool.status === "applied_by_mintswitch") {
      return { label: "Applied", tone: "success", full: meta.label };
    }
    if (tool.status === "modified_externally") {
      return { label: "Modified", tone: "warning", full: meta.label };
    }
    return { label: "Installed", tone: "neutral", full: meta.label };
  });
  // Apply needs an installed tool and a configured provider (the backend
  // fails fast without one). Restore only makes sense once we've changed
  // something.
  const canApply = $derived(tool.installed && hasProvider && !busy);
  const canRestore = $derived(
    tool.installed && tool.status !== "default" && tool.status !== "not_installed" && !busy,
  );

  // Per-tool model picker. The list comes from the tool's EFFECTIVE provider
  // (via the backend ToolView). selectedModel is the effective model; if it
  // isn't a member of the current list (e.g. the provider changed) we fall
  // back to the empty "Use provider default" option so the control never
  // shows a stale value.
  const models = $derived(tool.models ?? []);
  // Optional per-model display names: option labels show the friendly name
  // (fallback = the model ID) while option values stay the canonical ID.
  const modelNames = $derived(tool.model_names ?? {});
  const selectedModel = $derived(
    tool.selected_model && models.includes(tool.selected_model) ? tool.selected_model : "",
  );
  // When the model row exists, Apply sits inline at its right (one row saved);
  // otherwise it stays full-width in the actions block as before.
  const hasModelRow = $derived(tool.installed && models.length >= 1);
</script>

<article class="card tool" class:is-uninstalled={!tool.installed}
  aria-labelledby={`tool-${tool.id}`}>
  <div class="tool-head">
    {#if logoSrc && !logoFailed}
      <img class="tool-logo" src={logoSrc} alt="" width="28" height="28"
        loading="lazy" onerror={() => (logoFailed = true)} />
    {:else}
      <span class="tool-logo monogram" aria-hidden="true">{monogram}</span>
    {/if}
    <div class="tool-titles">
      <h3 class="tool-name" id={`tool-${tool.id}`}>{nameParts.name}</h3>
      {#if nameParts.subtitle}
        <p class="tool-subtitle">{nameParts.subtitle}</p>
      {/if}
    </div>
    <p class={`tool-status tone-${statusLine.tone}`} title={statusLine.full}>
      <span class="dot" aria-hidden="true"></span>
      {statusLine.label}
    </p>
  </div>

  <div class="tool-divider" aria-hidden="true"></div>

  {#snippet applyButton()}
    <button class="btn-primary sm soft" type="button" onclick={() => onApply(tool.id)}
      disabled={!canApply}
      title={!tool.installed ? "Tool is not installed" : !hasProvider ? "Add a provider first" : undefined}>
      Apply
    </button>
  {/snippet}

  {#if hasModelRow}
    <div class="tool-row">
      <select class="tool-model-select" id={`model-${tool.id}`}
        aria-label="Model"
        value={selectedModel}
        onchange={(e) => onModelChange(tool.id, e.currentTarget.value)}>
        <option value="">Use provider default</option>
        {#each models as m (m)}
          <option value={m}>{modelNames[m] || m}</option>
        {/each}
      </select>
      {@render applyButton()}
    </div>
  {/if}

  {#if tool.installed && tool.provider_name}
    <p class="tool-key"
      title={tool.provider_overridden
        ? "This tool uses its own provider instead of the active one"
        : "This tool uses the active provider"}>
      Provider: {tool.provider_name}{tool.provider_overridden ? " · override" : ""}
    </p>
  {/if}

  <div class="tool-actions">
    {#if !tool.installed && tool.installable}
      <button class="btn-primary sm soft" type="button" onclick={() => onInstall(tool.id)}
        disabled={busy} title="Install this tool with npm">
        {busy ? "Installing…" : "Install"}
      </button>
    {/if}
    {#if !hasModelRow}
      {@render applyButton()}
    {/if}
    <div class="actions-row secondary">
      <button class="btn-ghost sm quiet" type="button" onclick={() => onRestore(tool.id)}
        disabled={!canRestore}
        title={!tool.installed ? "Tool is not installed" : !canRestore ? "Nothing to restore" : undefined}>
        Restore default
      </button>
      {#if tool.installed && tool.installable}
        <button class="btn-ghost sm quiet danger" type="button" onclick={() => onUninstall(tool.id)}
          disabled={busy} title="Uninstall this tool with npm">
          {busy ? "Working…" : "Uninstall"}
        </button>
      {/if}
    </div>
  </div>
</article>

<style>
  /* Nested card on the tools panel ("My subscription" language): hairline
     border does the separation, radius one step below the panel's 12px, and
     no drop shadow — nested cards sit flat on the panel surface. */
  .tool {
    display: flex;
    flex-direction: column;
    gap: 10px;
    border-radius: var(--radius-sm);
    box-shadow: none;
  }
  .tool-head {
    display: flex;
    align-items: flex-start;
    gap: 0.55rem;
  }
  .tool-logo {
    flex: 0 0 auto;
    width: 28px;
    height: 28px;
    border-radius: 7px;
    border: 1px solid var(--border);
    display: block;
  }
  .tool-logo.monogram {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: var(--surface-2);
    color: var(--text);
    font-size: 0.86rem;
    font-weight: 700;
    line-height: 1;
  }
  .tool-titles {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
  }
  /* Titles wrap only at spaces (never mid-word — no overflow-wrap:anywhere),
     may run to two lines, and are never ellipsised. */
  .tool-name {
    margin: 0;
    font-size: 1.02rem;
    font-weight: 700;
    color: var(--text);
    line-height: 1.25;
  }
  .tool-subtitle {
    margin: 0;
    font-size: 0.78rem;
    color: var(--muted);
    line-height: 1.3;
  }
  /* Hairline separating the header from the card body (subscription-card
     section divider). */
  .tool-divider {
    flex: 0 0 auto;
    height: 1px;
    background: var(--border);
  }
  /* Body row holding the model select (no visible label — the select carries
     aria-label="Model" for accessibility) plus the inline Apply button pinned
     to its right at fit-content width — one row instead of two. */
  .tool-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .tool-row .btn-primary {
    flex: 0 0 auto;
    white-space: nowrap;
  }
  /* Custom-styled to match .field-input: native chevron removed (appearance:
     none) and replaced with a single inline-SVG chevron whose stroke tracks
     --muted per theme. padding-right clears the chevron so long model names
     never collide with it. Fills the full card width (min-width: 0 so it
     shrinks instead of forcing a wrap). */
  .tool-model-select {
    flex: 1 1 auto;
    min-width: 0;
    height: var(--control-h-sm); /* exact height parity with .btn-*.sm */
    padding: 0 1.8rem 0 0.7rem;  /* right pad clears the chevron */
    font-size: var(--fs-sm); /* matches .btn-*.sm */
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
  :global([data-theme="dark"]) .tool-model-select {
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='8' viewBox='0 0 12 8' fill='none'%3E%3Cpath d='M1 1.5 6 6.5 11 1.5' stroke='%2398989d' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  }
  .tool-model-select:hover { border-color: var(--border-strong); }
  .tool-model-select:focus-visible { border-color: var(--accent); box-shadow: var(--focus); }
  /* Compact status meta line pinned to the header's right edge: tone-coloured
     dot + short label, with the full sentence in the tooltip. Only the dot
     carries the tone — the label stays muted so a card holds at most two
     colour points (icon + dot). */
  .tool-status {
    margin: 0;
    margin-left: auto;
    align-self: flex-start;
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-size: var(--fs-micro);
    font-weight: var(--fw-semibold);
    color: var(--muted);
    line-height: 1.3;
  }
  .tool-status .dot {
    flex: 0 0 auto;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--muted);
  }
  .tool-status.tone-success .dot { background: var(--ok); }
  .tool-status.tone-warning .dot { background: var(--warn); }

  /* Which provider Apply will use: the provider's display name only (never
     any part of the key value), flagged when it comes from a per-tool
     override. */
  .tool-key {
    margin: 0;
    font-size: var(--fs-micro);
    font-weight: var(--fw-semibold);
    color: var(--muted);
    line-height: 1.3;
  }

  /* Not-installed: an intentional recessed treatment (inset surface + muted
     title) that reads as "inactive" while keeping all text at readable
     contrast — no whole-card opacity that would sink text below contrast. The
     Install button uses the same soft accent tint as Apply, keeping the main
     screen at exactly two solid-accent blocks. */
  .tool.is-uninstalled { background: var(--surface-2); }
  .tool.is-uninstalled .tool-name { color: var(--muted); }

  /* Action block pinned to the card foot: full-width soft primary button(s)
     stacked vertically, then the quiet secondary row (Restore left,
     destructive right). */
  .tool-actions {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: auto;
  }
  .tool-actions > .btn-primary { width: 100%; }
  .actions-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .actions-row .btn-ghost {
    flex: 0 0 auto;
    white-space: nowrap;
  }
  .actions-row.secondary { justify-content: space-between; }
  /* Destructive (Uninstall) stays pinned to the row's end even when Restore is
     absent. */
  .actions-row.secondary .danger { margin-left: auto; }
  /* Per-card Apply is a soft accent tint (not solid) so solid accent doesn't
     repeat across the grid. Falls back to color-mix while the --accent-soft
     tokens land in style.css. */
  .tool-row .btn-primary.soft,
  .tool-actions .btn-primary.soft {
    color: var(--accent-soft-text, var(--accent));
    background: var(--accent-soft, color-mix(in srgb, var(--accent) 12%, var(--surface)));
    box-shadow: none;
  }
  .tool-row .btn-primary.soft:hover:not(:disabled),
  .tool-actions .btn-primary.soft:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent) 20%, var(--surface));
    filter: none;
    box-shadow: none;
  }
  .tool-row .btn-primary.soft:active:not(:disabled),
  .tool-actions .btn-primary.soft:active:not(:disabled) {
    background: color-mix(in srgb, var(--accent) 26%, var(--surface));
    filter: none;
  }
  /* Secondary row is text-quiet: no fill, no border, muted text — but keeps a
     ≥26px hit area. Slight negative margins optically align the labels with
     the card edges. Uninstall only turns red on hover (intent). */
  .tool-actions .btn-ghost.quiet {
    min-height: 26px;
    padding: 0 0.5rem;
    background: transparent;
    border-color: transparent;
    color: var(--muted);
  }
  .actions-row.secondary .btn-ghost.quiet:first-child { margin-left: -0.5rem; }
  .actions-row.secondary .btn-ghost.quiet.danger { margin-right: -0.5rem; }
  .tool-actions .btn-ghost.quiet:hover:not(:disabled) {
    color: var(--text);
    background: var(--surface-2);
    border-color: transparent;
  }
  .tool-actions .btn-ghost.quiet.danger:hover:not(:disabled) {
    color: var(--danger-strong);
    background: color-mix(in srgb, var(--danger) 8%, var(--surface));
    border-color: transparent;
    filter: none;
  }
  /* Disabled controls must stay readable: override the global 0.4 opacity;
     quiet buttons stay borderless even when disabled. */
  .tool-row .btn-primary:disabled,
  .tool-actions .btn-primary:disabled,
  .tool-actions .btn-ghost:disabled {
    opacity: 0.55;
  }
  .tool-actions .btn-ghost.quiet:disabled { border-color: transparent; }
</style>
