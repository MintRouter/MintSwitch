// Runes-based i18n store. The `.svelte.ts` extension lets this module use
// `$state`, so every template expression (or $derived) that calls t() re-runs
// when the locale changes — the whole UI switches language live.
//
// Lookup rules:
//   - Keys are flat strings ("tool.applyConfig") resolved in the active
//     dictionary, falling back to English, then to the key itself.
//   - Interpolation: "{name}" placeholders are replaced from params.
//   - Pluralization: when params.count is a number, "<key>.one" is tried for
//     count === 1 and "<key>.other" otherwise. Locales without grammatical
//     plurals (vi/zh) define only "<key>.other", which is the fallback.
import { en } from "../locales/en";
import { vi } from "../locales/vi";
import { zh } from "../locales/zh";

export type Locale = "en" | "vi" | "zh";
export const locales: readonly Locale[] = ["en", "vi", "zh"];

const dictionaries: Record<Locale, Record<string, string>> = { en, vi, zh };

const STORAGE_KEY = "mintswitch-locale";

function isLocale(v: unknown): v is Locale {
  return v === "en" || v === "vi" || v === "zh";
}

// Initial locale: the persisted choice wins; otherwise the browser language
// picks vi/zh when it matches, defaulting to English.
function detectLocale(): Locale {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (isLocale(stored)) return stored;
  } catch {
    /* private mode / storage disabled — fall through to detection */
  }
  const lang = (typeof navigator !== "undefined" ? navigator.language || "" : "").toLowerCase();
  if (lang.startsWith("vi")) return "vi";
  if (lang.startsWith("zh")) return "zh";
  return "en";
}

const state = $state({ locale: detectLocale() });

export function getLocale(): Locale {
  return state.locale;
}

export function setLocale(l: Locale): void {
  state.locale = l;
  try {
    localStorage.setItem(STORAGE_KEY, l);
  } catch {
    /* private mode / storage disabled — keep the in-memory choice */
  }
}

export type TParams = Record<string, string | number>;

/** t resolves a message key in the active locale with interpolation and
 *  count-based pluralization. Reading it inside a template or $derived makes
 *  that expression reactive to locale changes. */
export function t(key: string, params?: TParams): string {
  const dict = dictionaries[state.locale];
  let template: string | undefined;
  if (params && typeof params.count === "number") {
    const suffix = params.count === 1 ? "one" : "other";
    template =
      dict[`${key}.${suffix}`] ?? dict[`${key}.other`] ??
      en[`${key}.${suffix}`] ?? en[`${key}.other`];
  }
  template ??= dict[key] ?? en[key];
  if (template === undefined) return key;
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (m, name: string) =>
    name in params ? String(params[name]) : m,
  );
}
