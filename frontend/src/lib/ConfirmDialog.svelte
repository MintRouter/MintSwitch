<script lang="ts">
  // Accessible confirmation modal shown before any write to real config files.
  // Native <dialog> semantics are emulated with role="dialog"/aria-modal so the
  // styling matches the app; focus is moved in on open, Escape and backdrop
  // close, and Tab is trapped within the dialog.
  interface Props {
    open: boolean;
    title: string;
    message: string;
    confirmLabel?: string;
    danger?: boolean;
    busy?: boolean;
    /** Inline failure notice; when set the dialog stays open so the user
     *  sees why the action did not happen (and may retry or cancel). */
    error?: string;
    onConfirm: () => void;
    onCancel: () => void;
  }
  let {
    open,
    title,
    message,
    confirmLabel = "Confirm",
    danger = false,
    busy = false,
    error = "",
    onConfirm,
    onCancel,
  }: Props = $props();

  let dialogEl = $state<HTMLDivElement | null>(null);
  let confirmBtn = $state<HTMLButtonElement | null>(null);
  let cancelBtn = $state<HTMLButtonElement | null>(null);
  // Element that had focus before the dialog opened, restored on close so
  // keyboard users return to where they were.
  let prevFocus: HTMLElement | null = null;

  // Move focus into the dialog when it opens so screen readers announce it.
  // For destructive (danger) confirms the initial focus lands on Cancel, so
  // an inertial Enter never triggers the destructive action; otherwise it
  // lands on the primary Confirm action. `danger` and the button refs are
  // read inside the microtask, so this effect re-runs only on open/close.
  $effect(() => {
    if (!open) return;
    prevFocus = document.activeElement as HTMLElement | null;
    queueMicrotask(() => (danger ? cancelBtn : confirmBtn)?.focus());
    return () => {
      if (prevFocus?.isConnected) prevFocus.focus();
      prevFocus = null;
    };
  });

  function onKeydown(e: KeyboardEvent): void {
    // Skip events an underlying dialog already handled in this same window
    // pass (e.g. the Esc that just OPENED this confirmation) — otherwise one
    // keypress would open and instantly cancel it.
    if (!open || e.defaultPrevented) return;
    if (e.key === "Escape") {
      e.preventDefault();
      if (!busy) onCancel();
      return;
    }
    if (e.key !== "Tab" || !dialogEl) return;
    const focusables = dialogEl.querySelectorAll<HTMLElement>(
      'button:not([disabled])',
    );
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = document.activeElement as HTMLElement | null;
    if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }
</script>

<svelte:window onkeydown={onKeydown} />

{#if open}
  <div
    class="backdrop"
    role="presentation"
    onclick={(e) => e.target === e.currentTarget && !busy && onCancel()}
  >
    <div
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="confirm-title"
      aria-describedby="confirm-message"
      tabindex="-1"
      bind:this={dialogEl}
    >
      <h2 class="title" id="confirm-title">{title}</h2>
      <p class="message" id="confirm-message">{message}</p>
      {#if error}
        <p class="error" role="alert">{error}</p>
      {/if}
      <div class="actions">
        <button class="btn-ghost" type="button" bind:this={cancelBtn} onclick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button
          class="btn-primary"
          class:danger
          type="button"
          bind:this={confirmBtn}
          onclick={onConfirm}
          disabled={busy}
        >
          {busy ? "Working…" : confirmLabel}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    /* Above every other dialog surface (ProvidersCard uses 55) so a
       confirmation stacked on the Manage/form dialogs renders on top. */
    z-index: 60;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--s-2);
    background: rgba(0, 0, 0, 0.4);
    -webkit-backdrop-filter: blur(4px);
    backdrop-filter: blur(4px);
    --wails-draggable: no-drag;
  }
  .dialog {
    width: 100%;
    max-width: 26rem;
    padding: var(--s-3);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  .title {
    margin: 0 0 var(--s-1);
    font-size: var(--fs-title);
    font-weight: var(--fw-bold);
    line-height: var(--lh-tight);
    letter-spacing: var(--tracking-tight);
    color: var(--text);
  }
  .message {
    margin: 0 0 var(--s-3);
    color: var(--muted);
    font-size: var(--fs-body);
    line-height: var(--lh);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .error {
    margin: calc(-1 * var(--s-2)) 0 var(--s-3);
    color: var(--danger);
    font-size: var(--fs-body);
    line-height: var(--lh);
    word-break: break-word;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--s-1);
  }
</style>
