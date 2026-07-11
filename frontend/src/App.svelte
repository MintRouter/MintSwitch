<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { Service } from "../bindings/mintswitch/internal/service";
  import type {
    ToolView,
    ProfileView,
    InstallResult,
  } from "../bindings/mintswitch/internal/service";
  import type { Profile } from "../bindings/mintswitch/internal/core";
  import { errMsg, npmCommand } from "./lib/ui";
  import ProfileForm from "./lib/ProfileForm.svelte";
  import ToolCard from "./lib/ToolCard.svelte";
  import ConfirmDialog from "./lib/ConfirmDialog.svelte";
  import PromoBanner from "./lib/PromoBanner.svelte";

  const emptyProfile: ProfileView = {
    label: "", base_url: "", models: [], model_names: {}, model: "", small_fast_model: "", has_key: false,
  };

  let tools = $state<ToolView[]>([]);
  let profile = $state<ProfileView>(emptyProfile);
  let loading = $state(true);
  let loadError = $state("");
  let saving = $state(false);
  let refreshing = $state(false);
  let busyIds = $state<string[]>([]);
  let installLog = $state<InstallResult | null>(null);
  let toast = $state<{ msg: string } | null>(null);
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

  function flash(msg: string): void {
    toast = { msg };
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (toast = null), 5000);
  }

  onDestroy(() => clearTimeout(toastTimer));

  async function refresh(): Promise<void> {
    const [t, p] = await Promise.all([
      Service.ListTools(),
      Service.GetProfile(),
    ]);
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

  async function saveProfile(p: Profile): Promise<boolean> {
    saving = true;
    try {
      await Service.SaveProfile(p);
      await refresh();
      return true;
    } catch (e) {
      flash(errMsg(e));
      return false;
    } finally {
      saving = false;
    }
  }

  // Auto-save from the Models dialog (add/remove/default change). Unlike
  // saveProfile it returns the error message instead of flashing a toast so
  // the dialog can show the failure inline; it still refreshes so the tool
  // cards' model dropdowns pick up the change immediately.
  async function saveProfileQuiet(p: Profile): Promise<string | null> {
    saving = true;
    try {
      await Service.SaveProfile(p);
      await refresh();
      return null;
    } catch (e) {
      return errMsg(e);
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

  // Safety valve: if an operation never settles, unlock the UI after 10 minutes
  // and tell the user. The underlying backend call may still complete later.
  const BUSY_TIMEOUT_MS = 10 * 60 * 1000;

  async function withBusy(id: string, fn: () => Promise<void>): Promise<void> {
    busyIds = [...busyIds, id];
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      busyIds = busyIds.filter((x) => x !== id);
      flash("Operation timed out. Please try again.");
    }, BUSY_TIMEOUT_MS);
    try {
      await fn();
    } finally {
      clearTimeout(timer);
      if (!timedOut) busyIds = busyIds.filter((x) => x !== id);
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
          await Service.ApplyOne(id);
        } catch (e) {
          flash(errMsg(e));
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
          await Service.RestoreOne(id);
        } catch (e) {
          flash(errMsg(e));
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
          if (!r.ok) flash(r.error || `Couldn't install ${name}.`);
        } catch (e) {
          flash(errMsg(e));
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
          if (!r.ok) flash(r.error || `Couldn't uninstall ${name}.`);
        } catch (e) {
          flash(errMsg(e));
        }
        await refresh();
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
    await refresh();
  }

</script>

<svelte:window onfocus={onWindowFocus} />
<svelte:document onvisibilitychange={onVisibility} />

<div class="app">
  <div class="titlebar" aria-hidden="true">
    <span class="titlebar-title">MintSwitch</span>
  </div>
  <header class="topbar">
    <!-- HOTFIX-PILL2: the wrapper mirrors .col-scroll's scroll-container CSS
         (same overflow + scrollbar-gutter + 3px webkit scrollbar) so the
         pill's right edge lands pixel-exact on the Endpoint card's — see the
         .topbar-brand-col rules below. -->
    <div class="topbar-brand-col">
      <div class="topbar-brand">
        <!-- Route Switch mark (design/logo-final/mark-master.svg geometry):
             optical square crop 4 4 40 40 keeps the mark's ink filling the
             16px box, so the 7px flex gap stays the real visual whitespace
             (feedback #39). Active route/nodes use --accent (brand blue per
             theme); the alternate branch uses --logo-alt (mint teal). -->
        <svg class="logo-mark" viewBox="4 4 40 40" width="16" height="16" fill="none" aria-hidden="true" focusable="false">
          <path d="M20 24C27 24 27 36 34 36H38" stroke="var(--logo-alt)" stroke-width="4.5" stroke-linecap="round" />
          <path d="M8.5 24H20C27 24 27 12 34 12H38" stroke="var(--accent)" stroke-width="4.5" stroke-linecap="round" />
          <circle cx="8.5" cy="24" r="4" fill="var(--accent)" />
          <circle cx="38" cy="12" r="4" fill="var(--accent)" />
          <circle cx="38" cy="36" r="4" fill="var(--logo-alt)" />
        </svg>
        <span class="wordmark">MintSwitch</span>
      </div>
    </div>
    <!-- Promo banner (user feedback #9/#10/#15/#16 + bulk-button removal):
         compact two-line navy banner sized to its content + Telegram tile,
         sitting on the right just before the utility cluster (Multilogin-style);
         the flexible free space sits between the brand block and the banner.
         HOTFIX-PILL: banner + cluster live in one right-side wrapper occupying
         the topbar grid's second column, so the brand pill can span column 1. -->
    <div class="topbar-right">
      <PromoBanner compact />
      <div class="topbar-cluster">
        <button
          class="btn-ghost sm theme-toggle"
          type="button"
          onclick={toggleTheme}
          aria-pressed={theme === "dark"}
          aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
        >
          {#if theme === "dark"}
            <svg class="theme-icon" viewBox="0 0 24 24" width="20" height="20" fill="none"
              stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
              aria-hidden="true" focusable="false">
              <circle cx="12" cy="12" r="4" />
              <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" />
            </svg>
          {:else}
            <svg class="theme-icon" viewBox="0 0 24 24" width="20" height="20" fill="none"
              stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
              aria-hidden="true" focusable="false">
              <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79Z" />
            </svg>
          {/if}
        </button>
      </div>
    </div>
  </header>

  <div class="layout">
    <section class="col-form" aria-label="Profile">
      <div class="col-scroll">
        <ProfileForm {profile} {saving} onSave={saveProfile} onAutoSave={saveProfileQuiet} />
      </div>
    </section>

    <section class="col-tools" aria-label="Tools">
      {#if loading}
        <div class="state" role="status" aria-live="polite">Loading tools…</div>
      {:else if loadError}
        <div class="state error" role="alert">
          <p>Couldn't load: {loadError}</p>
          <button class="btn-primary sm" type="button" onclick={load}>Retry</button>
        </div>
      {:else}
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
                onInstall={installOne} onUninstall={uninstallOne}
                onModelChange={changeToolModel} />
            {/each}
          </div>
        {/if}
      {/if}
    </section>
  </div>
</div>

{#if toast}
  <div class="toast error" role="alert" aria-live="assertive">
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
    /* The body is frameless (feedback #21): the panel area carries its own
       side/bottom margins so the gray window bg frames the white panels. */
    margin: 0 var(--s-3) var(--s-3);
    display: grid;
    /* Left profile column is a compact inspector — kept noticeably narrower than
       the tools area so the form fields/dropdowns sit at a natural width rather
       than stretching full-bleed. The right column keeps a comfortable 2+ cols. */
    grid-template-columns: minmax(280px, 320px) 1fr;
    /* The two white blocks sit almost flush (user feedback #5/#5b): the whole
       visual seam is the 3px scrollbar-gutter reserved by .col-scroll (stable,
       so no shift when its scrollbar shows) — grid gap adds nothing on top. */
    gap: 0;
    align-items: stretch;
  }
  /* Left column: the brand title stays pinned while the profile form scrolls
     internally when its content (e.g. many models) is taller than the column,
     so the shell itself never scrolls. */
  .col-form {
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  /* The inspector column scrolls as one region so the shell itself never
     scrolls; each card sizes to its content. */
  .col-scroll {
    flex: 1 1 auto;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: var(--s-3);
    overflow-y: auto;
    /* This gutter IS the whole seam between the two white blocks (feedback
       #5b), so it must stay exactly 3px: don't set the standard
       scrollbar-width/color here — they'd disable the ::-webkit-scrollbar
       rules below and widen the reserved gutter to the UA "thin" width
       (11px). The 3px rail is still draggable and wheel/trackpad scrolling is
       unaffected; being gutter-reserved, its appearance never shifts layout. */
    scrollbar-gutter: stable;
  }
  .col-scroll::-webkit-scrollbar { width: 3px; height: 3px; }
  .col-scroll::-webkit-scrollbar-track { background: transparent; }
  .col-scroll::-webkit-scrollbar-thumb {
    background: color-mix(in srgb, var(--muted) 55%, transparent);
    border-radius: 8px;
  }
  .col-scroll::-webkit-scrollbar-thumb:hover {
    background: color-mix(in srgb, var(--muted) 70%, transparent);
  }
  /* The whole tools column is ONE rounded surface panel (Multilogin content
     area): tool cards nest inside it. Padding keeps the internal scrollbar of
     .tool-grid clear of the panel's border and rounded corners. */
  .col-tools {
    display: flex;
    flex-direction: column;
    min-height: 0;
    padding: 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  /* Responsive card grid in left→right reading order. Rows split the panel
     height evenly (feedback #33): 1fr auto-rows + stretch fill the panel to
     its bottom padding — no dead strip under the last row; cards stay tidy
     because .tool-actions anchors to each card's bottom (margin-top auto),
     mirroring the left card's #32 pattern. With many cards the rows floor at
     their content height and only this region scrolls, as before. */
  .tool-grid {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    grid-auto-rows: 1fr;
    gap: 12px;
    align-items: stretch;
    align-content: stretch;
    scrollbar-gutter: stable;
    scrollbar-width: thin;
    scrollbar-color: color-mix(in srgb, var(--muted) 55%, transparent) transparent;
  }
  .tool-grid::-webkit-scrollbar { width: 8px; height: 8px; }
  .tool-grid::-webkit-scrollbar-track { background: transparent; }
  .tool-grid::-webkit-scrollbar-thumb {
    background: color-mix(in srgb, var(--muted) 55%, transparent);
    border-radius: 8px;
  }
  .tool-grid::-webkit-scrollbar-thumb:hover {
    background: color-mix(in srgb, var(--muted) 70%, transparent);
  }
  @media (max-width: 860px), (max-height: 600px) {
    /* Stacked: no scrollbar gutter sits between sections, so restore the full
       12px row gap. */
    .layout { grid-template-columns: 1fr; min-height: 0; gap: 12px; }
    .col-tools { min-height: 0; }
    /* Stacked: single column so the whole page scrolls normally and cards size
       to their content (no 1fr stretch — the panel has no fixed height here). */
    .tool-grid {
      overflow: visible;
      flex: none;
      min-height: 0;
      max-height: none;
      grid-template-columns: 1fr;
      grid-auto-rows: auto;
      align-content: start;
      align-items: start;
    }
    /* Stacked: let the whole page scroll instead of the column scrolling on its own. */
    .col-scroll { overflow: visible; flex: none; }
  }

  /* Titlebar strip (feedback #21 → #25 FINAL): a WHITE --surface band flush
     with the window top (traffic lights + centered bold title), exactly
     like Multilogin's titlebar. Below it the topbar row sits on the GRAY
     window bg — no hard hairline between them, the surface change itself
     is the seam. Reserves the frameless macOS traffic-light zone and acts
     as the window drag handle. */
  .titlebar {
    flex: 0 0 auto;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--surface);
    --wails-draggable: drag;
  }
  .titlebar-title {
    font-size: 15px;
    font-weight: var(--fw-bold);
    line-height: var(--lh-tight);
    letter-spacing: var(--tracking-tight);
    color: var(--text);
  }

  /* Real app top bar (feedback #21 → #24 FINAL): a quiet strip on the GRAY
     window bg, exactly like Multilogin's chrome — the white blocks (brand
     pill, navy banner, Telegram/theme tiles) float on it by contrast; the
     same gray then reads as a 6px breathing gap before the white panels.
     Left: the brand pill (logo + wordmark + hairline + Re-detect); right:
     the compact MintRouter.AI promo + Telegram + theme toggle tiles. */
  .topbar {
    flex: 0 0 auto;
    /* HOTFIX-PILL: the topbar mirrors .layout's grid (same template columns;
       side padding = .layout's var(--s-3) side margins) so the brand pill
       spans exactly the left Endpoint-profile column and its right edge
       lines up with the card's. */
    display: grid;
    grid-template-columns: minmax(280px, 320px) 1fr;
    gap: 0;
    align-items: center;
    /* Even vertical rhythm (feedback #26 → #16 tightened to half): the gray
       breathing space ABOVE the row (titlebar → pill/tiles) and BELOW it
       (row → white panels) is the same 6px. The row itself is exactly the
       40px pill/banner/tile height — no vertical padding or border to skew
       the two gaps. */
    margin: 6px 0;
    padding: 0 var(--s-3);
    border: 0;
    border-radius: 0;
  }
  /* Right side of the topbar (grid column 2): promo banner + utility tiles
     pinned to the right edge, keeping the old var(--s-2) rhythm between them. */
  .topbar-right {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    flex-wrap: wrap;
    gap: var(--s-2);
    min-width: 0;
  }
  /* HOTFIX-PILL2 (supersedes HOTFIX-PILL's hard-coded 3px pill inset): the
     Endpoint card's right edge sits at its grid column's right edge MINUS
     whatever scrollbar gutter the engine reserves for .col-scroll — 3px in
     Chromium (the styled ::-webkit-scrollbar), but 0 in WKWebView/Wails,
     where overlay scrollbars reserve no gutter, so any fixed inset is wrong
     somewhere. This wrapper occupies the topbar grid's column 1 and IS a
     scroll container with the exact same overflow + scrollbar-gutter +
     3px ::-webkit-scrollbar rules as .col-scroll: the engine reserves the
     identical gutter here, and the pill (stretching to the wrapper's
     content box) ends pixel-exact on the card's right edge in every engine.
     It never actually scrolls (the content is the fixed 40px pill; the
     padding below is included in the scroll height). The padding/negative-
     margin pair keeps the pill's shadow unclipped on the top/left/bottom;
     on the right it clips at the gutter — exactly like the card's own
     shadow inside .col-scroll. */
  .topbar-brand-col {
    min-width: 0;
    overflow-y: auto;
    scrollbar-gutter: stable;
    padding: 18px 0 18px 18px;
    margin: -18px 0 -18px -18px;
  }
  .topbar-brand-col::-webkit-scrollbar { width: 3px; height: 3px; }
  /* Brand pill (feedback #24, Multilogin's left pill): logo + wordmark +
     vertical hairline + icon-only Re-detect live inside one WHITE rounded
     card (--surface, whisper of card shadow, no border) that floats on the
     gray chrome, height-matched to the 40px banner/tiles. Corner radius is
     --radius-sm (8px) to match the COMPACT promo banner/Telegram tile beside
     it (feedback #39: the old 12px read visibly rounder than the banner). */
  .topbar-brand {
    display: flex;
    align-items: center;
    /* Tight logo ↔ wordmark rhythm (feedback #26): 7px, like the crop's
       logo↔MULTILOGIN spacing; the hairline keeps its wider 14px/side
       breathing room via its own margins below. */
    gap: 7px;
    min-width: 0;
    height: 40px;
    padding: 0 12px;
    background: var(--surface);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-card);
  }
  /* Stacked breakpoint (must come AFTER the base .topbar rules to win the
     cascade): .layout is one column, so the pill has no card column to
     mirror — fall back to the previous flex row (content-sized pill,
     banner + cluster on the right, wrapping when tight). */
  @media (max-width: 860px), (max-height: 600px) {
    .topbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      flex-wrap: wrap;
      gap: var(--s-2);
    }
    /* Stacked: .layout is one column, so there is no card column (and no
       .col-scroll gutter) to mirror — the wrapper dissolves so the pill is a
       plain content-sized flex item, as before. */
    .topbar-brand-col { display: contents; }
  }
  /* 16px box = the mark's ink size; the SVG viewBox is cropped to the ink so
     the flex gap is the real visual whitespace (feedback #39). */
  .logo-mark { flex: 0 0 auto; width: 16px; height: 16px; }
  .wordmark {
    font-size: var(--fs-title);
    font-weight: var(--fw-bold);
    line-height: var(--lh-tight);
    letter-spacing: var(--tracking-tight);
    color: var(--text);
  }
  /* Theme toggle (feedback #18 → #24 FINAL): a 40×40 --surface tile matching
     the Telegram tile — WHITE floating on the gray chrome by pure contrast,
     border dropped per the Multilogin crop (no visible outline). 20px
     sun/moon glyph in the --text ink; hover darkens the tile slightly; the
     global focus ring is untouched. */
  .topbar-cluster {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .topbar-cluster .btn-ghost.theme-toggle {
    flex: 0 0 auto;
    width: 40px;
    min-height: 40px;
    padding: 0;
    background: var(--surface);
    border-color: transparent;
    border-radius: var(--radius-sm);
    color: var(--text);
  }
  .topbar-cluster .btn-ghost.theme-toggle:hover:not(:disabled) {
    filter: brightness(0.96);
    background: var(--surface);
    border-color: transparent;
  }
  .theme-icon { display: block; flex: 0 0 auto; }

  .install-log {
    margin-bottom: var(--s-2);
    padding: 0.75rem 0.85rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface-2);
  }
  .install-log.ok {
    border-color: var(--border);
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
</style>
