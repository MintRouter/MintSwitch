<script lang="ts">
  // Sidebar-footer language switcher: a trigger button opening an upward
  // listbox menu. Focus stays on the trigger while ArrowUp/Down move the
  // active option (aria-activedescendant), Enter/Space select, Esc / outside
  // click / Tab close. Flags are inline SVGs so rendering is identical on
  // every platform (Windows has no emoji flags).
  import { getLocale, setLocale, t, type Locale } from "./i18n.svelte";

  // Option labels stay in their own language (standard for language menus)
  // so users always recognise theirs; they are never translated.
  const options: { id: Locale; label: string }[] = [
    { id: "en", label: "English" },
    { id: "vi", label: "Tiếng Việt" },
    { id: "zh", label: "中文" },
  ];

  let open = $state(false);
  let activeIndex = $state(-1);
  let wrapEl = $state<HTMLDivElement | null>(null);
  let btnEl = $state<HTMLButtonElement | null>(null);

  const current = $derived(options.find((o) => o.id === getLocale()) ?? options[0]);

  function openMenu(): void {
    open = true;
    activeIndex = options.findIndex((o) => o.id === getLocale());
  }
  function closeMenu(): void {
    open = false;
    activeIndex = -1;
  }
  function select(id: Locale): void {
    setLocale(id);
    closeMenu();
    btnEl?.focus();
  }

  function onKeydown(e: KeyboardEvent): void {
    if (!open) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        openMenu();
      }
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      closeMenu();
      return;
    }
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const d = e.key === "ArrowDown" ? 1 : -1;
      const n = options.length;
      activeIndex = activeIndex < 0 ? (d > 0 ? 0 : n - 1) : (activeIndex + d + n) % n;
      return;
    }
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      if (activeIndex >= 0) select(options[activeIndex].id);
      return;
    }
    if (e.key === "Tab") closeMenu();
  }

  // Close on any pointer press outside the switcher while the menu is open.
  $effect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      if (wrapEl && !wrapEl.contains(e.target as Node)) closeMenu();
    };
    document.addEventListener("pointerdown", onPointerDown, true);
    return () => document.removeEventListener("pointerdown", onPointerDown, true);
  });
</script>

{#snippet flag(id: Locale)}
  {#if id === "en"}
    <svg class="flag" viewBox="0 0 24 16" aria-hidden="true" focusable="false">
      <rect width="24" height="16" fill="#b22234" />
      <g fill="#fff">
        <rect y="1.23" width="24" height="1.23" /><rect y="3.69" width="24" height="1.23" />
        <rect y="6.15" width="24" height="1.23" /><rect y="8.61" width="24" height="1.23" />
        <rect y="11.07" width="24" height="1.23" /><rect y="13.53" width="24" height="1.23" />
      </g>
      <rect width="10.5" height="8.61" fill="#3c3b6e" />
      <g fill="#fff">
        <circle cx="1.9" cy="1.6" r=".55" /><circle cx="4.4" cy="1.6" r=".55" /><circle cx="6.9" cy="1.6" r=".55" /><circle cx="9.4" cy="1.6" r=".55" />
        <circle cx="3.15" cy="3.6" r=".55" /><circle cx="5.65" cy="3.6" r=".55" /><circle cx="8.15" cy="3.6" r=".55" />
        <circle cx="1.9" cy="5.6" r=".55" /><circle cx="4.4" cy="5.6" r=".55" /><circle cx="6.9" cy="5.6" r=".55" /><circle cx="9.4" cy="5.6" r=".55" />
        <circle cx="3.15" cy="7.4" r=".55" /><circle cx="5.65" cy="7.4" r=".55" /><circle cx="8.15" cy="7.4" r=".55" />
      </g>
    </svg>
  {:else if id === "vi"}
    <svg class="flag" viewBox="0 0 24 16" aria-hidden="true" focusable="false">
      <rect width="24" height="16" fill="#da251d" />
      <polygon fill="#ffff00" transform="translate(12,8.4)"
        points="0,-4.8 1.13,-1.55 4.57,-1.48 1.83,0.59 2.82,3.88 0,1.92 -2.82,3.88 -1.83,0.59 -4.57,-1.48 -1.13,-1.55" />
    </svg>
  {:else}
    <svg class="flag" viewBox="0 0 24 16" aria-hidden="true" focusable="false">
      <rect width="24" height="16" fill="#de2910" />
      <polygon fill="#ffde00" transform="translate(4.4,4.4) scale(0.55)"
        points="0,-4.8 1.13,-1.55 4.57,-1.48 1.83,0.59 2.82,3.88 0,1.92 -2.82,3.88 -1.83,0.59 -4.57,-1.48 -1.13,-1.55" />
      <g fill="#ffde00">
        <circle cx="8.6" cy="1.7" r=".65" /><circle cx="9.9" cy="3.1" r=".65" />
        <circle cx="9.9" cy="5.1" r=".65" /><circle cx="8.6" cy="6.6" r=".65" />
      </g>
    </svg>
  {/if}
{/snippet}

<div class="locale-switcher" bind:this={wrapEl}>
  <button
    class="locale-trigger"
    type="button"
    bind:this={btnEl}
    role="combobox"
    aria-label={t("locale.label")}
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-controls="locale-listbox"
    aria-activedescendant={open && activeIndex >= 0 ? `locale-opt-${options[activeIndex].id}` : undefined}
    onclick={() => (open ? closeMenu() : openMenu())}
    onkeydown={onKeydown}
  >
    {@render flag(current.id)}
    <span class="locale-code">{current.id.toUpperCase()}</span>
    <svg class="chev" viewBox="0 0 10 6" width="9" height="6" fill="none" aria-hidden="true">
      <path d="m1 1 4 4 4-4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  </button>
  {#if open}
    <div class="locale-menu" id="locale-listbox" role="listbox" aria-label={t("locale.label")}>
      {#each options as o, i (o.id)}
        <button
          class="locale-option"
          type="button"
          role="option"
          id={`locale-opt-${o.id}`}
          tabindex="-1"
          aria-selected={o.id === getLocale()}
          class:selected={o.id === getLocale()}
          class:active={i === activeIndex}
          onmousedown={(e) => e.preventDefault()}
          onclick={() => select(o.id)}
        >
          {@render flag(o.id)}
          <span class="locale-name">{o.label}</span>
          {#if o.id === getLocale()}
            <svg class="check" viewBox="0 0 10 8" width="10" height="8" fill="none" aria-hidden="true">
              <path d="M1 4.2 3.8 7 9 1" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          {/if}
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .locale-switcher {
    position: relative;
    display: inline-flex;
    margin-right: auto; /* keeps the theme toggle pinned to the footer's right */
  }
  .locale-trigger {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 34px;
    padding: 0 9px;
    color: var(--muted);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 9px;
    cursor: pointer;
    font-size: 10.5px;
    font-weight: 650;
    transition: 0.15s;
  }
  .locale-trigger:hover {
    color: var(--text);
    border-color: var(--border-strong);
    background: var(--surface-hover);
  }
  .locale-trigger:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .locale-code {
    line-height: 1;
    letter-spacing: 0.04em;
  }
  .chev {
    flex: 0 0 auto;
  }
  .flag {
    display: block;
    width: 18px;
    height: 12px;
    flex: 0 0 auto;
    border-radius: 2px;
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--border) 80%, transparent);
  }
  /* Menu opens UPWARD: the switcher sits in the sidebar footer at the bottom
     of the window, so downward would clip. */
  .locale-menu {
    position: absolute;
    bottom: calc(100% + 6px);
    left: 0;
    z-index: 60;
    min-width: 148px;
    display: flex;
    flex-direction: column;
    padding: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
    box-shadow: var(--shadow-pop);
  }
  .locale-option {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 8px;
    text-align: left;
    color: var(--text);
    background: transparent;
    border: 0;
    border-radius: 7px;
    cursor: pointer;
    font-size: 11.5px;
    font-weight: 550;
  }
  .locale-option:hover,
  .locale-option.active {
    background: var(--surface-hover);
  }
  .locale-option.selected {
    font-weight: 700;
  }
  .locale-name {
    flex: 1;
    min-width: 0;
  }
  .check {
    flex: 0 0 auto;
    color: var(--accent);
  }
</style>
