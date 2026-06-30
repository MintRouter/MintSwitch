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
    onConfirm,
    onCancel,
  }: Props = $props();

  let dialogEl = $state<HTMLDivElement | null>(null);
  let confirmBtn = $state<HTMLButtonElement | null>(null);

  // Move focus into the dialog when it opens so keyboard users land on the
  // primary action and screen readers announce the dialog.
  $effect(() => {
    if (open && confirmBtn) confirmBtn.focus();
  });

  function onKeydown(e: KeyboardEvent): void {
    if (!open) return;
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
      <div class="actions">
        <button class="btn-ghost" type="button" onclick={onCancel} disabled={busy}>
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
    z-index: 50;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--s-2);
    background: rgba(6, 7, 15, 0.66);
    -webkit-backdrop-filter: blur(4px);
    backdrop-filter: blur(4px);
    --wails-draggable: no-drag;
  }
  .dialog {
    width: 100%;
    max-width: 26rem;
    padding: var(--s-3);
    background: rgba(16, 20, 33, 0.96);
    border: 1px solid var(--glass-border);
    border-radius: var(--radius);
    box-shadow: 0 24px 60px rgba(0, 0, 0, 0.55);
  }
  .title {
    margin: 0 0 0.5rem;
    font-size: 1.15rem;
    font-weight: 700;
    color: var(--text);
  }
  .message {
    margin: 0 0 var(--s-3);
    color: var(--muted);
    font-size: 0.95rem;
    line-height: 1.5;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
  }
</style>
