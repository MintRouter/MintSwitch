// Small presentational helpers shared across the MintSwitch UI. Pure functions,
// no Svelte/DOM dependencies, so they are trivially typecheckable and reusable.

/** Visual tone keys map to the status-badge colour classes in the components. */
export type Tone = "neutral" | "info" | "success" | "warning";

/** StatusMeta is the human label + colour tone for a backend tool status. */
export interface StatusMeta {
  label: string;
  tone: Tone;
}

/**
 * statusMeta maps a backend ToolView.status string to a display label and a
 * colour tone. Unknown values fall back to a neutral label so the UI never
 * renders a raw identifier or breaks on a new status.
 */
export function statusMeta(status: string): StatusMeta {
  switch (status) {
    case "applied_by_mintswitch":
      return { label: "Applied by MintSwitch", tone: "success" };
    case "modified_externally":
      return { label: "Modified externally", tone: "warning" };
    case "default":
      return { label: "Default config", tone: "info" };
    case "not_installed":
      return { label: "Not installed", tone: "neutral" };
    default:
      return { label: status || "Unknown", tone: "neutral" };
  }
}

/**
 * errMsg extracts a human-readable, secret-free message from an unknown thrown
 * value. Backend errors and result messages are already safe to display (the Go
 * side never includes the API key), so we only normalise the shape here.
 */
export function errMsg(e: unknown): string {
  const fallback = "Something went wrong. Please try again.";
  if (e instanceof Error) return e.message || fallback;
  if (typeof e === "string") return e || fallback;
  if (e && typeof e === "object" && "message" in e) {
    const m = (e as { message?: unknown }).message;
    if (typeof m === "string" && m) return m;
  }
  return fallback;
}

/**
 * npmPackages mirrors the backend installer whitelist (internal/installer) for
 * display only: it lets the confirm dialog preview the exact npm command before
 * the call is made. The authoritative command is still returned in
 * InstallResult.command after the operation runs.
 */
const npmPackages: Record<string, { pkg: string; installFlags?: string[] }> = {
  "claude-code": { pkg: "@anthropic-ai/claude-code" },
  codex: { pkg: "@openai/codex" },
  opencode: { pkg: "opencode-ai" },
  droid: { pkg: "droid" },
  kilo: { pkg: "@kilocode/cli" },
};

/**
 * npmCommand returns the exact npm command that Install/Uninstall will run for a
 * tool, for preview in the confirm dialog. Unknown tools return an empty string.
 */
export function npmCommand(action: "install" | "uninstall", toolID: string): string {
  const spec = npmPackages[toolID];
  if (!spec) return "";
  if (action === "uninstall") return `npm uninstall -g ${spec.pkg}`;
  const flags = spec.installFlags?.length ? `${spec.installFlags.join(" ")} ` : "";
  return `npm install -g ${flags}${spec.pkg}`;
}

/**
 * builtinLogoIds is the set of tool IDs that ship a bundled app-icon SVG under
 * /logos/<id>.svg.
 */
export const builtinLogoIds = new Set<string>([
  "claude-code",
  "codex",
  "opencode",
  "droid",
  "zed",
  "kilo",
]);

/**
 * toolLogoSrc returns the bundled logo path for a built-in tool, or null when
 * the tool has no preset icon so the card renders a monogram fallback instead.
 */
export function toolLogoSrc(id: string): string | null {
  return builtinLogoIds.has(id) ? `/logos/${id}.svg` : null;
}

/** isHttpUrl reports whether v is a non-empty http(s) URL. */
export function isHttpUrl(v: string): boolean {
  const s = v.trim();
  if (!s) return false;
  try {
    const u = new URL(s);
    return u.protocol === "http:" || u.protocol === "https:";
  } catch {
    return false;
  }
}

/**
 * isLocalHostname reports whether a hostname refers to a local or private
 * address where plain http is legitimate and must NOT be upgraded to https.
 * Mirrors the host classes in the backend core.NormalizeBaseURL.
 */
function isLocalHostname(h: string): boolean {
  // Strip brackets from IPv6 literals ([fe80::1] -> fe80::1) before matching.
  const host = h.startsWith("[") && h.endsWith("]") ? h.slice(1, -1) : h;
  if (host === "localhost") return true;
  if (host.endsWith(".local") || host.endsWith(".localhost")) return true;
  if (host === "::1") return true;
  if (host.startsWith("127.")) return true;
  if (host.startsWith("10.") || host.startsWith("192.168.")) return true;
  if (host.startsWith("169.254.")) return true;
  if (host.startsWith("172.")) {
    const second = Number(host.split(".")[1]);
    if (Number.isInteger(second) && second >= 16 && second <= 31) return true;
  }
  // IPv6 link-local (fe80::/10) and unique-local (fc00::/7 = fc/fd) keep http,
  // matching the Go backend's net.IP IsLinkLocalUnicast / IsPrivate checks. Gate
  // on a ":" so IPv6 literals match while domain names like "fc2.com" do not.
  const lower = host.toLowerCase();
  if (lower.includes(":")) {
    if (lower.startsWith("fe80")) return true;
    if (lower.startsWith("fc") || lower.startsWith("fd")) return true;
  }
  return false;
}

/**
 * normalizeBaseUrl mirrors the backend core.NormalizeBaseURL exactly: it trims
 * trailing slashes from the path and upgrades public http endpoints to https
 * (auth headers are commonly dropped on the http→https redirect). Local and
 * private hosts keep http. Invalid input is returned trimmed and untouched so
 * the caller's own validation handles it. `upgraded` is true only when the
 * scheme was switched, which is the cue to surface a non-blocking notice.
 */
export function normalizeBaseUrl(v: string): { url: string; upgraded: boolean } {
  const trimmed = v.trim();
  let u: URL;
  try {
    u = new URL(trimmed);
  } catch {
    return { url: trimmed, upgraded: false };
  }
  u.pathname = u.pathname.replace(/\/+$/, "");
  let upgraded = false;
  if (u.protocol === "http:" && !isLocalHostname(u.hostname)) {
    u.protocol = "https:";
    upgraded = true;
  }
  let url = u.toString();
  // The URL serialiser re-adds a "/" once the path is emptied; drop it when there
  // is no path/query/hash so the stored value stays bare, matching u.String().
  if (u.pathname === "/" && !u.search && !u.hash && url.endsWith("/")) {
    url = url.slice(0, -1);
  }
  return { url, upgraded };
}
