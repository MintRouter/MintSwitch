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
  if (e instanceof Error) return e.message;
  if (typeof e === "string") return e;
  if (e && typeof e === "object" && "message" in e) {
    const m = (e as { message?: unknown }).message;
    if (typeof m === "string") return m;
  }
  return "Something went wrong. Please try again.";
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
  "factory-droid": { pkg: "droid" },
  pi: { pkg: "@earendil-works/pi-coding-agent", installFlags: ["--ignore-scripts"] },
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
