<script lang="ts">
  import type { ToolView, ProviderView } from "../../bindings/mintswitch/internal/service";
  import type { Provider } from "../../bindings/mintswitch/internal/core";
  import { statusMeta, toolLogoSrc } from "./ui";

  interface Props {
    tool: ToolView;
    busy: boolean;
    providers: ProviderView[];
    onApply: (id: string) => void;
    onRestore: (id: string) => void;
    onInstall: (id: string) => void;
    onUninstall: (id: string) => void;
    onModelChange: (toolID: string, model: string) => void;
    onApplyModeChange: (toolID: string, mode: string) => void;
    onProviderUpdate: (p: Provider) => Promise<string | null>;
    onAddProvider: () => void;
  }
  let {
    tool, busy, providers,
    onApply, onRestore, onInstall, onUninstall, onModelChange, onApplyModeChange,
    onProviderUpdate, onAddProvider,
  }: Props = $props();

  // The provider in effect for this tool (per-tool override or the active
  // provider). Empty when no provider is configured yet — Apply then stays
  // disabled since the backend would fail fast anyway.
  const hasProvider = $derived(!!tool.selected_provider_id);

  // Provider logos are self-contained app-icon SVGs under /logos/<id>.svg. If a
  // tool has no asset or it fails to load we fall back to a neutral monogram
  // tile so the card layout never breaks.
  let logoFailed = $state(false);
  const logoSrc = $derived(toolLogoSrc(tool.id));
  const monogram = $derived((tool.name ?? "?").trim().charAt(0).toUpperCase() || "?");

  // Split the display name on its first " (" so the product name stays bold on
  // line one and the parenthetical (e.g. "CLI + IDE") becomes a small muted
  // subtitle — keeping every header tidy at ~2 lines instead of a ragged wrap.
  const nameParts = $derived.by(() => {
    const full = (tool.name ?? "").trim();
    const i = full.indexOf(" (");
    if (i === -1) return { name: full, subtitle: "" };
    let subtitle = full.slice(i + 2).trim();
    if (subtitle.endsWith(")")) subtitle = subtitle.slice(0, -1).trim();
    return { name: full.slice(0, i).trim(), subtitle };
  });

  const meta = $derived(statusMeta(tool.status));
  // ONE compact status line per card (short label + tone dot) under the title;
  // the full statusMeta sentence lives in the tooltip. Only "Applied" carries
  // the success tone — plain "Installed" stays neutral so a card never shows
  // two different greens meaning different things.
  const statusLine = $derived.by(() => {
    if (!tool.installed) return { label: "Not installed", tone: "neutral", full: meta.label };
    if (tool.status === "applied_by_mintswitch") {
      return { label: "Applied", tone: "success", full: meta.label };
    }
    if (tool.status === "modified_externally") {
      return { label: "Modified", tone: "warning", full: meta.label };
    }
    return { label: "Installed", tone: "neutral", full: meta.label };
  });
  // Apply needs an installed tool and a configured provider (the backend
  // fails fast without one). Restore only makes sense once we've changed
  // something.
  const canApply = $derived(tool.installed && hasProvider && !busy);
  const canRestore = $derived(
    tool.installed && tool.status !== "default" && tool.status !== "not_installed" && !busy,
  );

  // Per-tool model picker. The list comes from the tool's EFFECTIVE provider
  // (via the backend ToolView). selectedModel is the effective model; if it
  // isn't a member of the current list (e.g. the provider changed) we fall
  // back to the empty "Use provider default" option so the control never
  // shows a stale value.
  const models = $derived(tool.models ?? []);
  // Optional per-model display names: option labels show the friendly name
  // (fallback = the model ID) while option values stay the canonical ID.
  const modelNames = $derived(tool.model_names ?? {});
  const selectedModel = $derived(
    tool.selected_model && models.includes(tool.selected_model) ? tool.selected_model : "",
  );
  // When the model row exists, Apply sits inline at its right (one row saved);
  // otherwise it stays full-width in the actions block as before.
  const hasModelRow = $derived(tool.installed && models.length >= 1);

  // Report the choice to the parent, then immediately snap the DOM back to
  // the current state value: if the backend rejects the change, the refresh
  // leaves `selectedModel` unchanged and Svelte would otherwise skip
  // re-applying `value=`, leaving the <select> stuck on the rejected option.
  function onSelectModel(e: Event & { currentTarget: HTMLSelectElement }): void {
    const chosen = e.currentTarget.value;
    e.currentTarget.value = selectedModel;
    onModelChange(tool.id, chosen);
  }

  // Per-tool apply mode: "one" (default) applies only the selected model,
  // "all" applies every model of the effective provider — the dropdown then
  // picks the DEFAULT model. Anything unrecognized renders as "one" (matches
  // the backend's applyModeFor fallback).
  const applyMode = $derived(tool.apply_mode === "all" ? "all" : "one");
  // Claude Code / Claude Desktop only accept claude-* models in "all" mode
  // (an Anthropic-side limit) — surfaced as an ⓘ tooltip next to the toggle
  // (hover + keyboard focus, Esc to dismiss) so it takes no card height.
  const claudeOnlyAll = $derived(tool.id === "claude-code" || tool.id === "claude-desktop");
  let infoOpen = $state(false);
  function selectMode(mode: "one" | "all"): void {
    if (mode === applyMode) return;
    onApplyModeChange(tool.id, mode);
  }

  // ---- Model tiers dialog (Claude Code only) ----
  // Pins the models Claude Code uses for its opus/sonnet/haiku/fable aliases
  // and background (small/fast) tasks. The pins live on the tool's EFFECTIVE
  // provider; Save persists them through the existing UpdateProvider path
  // (api_key stays empty = keep the stored key). Nothing persists on Cancel.
  const effectiveProvider = $derived(
    providers.find((p) => p.id === tool.selected_provider_id) ?? null,
  );
  const showTiers = $derived(tool.id === "claude-code" && tool.installed && !!effectiveProvider);
  const tierModels = $derived(effectiveProvider?.models ?? []);
  const tierNames = $derived(effectiveProvider?.model_names ?? {});
  let tiersOpen = $state(false);
  let tiersSaving = $state(false);
  let tiersError = $state("");
  let tOpus = $state("");
  let tSonnet = $state("");
  let tHaiku = $state("");
  let tSmallFast = $state("");
  let tFable = $state("");
  let tiersDialogEl = $state<HTMLDivElement | null>(null);
  // Focus restore for keyboard users when the dialog closes.
  let tiersReturnFocus: HTMLElement | null = null;

  const tierRows = $derived([
    { id: "opus", label: "Opus", get: () => tOpus, set: (v: string) => (tOpus = v) },
    { id: "sonnet", label: "Sonnet", get: () => tSonnet, set: (v: string) => (tSonnet = v) },
    { id: "haiku", label: "Haiku", get: () => tHaiku, set: (v: string) => (tHaiku = v) },
    { id: "smallfast", label: "Small/Fast", get: () => tSmallFast, set: (v: string) => (tSmallFast = v) },
    { id: "fable", label: "Fable", get: () => tFable, set: (v: string) => (tFable = v) },
  ]);

  function tierName(m: string): string {
    return tierNames[m] || m;
  }

  function openTiers(): void {
    const p = effectiveProvider;
    if (!p) return;
    tiersReturnFocus = document.activeElement as HTMLElement | null;
    tOpus = p.opus_model ?? "";
    tSonnet = p.sonnet_model ?? "";
    tHaiku = p.haiku_model ?? "";
    tSmallFast = p.small_fast_model ?? "";
    tFable = p.fable_model ?? "";
    tiersError = "";
    tiersOpen = true;
  }

  function closeTiers(): void {
    if (tiersSaving) return;
    tiersOpen = false;
    tiersError = "";
    const target = tiersReturnFocus;
    tiersReturnFocus = null;
    if (target?.isConnected) queueMicrotask(() => target.focus());
  }

  async function saveTiers(): Promise<void> {
    const p = effectiveProvider;
    if (!p || tiersSaving) return;
    tiersSaving = true;
    tiersError = "";
    // Full provider payload with only the tier pins changed; an empty api_key
    // means "keep the stored one" on the backend.
    const payload: Provider = {
      id: p.id,
      name: p.name,
      note: p.note,
      api_key: "",
      base_url: p.base_url,
      models: p.models ?? [],
      model_names: p.model_names ?? {},
      model: p.model,
      small_fast_model: tSmallFast,
      opus_model: tOpus,
      sonnet_model: tSonnet,
      haiku_model: tHaiku,
      fable_model: tFable,
    };
    const err = await onProviderUpdate(payload);
    tiersSaving = false;
    if (err != null) {
      tiersError = err;
      return;
    }
    closeTiers();
  }

  // Esc dismisses the mode-info tooltip, then closes the dialog; Tab is
  // trapped inside the dialog while it is open.
  function onTiersKeydown(e: KeyboardEvent): void {
    if (e.key === "Escape" && infoOpen && !e.defaultPrevented) infoOpen = false;
    if (!tiersOpen || e.defaultPrevented) return;
    if (e.key === "Escape") {
      e.preventDefault();
      closeTiers();
      return;
    }
    if (e.key !== "Tab" || !tiersDialogEl) return;
    const focusables = tiersDialogEl.querySelectorAll<HTMLElement>(
      "button:not([disabled]), select:not([disabled])",
    );
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const activeEl = document.activeElement as HTMLElement | null;
    if (e.shiftKey && activeEl === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && activeEl === last) {
      e.preventDefault();
      first.focus();
    }
  }

  // Focus the first control when the dialog opens; runs after render so the
  // element ref exists.
  $effect(() => {
    if (tiersOpen) {
      queueMicrotask(() => tiersDialogEl?.querySelector<HTMLElement>("select")?.focus());
    }
  });
</script>

<article class="tool-card" class:is-uninstalled={!tool.installed} class:is-modified={tool.status === "modified_externally"} aria-labelledby={`tool-${tool.id}`}>
  <div class="tool-head">
    <div class="logo-wrap" class:inactive={!tool.installed}>
      {#if logoSrc && !logoFailed}
        <img class="tool-logo" src={logoSrc} alt="" width="32" height="32" loading="lazy" onerror={() => (logoFailed = true)} />
      {:else}
        <span class="tool-logo monogram" aria-hidden="true">{monogram}</span>
      {/if}
    </div>
    <div class="tool-titles">
      <h3 class="tool-name" id={`tool-${tool.id}`}>{nameParts.name}</h3>
      <p class="tool-subtitle">{nameParts.subtitle || (tool.installed ? "Local application" : "Available integration")}</p>
    </div>
    <div class={`tool-status tone-${statusLine.tone}`} aria-label={statusLine.full}>
      <span class="dot" aria-hidden="true"></span><span>{statusLine.label}</span>
    </div>
  </div>

  {#if tool.status === "modified_externally"}
    <div class="status-notice" role="status">
      <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M12 9v4m0 4h.01"/><path d="M10.3 3.7 2.4 17.4A2 2 0 0 0 4.1 20h15.8a2 2 0 0 0 1.7-2.6L13.7 3.7a2 2 0 0 0-3.4 0Z"/></svg>
      Configuration differs from the last apply
    </div>
  {/if}

  <div class="tool-body">
    {#if hasModelRow}
      <div class="model-control">
        <div class="model-head">
          <label class="control-label" for={`model-${tool.id}`}>{applyMode === "all" ? "Default model" : "Model"}</label>
          <div class="mode-controls">
            <div class="mode-toggle" role="group" aria-label={`Apply mode for ${nameParts.name}`}>
              <button type="button" class="mode-btn" class:active={applyMode === "one"} aria-pressed={applyMode === "one"} disabled={busy} onclick={() => selectMode("one")}>1 model</button>
              <button type="button" class="mode-btn" class:active={applyMode === "all"} aria-pressed={applyMode === "all"} disabled={busy} onclick={() => selectMode("all")}>All models</button>
            </div>
            {#if applyMode === "all" && claudeOnlyAll}
              <span class="info-wrap">
                <button type="button" class="info-btn" aria-label="About All models mode" aria-describedby={`allinfo-${tool.id}`}
                  onmouseenter={() => (infoOpen = true)}
                  onmouseleave={(e) => { if (document.activeElement !== e.currentTarget) infoOpen = false; }}
                  onfocus={() => (infoOpen = true)} onblur={() => (infoOpen = false)}>
                  <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v5m0-8.5h.01"/></svg>
                </button>
                <span class="info-tip" id={`allinfo-${tool.id}`} role="tooltip" class:open={infoOpen}>All mode adds Claude models only (Anthropic limit).</span>
              </span>
            {/if}
          </div>
        </div>
        <select class="tool-model-select" id={`model-${tool.id}`} aria-label={`${applyMode === "all" ? "Default model" : "Model"} for ${nameParts.name}`} value={selectedModel} disabled={busy} onchange={onSelectModel}>
          <option value="">Use provider default</option>
          {#each models as m (m)}<option value={m}>{modelNames[m] || m}</option>{/each}
        </select>
      </div>
    {:else}
      <div class="model-control placeholder" aria-hidden="true"><span>Model</span><div>{tool.installed ? "Add models to your provider" : "Install to configure"}</div></div>
    {/if}

    <div class="provider-row">
      <div class="provider-copy">
        <span class="provider-label">Provider</span>
        <strong>{tool.provider_name || "Not configured"}</strong>
      </div>
      {#if tool.provider_overridden}<span class="override-pill">Override</span>{/if}
    </div>
  </div>

  <div class="tool-actions">
    {#if !tool.installed && tool.installable}
      <button class="btn-soft primary-action" type="button" onclick={() => onInstall(tool.id)} disabled={busy}>
        {busy ? "Installing…" : "Install tool"}
      </button>
    {:else if tool.installed && !hasProvider}
      <button class="btn-soft primary-action" type="button" onclick={onAddProvider} disabled={busy}>
        Add provider…
      </button>
    {:else}
      <button class="btn-soft primary-action" type="button" onclick={() => onApply(tool.id)} disabled={!canApply}
        title={!tool.installed ? "Tool is not installed" : !hasProvider ? "Add a provider first" : undefined}>
        {busy ? "Working…" : "Apply configuration"}
      </button>
    {/if}
    <div class="secondary-actions">
      <button class="text-action" type="button" onclick={() => onRestore(tool.id)} disabled={!canRestore}>Restore</button>
      {#if showTiers}
        <button class="text-action" type="button" onclick={openTiers} disabled={busy}
          title="Pin the models used for Claude Code's opus / sonnet / haiku / fable tiers">Tiers</button>
      {/if}
      {#if tool.installed && tool.installable}
        <button class="text-action danger" type="button" onclick={() => onUninstall(tool.id)} disabled={busy}>Uninstall</button>
      {:else if !tool.installed && !tool.installable}
        <span class="manual-note">Manual installation</span>
      {/if}
    </div>
  </div>
</article>

<svelte:window onkeydown={onTiersKeydown} />

{#if tiersOpen}
  <div class="backdrop" role="presentation"
    onclick={(e) => e.target === e.currentTarget && closeTiers()}>
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby={`tiers-title-${tool.id}`}
      tabindex="-1" bind:this={tiersDialogEl}>
      <h2 class="dialog-title" id={`tiers-title-${tool.id}`}>Model tiers — {nameParts.name}</h2>
      <p class="dialog-hint">
        Pin the models Claude Code uses for its opus / sonnet / haiku / fable aliases and
        background (small/fast) tasks on <strong>{effectiveProvider?.name}</strong>.
        Empty tiers follow the default model. Re-apply the configuration for changes to take effect.
      </p>
      <div class="tiers-grid">
        {#each tierRows as tier (tier.id)}
          <label class="tier-field" for={`tc-tier-${tool.id}-${tier.id}`}>
            <span class="tier-label">{tier.label}</span>
            <select class="tier-select" id={`tc-tier-${tool.id}-${tier.id}`} value={tier.get()}
              disabled={tiersSaving} onchange={(e) => tier.set(e.currentTarget.value)}>
              <option value="">Use default model</option>
              {#each tierModels as m (m)}
                <option value={m}>{tierName(m)}</option>
              {/each}
              {#if tier.get() && !tierModels.includes(tier.get())}
                <option value={tier.get()}>{tier.get()}</option>
              {/if}
            </select>
          </label>
        {/each}
      </div>
      {#if tiersError}
        <p class="tiers-error" role="alert">Couldn't save: {tiersError}</p>
      {/if}
      <div class="dialog-actions">
        <button class="btn-ghost" type="button" onclick={closeTiers} disabled={tiersSaving}>Cancel</button>
        <button class="btn-primary" type="button" onclick={() => void saveTiers()} disabled={tiersSaving}>
          {tiersSaving ? "Saving…" : "Save tiers"}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .tool-card{min-width:0;display:flex;flex-direction:column;gap:12px;padding:14px;border:1px solid var(--border);border-radius:15px;background:var(--surface);box-shadow:var(--shadow-card);transition:transform .16s ease,border-color .16s ease,box-shadow .16s ease}.tool-card:hover{transform:translateY(-1px);border-color:var(--border-strong);box-shadow:0 2px 4px rgba(23,28,40,.04),0 12px 30px rgba(23,28,40,.07)}.tool-card.is-modified{border-color:color-mix(in srgb,var(--warn) 25%,var(--border))}.tool-card.is-uninstalled{background:color-mix(in srgb,var(--surface) 72%,var(--surface-2))}
  .tool-head{display:flex;align-items:center;gap:10px;min-width:0}.logo-wrap{width:38px;height:38px;display:grid;place-items:center;flex:0 0 auto;border-radius:11px;background:var(--surface-2);border:1px solid var(--border)}.logo-wrap.inactive{filter:grayscale(.65);opacity:.7}.tool-logo{display:block;width:30px;height:30px;border-radius:8px}.tool-logo.monogram{display:grid;place-items:center;color:var(--text);font-size:14px;font-weight:750}.tool-titles{flex:1;min-width:0}.tool-name{margin:0;color:var(--text);font-size:14px;line-height:1.2;font-weight:720;letter-spacing:-.015em}.tool-subtitle{margin:3px 0 0;color:var(--muted);font-size:10.5px;line-height:1.25;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.tool-status{display:inline-flex;align-items:center;gap:5px;flex:0 0 auto;padding:4px 7px;border-radius:99px;background:var(--surface-2);color:var(--muted);font-size:9.5px;font-weight:650}.tool-status .dot{width:6px;height:6px;border-radius:50%;background:var(--muted)}.tool-status.tone-success{color:var(--ok-strong);background:color-mix(in srgb,var(--ok) 9%,var(--surface))}.tool-status.tone-success .dot{background:var(--ok)}.tool-status.tone-warning{color:var(--warn);background:color-mix(in srgb,var(--warn) 10%,var(--surface))}.tool-status.tone-warning .dot{background:var(--warn)}
  .status-notice{display:flex;align-items:center;gap:6px;margin:-2px 0;padding:7px 8px;border-radius:8px;background:color-mix(in srgb,var(--warn) 8%,var(--surface-2));color:var(--warn);font-size:10px;font-weight:600}.status-notice svg{flex:0 0 auto}
  .tool-body{display:flex;flex-direction:column;gap:9px;padding:10px;border-radius:11px;background:var(--surface-2);border:1px solid color-mix(in srgb,var(--border) 75%,transparent)}.model-control{display:flex;flex-direction:column;gap:5px;min-width:0}.model-control>span,.control-label,.provider-label{color:var(--muted);font-size:9px;font-weight:700;letter-spacing:.075em;text-transform:uppercase}.model-head{display:flex;align-items:center;justify-content:space-between;gap:8px;min-width:0}.mode-toggle{display:inline-flex;flex:0 0 auto;padding:2px;border:1px solid var(--border);border-radius:99px;background:var(--surface)}.mode-btn{padding:2px 8px;border:0;border-radius:99px;background:transparent;color:var(--muted);font-size:9px;font-weight:650;line-height:1.4;cursor:pointer;white-space:nowrap}.mode-btn:hover:not(:disabled):not(.active){color:var(--text)}.mode-btn.active{background:var(--accent-soft);color:var(--accent-soft-text)}.mode-btn:disabled{opacity:.5;cursor:default}.mode-controls{display:inline-flex;align-items:center;gap:5px;flex:0 0 auto}.info-wrap{position:relative;display:inline-flex}.info-btn{width:18px;height:18px;padding:0;display:inline-grid;place-items:center;color:var(--muted);background:transparent;border:0;border-radius:50%;cursor:default}.info-btn:hover{color:var(--text)}.info-btn:focus-visible{outline:none;color:var(--text);box-shadow:var(--focus)}.info-tip{position:absolute;top:calc(100% + 6px);right:-4px;z-index:20;width:max-content;max-width:186px;padding:6px 8px;border:1px solid var(--border);border-radius:8px;background:var(--surface);box-shadow:var(--shadow-pop);color:var(--muted);font-size:9.5px;font-weight:500;line-height:1.4;text-transform:none;letter-spacing:normal;visibility:hidden;opacity:0;transition:opacity .12s ease}.info-tip.open{visibility:visible;opacity:1}.tool-model-select{width:100%;height:31px;min-width:0;padding:0 28px 0 9px;color:var(--text);background-color:var(--surface);background-image:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='10' height='6' viewBox='0 0 10 6' fill='none'%3E%3Cpath d='m1 1 4 4 4-4' stroke='%2369707d' stroke-width='1.4' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");background-repeat:no-repeat;background-position:right 9px center;border:1px solid var(--border);border-radius:8px;outline:none;appearance:none;font-size:11px;white-space:nowrap;text-overflow:ellipsis}.tool-model-select:hover{border-color:var(--border-strong)}.tool-model-select:focus{border-color:var(--accent);box-shadow:var(--focus)}.model-control.placeholder div{height:31px;display:flex;align-items:center;padding:0 9px;border:1px dashed var(--border);border-radius:8px;color:var(--muted);font-size:10.5px}.provider-row{display:flex;align-items:end;justify-content:space-between;gap:8px;padding-top:8px;border-top:1px solid var(--border)}.provider-copy{min-width:0;display:flex;flex-direction:column;gap:3px}.provider-copy strong{color:var(--text);font-size:10.5px;font-weight:650;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.override-pill{padding:3px 6px;border-radius:99px;background:var(--accent-soft);color:var(--accent-soft-text);font-size:9px;font-weight:650}
  .tool-actions{display:flex;flex-direction:column;gap:7px;margin-top:auto}.primary-action{width:100%;min-height:32px}.secondary-actions{min-height:20px;display:flex;align-items:center;justify-content:space-between;gap:8px}.text-action{padding:2px 0;color:var(--muted);background:transparent;border:0;cursor:pointer;font-size:10px;font-weight:600}.text-action:hover:not(:disabled){color:var(--text)}.text-action.danger{margin-left:auto}.text-action.danger:hover:not(:disabled){color:var(--danger-strong)}.text-action:disabled{opacity:.35;cursor:default}.manual-note{margin-left:auto;color:var(--muted);font-size:9.5px}
  @media(max-width:860px){.tool-card{padding:13px}.tool-body{padding:9px}}
  /* ---- Model tiers dialog (Claude Code) ---- */
  .backdrop{position:fixed;inset:0;z-index:55;display:flex;align-items:center;justify-content:center;padding:16px;background:rgba(10,13,20,.48);-webkit-backdrop-filter:blur(8px);backdrop-filter:blur(8px);--wails-draggable:no-drag}
  .dialog{width:100%;max-width:26rem;max-height:min(90dvh,40rem);display:flex;flex-direction:column;gap:10px;padding:18px;background:var(--surface);border:1px solid var(--border);border-radius:16px;box-shadow:var(--shadow-pop);overflow-y:auto}
  .dialog-title{margin:0;color:var(--text);font-size:15px;font-weight:720;letter-spacing:-.015em}
  .dialog-hint{margin:0;color:var(--muted);font-size:11px;line-height:1.5}
  .dialog-hint strong{color:var(--text);font-weight:650}
  .tiers-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:9px 12px}
  .tier-field{display:flex;flex-direction:column;gap:4px;min-width:0}
  .tier-label{color:var(--muted);font-size:9px;font-weight:700;letter-spacing:.075em;text-transform:uppercase}
  .tier-select{width:100%;height:31px;min-width:0;padding:0 28px 0 9px;color:var(--text);background-color:var(--surface);background-image:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='10' height='6' viewBox='0 0 10 6' fill='none'%3E%3Cpath d='m1 1 4 4 4-4' stroke='%2369707d' stroke-width='1.4' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");background-repeat:no-repeat;background-position:right 9px center;border:1px solid var(--border);border-radius:8px;outline:none;appearance:none;font-size:11px;white-space:nowrap;text-overflow:ellipsis;cursor:pointer}
  .tier-select:hover:not(:disabled){border-color:var(--border-strong)}
  .tier-select:focus{border-color:var(--accent);box-shadow:var(--focus)}
  .tier-select:disabled{opacity:.55;cursor:default}
  .tiers-error{margin:0;color:var(--danger-strong);font-size:11px;line-height:1.4}
  .dialog-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:4px}
</style>
