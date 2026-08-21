<script lang="ts">
  // First-run "Get started" checklist. Purely presentational: the parent
  // derives each step's done-state from real data (providers/tools) and
  // decides visibility, so this component can never drift from the truth.
  interface Props {
    providerDone: boolean;
    modelDone: boolean;
    applyDone: boolean;
    onAddProvider: () => void;
    onShowTools: () => void;
    onDismiss: () => void;
  }
  let { providerDone, modelDone, applyDone, onAddProvider, onShowTools, onDismiss }: Props = $props();

  // Step 3's hint doubles as the trust message (backup-first) — new users'
  // biggest fear is that applying breaks their tool configs.
  const steps = $derived([
    { label: "Add a provider", hint: "Connect an endpoint and API key.", done: providerDone, actionLabel: "Add provider", action: onAddProvider },
    { label: "Pick a model", hint: "Choose which model each tool uses.", done: modelDone, actionLabel: "Show tools", action: onShowTools },
    { label: "Apply to your tools", hint: "Writes the config to each tool — the original is backed up first.", done: applyDone, actionLabel: "Show tools", action: onShowTools },
  ]);
  // The single "current" step: the first unfinished one. Only it gets an
  // action button so the checklist reads as a sequence, not three demands.
  const activeIndex = $derived(steps.findIndex((s) => !s.done));
</script>

<section class="onboard-card" aria-labelledby="onboard-title">
  <div class="onboard-head">
    <h2 id="onboard-title">Get started in 3 steps</h2>
    <button class="onboard-dismiss" type="button" onclick={onDismiss}
      aria-label="Dismiss setup checklist" title="Dismiss setup checklist">×</button>
  </div>
  <ol class="onboard-steps">
    {#each steps as s, i (s.label)}
      <li class="onboard-step" class:done={s.done} class:active={i === activeIndex}
        aria-current={i === activeIndex ? "step" : undefined}>
        <span class="step-mark" aria-hidden="true">{s.done ? "✓" : i + 1}</span>
        <div class="step-copy">
          <strong>{s.label}{#if s.done}<span class="sr-only"> (done)</span>{/if}</strong>
          <span>{s.hint}</span>
        </div>
        {#if i === activeIndex}
          <button class="btn-primary sm" type="button" onclick={s.action}>{s.actionLabel}</button>
        {/if}
      </li>
    {/each}
  </ol>
</section>

<style>
  .onboard-card{margin-bottom:12px;padding:13px 14px 12px;border:1px solid color-mix(in srgb,var(--accent) 22%,var(--border));border-radius:14px;background:color-mix(in srgb,var(--accent) 4%,var(--surface));box-shadow:var(--shadow-card)}
  .onboard-head{display:flex;align-items:center;justify-content:space-between;gap:8px}
  .onboard-head h2{margin:0;font-size:13px;font-weight:750;letter-spacing:-.015em}
  .onboard-dismiss{width:26px;height:26px;padding:0;display:grid;place-items:center;border:0;border-radius:7px;background:none;color:var(--muted);font-size:15px;line-height:1;cursor:pointer}
  .onboard-dismiss:hover{color:var(--text);background:var(--surface-hover)}
  .onboard-steps{display:flex;flex-direction:column;gap:2px;margin:9px 0 0;padding:0;list-style:none}
  .onboard-step{display:flex;align-items:center;gap:10px;padding:6px 7px;border-radius:9px}
  .onboard-step.active{background:var(--surface)}
  .step-mark{width:21px;height:21px;flex:0 0 auto;display:grid;place-items:center;border-radius:50%;border:1px solid var(--border-strong);color:var(--muted);font-size:10.5px;font-weight:800}
  .onboard-step.active .step-mark{border-color:var(--accent);color:var(--accent)}
  .onboard-step.done .step-mark{border-color:color-mix(in srgb,var(--ok) 40%,transparent);color:var(--ok);background:color-mix(in srgb,var(--ok) 9%,transparent)}
  .step-copy{flex:1;min-width:0;display:flex;flex-direction:column;gap:1px}
  .step-copy strong{font-size:12px}
  .onboard-step.done .step-copy strong{color:var(--muted);text-decoration:line-through}
  .step-copy span{color:var(--muted);font-size:11px}
  .onboard-step:not(.active) .step-copy span{display:none}
  .sr-only{position:absolute;width:1px;height:1px;margin:-1px;padding:0;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap;border:0}
</style>
