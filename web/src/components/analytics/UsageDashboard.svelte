<script lang="ts">
  // Details-section dashboard (issue #280): activity heatmap and node health
  // above the request-details table.
  import { api, type UsageDashboardResponse, type UsageLifetime } from '../../api/client'
  import { fmtTokens } from '../../lib/compactNumber'
  import Card from '../../lib/ui/Card.svelte'
  import ActivityHeatmap from './ActivityHeatmap.svelte'
  import NodeHealthTable from './NodeHealthTable.svelte'
  import { isDetailsExpandedByDefault } from '../../lib/displayPrefs'
  import { usageBlocks } from '../../lib/usagePanel.svelte'
  import { USAGE_PANEL_FIXED_POSITION } from '../../lib/usagePanel'

  let data = $state<UsageDashboardResponse | null>(null)
  let loading = $state(true)
  let failed = $state(false)


  const lifetime = $derived(data?.lifetime ?? null)

  // Both detail panels start collapsed unless Settings > Display says
  // otherwise; each one still opens on its own for the current view.
  let heatmapOpen = $state(isDetailsExpandedByDefault())
  let healthOpen = $state(isDetailsExpandedByDefault())

  interface CardDef {
    label: string
    value: string
    sub?: string
    valueClass: string
    shrink?: boolean
  }

  const cards = $derived.by((): CardDef[] => {
    const l: UsageLifetime | null = lifetime
    if (!l) return []
    return [
      // Lifetime scope, unlike the Overview KPI of the same name, which counts
      // the selected window only. The label says so, and the row's other cards
      // ("Lifetime Tokens", "Peak Day Tokens") are all-time too.
      { label: 'Lifetime Requests', value: l.totalRequests.toLocaleString(), valueClass: 'text-text-main' },
      { label: 'Lifetime Tokens', value: fmtTokens(l.totalTokens), valueClass: 'text-primary' },
      {
        label: 'Peak Day Tokens',
        value: fmtTokens(l.peakTokens),
        sub: l.peakDate || undefined,
        valueClass: 'text-info',
      },
      {
        label: 'Cache Hit Rate',
        value: l.cacheHitRate != null ? `${l.cacheHitRate}%` : '—',
        sub: 'Only real valid data is counted',
        valueClass: 'text-success',
      },
      {
        label: 'Most Used Model',
        value: l.topModel?.model || '—',
        sub: l.topModel
          ? `Last 7 days ${fmtTokens(l.topModel.tokens, 'en', true)}${l.topModel.provider ? ` · ${l.topModel.provider}` : ''}`
          : undefined,
        valueClass: 'text-warning',
        shrink: true,
      },
    ]
  })

  // The Details arrangement. The paged request table and the payload dialog keep
  // fixed positions (USAGE_PANEL_FIXED_POSITION): the table is drawn by
  // RequestDetailsTab below this stack, and the dialog is an overlay, so only the
  // panels above the table are arranged here.
  const detailBlocks = $derived(
    usageBlocks('details').filter((block) => !USAGE_PANEL_FIXED_POSITION[block.ids[0]])
  )
  // A read that never answers leaves the card spinning with no way out, so
  // the wait is bounded.
  const DASHBOARD_TIMEOUT_MS = 20000

  // The payload is read only while one of its cards is on screen. Hiding every
  // card in Settings stops the read and its loading card; re-enabling one
  // starts it again. The read used to run once on mount, so a section whose
  // cards were all hidden still fetched the payload and painted a loading
  // card over a section that draws nothing (issue #506).
  $effect(() => {
    if (detailBlocks.length === 0) {
      loading = false
      return
    }
    let cancelled = false
    let settled = false
    loading = true
    failed = false
    // A read that never answers leaves the card spinning with no way out, so
    // the wait is bounded: the card says it could not be loaded.
    const timer = setTimeout(() => {
      if (settled || cancelled) return
      settled = true
      failed = true
      loading = false
    }, DASHBOARD_TIMEOUT_MS)
    api
      .getUsageDashboard()
      .then((res) => {
        if (cancelled || settled) return
        settled = true
        clearTimeout(timer)
        data = res
      })
      .catch(() => {
        if (cancelled || settled) return
        settled = true
        clearTimeout(timer)
        failed = true
      })
      .finally(() => {
        if (!cancelled && !settled) loading = false
      })
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  })
</script>

<div class="flex min-w-0 flex-col gap-6">
  {#if detailBlocks.length === 0}
    <!-- Every card in this stack is hidden in the arrangement: the section
         draws nothing, so it must not paint a loading card either (#506). -->
  {:else if loading}
    <Card padding="md">
      <div class="flex items-center justify-center gap-2 py-8 text-text-muted text-sm">
        <span class="material-symbols-outlined animate-spin text-[20px]" aria-hidden="true">progress_activity</span>
        Loading dashboard...
      </div>
    </Card>
  {:else if failed || !data}
    <Card padding="md">
      <p class="py-6 text-center text-sm text-text-muted">Failed to load usage dashboard</p>
    </Card>
  {:else}
    {#each detailBlocks as block (block.key)}
      {#if block.key === 'details.lifetime'}
    {#if cards.length > 0}
      <div class="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-5 sm:gap-4">
        {#each cards as c (c.label)}
          <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
            <span class="truncate text-text-muted text-sm uppercase font-semibold">{c.label}</span>
            <div class="flex h-8 items-center min-w-0">
              <span
                class="whitespace-nowrap font-bold truncate {c.shrink ? 'text-sm' : 'text-2xl'} {c.valueClass}"
                title={c.value}
              >{c.value}</span>
            </div>
            {#if c.sub}
              <span class="truncate text-[10px] text-text-muted">{c.sub}</span>
            {:else}
              <span class="text-[10px] invisible select-none leading-none">&nbsp;</span>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
      {:else if block.key === 'details.heatmap'}

    <Card padding="none" class="overflow-hidden border border-border">
      <div
        class="flex flex-col gap-3 bg-surface-2 px-5 py-3 sm:flex-row sm:items-center sm:justify-between {heatmapOpen
          ? 'border-b border-border'
          : ''}"
      >
        <div class="min-w-0">
          <h2 class="font-headline text-sm font-bold text-text-main">Activity Heatmap</h2>
          <p class="font-body text-xs text-text-muted">Requests per day</p>
        </div>
        {@render panelToggle(heatmapOpen, 'Activity Heatmap', () => (heatmapOpen = !heatmapOpen))}
      </div>
      {#if heatmapOpen}
        <div class="p-5">
          <ActivityHeatmap daily={data.daily || []} currentStreak={lifetime?.currentStreak ?? 0} />
        </div>
      {/if}
    </Card>
      {:else if block.key === 'details.nodeHealth'}
    <Card padding="none" class="overflow-hidden border border-border">
      <div
        class="flex flex-col gap-3 bg-surface-2 px-5 py-3 sm:flex-row sm:items-center sm:justify-between {healthOpen
          ? 'border-b border-border'
          : ''}"
      >
        <div class="min-w-0">
          <h2 class="font-headline text-sm font-bold text-text-main">Node Health</h2>
          <p class="font-body text-xs text-text-muted">Last 7 days (≥50 real requests)</p>
        </div>
        {@render panelToggle(healthOpen, 'Node Health', () => (healthOpen = !healthOpen))}
      </div>
      {#if healthOpen}
        <div class="p-5">
          <NodeHealthTable nodes={data.nodes || []} models={data.models || []} />
        </div>
      {/if}
    </Card>
      {/if}
    {/each}
  {/if}
</div>

{#snippet panelToggle(open: boolean, label: string, onToggle: () => void)}
  <button
    type="button"
    aria-expanded={open}
    aria-label="Toggle {label}"
    onclick={onToggle}
    class="shrink-0 cursor-pointer self-start rounded-lg p-1 text-text-muted transition-colors hover:bg-surface-3 hover:text-text-main focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 sm:self-auto"
  >
    <span
      class="material-symbols-outlined text-[18px] transition-transform duration-200"
      style:transform={open ? 'rotate(180deg)' : 'rotate(0deg)'}
    >
      expand_more
    </span>
  </button>
{/snippet}
