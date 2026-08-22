<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { Service } from "../bindings/mintswitch/internal/service";
  import type {
    ToolView,
    ProviderView,
    InstallResult,
    ToolOpResult,
  } from "../bindings/mintswitch/internal/service";
  import type { Provider } from "../bindings/mintswitch/internal/core";
  import { errMsg, npmCommand } from "./lib/ui";
  import ProvidersCard from "./lib/ProvidersCard.svelte";
  import ToolCard from "./lib/ToolCard.svelte";
  import ConfirmDialog from "./lib/ConfirmDialog.svelte";
  import PromoBanner from "./lib/PromoBanner.svelte";

  let tools = $state<ToolView[]>([]);
  let providers = $state<ProviderView[]>([]);
  // ProvidersCard instance so tool cards' "Add provider…" CTA can open its
  // Add-provider form directly.
  let providersCard = $state<{ openAddProvider: () => void } | null>(null);
  let loading = $state(true);
  let loadError = $state("");
  // Count of in-flight provider mutations; `saving` derives from it so two
  // overlapping ops can't have the first one clear the flag while the second
  // is still running.
  let savingCount = $state(0);
  const saving = $derived(savingCount > 0);
  let refreshing = $state(false);
  let busyIds = $state<string[]>([]);
  let installLog = $state<InstallResult | null>(null);
  let toast = $state<{ msg: string; tone: "success" | "error" } | null>(null);
  let toastTimer: ReturnType<typeof setTimeout>;

  let confirm = $state<{
    open: boolean; title: string; message: string; confirmLabel: string;
    danger: boolean; busy: boolean; action: () => Promise<void>;
  }>({ open: false, title: "", message: "", confirmLabel: "Confirm", danger: false, busy: false, action: async () => {} });

  // Theme is applied to <html data-theme> by an inline head script before first
  // paint (no flash). Mirror that into reactive state so the toggle stays in sync.
  let theme = $state<"light" | "dark">(
    typeof document !== "undefined" &&
      document.documentElement.getAttribute("data-theme") === "dark"
      ? "dark"
      : "light",
  );

  function toggleTheme(): void {
    const next = theme === "dark" ? "light" : "dark";
    theme = next;
    document.documentElement.setAttribute("data-theme", next);
    try {
      localStorage.setItem("mintswitch-theme", next);
    } catch (e) {
      /* private mode / storage disabled — keep the in-memory choice */
    }
  }

  function flash(msg: string, tone: "success" | "error" = "error"): void {
    toast = { msg, tone };
    clearTimeout(toastTimer);
    // Errors stay until dismissed or replaced by a newer toast; successes
    // auto-clear after a few seconds.
    if (tone === "success") toastTimer = setTimeout(() => (toast = null), 5000);
  }

  onDestroy(() => clearTimeout(toastTimer));

  // Monotonic token so a stale (slow) refresh response can never clobber the
  // state written by a newer one when several callers overlap (focus,
  // redetect, finishing ops).
  let refreshSeq = 0;

  async function refresh(): Promise<void> {
    const seq = ++refreshSeq;
    const [t, p] = await Promise.all([
      Service.ListTools(),
      Service.ListProviders(),
    ]);
    if (seq !== refreshSeq) return;
    tools = t ?? [];
    providers = p ?? [];
  }

  // refresh() for fire-and-forget call sites inside actions: a failure is
  // surfaced as a toast instead of an unhandled rejection.
  async function safeRefresh(): Promise<void> {
    try {
      await refresh();
    } catch (e) {
      flash(errMsg(e));
    }
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

  // The silent auto-refreshes below share one in-flight guard so overlapping
  // triggers (focus + a finishing op) don't stampede.
  async function redetect(silent = false): Promise<void> {
    if (loading || refreshing) return;
    refreshing = true;
    try {
      await refresh();
    } catch (e) {
      if (!silent) flash(errMsg(e));
    } finally {
      refreshing = false;
    }
  }

  // Re-detect when the window/tab regains focus so a tool installed while the
  // app is open is picked up without a manual click.
  function onWindowFocus(): void {
    void redetect(true);
  }
  function onVisibility(): void {
    if (document.visibilityState === "visible") void redetect(true);
  }

  // Provider mutations from the Providers card / Manage dialog. Each returns
  // the error message (instead of flashing a toast) so the dialog can show the
  // failure inline; each refreshes so the tool cards pick up the change
  // immediately (on failure the refresh re-syncs the UI to persisted state).
  async function providerOp(fn: () => Promise<unknown>): Promise<string | null> {
    savingCount++;
    try {
      await fn();
      await refresh();
      return null;
    } catch (e) {
      return errMsg(e);
    } finally {
      savingCount--;
    }
  }

  const addProvider = (p: Provider) => providerOp(() => Service.AddProvider(p));
  const updateProvider = (p: Provider) => providerOp(() => Service.UpdateProvider(p));
  const removeProvider = (id: string) => providerOp(() => Service.RemoveProvider(id));
  const setActiveProvider = (id: string) => providerOp(() => Service.SetActiveProvider(id));

  function ask(opts: { title: string; message: string; confirmLabel: string; danger?: boolean; action: () => Promise<void> }): void {
    confirm = {
      open: true, busy: false,
      title: opts.title, message: opts.message,
      confirmLabel: opts.confirmLabel, danger: !!opts.danger,
      action: opts.action,
    };
  }

  async function runConfirm(): Promise<void> {
    confirm.busy = true;
    try {
      await confirm.action();
    } finally {
      confirm = { ...confirm, open: false, busy: false };
    }
  }

  // After 10 minutes without settling, tell the user the operation is still
  // running — but keep the tool locked. Unlocking early would let a second
  // click start an overlapping config write while the first backend call is
  // still in flight (the Go service methods take no context, so a client-side
  // CancellablePromise.cancel() cannot actually stop them). The lock is only
  // released when the promise settles.
  const BUSY_TIMEOUT_MS = 10 * 60 * 1000;

  async function withBusy(id: string, fn: () => Promise<void>): Promise<void> {
    busyIds = [...busyIds, id];
    const timer = setTimeout(() => {
      flash("This operation is taking longer than expected. It is still running; the tool stays locked until it finishes.");
    }, BUSY_TIMEOUT_MS);
    try {
      await fn();
    } finally {
      clearTimeout(timer);
      busyIds = busyIds.filter((x) => x !== id);
    }
  }

  function applyOne(id: string): void {
    const name = tools.find((t) => t.id === id)?.name ?? id;
    ask({
      title: `Apply profile to ${name}?`,
      message: "This writes your profile to the tool's real config file (a backup is created first).",
      confirmLabel: "Apply",
      action: () => withBusy(id, async () => {
        try {
          const r = await Service.ApplyOne(id);
          flash(r.message || "Applied.", "success");
        } catch (e) {
          flash(errMsg(e));
        }
        await safeRefresh();
      }),
    });
  }

  function restoreOne(id: string): void {
    const name = tools.find((t) => t.id === id)?.name ?? id;
    ask({
      title: `Restore ${name} to default?`,
      message: "This reverts the tool's config to its pre-apply state from the backup.",
      confirmLabel: "Restore", danger: true,
      action: () => withBusy(id, async () => {
        try {
          const r = await Service.RestoreOne(id);
          flash(r.message || "Restored.", "success");
        } catch (e) {
          flash(errMsg(e));
        }
        await safeRefresh();
      }),
    });
  }

  function installOne(id: string): void {
    const name = tools.find((t) => t.id === id)?.name ?? id;
    const cmd = npmCommand("install", id);
    ask({
      title: `Install ${name}?`,
      message: `This runs the following command to install ${name} globally with npm:\n\n${cmd}`,
      confirmLabel: "Install",
      action: () => withBusy(id, async () => {
        try {
          const r = await Service.Install(id);
          installLog = r;
          if (!r.ok) flash(r.error || `Couldn't install ${name}.`);
        } catch (e) {
          flash(errMsg(e));
        }
        await safeRefresh();
      }),
    });
  }

  function uninstallOne(id: string): void {
    const name = tools.find((t) => t.id === id)?.name ?? id;
    const cmd = npmCommand("uninstall", id);
    ask({
      title: `Uninstall ${name}?`,
      message: `This runs the following command to remove ${name} globally with npm:\n\n${cmd}`,
      confirmLabel: "Uninstall", danger: true,
      action: () => withBusy(id, async () => {
        try {
          const r = await Service.Uninstall(id);
          installLog = r;
          if (!r.ok) flash(r.error || `Couldn't uninstall ${name}.`);
        } catch (e) {
          flash(errMsg(e));
        }
        await safeRefresh();
      }),
    });
  }

  // Persist a per-tool model selection then refresh so the tool's badge and
  // selected_model reflect the change. An empty model clears the override (the
  // tool falls back to the profile default). Always refresh — on failure it
  // re-syncs the dropdown to the actually persisted value.
  async function changeToolModel(id: string, model: string): Promise<void> {
    try {
      await Service.SetToolModel(id, model);
    } catch (e) {
      flash(errMsg(e));
    }
    await safeRefresh();
  }

  // Persist a per-tool apply mode ("one" or "all") then refresh: ListTools
  // re-evaluates status against the new mode, so an applied tool flips to
  // "modified" (needs re-apply) when its on-disk config no longer matches.
  async function changeToolApplyMode(id: string, mode: string): Promise<void> {
    try {
      await Service.SetToolApplyMode(id, mode);
    } catch (e) {
      flash(errMsg(e));
    }
    await safeRefresh();
  }

  // Persist (or clear, with an empty providerID) a per-tool provider
  // override, then refresh so the providers dialog and the tool cards'
  // provider badge pick up the change. Returns the error message (instead of
  // flashing) so the dialog can show it inline; always refreshes to re-sync
  // the select on failure.
  async function changeToolProvider(id: string, providerID: string): Promise<string | null> {
    let err: string | null = null;
    try {
      await Service.SetToolProvider(id, providerID);
    } catch (e) {
      err = errMsg(e);
    }
    await safeRefresh();
    return err;
  }

  const activeProvider = $derived(providers.find((p) => p.active) ?? null);
  const installedCount = $derived(tools.filter((t) => t.installed).length);
  const appliedCount = $derived(tools.filter((t) => t.status === "applied_by_mintswitch").length);
  const modifiedCount = $derived(tools.filter((t) => t.status === "modified_externally").length);
  const canApplyAll = $derived(!!activeProvider && installedCount > 0 && busyIds.length === 0);
  const canRestoreAll = $derived(tools.some((t) => t.installed && t.status !== "default" && t.status !== "not_installed") && busyIds.length === 0);
  // Summarize a bulk apply/restore. On failure, name the failing tools and
  // include the first error (truncated) so the toast is actionable instead of
  // just a count.
  function summarizeBulk(results: ToolOpResult[] | null, verb: string): void {
    const list = results ?? [];
    const failed = list.filter((r) => !r.ok);
    if (!failed.length) {
      flash(`${verb} completed for ${list.length} tool${list.length === 1 ? "" : "s"}.`, "success");
      return;
    }
    const toolName = (id: string) => tools.find((t) => t.id === id)?.name ?? id;
    const names = failed.map((r) => toolName(r.id)).join(", ");
    const firstError = failed.find((r) => r.error)?.error ?? "";
    const detail = firstError ? `: ${firstError.length > 140 ? `${firstError.slice(0, 140)}…` : firstError}` : ".";
    flash(`${verb} failed for ${names}${detail}`);
  }

  function applyAll(): void {
    ask({
      title: "Apply to all installed tools?",
      message: "MintSwitch will back up each existing configuration, then apply the effective provider and model to every installed tool.",
      confirmLabel: "Apply to all",
      action: () => withBusy("__all__", async () => {
        try {
          summarizeBulk(await Service.ApplyAll(), "Apply");
        } catch (e) {
          flash(errMsg(e));
        }
        await safeRefresh();
      }),
    });
  }

  function restoreAll(): void {
    ask({
      title: "Restore all tool configurations?",
      message: "Every configuration managed by MintSwitch will be restored from its pre-apply backup.",
      confirmLabel: "Restore all", danger: true,
      action: () => withBusy("__all__", async () => {
        try {
          summarizeBulk(await Service.RestoreAll(), "Restore");
        } catch (e) {
          flash(errMsg(e));
        }
        await safeRefresh();
      }),
    });
  }

</script>
<svelte:window onfocus={onWindowFocus} />
<svelte:document onvisibilitychange={onVisibility} />

<div class="app-shell">
  <div class="titlebar" aria-hidden="true">MintSwitch</div>
  <div class="workspace">
    <aside class="sidebar" aria-label="MintSwitch settings">
      <div class="brand-block">
        <div class="brand-mark" aria-hidden="true">
          <svg viewBox="4 4 40 40" width="22" height="22" fill="none">
            <path d="M20 24C27 24 27 36 34 36H38" stroke="var(--logo-alt)" stroke-width="4.5" stroke-linecap="round" />
            <path d="M8.5 24H20C27 24 27 12 34 12H38" stroke="currentColor" stroke-width="4.5" stroke-linecap="round" />
            <circle cx="8.5" cy="24" r="4" fill="currentColor" /><circle cx="38" cy="12" r="4" fill="currentColor" /><circle cx="38" cy="36" r="4" fill="var(--logo-alt)" />
          </svg>
        </div>
        <div><p class="brand-name">MintSwitch</p><p class="brand-tagline">AI tool configuration</p></div>
      </div>

      <div class="sidebar-scroll">
        <ProvidersCard bind:this={providersCard} {providers} {tools} {saving}
          onAdd={addProvider} onUpdate={updateProvider} onRemove={removeProvider}
          onSetActive={setActiveProvider} onToolProviderChange={changeToolProvider} />
        <section class="health-card" aria-labelledby="health-title">
          <div class="section-label" id="health-title">Workspace health</div>
          <div class="health-grid">
            <div class="health-stat"><strong>{installedCount}</strong><span>Installed</span></div>
            <div class="health-stat success"><strong>{appliedCount}</strong><span>Applied</span></div>
            <div class="health-stat" class:warning={modifiedCount > 0}><strong>{modifiedCount}</strong><span>Modified</span></div>
          </div>
        </section>
      </div>

      <PromoBanner />

      <div class="sidebar-footer">
        <div class="security-note">
          <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><rect x="4" y="10" width="16" height="11" rx="3" /><path d="M8 10V7a4 4 0 0 1 8 0v3" /></svg>
          <span>Keys stored in keychain</span>
        </div>
        <button class="icon-button" type="button" onclick={toggleTheme} aria-pressed={theme === "dark"}
          aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}>
          {#if theme === "dark"}
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" /></svg>
          {:else}
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79Z" /></svg>
          {/if}
        </button>
      </div>
    </aside>

    <main class="main-panel">
      <header class="main-header">
        <div class="heading-copy"><div class="eyebrow">Workspace</div><h1>Your AI tools</h1><p>Choose a model and keep every local tool in sync.</p></div>
        <div class="header-actions">
          <button class="icon-button" type="button" onclick={() => void redetect()} disabled={refreshing || loading} aria-label="Refresh detected tools" title="Refresh detected tools">
            <svg class:spinning={refreshing} viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M20 11a8 8 0 1 0 2 5.3" /><path d="M20 4v7h-7" /></svg>
          </button>
          <button class="btn-ghost bulk-restore" type="button" onclick={restoreAll} disabled={!canRestoreAll}>Restore all</button>
          <button class="btn-primary bulk-apply" type="button" onclick={applyAll} disabled={!canApplyAll}>
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="m5 12 4 4L19 6" /></svg>Apply to all
          </button>
        </div>
      </header>

      <div class="content-scroll">
        {#if loading}
          <div class="state-card" role="status" aria-live="polite"><span class="loader" aria-hidden="true"></span><div><strong>Detecting tools</strong><span>Reading local configuration…</span></div></div>
        {:else if loadError}
          <div class="state-card error" role="alert"><div><strong>We couldn't load your tools</strong><span>{loadError}</span></div><button class="btn-primary" type="button" onclick={load}>Try again</button></div>
        {:else}
          {#if installLog}
            <div class="install-log" class:ok={installLog.ok} aria-label="Last command result">
              <span class="install-mark" aria-hidden="true">{installLog.ok ? "✓" : "!"}</span>
              <div class="install-copy"><strong>{installLog.ok ? "Command completed" : "Command needs attention"}</strong><code>{installLog.command}</code>
                {#if !installLog.ok && installLog.error}<p role="alert">{installLog.error}</p>{/if}
                {#if installLog.output}<pre aria-label="Command output">{installLog.output}</pre>{/if}
              </div>
              <button class="icon-button" type="button" onclick={() => (installLog = null)} aria-label="Dismiss result">×</button>
            </div>
          {/if}
          {#if tools.length === 0}
            <div class="empty-state"><div class="empty-icon" aria-hidden="true">⌘</div><h2>No tools detected</h2><p>Install a supported AI coding tool, then refresh this workspace.</p></div>
          {:else}
            <div class="tool-grid">
              {#each tools as t (t.id)}
                <ToolCard tool={t} busy={busyIds.includes(t.id) || busyIds.includes("__all__")} {providers}
                  onApply={applyOne} onRestore={restoreOne} onInstall={installOne} onUninstall={uninstallOne} onModelChange={changeToolModel}
                  onApplyModeChange={changeToolApplyMode} onProviderUpdate={updateProvider}
                  onAddProvider={() => providersCard?.openAddProvider()} />
              {/each}
            </div>
          {/if}
        {/if}
      </div>
    </main>
  </div>
</div>

{#if toast}<div class="toast" class:success={toast.tone === "success"} class:error={toast.tone === "error"} role={toast.tone === "error" ? "alert" : "status"}><span class="toast-icon" aria-hidden="true">{toast.tone === "success" ? "✓" : "!"}</span>{toast.msg}<button class="toast-dismiss" type="button" onclick={() => (toast = null)} aria-label="Dismiss notification">×</button></div>{/if}
<ConfirmDialog open={confirm.open} title={confirm.title} message={confirm.message} confirmLabel={confirm.confirmLabel} danger={confirm.danger} busy={confirm.busy} onConfirm={runConfirm} onCancel={() => (confirm = { ...confirm, open: false })} />

<style>
  .app-shell{height:100dvh;display:flex;flex-direction:column;background:var(--ink);overflow:hidden}.titlebar{flex:0 0 30px;display:flex;align-items:center;justify-content:center;border-bottom:1px solid var(--border);background:var(--chrome);color:var(--muted);font-size:11px;font-weight:600;--wails-draggable:drag}.workspace{flex:1;min-height:0;display:grid;grid-template-columns:292px minmax(0,1fr)}
  .sidebar{min-height:0;display:flex;flex-direction:column;padding:16px 14px 12px;background:var(--sidebar);border-right:1px solid var(--border)}.brand-block{display:flex;align-items:center;gap:10px;padding:0 4px 16px}.brand-mark{width:38px;height:38px;display:grid;place-items:center;color:var(--accent);border-radius:11px;background:var(--accent-soft);border:1px solid color-mix(in srgb,var(--accent) 18%,transparent)}.brand-name{margin:0;font-size:16px;line-height:1.15;font-weight:750;letter-spacing:-.025em}.brand-tagline{margin:3px 0 0;color:var(--muted);font-size:11px}.sidebar-scroll{flex:1;min-height:0;overflow-y:auto;display:flex;flex-direction:column;gap:11px;padding:1px 2px 12px}.sidebar-scroll::-webkit-scrollbar,.content-scroll::-webkit-scrollbar{width:7px}.sidebar-scroll::-webkit-scrollbar-thumb,.content-scroll::-webkit-scrollbar-thumb{background:var(--scrollbar);border-radius:99px;border:2px solid transparent;background-clip:padding-box}
  .health-card{padding:16px;border:1px solid var(--border);border-radius:14px;background:var(--surface);box-shadow:var(--shadow-card)}.section-label,.eyebrow{color:var(--muted);font-size:11px;font-weight:700;letter-spacing:.08em;text-transform:uppercase}.health-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:6px;margin-top:12px}.health-stat{padding:10px 2px;text-align:center;border-radius:10px;background:var(--surface-2)}.health-stat strong{display:block;font-size:18px;line-height:1}.health-stat span{display:block;margin-top:6px;color:var(--muted);font-size:11px}.health-stat.success strong{color:var(--ok)}.health-stat.warning strong{color:var(--warn)}
  aside.sidebar :global(.promo-row.promo-row){margin:0 2px 12px}
  .sidebar-footer{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:11px 2px 0;border-top:1px solid var(--border)}.security-note{display:flex;align-items:center;gap:7px;color:var(--muted);font-size:11.5px}.security-note svg{color:var(--ok)}.icon-button{width:34px;min-height:34px;padding:0;display:inline-grid;place-items:center;flex:0 0 auto;color:var(--muted);background:var(--surface);border:1px solid var(--border);border-radius:9px;cursor:pointer;transition:.15s}.icon-button:hover:not(:disabled){color:var(--text);border-color:var(--border-strong);background:var(--surface-hover)}.icon-button:disabled{opacity:.45;cursor:default}
  .main-panel{min-width:0;min-height:0;display:flex;flex-direction:column}.main-header{flex:0 0 auto;min-height:94px;padding:16px 22px;display:flex;align-items:center;justify-content:space-between;gap:18px;border-bottom:1px solid var(--border)}.heading-copy h1{margin:4px 0 3px;font-size:22px;line-height:1.15;font-weight:760;letter-spacing:-.035em}.heading-copy p{margin:0;color:var(--muted);font-size:12px}.header-actions{display:flex;align-items:center;gap:7px}.header-actions .btn-primary,.header-actions .btn-ghost{min-height:34px}.bulk-apply{box-shadow:0 5px 16px color-mix(in srgb,var(--accent) 24%,transparent)}.spinning{animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}
  .content-scroll{flex:1;min-height:0;overflow-y:auto;padding:16px 22px 24px}.tool-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:11px}
  .state-card,.empty-state{min-height:180px;display:flex;align-items:center;justify-content:center;gap:13px;padding:24px;border:1px dashed var(--border-strong);border-radius:16px;background:var(--surface);color:var(--muted)}.state-card>div{display:flex;flex-direction:column;gap:3px}.state-card strong{color:var(--text);font-size:13px}.state-card span{font-size:11px}.loader{width:20px;height:20px;border:2px solid var(--border-strong);border-top-color:var(--accent);border-radius:50%;animation:spin .8s linear infinite}.empty-state{flex-direction:column;text-align:center}.empty-state h2{margin:0;color:var(--text);font-size:16px}.empty-state p{margin:0;font-size:11px}.empty-icon{width:42px;height:42px;display:grid;place-items:center;border-radius:12px;background:var(--surface-2);color:var(--accent)}
  .install-log{display:flex;align-items:flex-start;gap:9px;margin-bottom:12px;padding:11px;border:1px solid color-mix(in srgb,var(--danger) 25%,var(--border));border-radius:13px;background:color-mix(in srgb,var(--danger) 5%,var(--surface))}.install-log.ok{border-color:color-mix(in srgb,var(--ok) 25%,var(--border));background:color-mix(in srgb,var(--ok) 5%,var(--surface))}.install-mark{width:22px;height:22px;display:grid;place-items:center;border-radius:50%;color:var(--danger);font-weight:800}.install-log.ok .install-mark{color:var(--ok)}.install-copy{flex:1;min-width:0;display:flex;flex-direction:column;gap:3px}.install-copy strong{font-size:11px}.install-copy code,.install-copy p{margin:0;color:var(--muted);font-size:10px;word-break:break-all}.install-copy pre{max-height:130px;overflow:auto;margin:5px 0 0;padding:7px;border-radius:8px;background:var(--surface-2);font-size:9.5px;white-space:pre-wrap}.toast{top:42px;right:16px;display:flex;align-items:center;gap:8px}.toast-dismiss{margin-left:2px;padding:0 2px;border:0;background:none;color:inherit;font-size:14px;line-height:1;cursor:pointer;opacity:.7}.toast-dismiss:hover{opacity:1}.toast-icon{width:19px;height:19px;display:grid;place-items:center;border-radius:50%;background:color-mix(in srgb,currentColor 12%,transparent);font-size:10px;font-weight:800}.toast.success{color:var(--ok)}.toast.error{color:var(--danger-strong)}
  @media(max-width:860px){.workspace{grid-template-columns:252px minmax(0,1fr)}.sidebar{padding-inline:11px}.main-header,.content-scroll{padding-inline:16px}.bulk-restore{display:none}.tool-grid{grid-template-columns:1fr}}@media(max-height:620px){.main-header{min-height:80px;padding-block:11px}.heading-copy p{display:none}.content-scroll{padding-top:13px}}
</style>
