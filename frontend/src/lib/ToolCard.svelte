<script lang="ts">
  import type { ToolView } from "../../bindings/mintconfig/internal/service";
  import { statusMeta } from "./ui";

  interface Props {
    tool: ToolView;
    hasSavedProfile: boolean;
    busy: boolean;
    onApply: (id: string) => void;
    onRestore: (id: string) => void;
  }
  let { tool, hasSavedProfile, busy, onApply, onRestore }: Props = $props();

  const meta = $derived(statusMeta(tool.status));
  // Apply needs an installed tool and a saved profile (the backend fails fast
  // without one). Restore only makes sense once we've changed something.
  const canApply = $derived(tool.installed && hasSavedProfile && !busy);
  const canRestore = $derived(
    tool.installed && tool.status !== "default" && tool.status !== "not_installed" && !busy,
  );
  const paths = $derived(tool.config_paths ?? []);
</script>

<article class="card tool" aria-labelledby={`tool-${tool.id}`}>
  <div class="tool-head">
    <h3 class="tool-name" id={`tool-${tool.id}`}>{tool.name}</h3>
    <span class="badge install" class:on={tool.installed}>
      <span class="dot" aria-hidden="true"></span>
      {tool.installed ? "Installed" : "Not installed"}
    </span>
  </div>

  <span class={`badge status tone-${meta.tone}`}>{meta.label}</span>

  {#if tool.detail}
    <p class="tool-detail">{tool.detail}</p>
  {/if}

  {#if paths.length}
    <ul class="paths" aria-label="Config paths">
      {#each paths as p (p)}
        <li><code>{p}</code></li>
      {/each}
    </ul>
  {/if}

  <div class="tool-actions">
    <button class="btn-primary sm" type="button" onclick={() => onApply(tool.id)}
      disabled={!canApply}
      title={!tool.installed ? "Tool is not installed" : !hasSavedProfile ? "Save a profile first" : undefined}>
      Apply
    </button>
    <button class="btn-ghost sm" type="button" onclick={() => onRestore(tool.id)}
      disabled={!canRestore}
      title={!canRestore ? "Nothing to restore" : undefined}>
      Restore default
    </button>
  </div>
</article>

<style>
  .tool { display: flex; flex-direction: column; gap: 0.6rem; }
  .tool-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .tool-name {
    margin: 0;
    font-size: 1.02rem;
    font-weight: 700;
    color: var(--text);
    line-height: 1.3;
  }
  .status { align-self: flex-start; }
  .tool-detail {
    margin: 0;
    color: var(--muted);
    font-size: 0.88rem;
    line-height: 1.45;
  }
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
  .tool-actions {
    display: flex;
    gap: 0.5rem;
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
    background: #34d399;
    box-shadow: 0 0 7px rgba(52, 211, 153, 0.7);
  }
</style>
