<script lang="ts">
  import { Service } from "../../bindings/mintswitch/internal/service";
  import type {
    MCPState,
    MCPTestResult,
  } from "../../bindings/mintswitch/internal/service";
  import { errMsg } from "./ui";

  interface Props {
    // MCP state is owned by App (single source) and passed down; this panel no
    // longer fetches it. onToggleEnabled flips the global Context Engine flag.
    mcpState: MCPState | null;
    onToggleEnabled: (enabled: boolean) => void;
    flash: (msg: string, kind: "success" | "error") => void;
  }
  let { mcpState, onToggleEnabled, flash }: Props = $props();

  let testing = $state(false);
  let testResult = $state<MCPTestResult | null>(null);

  const hasKey = $derived(!!mcpState?.has_key);
  const enabled = $derived(!!mcpState?.enabled);
  const endpoint = $derived(mcpState?.endpoint ?? "");
  // Master is OFF but non-destructive (option A): already-injected tools keep
  // status "configured_by_mintswitch" and stay active, yet per-card controls are
  // hidden. Count them so we can surface a discoverable, non-alarming note.
  const configuredCount = $derived(
    (mcpState?.tools ?? []).filter((t) => t.status === "configured_by_mintswitch").length,
  );

  async function testConnection(): Promise<void> {
    if (testing) return;
    testing = true;
    try {
      testResult = await Service.TestMCPConnection();
    } catch (e) {
      testResult = null;
      flash(errMsg(e), "error");
    } finally {
      testing = false;
    }
  }

  // Short, action-oriented hint layered under the backend meaning for a failed
  // test. The meaning already explains the code; the hint says what to do next.
  function testHint(r: MCPTestResult): string {
    if (r.ok) return "";
    switch (r.status) {
      case 401:
        return "Check the MintRouter API key saved in your profile above.";
      case 403:
        return "Ask your admin to enable the Context-Engine opt-in for your MintRouter account.";
      case 404:
        return "Enable MCP (Context-Engine opt-in) on your MintRouter account, or verify the endpoint.";
      case 429:
        return "Wait a moment, then test again.";
      case 0:
        return "Check your network connection and that the endpoint is reachable.";
      default:
        return "";
    }
  }
</script>

<section class="card mcp" aria-labelledby="mcp-h">
  <div class="card-head mcp-head">
    <div class="mcp-head-text">
      <h2 class="card-title" id="mcp-h">Context Engine</h2>
      <p class="card-sub">
        Adds MintRouter's Context Engine — code search and context retrieval — to
        your AI tools, using the MintRouter API key saved in your profile above.
      </p>
    </div>
    <button class="btn-ghost sm mcp-test-btn" type="button" onclick={testConnection}
      disabled={testing || !hasKey}
      title={hasKey ? undefined : "Save your MintRouter API key in the profile first"}>
      {testing ? "Testing…" : "Test connection"}
    </button>
  </div>

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

  <p class="mcp-note">
    MintRouter proxies to Augment server-side, so it needs the Context-Engine
    opt-in enabled on your MintRouter account. Tool names such as
    <code>augment_code_search</code> appear as-is in your tools.
  </p>

  {#if !hasKey}
    <p class="mcp-hint" role="note">
      Save your MintRouter API key in the profile above to enable Context Engine.
    </p>
  {/if}

  {#if !enabled && configuredCount > 0}
    <p class="mcp-hint" role="note">
      Context Engine is off, but {configuredCount === 1
        ? "1 tool still has it configured"
        : `${configuredCount} tools still have it configured`}. Turn it on to
      manage or remove them per tool.
    </p>
  {/if}

  {#if endpoint}
    <p class="field-hint mcp-endpoint">Endpoint: <code>{endpoint}</code></p>
  {/if}
  {#if testResult}
    <div class={`mcp-test ${testResult.ok ? "ok" : "err"}`}
      role={testResult.ok ? "status" : "alert"}>
      <span class="mcp-test-mark" aria-hidden="true">{testResult.ok ? "✓" : "✕"}</span>
      <span class="mcp-test-body">
        <span class="mcp-test-msg">{testResult.meaning}</span>
        {#if testHint(testResult)}
          <span class="mcp-test-hint">{testHint(testResult)}</span>
        {/if}
      </span>
    </div>
  {/if}
</section>

<style>
  .mcp { display: flex; flex-direction: column; gap: var(--s-2); }
  .card-head { margin-bottom: 0.25rem; }

  /* Header: title/subtitle on the left, Test-connection on the trailing edge. */
  .mcp-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 0.6rem;
  }
  .mcp-head-text { min-width: 0; }
  .mcp-test-btn { flex: 0 0 auto; margin-top: 0.1rem; }

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

  /* Inline call-to-action when no profile key is saved yet: accent left rule so
     it reads as guidance, not an error. */
  .mcp-hint {
    margin: 0;
    padding: 0.55rem 0.7rem;
    font-size: 0.8rem;
    line-height: 1.45;
    color: var(--text);
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-left: 3px solid var(--accent);
    border-radius: 8px;
  }
  .mcp-endpoint { margin-top: -0.1rem; }

  /* Non-blocking explainer. Muted, low-emphasis so it informs without alarming. */
  .mcp-note {
    margin: 0;
    padding: 0.55rem 0.7rem;
    font-size: 0.78rem;
    line-height: 1.45;
    color: var(--muted);
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .mcp-note code {
    font-size: 0.74rem;
    padding: 0.02rem 0.25rem;
    border-radius: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    word-break: break-all;
  }

  .field-hint code {
    font-size: 0.74rem;
    padding: 0.02rem 0.25rem;
    border-radius: 4px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    word-break: break-all;
  }

  /* Test-connection result: colour-coded left rule mirroring the toast pattern. */
  .mcp-test {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.15rem;
    padding: 0.55rem 0.7rem;
    font-size: 0.82rem;
    line-height: 1.4;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .mcp-test.ok { border-left: 3px solid var(--ok); }
  .mcp-test.err { border-left: 3px solid var(--danger); }
  .mcp-test-mark { font-weight: 800; color: var(--danger); }
  .mcp-test.ok .mcp-test-mark { color: var(--ok); }
  .mcp-test-body { display: flex; flex-direction: column; gap: 0.2rem; min-width: 0; }
  .mcp-test-msg { color: var(--text); }
  .mcp-test-hint { color: var(--muted); font-size: 0.78rem; }
</style>
