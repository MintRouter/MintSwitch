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
  let apiKey = $state("");
  let savingKey = $state(false);
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

  // Persist the key, then clear the input and refresh so HasKey flips. The raw
  // value is never kept or echoed — the field resets to blank after saving.
  async function saveKey(): Promise<void> {
    const k = apiKey.trim();
    if (!k || savingKey) return;
    savingKey = true;
    try {
      await Service.SetMCPKey(k);
      apiKey = "";
      testResult = null;
      await refresh();
      flash("MintRouter key saved.", "success");
    } catch (e) {
      flash(errMsg(e), "error");
    } finally {
      savingKey = false;
    }
  }

  // Clear the saved key (SetMCPKey("")), so the user can rotate/remove it.
  async function clearKey(): Promise<void> {
    if (savingKey) return;
    savingKey = true;
    try {
      await Service.SetMCPKey("");
      apiKey = "";
      testResult = null;
      await refresh();
      flash("MintRouter key cleared.", "success");
    } catch (e) {
      flash(errMsg(e), "error");
    } finally {
      savingKey = false;
    }
  }

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
        flash(r.message || `Injected MintRouter MCP into ${name(id)}.`, "success");
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
        flash(r.message || `Removed MintRouter MCP from ${name(id)}.`, "success");
      } catch (e) {
        flash(errMsg(e), "error");
      }
      await refresh();
    });
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
        return "Check the MintRouter API key you saved above.";
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
  <div class="card-head">
    <h2 class="card-title" id="mcp-h">MintRouter MCP</h2>
    <p class="card-sub">Inject the MintRouter Remote MCP server into your tools.</p>
  </div>

  <p class="mcp-note">
    MintRouter proxies to Augment server-side, so it needs the Context-Engine
    opt-in enabled on your MintRouter account. Tool names such as
    <code>augment_code_search</code> appear as-is in your tools.
  </p>

  <div class="field">
    <label class="field-label" for="mcp-key">MintRouter API key</label>
    <div class="mcp-key-row">
      <input class="field-input" id="mcp-key" type="password" bind:value={apiKey}
        placeholder={hasKey ? "•••• key saved — enter a new key to replace" : "mint_…"}
        autocomplete="off" spellcheck="false" />
      <button class="btn-primary sm" type="button" onclick={saveKey}
        disabled={savingKey || !apiKey.trim()}>
        {savingKey ? "Saving…" : "Save key"}
      </button>
    </div>
    <div class="mcp-key-state">
      {#if hasKey}
        <span class="badge tone-success">Key saved</span>
        <button class="btn-ghost sm danger" type="button" onclick={clearKey}
          disabled={savingKey}>Clear</button>
      {:else}
        <span class="field-hint">No key saved yet. Stored locally and never shown again.</span>
      {/if}
      <button class="btn-ghost sm" type="button" onclick={testConnection}
        disabled={testing || !hasKey}
        title={hasKey ? undefined : "Save a key first"}>
        {testing ? "Testing…" : "Test connection"}
      </button>
    </div>
    {#if endpoint}
      <p class="field-hint">Endpoint: <code>{endpoint}</code></p>
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
  </div>

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
    <ul class="mcp-tools" aria-label="MCP tools">
      {#each tools as t (t.id)}
        {@const meta = mcpMeta(t.status)}
        {@const busy = busyIds.includes(t.id)}
        <li class="mcp-tool">
          <div class="mcp-tool-info">
            <span class="mcp-tool-name">{name(t.id)}</span>
            <span class={`badge tone-${meta.tone}`}>{meta.label}</span>
          </div>
          <div class="mcp-tool-actions">
            <button class="btn-primary sm" type="button" onclick={() => inject(t.id)}
              disabled={!hasKey || !t.installed || busy}
              title={!hasKey ? "Save a key first" : !t.installed ? "Tool is not installed" : undefined}>
              {busy ? "Working…" : "Inject"}
            </button>
            <button class="btn-ghost sm danger" type="button" onclick={() => remove(t.id)}
              disabled={busy || t.status === "not_installed" || t.status === "not_configured"}
              title={t.status === "not_configured" ? "Nothing to remove" : undefined}>
              Remove
            </button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .mcp { display: flex; flex-direction: column; gap: var(--s-2); }
  .card-head { margin-bottom: 0.25rem; }

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

  .mcp-key-row { display: flex; gap: 0.4rem; align-items: stretch; }
  .mcp-key-row .field-input { flex: 1 1 auto; min-width: 0; }
  .mcp-key-row .btn-primary { flex: 0 0 auto; }
  .mcp-key-state { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
  .mcp-key-state .field-hint { flex: 1 1 auto; }
  /* Push the Test button to the trailing edge of the state row. */
  .mcp-key-state .btn-ghost:last-child { margin-left: auto; }
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
  .mcp-tool-actions { display: flex; align-items: center; gap: 0.4rem; flex: 0 0 auto; }
</style>
