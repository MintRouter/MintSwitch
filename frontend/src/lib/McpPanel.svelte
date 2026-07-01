<script lang="ts">
  import type { MCPState } from "../../bindings/mintswitch/internal/service";

  interface Props {
    // MCP state is owned by App (single source) and passed down; this panel no
    // longer fetches it. onToggleEnabled flips the global Context Engine flag.
    mcpState: MCPState | null;
    onToggleEnabled: (enabled: boolean) => void;
    flash: (msg: string, kind: "success" | "error") => void;
  }
  let { mcpState, onToggleEnabled }: Props = $props();

  const hasKey = $derived(!!mcpState?.has_key);
  const enabled = $derived(!!mcpState?.enabled);
</script>

<section class="card mcp">
  <div class="mcp-master">
    <label class="mcp-switch" class:is-disabled={!hasKey}
      title={hasKey ? undefined : "Save your MintRouter API key in the profile first"}>
      <input class="mcp-switch-input" type="checkbox" role="switch"
        checked={enabled} disabled={!hasKey}
        onchange={(e) => onToggleEnabled(e.currentTarget.checked)} />
      <span class="mcp-switch-track" aria-hidden="true">
        <span class="mcp-switch-thumb"></span>
      </span>
      <span class="mcp-switch-text">
        <span class="mcp-switch-label">Enable Context Engine</span>
        <span class="mcp-switch-state">{enabled ? "On" : "Off"}</span>
      </span>
    </label>
  </div>
</section>

<style>
  .mcp { display: flex; flex-direction: column; gap: var(--s-2); }

  /* Single master ON/OFF switch — the panel's primary control. A native
     checkbox (role="switch") drives an accent track + sliding thumb; the visible
     label is the switch's accessible name via the wrapping <label>. */
  .mcp-master {
    display: flex;
    padding: 0.6rem 0.7rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .mcp-switch {
    display: inline-flex;
    align-items: center;
    gap: 0.6rem;
    cursor: pointer;
    user-select: none;
  }
  .mcp-switch.is-disabled { cursor: not-allowed; }
  /* Visually-hidden but focusable: the ring is drawn on the track instead. */
  .mcp-switch-input {
    position: absolute;
    width: 1px;
    height: 1px;
    margin: -1px;
    padding: 0;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    border: 0;
  }
  .mcp-switch-track {
    position: relative;
    flex: 0 0 auto;
    width: 42px;
    height: 24px;
    border-radius: 999px;
    /* Off state: solid muted fill so the control clears 3:1 against the card. */
    background: var(--muted);
    transition: background-color 0.15s ease;
  }
  .mcp-switch-thumb {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 20px;
    height: 20px;
    border-radius: 50%;
    background: #fff;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.35);
    transition: transform 0.15s ease;
  }
  .mcp-switch-input:checked + .mcp-switch-track { background: var(--accent); }
  .mcp-switch-input:checked + .mcp-switch-track .mcp-switch-thumb {
    transform: translateX(18px);
  }
  .mcp-switch-input:focus-visible + .mcp-switch-track { box-shadow: var(--focus); }
  .mcp-switch-input:disabled + .mcp-switch-track { opacity: 0.5; }
  .mcp-switch-text { display: flex; flex-direction: column; gap: 0.1rem; min-width: 0; }
  .mcp-switch-label {
    font-size: 0.9rem;
    font-weight: 700;
    color: var(--text);
    line-height: 1.2;
  }
  .mcp-switch.is-disabled .mcp-switch-label { color: var(--muted); }
  .mcp-switch-state {
    font-size: 0.76rem;
    font-weight: 600;
    color: var(--muted);
    line-height: 1.2;
  }
  @media (prefers-reduced-motion: reduce) {
    .mcp-switch-track,
    .mcp-switch-thumb { transition: none; }
  }
</style>
