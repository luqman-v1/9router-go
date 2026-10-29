<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../api/client'

  interface AuditEntry {
    provider: string
    model: string
    action: 'disabled' | 'enabled'
    reason?: string
    actor: 'scheduler' | 'operator'
    at: string
  }

  interface Failures {
    count: number
    lastError?: string
    lastFailedAt?: string
    disabledByScheduler?: boolean
    resetAt?: string
  }

  interface HealthConfig {
    enabled: boolean
    intervalMin: number
    threshold: number
    scope: 'history' | 'failed'
    autoEnable: boolean
  }

  interface Props {
    /** Compact mode drops the audit table, for use inside a narrow column. */
    compact?: boolean
  }

  let { compact = false }: Props = $props()

  let config = $state<HealthConfig>({
    enabled: false,
    intervalMin: 60,
    threshold: 3,
    scope: 'history',
    autoEnable: false,
  })
  let audit = $state<AuditEntry[]>([])
  let failures = $state<Record<string, Failures>>({})
  let loading = $state(true)
  let saving = $state(false)
  let error = $state('')
  let savedAt = $state('')

  onMount(load)

  async function load() {
    loading = true
    error = ''
    try {
      const res = await api.getModelHealth()
      config = res.config
      audit = res.audit ?? []
      failures = res.failures ?? {}
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      loading = false
    }
  }

  async function save() {
    saving = true
    error = ''
    try {
      const res = await api.saveModelHealth(config)
      config = res.config
      savedAt = new Date().toLocaleTimeString()
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      saving = false
    }
  }

  // The counter rows the scheduler is currently watching: anything with a
  // non-zero count, or carrying the scheduler-disabled mark. A row at zero
  // with no mark is a model that passed its last probe and is not worth a line.
  let watched = $derived(
    Object.entries(failures)
      .filter(([, f]) => f.count > 0 || f.disabledByScheduler)
      .sort((a, b) => b[1].count - a[1].count)
  )

  function formatWhen(iso?: string): string {
    if (!iso) return '—'
    const d = new Date(iso)
    return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
  }
</script>

<div class="space-y-4">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <div>
      <h3 class="font-headline text-sm font-bold text-text-main">Model Health</h3>
      <p class="font-body text-xs text-text-muted mt-0.5 max-w-xl leading-relaxed">
        Probe the models you have already looked at, and switch off the ones that keep failing.
        A model goes off after the threshold is reached, and comes back on its own once a probe
        succeeds again — only if you turn auto-enable on.
      </p>
    </div>
    <button
      type="button"
      onclick={load}
      disabled={loading}
      class="inline-flex items-center gap-1 rounded-md border border-border bg-surface-2 px-2 py-1 text-[11px] font-medium text-text-muted transition-colors hover:text-text-main cursor-pointer disabled:opacity-50"
    >
      <span class="material-symbols-outlined text-[13px] {loading ? 'animate-spin' : ''}">sync</span>
      Refresh
    </button>
  </div>

  {#if error}
    <p class="rounded-md border border-red-500/30 bg-red-500/10 px-2.5 py-1.5 text-xs text-red-600 dark:text-red-400" role="alert">
      {error}
    </p>
  {/if}

  <!-- Master switch -->
  <label class="flex items-start gap-2.5 rounded-lg border border-border bg-surface-2/40 p-3 cursor-pointer">
    <input
      type="checkbox"
      bind:checked={config.enabled}
      class="mt-0.5 size-3.5 accent-brand-500 cursor-pointer"
    />
    <span class="min-w-0">
      <span class="block text-xs font-semibold text-text-main">Enable automatic model health checks</span>
      <span class="block text-[11px] text-text-muted mt-0.5 leading-relaxed">
        Off by default. The loop only probes models that already carry a verdict, so a fresh
        install costs nothing until you press Test on a model.
      </span>
    </span>
  </label>

  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
    <label class="block">
      <span class="mb-1 block text-[10px] font-semibold uppercase tracking-wide text-text-muted/70">Check every</span>
      <div class="flex items-center gap-1.5">
        <input
          type="number"
          min="5"
          max="1440"
          bind:value={config.intervalMin}
          class="w-24 rounded-md border border-border bg-background px-2 py-1.5 text-xs text-text-main focus:border-primary focus:outline-none"
        />
        <span class="text-xs text-text-muted">minutes</span>
      </div>
    </label>

    <label class="block">
      <span class="mb-1 block text-[10px] font-semibold uppercase tracking-wide text-text-muted/70">Disable after</span>
      <div class="flex items-center gap-1.5">
        <input
          type="number"
          min="2"
          max="100"
          bind:value={config.threshold}
          class="w-24 rounded-md border border-border bg-background px-2 py-1.5 text-xs text-text-main focus:border-primary focus:outline-none"
        />
        <span class="text-xs text-text-muted">failures in a row</span>
      </div>
      <span class="mt-1 block text-[11px] text-text-muted leading-relaxed">
        One success resets the count, so a model that flaps is never switched off.
      </span>
    </label>

    <label class="block">
      <span class="mb-1 block text-[10px] font-semibold uppercase tracking-wide text-text-muted/70">Which models to probe</span>
      <select
        bind:value={config.scope}
        class="w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs text-text-main focus:border-primary focus:outline-none cursor-pointer"
      >
        <option value="history">Everything with a verdict</option>
        <option value="failed">Only the ones already failing</option>
      </select>
      <span class="mt-1 block text-[11px] text-text-muted leading-relaxed">
        "Only failing" is cheaper and re-enables sooner, but a model nobody has probed yet is never watched.
      </span>
    </label>

    <label class="flex items-start gap-2.5 rounded-lg border border-border bg-surface-2/40 p-3 cursor-pointer sm:mt-5">
      <input
        type="checkbox"
        bind:checked={config.autoEnable}
        class="mt-0.5 size-3.5 accent-brand-500 cursor-pointer"
      />
      <span class="min-w-0">
        <span class="block text-xs font-semibold text-text-main">Re-enable automatically</span>
        <span class="block text-[11px] text-text-muted mt-0.5 leading-relaxed">
          Only models the scheduler itself switched off. Anything you disabled by hand stays off.
        </span>
      </span>
    </label>
  </div>

  <div class="flex items-center gap-2">
    <button
      type="button"
      onclick={save}
      disabled={saving}
      class="inline-flex items-center gap-1.5 rounded-md bg-brand-500 px-3 py-1.5 text-xs font-semibold text-white shadow-sm transition-colors hover:bg-brand-600 cursor-pointer disabled:opacity-50"
    >
      <span class="material-symbols-outlined text-[14px] {saving ? 'animate-spin' : ''}">{saving ? 'sync' : 'save'}</span>
      {saving ? 'Saving...' : 'Save'}
    </button>
    {#if savedAt}
      <span class="text-[11px] text-text-muted">Saved at {savedAt} — takes effect on the next pass, no restart needed.</span>
    {/if}
  </div>

  {#if watched.length > 0}
    <div>
      <h4 class="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-text-muted/70">
        Being watched ({watched.length})
      </h4>
      <div class="divide-y divide-border rounded-lg border border-border">
        {#each watched as [key, f] (key)}
          <div class="flex flex-wrap items-center gap-2 px-2.5 py-2">
            <code class="font-code text-[11px] text-text-main">{key}</code>
            {#if f.disabledByScheduler}
              <span class="rounded-full border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-amber-600 dark:text-amber-400">
                off by scheduler
              </span>
            {/if}
            <span class="ml-auto flex items-center gap-2 text-[11px] text-text-muted">
              <span class="font-mono">{f.count} fail{f.count === 1 ? '' : 's'}</span>
              {#if f.resetAt}
                <span title="When the block lifts">· resets {formatWhen(f.resetAt)}</span>
              {/if}
            </span>
            {#if f.lastError}
              <span class="w-full truncate text-[10px] text-text-muted/80" title={f.lastError}>{f.lastError}</span>
            {/if}
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if !compact && audit.length > 0}
    <div>
      <h4 class="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-text-muted/70">
        Recent actions
      </h4>
      <div class="divide-y divide-border rounded-lg border border-border">
        {#each audit.slice(0, 12) as entry (entry.at + entry.provider + entry.model + entry.action)}
          <div class="flex flex-wrap items-center gap-2 px-2.5 py-2 text-[11px]">
            <span
              class="material-symbols-outlined text-[13px] {entry.action === 'disabled' ? 'text-amber-500' : 'text-green-500'}"
            >
              {entry.action === 'disabled' ? 'visibility_off' : 'visibility'}
            </span>
            <code class="font-code text-text-main">{entry.provider}/{entry.model}</code>
            <span class="font-medium {entry.action === 'disabled' ? 'text-amber-600 dark:text-amber-400' : 'text-green-600 dark:text-green-400'}">
              {entry.action}
            </span>
            <span class="rounded-full border border-border px-1.5 py-0.5 text-[10px] text-text-muted">
              {entry.actor}
            </span>
            <span class="ml-auto text-text-muted">{formatWhen(entry.at)}</span>
            {#if entry.reason}
              <span class="w-full truncate text-[10px] text-text-muted/80" title={entry.reason}>{entry.reason}</span>
            {/if}
          </div>
        {/each}
      </div>
    </div>
  {/if}
</div>
