<script lang="ts">
  import type { ToolView } from "../../bindings/mintswitch/internal/service";
  import { statusMeta } from "./ui";

  interface Props {
    tool: ToolView;
    hasSavedProfile: boolean;
    busy: boolean;
    onApply: (id: string) => void;
    onRestore: (id: string) => void;
    onInstall: (id: string) => void;
    onUninstall: (id: string) => void;
  }
  let { tool, hasSavedProfile, busy, onApply, onRestore, onInstall, onUninstall }: Props = $props();

  // Provider logos are self-contained app-icon SVGs under /logos/<id>.svg. If a
  // tool has no asset (or it fails to load) we fall back to a neutral monogram
  // tile so the card layout never breaks.
  const LOGO_IDS = new Set(["claude-code", "codex", "opencode", "factory-droid", "pi"]);
  let logoFailed = $state(false);
  const logoSrc = $derived(LOGO_IDS.has(tool.id) ? `/logos/${tool.id}.svg` : null);
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
    {#if !tool.installed}
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
    {#if tool.installed}
      <button class="btn-ghost sm danger" type="button" onclick={() => onUninstall(tool.id)}
        disabled={busy} title="Uninstall this tool with npm">
        {busy ? "Working…" : "Uninstall"}
      </button>
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
</style>
