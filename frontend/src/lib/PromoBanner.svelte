<script lang="ts">
  import { Browser } from "@wailsio/runtime";
  import { t } from "./i18n.svelte";

  interface Props {
    /** Topbar variant: a tall 40px two-line banner sized to its content. */
    compact?: boolean;
  }
  let { compact = false }: Props = $props();

  // Main banner opens the MintRouter.AI website; the Telegram tile opens the
  // community invite link (user feedback #10).
  const SITE_URL = "https://mintrouter.ai";
  const TELEGRAM_URL = "https://t.me/mintrouter";

  // Open in the SYSTEM browser. In the desktop app Wails routes this through
  // Browser.OpenURL (native open); in web/server mode the runtime call can't
  // reach a desktop app, so fall back to a regular new-tab open.
  function openUrl(url: string): void {
    Browser.OpenURL(url).catch(() => {
      window.open(url, "_blank", "noopener,noreferrer");
    });
  }
</script>

<!-- Promo row (Multilogin-style): a navy ad banner opening mintrouter.ai plus
     a separate Telegram tile opening the community link. Default sizing suits
     a column footer; `compact` shrinks it to topbar control height. -->
<div class="promo-row" class:compact>
  <button
    class="promo-main"
    type="button"
    onclick={() => openUrl(SITE_URL)}
    aria-label={t("promo.site")}
  >
    <img class="promo-logo" src="favicon.svg" alt="" aria-hidden="true" />
    <span class="promo-lines">
      <span class="promo-title">Mint<span class="promo-accent">Router.AI</span></span>
    </span>
    <svg class="promo-chevron" viewBox="0 0 24 24" width="16" height="16" fill="none"
      stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"
      aria-hidden="true" focusable="false">
      <path d="M9 6l6 6-6 6" />
    </svg>
  </button>
  <button
    class="promo-telegram"
    type="button"
    onclick={() => openUrl(TELEGRAM_URL)}
    aria-label={t("promo.telegram")}
    title="Telegram"
  >
    <svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor"
      aria-hidden="true" focusable="false">
      <path d="M9.78 18.65l.28-4.23 7.68-6.92c.34-.31-.07-.46-.52-.19L7.74 13.3 3.64 12c-.88-.25-.89-.86.2-1.3l15.97-6.16c.73-.33 1.43.18 1.15 1.3l-2.72 12.81c-.19.91-.74 1.13-1.5.71L12.6 16.3l-1.99 1.93c-.23.23-.42.42-.83.42z" />
    </svg>
  </button>
</div>

<style>
  /* Row layout mirrors the crop: main banner takes the width, Telegram tile is
     a square card beside it. The 3px right margin matches the scrollbar gutter
     .col-scroll reserves, so the row's right edge lines up with the cards. */
  .promo-row {
    flex: 0 0 auto;
    display: flex;
    align-items: stretch;
    gap: 8px;
    margin-top: var(--s-3);
    margin-right: 3px;
    min-width: 0;
  }

  .promo-main,
  .promo-telegram {
    appearance: none;
    border: 0;
    margin: 0;
    padding: 0;
    font: inherit;
    cursor: pointer;
    border-radius: var(--radius);
    transition: filter 120ms ease, transform 120ms ease, background-color 120ms ease;
  }
  .promo-main:active,
  .promo-telegram:active {
    transform: translateY(1px);
  }

  /* Deep-navy ad banner: intentionally the same in both themes (it's a brand
     surface, like Multilogin's), with white text that passes AA on the navy. */
  .promo-main {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 7px;
    height: 52px;
    padding: 0 12px 0 14px;
    text-align: left;
    color: #ffffff;
    background:
      radial-gradient(120% 180% at 100% 0%, rgba(77, 141, 255, 0.35), transparent 55%),
      linear-gradient(120deg, #0a1a3f 0%, #122c66 100%);
  }
  .promo-main:hover {
    filter: brightness(1.15);
  }
  /* Reuses the hub favicon (white rounded tile + blue 6-node hub) sitting
     tight against the wordmark — 7px flex gap per the crop (feedback #10). */
  .promo-logo {
    flex: 0 0 auto;
    display: block;
    width: 24px;
    height: 24px;
  }
  /* Two stacked text lines RIGHT of the logo (feedback #15): bold wordmark on
     top, smaller promo copy below, both starting at the same left edge — so
     the subline lines up under the wordmark, not under the logo. Being a
     child column of the fixed-height banner, the subline can never render
     outside the navy block. */
  .promo-lines {
    min-width: 0;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
  }
  .promo-title {
    min-width: 0;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    line-height: 1.15;
    font-size: var(--fs-body);
    font-weight: var(--fw-bold);
    letter-spacing: var(--tracking-tight);
  }
  .promo-accent {
    color: #8ab6ff;
  }
  .promo-chevron {
    flex: 0 0 auto;
    margin-left: auto; /* pins to the far end when the banner is stretched */
    color: rgba(255, 255, 255, 0.8);
  }

  /* Separate Telegram button (feedback #13 → #24 FINAL): a WHITE --surface
     tile floating on the gray chrome strip by pure contrast — border
     dropped per the Multilogin crop (no visible outline). The paper-plane
     is dark #1a1a2e ink; in dark theme the tile takes the surface token
     (lighter than the window bg, so it still floats) and the ink flips to
     the light --text token. */
  .promo-telegram {
    flex: 0 0 auto;
    width: 52px;
    height: 52px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: var(--surface);
    color: #1a1a2e;
  }
  .promo-telegram:hover {
    filter: brightness(0.96);
  }
  :global([data-theme="dark"]) .promo-telegram {
    color: var(--text);
  }

  /* Compact topbar variant (feedback #10/#13/#15/#16): a tall two-line
     banner — 40px, dominating the 50px topbar like Multilogin's — that fits
     its content instead of stretching across the gap, and sits on the RIGHT,
     just before the utility cluster (margin-left: auto absorbs the free
     space after the brand block). The -4px right margin trims the topbar's
     12px flex gap so banner → tile → cluster all read as even ~8px gaps.
     The chevron hugs the banner's right edge via the small 8px right
     padding, centered against the whole two-line block. The Telegram tile
     matches the 40px height. */
  .promo-row.compact {
    flex: 0 0 auto;
    margin: 0 -4px 0 auto;
    gap: 8px;
  }
  .compact .promo-main,
  .compact .promo-telegram {
    border-radius: var(--radius-sm);
  }
  .compact .promo-main {
    flex: 0 0 auto;
    height: 40px;
    padding: 0 8px 0 14px;
  }
  .compact .promo-logo {
    width: 22px;
    height: 22px;
  }
  /* Optical vertical centering (feedback #16): the flex-centered text block
     is geometrically symmetric, but the title's line box carries dead
     leading above its caps while the subline's descenders touch its bottom
     edge — so the copy reads bottom-heavy. The 3px bottom padding lifts the
     glyphs ~1.5px, evening out the visible top/bottom breathing room. */
  .compact .promo-lines {
    padding-bottom: 3px;
  }
  .compact .promo-title {
    font-size: 16px;
  }
  .compact .promo-chevron {
    width: 16px;
    height: 16px;
    margin-left: 8px; /* replaces the auto pin: fit-content leaves no free space */
  }
  .compact .promo-telegram {
    width: 40px;
    height: 40px;
  }
  .compact .promo-telegram svg {
    width: 20px;
    height: 20px;
  }
</style>
