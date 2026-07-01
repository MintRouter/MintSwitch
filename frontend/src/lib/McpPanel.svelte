<script lang="ts">
  import { onMount } from "svelte";
  import { Service } from "../../bindings/mintswitch/internal/service";
  import type {
    MCPState,
    MCPTestResult,
  } from "../../bindings/mintswitch/internal/service";
  import { errMsg } from "./ui";
  import type { Tone } from "./ui";

  interface Props {
    // toolNames maps an MCP tool id to its display name (from ListTools) so the
    // rows read the same as the tool cards; unknown ids fall back to a prettified
    // id. It stays data-driven — the row set comes only from GetMCPState.
    toolNames?: Record<string, string>;
    flash: (msg: string, kind: "success" | "error") => void;
  }
  let { toolNames = {}, flash }: Props = $props();

  let mcpState = $state<MCPState | null>(null);
  let loading = $state(true);
  let loadError = $state("");
  let testing = $state(false);
  let testResult = $state<MCPTestResult | null>(null);
  let busyIds = $state<string[]>([]);

  const hasKey = $derived(!!mcpState?.has_key);
  const endpoint = $derived(mcpState?.endpoint ?? "");
  const tools = $derived(mcpState?.tools ?? []);

  async function refresh(): Promise<void> {
    mcpState = await Service.GetMCPState();
  }

  async function load(): Promise<void> {
    loading = true;
    loadError = "";
    try {
      await refresh();
    } catch (e) {
      loadError = errMsg(e);
    } finally {
      loading = false;
    }
  }
  onMount(load);

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

  async function withBusy(id: string, fn: () => Promise<void>): Promise<void> {
    busyIds = [...busyIds, id];
    try {
      await fn();
    } finally {
      busyIds = busyIds.filter((x) => x !== id);
    }
  }

  function inject(id: string): void {
    void withBusy(id, async () => {
      try {
        const r = await Service.InjectMCPOne(id);
        flash(r.message || `Enabled Context Engine for ${name(id)}.`, "success");
      } catch (e) {
        flash(errMsg(e), "error");
      }
      await refresh();
    });
  }

  function remove(id: string): void {
    void withBusy(id, async () => {
      try {
        const r = await Service.RemoveMCPOne(id);
        flash(r.message || `Disabled Context Engine for ${name(id)}.`, "success");
      } catch (e) {
        flash(errMsg(e), "error");
      }
      await refresh();
    });
  }

  // A tool is "on" when MintSwitch itself wrote the Context Engine config. The
  // right-hand checkbox reflects this; toggling injects (on) or removes (off).
  function isEnabled(status: string): boolean {
    return status === "configured_by_mintswitch";
  }

  // Toggle handler for the per-tool Context Engine checkbox. `checked` is the
  // control's new desired state; the underlying status is re-synced by refresh().
  function toggleTool(id: string, checked: boolean): void {
    if (checked) inject(id);
    else remove(id);
  }

  // Canonical display names for the known MCP injector ids (from GetMCPState).
  // These win over the toolNames prop so the panel reads consistently even when
  // an id has no endpoint adapter (cursor, auggie) or the adapter name carries a
  // suffix (claude-code). Any id not listed here falls back to toolNames, then a
  // prettified id — so a new injector never shows a raw kebab-case id.
  const mcpNames: Record<string, string> = {
    "claude-code": "Claude Code",
    opencode: "OpenCode",
    "factory-droid": "Factory Droid",
    cursor: "Cursor",
    auggie: "Auggie",
  };

  // Prettify a tool id (claude-code -> "Claude Code") for the fallback name.
  function prettify(id: string): string {
    return id
      .split("-")
      .filter(Boolean)
      .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
      .join(" ");
  }
  function name(id: string): string {
    return mcpNames[id] || toolNames[id] || prettify(id);
  }

  interface McpMeta {
    label: string;
    tone: Tone;
  }
  // Map the backend MCPStatus string to a display label + colour tone. Unknown
  // values fall back to a neutral label so a new status never breaks the UI.
  function mcpMeta(status: string): McpMeta {
    switch (status) {
      case "configured_by_mintswitch":
        return { label: "Configured by MintSwitch", tone: "success" };
      case "configured_externally":
        return { label: "Configured externally", tone: "warning" };
      case "not_configured":
        return { label: "Not configured", tone: "neutral" };
      case "not_installed":
        return { label: "Not installed", tone: "neutral" };
      default:
        return { label: status || "Unknown", tone: "neutral" };
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

  <p class="mcp-note">
    MintRouter proxies to Augment server-side, so it needs the Context-Engine
    opt-in enabled on your MintRouter account. Tool names such as
    <code>augment_code_search</code> appear as-is in your tools.
  </p>

  {#if !loading && !loadError && !hasKey}
    <p class="mcp-hint" role="note">
      Save your MintRouter API key in the profile above to enable Context Engine.
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

  {#if loading}
    <div class="state" role="status" aria-live="polite">Loading MCP state…</div>
  {:else if loadError}
    <div class="state error" role="alert">
      <p>Couldn't load MCP state: {loadError}</p>
      <button class="btn-primary sm" type="button" onclick={load}>Retry</button>
    </div>
  {:else if tools.length === 0}
    <div class="state">No MCP-capable tools.</div>
  {:else}
    <ul class="mcp-tools" aria-label="Context Engine tools">
      {#each tools as t (t.id)}
        {@const meta = mcpMeta(t.status)}
        {@const busy = busyIds.includes(t.id)}
        {@const enabled = isEnabled(t.status)}
        {@const disabled = !hasKey || !t.installed || busy}
        <li class="mcp-tool">
          <div class="mcp-tool-info">
            <span class="mcp-tool-name">{name(t.id)}</span>
            <span class={`badge tone-${meta.tone}`}>{meta.label}</span>
          </div>
          <label class={`mcp-toggle ${disabled ? "is-disabled" : ""}`}
            title={!hasKey
              ? "Save your MintRouter API key in the profile first"
              : !t.installed
                ? "Tool is not installed"
                : undefined}>
            <input class="mcp-toggle-input" type="checkbox"
              checked={enabled} {disabled}
              onchange={(e) => toggleTool(t.id, e.currentTarget.checked)} />
            <span class="mcp-toggle-label">{busy ? "Working…" : "Context Engine"}</span>
          </label>
        </li>
      {/each}
    </ul>
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

  /* Data-driven per-tool rows: name + status badge on the left, actions right;
     wraps cleanly in the narrow inspector column. */
  .mcp-tools {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }
  .mcp-tool {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    flex-wrap: wrap;
    padding: 0.5rem 0.6rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .mcp-tool-info {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    min-width: 0;
    flex: 1 1 auto;
    flex-wrap: wrap;
  }
  .mcp-tool-name {
    font-size: 0.9rem;
    font-weight: 700;
    color: var(--text);
    overflow-wrap: anywhere;
  }
  /* Right-aligned enable control: a native checkbox labelled "Context Engine".
     Checked = configured_by_mintswitch. accent-color keeps it on-brand in both
     themes; the shared :focus-visible ring covers keyboard focus. */
  .mcp-toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    flex: 0 0 auto;
    cursor: pointer;
    user-select: none;
    font-size: 0.84rem;
    font-weight: 600;
    color: var(--text);
    white-space: nowrap;
  }
  .mcp-toggle.is-disabled { cursor: not-allowed; color: var(--muted); }
  .mcp-toggle-input {
    width: 1rem;
    height: 1rem;
    margin: 0;
    flex: 0 0 auto;
    accent-color: var(--accent);
    cursor: inherit;
  }
  .mcp-toggle-input:disabled { cursor: not-allowed; }
  .mcp-toggle-label { line-height: 1; }
</style>
