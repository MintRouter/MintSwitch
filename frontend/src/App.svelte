<script lang="ts">
  import { onMount } from "svelte";
  import { Service } from "../bindings/mintswitch/internal/service";
  import type {
    ToolView,
    ToolOpResult,
    ProfileView,
    InstallResult,
  } from "../bindings/mintswitch/internal/service";
  import type { Profile } from "../bindings/mintswitch/internal/core";
  import { errMsg, npmCommand } from "./lib/ui";
  import ProfileForm from "./lib/ProfileForm.svelte";
  import ToolCard from "./lib/ToolCard.svelte";
  import ConfirmDialog from "./lib/ConfirmDialog.svelte";

  const emptyProfile: ProfileView = {
    label: "", base_url: "", model: "", small_fast_model: "", has_key: false,
  };

  let tools = $state<ToolView[]>([]);
  let profile = $state<ProfileView>(emptyProfile);
  let loading = $state(true);
  let loadError = $state("");
  let saving = $state(false);
  let refreshing = $state(false);
  let busyIds = $state<string[]>([]);
  let opResults = $state<ToolOpResult[] | null>(null);
  let installLog = $state<InstallResult | null>(null);
  let toast = $state<{ msg: string; kind: "success" | "error" } | null>(null);
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

  const hasSavedProfile = $derived(
    !!(profile.base_url && profile.model && profile.has_key),
  );

  function flash(msg: string, kind: "success" | "error"): void {
    toast = { msg, kind };
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (toast = null), 5000);
  }

  async function refresh(): Promise<void> {
    const [t, p] = await Promise.all([Service.ListTools(), Service.GetProfile()]);
    tools = t ?? [];
    profile = p ?? emptyProfile;
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

  // Manual "Re-detect" + the silent auto-refreshes below share one in-flight
  // guard so overlapping triggers (focus + a finishing op) don't stampede.
  async function redetect(silent = false): Promise<void> {
    if (loading || refreshing) return;
    refreshing = true;
    try {
      await refresh();
    } catch (e) {
      if (!silent) flash(errMsg(e), "error");
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

  async function saveProfile(p: Profile): Promise<boolean> {
    saving = true;
    try {
      await Service.SaveProfile(p);
      await refresh();
      flash("Profile saved.", "success");
      return true;
    } catch (e) {
      flash(errMsg(e), "error");
      return false;
    } finally {
      saving = false;
    }
  }

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

  async function withBusy(id: string, fn: () => Promise<void>): Promise<void> {
    busyIds = [...busyIds, id];
    try {
      await fn();
    } finally {
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
          flash(r.message || `Applied to ${name}.`, "success");
        } catch (e) {
          flash(errMsg(e), "error");
        }
        await refresh();
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
          flash(r.message || `Restored ${name}.`, "success");
        } catch (e) {
          flash(errMsg(e), "error");
        }
        await refresh();
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
          flash(r.ok ? `Installed ${name}.` : (r.error || `Couldn't install ${name}.`), r.ok ? "success" : "error");
        } catch (e) {
          flash(errMsg(e), "error");
        }
        await refresh();
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
          flash(r.ok ? `Uninstalled ${name}.` : (r.error || `Couldn't uninstall ${name}.`), r.ok ? "success" : "error");
        } catch (e) {
          flash(errMsg(e), "error");
        }
        await refresh();
      }),
    });
  }

  function applyAll(): void {
    ask({
      title: "Apply profile to all tools?",
      message: "This writes your profile to every installed tool's real config (backups are created first).",
      confirmLabel: "Apply to all",
      action: async () => {
        try {
          opResults = await Service.ApplyAll();
          flash("Apply to all finished.", "success");
        } catch (e) {
          flash(errMsg(e), "error");
        }
        await refresh();
      },
    });
  }

  function restoreAll(): void {
    ask({
      title: "Restore all tools to default?",
      message: "This reverts every tool's config to its pre-apply state from backups.",
      confirmLabel: "Restore all", danger: true,
      action: async () => {
        try {
          opResults = await Service.RestoreAll();
          flash("Restore all finished.", "success");
        } catch (e) {
          flash(errMsg(e), "error");
        }
        await refresh();
      },
    });
  }
</script>

<svelte:window onfocus={onWindowFocus} />
<svelte:document onvisibilitychange={onVisibility} />

<div class="app">
  <div class="layout">
    <section class="col-form" aria-label="Profile">
      <header class="brand">
        <h1 class="app-title">MintSwitch</h1>
      </header>
      <ProfileForm {profile} {saving} onSave={saveProfile} />
    </section>

    <section class="col-tools" aria-label="Tools">
      <header class="tools-head">
        <div class="tools-head-left">
          <button class="btn-ghost sm refresh" type="button" onclick={() => redetect(false)}
            disabled={refreshing} aria-label="Re-detect installed tools">
            <span class="spinner" class:spin={refreshing} aria-hidden="true"></span>
            {refreshing ? "Detecting…" : "Re-detect"}
          </button>
          <span class="sr-only" role="status" aria-live="polite">
            {refreshing ? "Re-detecting tools" : ""}
          </span>
        </div>
        <div class="global-actions">
          <button class="btn-primary sm" type="button" onclick={applyAll} disabled={!hasSavedProfile}>
            Apply to all
          </button>
          <button class="btn-ghost sm" type="button" onclick={restoreAll}>Restore all</button>
          <button
            class="btn-ghost sm theme-toggle"
            type="button"
            onclick={toggleTheme}
            aria-pressed={theme === "dark"}
            aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
          >
            <span class="theme-glyph" aria-hidden="true">{theme === "dark" ? "☀" : "☾"}</span>
            {theme === "dark" ? "Light" : "Dark"}
          </button>
        </div>
      </header>

      {#if loading}
        <div class="state" role="status" aria-live="polite">Loading tools…</div>
      {:else if loadError}
        <div class="state error" role="alert">
          <p>Couldn't load: {loadError}</p>
          <button class="btn-primary sm" type="button" onclick={load}>Retry</button>
        </div>
      {:else}
        {#if opResults}
          <ul class="results" aria-label="Last bulk operation results">
            {#each opResults as r (r.id)}
              <li class="result" class:ok={r.ok}>
                <span class="result-mark" aria-hidden="true">{r.ok ? "✓" : "✕"}</span>
                <span class="result-id">{tools.find((t) => t.id === r.id)?.name ?? r.id}</span>
                <span class="result-msg">{r.ok ? (r.message || "OK") : (r.error || "Failed")}</span>
              </li>
            {/each}
          </ul>
        {/if}

        {#if installLog}
          <div class="install-log" class:ok={installLog.ok} aria-label="Last install/uninstall result">
            <div class="install-log-head">
              <span class="install-mark" aria-hidden="true">{installLog.ok ? "✓" : "✕"}</span>
              <code class="install-cmd">{installLog.command}</code>
              <button class="btn-ghost sm" type="button" onclick={() => (installLog = null)}
                aria-label="Dismiss install output">Dismiss</button>
            </div>
            {#if !installLog.ok && installLog.error}
              <p class="install-err" role="alert">{installLog.error}</p>
            {/if}
            {#if installLog.output}
              <pre class="install-output" aria-label="Command output">{installLog.output}</pre>
            {/if}
          </div>
        {/if}

        {#if tools.length === 0}
          <div class="state">No tools detected.</div>
        {:else}
          <div class="tool-grid">
            {#each tools as t (t.id)}
              <ToolCard tool={t} {hasSavedProfile} busy={busyIds.includes(t.id)}
                onApply={applyOne} onRestore={restoreOne}
                onInstall={installOne} onUninstall={uninstallOne} />
            {/each}
          </div>
        {/if}
      {/if}
    </section>
  </div>
</div>

{#if toast}
  <div class={`toast ${toast.kind}`} role={toast.kind === "error" ? "alert" : "status"}
    aria-live={toast.kind === "error" ? "assertive" : "polite"}>
    {toast.msg}
  </div>
{/if}

<ConfirmDialog open={confirm.open} title={confirm.title} message={confirm.message}
  confirmLabel={confirm.confirmLabel} danger={confirm.danger} busy={confirm.busy}
  onConfirm={runConfirm} onCancel={() => (confirm = { ...confirm, open: false })} />

<style>
  /* Two columns: Profile (left, inspector) + Tools (right). The shell fills
     100dvh; only the tool list scrolls if it overflows — never the page. */
  .layout {
    flex: 1 1 auto;
    min-height: 0;
    display: grid;
    grid-template-columns: minmax(360px, 400px) 1fr;
    gap: var(--s-4);
    align-items: stretch;
  }
  /* Left column stays put at default size (does not scroll). */
  .col-form { min-height: 0; }
  .col-tools { display: flex; flex-direction: column; min-height: 0; }
  /* Only this region scrolls if the cards overflow; at 5 tools it fits. */
  .tool-grid {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: var(--s-2);
    align-content: start;
    padding-right: 0.25rem;
  }
  @media (max-width: 860px), (max-height: 600px) {
    .layout { grid-template-columns: 1fr; min-height: 0; }
    .col-tools { min-height: 0; }
    .tool-grid { overflow: visible; flex: none; min-height: 0; }
  }

  .theme-toggle { flex: 0 0 auto; }
  .theme-glyph { font-size: 0.95rem; line-height: 1; }

  .refresh { gap: 0.45rem; }
  .spinner {
    width: 13px;
    height: 13px;
    border-radius: 50%;
    border: 2px solid currentColor;
    border-top-color: transparent;
    opacity: 0.85;
  }
  .spinner.spin { animation: spin 0.7s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) {
    .spinner.spin { animation: none; }
  }

  .install-log {
    margin-bottom: var(--s-2);
    padding: 0.75rem 0.85rem;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--surface-2);
  }
  .install-log.ok {
    border-color: var(--border-strong);
    background: var(--surface-2);
  }
  .install-log-head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .install-mark { font-weight: 700; color: var(--danger); }
  .install-log.ok .install-mark { color: var(--ok); }
  .install-cmd {
    flex: 1;
    font-size: 0.8rem;
    color: var(--text);
    word-break: break-all;
  }
  .install-err {
    margin: 0.5rem 0 0;
    color: var(--danger);
    font-size: 0.86rem;
  }
  .install-output {
    margin: 0.5rem 0 0;
    max-height: 220px;
    overflow: auto;
    padding: 0.5rem 0.6rem;
    border-radius: 8px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--muted);
    font-size: 0.76rem;
    line-height: 1.45;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .sr-only {
    position: absolute;
    width: 1px; height: 1px;
    padding: 0; margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
</style>
