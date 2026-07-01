<script lang="ts">
  import type { ToolView } from "../../bindings/mintswitch/internal/service";
  import { statusMeta, toolLogoSrc } from "./ui";

  interface Props {
    tool: ToolView;
    hasSavedProfile: boolean;
    busy: boolean;
    // Context Engine (MCP) per-tool inject, driven by App's single MCP state.
    mcpEnabled: boolean;
    mcpCapable: boolean;
    mcpStatus?: string;
    hasMcpKey: boolean;
    mcpBusy: boolean;
    onMcpToggle: (id: string, checked: boolean) => void;
    onApply: (id: string) => void;
    onRestore: (id: string) => void;
    onInstall: (id: string) => void;
    onUninstall: (id: string) => void;
    onRemove: (id: string) => void;
    onModelChange: (toolID: string, model: string) => void;
  }
  let {
    tool, hasSavedProfile, busy,
    mcpEnabled, mcpCapable, mcpStatus, hasMcpKey, mcpBusy, onMcpToggle,
    onApply, onRestore, onInstall, onUninstall, onRemove, onModelChange,
  }: Props = $props();

  // Provider logos are self-contained app-icon SVGs under /logos/<id>.svg. If a
  // tool has no asset (custom providers) or it fails to load we fall back to a
  // neutral monogram tile so the card layout never breaks.
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
  // Apply needs an installed tool and a saved profile (the backend fails fast
  // without one). Restore only makes sense once we've changed something.
  const canApply = $derived(tool.installed && hasSavedProfile && !busy);
  const canRestore = $derived(
    tool.installed && tool.status !== "default" && tool.status !== "not_installed" && !busy,
  );
  const paths = $derived(tool.config_paths ?? []);

  // Per-tool model picker. The list comes from the active profile (via the
  // backend ToolView). selectedModel is the effective model; if it isn't a
  // member of the current list (e.g. the profile changed) we fall back to the
  // empty "Use profile default" option so the control never shows a stale value.
  const models = $derived(tool.models ?? []);
  const selectedModel = $derived(
    tool.selected_model && models.includes(tool.selected_model) ? tool.selected_model : "",
  );

  // Context Engine inject control: only meaningful for an MCP-capable, installed
  // tool once the master toggle is on and a key is saved. When the master is
  // off the control simply isn't rendered (non-destructive). Checked reflects
  // whether MintSwitch itself wrote the config for this tool.
  const showMcp = $derived(mcpCapable && tool.installed && mcpEnabled && hasMcpKey);
  const mcpChecked = $derived(mcpStatus === "configured_by_mintswitch");
  // A pre-existing external "mintrouter" entry: enabling would overwrite it, so
  // we surface a subtle "(external)" marker and an explanatory tooltip instead
  // of silently letting the checkbox look like a fresh, safe opt-in.
  const mcpExternal = $derived(mcpStatus === "configured_externally");
  const mcpTitle = $derived(
    mcpExternal
      ? "A different 'mintrouter' MCP entry already exists; enabling replaces it (a backup is kept)."
      : mcpChecked
        ? `Disable Context Engine for ${tool.name}`
        : `Enable Context Engine for ${tool.name}`,
  );
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
    <span class="badge install" class:on={tool.installed}>
      <span class="dot" aria-hidden="true"></span>
      {tool.installed ? "Installed" : "Not installed"}
    </span>
  </div>

  {#if tool.status !== "default" && tool.status !== "not_installed"}
    <span class={`badge status tone-${meta.tone}`}>{meta.label}</span>
  {/if}

  {#if tool.installed}
    {#if paths.length}
      <ul class="paths" aria-label="Config paths">
        {#each paths as p (p)}
          <li><code>{p}</code></li>
        {/each}
      </ul>
    {/if}
  {:else}
    {#if paths.length}
      <ul class="paths" aria-label="Config path that would be used">
        <li class="paths-label">Would manage:</li>
        {#each paths as p (p)}
          <li><code>{p}</code></li>
        {/each}
      </ul>
    {/if}
  {/if}

  <div class="tool-actions">
    {#if tool.installed && models.length >= 1}
      <select class="tool-model-select" id={`model-${tool.id}`} aria-label="Model"
        value={selectedModel}
        onchange={(e) => onModelChange(tool.id, e.currentTarget.value)}>
        <option value="">Use profile default</option>
        {#each models as m (m)}
          <option value={m}>{m}</option>
        {/each}
      </select>
    {/if}
    {#if !tool.installed && !tool.custom}
      <button class="btn-primary sm install-btn" type="button" onclick={() => onInstall(tool.id)}
        disabled={busy} title="Install this tool with npm">
        {busy ? "Installing…" : "Install"}
      </button>
    {/if}
    <button class="btn-primary sm" type="button" onclick={() => onApply(tool.id)}
      disabled={!canApply}
      title={!tool.installed ? "Tool is not installed" : !hasSavedProfile ? "Save a profile first" : undefined}>
      Apply
    </button>
    <button class="btn-ghost sm" type="button" onclick={() => onRestore(tool.id)}
      disabled={!canRestore}
      title={!tool.installed ? "Tool is not installed" : !canRestore ? "Nothing to restore" : undefined}>
      Restore default
    </button>
    {#if tool.custom}
      <button class="btn-ghost sm danger" type="button" onclick={() => onRemove(tool.id)}
        disabled={busy} title="Remove this custom provider from MintSwitch">
        {busy ? "Working…" : "Remove provider"}
      </button>
    {:else if tool.installed}
      <button class="btn-ghost sm danger" type="button" onclick={() => onUninstall(tool.id)}
        disabled={busy} title="Uninstall this tool with npm">
        {busy ? "Working…" : "Uninstall"}
      </button>
    {/if}
    {#if showMcp}
      <label class={`mcp-inject ${mcpBusy ? "is-busy" : ""}`} title={mcpTitle}>
        <input class="mcp-inject-input" type="checkbox"
          checked={mcpChecked} disabled={mcpBusy}
          onchange={(e) => onMcpToggle(tool.id, e.currentTarget.checked)} />
        <span class="mcp-inject-label">{mcpBusy ? "Working…" : "Context Engine"}</span>
        {#if mcpExternal}
          <span class="mcp-inject-external">(external)</span>
        {/if}
      </label>
    {/if}
  </div>
</article>

<style>
  .tool { display: flex; flex-direction: column; gap: 0.6rem; }
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
  .tool-name {
    margin: 0;
    font-size: 1.02rem;
    font-weight: 700;
    color: var(--text);
    line-height: 1.25;
    overflow-wrap: anywhere;
  }
  .tool-subtitle {
    margin: 0;
    font-size: 0.78rem;
    color: var(--muted);
    line-height: 1.3;
    overflow-wrap: anywhere;
  }
  /* Custom-styled to match .field-input: native chevron removed (appearance:
     none) and replaced with a single inline-SVG chevron whose stroke tracks
     --muted per theme. padding-right clears the chevron so long model names
     never collide with it. Content-sized so it sits inline with the action
     buttons rather than claiming a full-width row; a min-width keeps short
     names like "opus4.8" from looking empty and it wraps to its own line when
     the actions row runs out of space. */
  .tool-model-select {
    width: auto;
    min-width: 7rem;
    max-width: 12rem;
    padding: 0.36rem 1.8rem 0.36rem 0.7rem; /* matches .btn-*.sm vertical box; right pad clears chevron */
    font-size: 0.84rem; /* matches .btn-*.sm */
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
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    outline: none;
    cursor: pointer;
    appearance: none;
    -webkit-appearance: none;
    transition: border-color 0.15s ease, box-shadow 0.15s ease;
  }
  :global([data-theme="dark"]) .tool-model-select {
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='8' viewBox='0 0 12 8' fill='none'%3E%3Cpath d='M1 1.5 6 6.5 11 1.5' stroke='%2398989d' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  }
  .tool-model-select:hover { border-color: var(--muted); }
  .tool-model-select:focus-visible { border-color: var(--accent); box-shadow: var(--focus); }
  .badge.install { flex: 0 0 auto; align-self: flex-start; }
  .status { align-self: flex-start; }
  .paths {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
  .paths code {
    font-size: 0.76rem;
    color: var(--muted);
    word-break: break-all;
  }
  .paths-label {
    font-size: 0.72rem;
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--muted);
  }

  /* Not-installed: visually dimmed to signal config controls are inert. Apply
     and Restore are disabled in the markup; the Install button stays usable, so
     we lift it back to full strength as the card's primary call to action. */
  .tool.is-uninstalled {
    opacity: 0.6;
    filter: saturate(0.7);
  }
  .tool.is-uninstalled .tool-name { color: var(--muted); }
  .tool.is-uninstalled .install-btn { opacity: 1; filter: none; }
  .tool-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.4rem;
    margin-top: auto;
    padding-top: 0.4rem;
  }
  .install .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--muted);
    box-shadow: none;
  }
  .install.on .dot {
    background: var(--ok);
    box-shadow: none;
  }

  /* Context Engine inject: a compact labelled checkbox pinned to the trailing
     edge of the action row (margin-left:auto pushes it past Apply/Restore/
     Uninstall). accent-color keeps it on-brand; the global :focus-visible ring
     covers keyboard focus. */
  .mcp-inject {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    flex: 0 0 auto;
    margin-left: auto;
    cursor: pointer;
    user-select: none;
    font-size: 0.84rem;
    font-weight: 600;
    color: var(--text);
    white-space: nowrap;
  }
  .mcp-inject.is-busy { cursor: default; color: var(--muted); }
  .mcp-inject-input {
    width: 1rem;
    height: 1rem;
    margin: 0;
    flex: 0 0 auto;
    accent-color: var(--accent);
    cursor: inherit;
  }
  .mcp-inject-input:disabled { cursor: default; }
  .mcp-inject-label { line-height: 1; }
  .mcp-inject-external {
    line-height: 1;
    font-weight: 500;
    color: var(--muted);
  }
</style>
