<script lang="ts">
  // Usage trend chart for the Overview section (issue #280: visualization under
  // Recent Requests, above the usage breakdown filter). Port of
  // techysy/10router's UsageChart, drawn as plain SVG — the dashboard ships no
  // chart dependency, matching CacheAnalyticsView / CompressionAnalyticsView.
  import { api } from '../../api/client'
  import { fmtTokens } from '../../lib/compactNumber'

  interface Props {
    period?: string
  }

  interface ChartBucket {
    label: string
    tokens: number
    cost: number
    byModel: Record<string, number>
  }

  interface Series {
    family: string
    color: string
    total: number
  }

  let { period = '7d' }: Props = $props()

  let buckets = $state<ChartBucket[]>([])
  let loading = $state(true)
  let failed = $state(false)
  let viewMode = $state<'tokens' | 'costs'>('tokens')
  let hovered = $state<number | null>(null)

  const WIDTH = 640
  const HEIGHT = 200
  // At most six plotted families, mirroring 10router's legend cap so every
  // plotted color has a named entry and nothing is left unexplained.
  const MAX_SERIES = 6
  const FAMILY_COLORS = ['#6366f1', '#f59e0b', '#10b981', '#ef4444', '#8b5cf6', '#06b6d4', '#f43f5e', '#84cc16']
  const OTHER_COLOR = '#64748b'

  $effect(() => {
    const wanted = period
    let cancelled = false
    loading = true
    failed = false
    api
      .getUsageChart(wanted)
      .then((data) => {
        if (cancelled) return
        buckets = Array.isArray(data) ? data : []
        loading = false
      })
      .catch(() => {
        if (cancelled) return
        buckets = []
        failed = true
        loading = false
      })
    return () => {
      cancelled = true
    }
  })

  // Rank families by period total, keep the top six and fold the tail into
  // "other" so the legend stays bounded.
  const families = $derived.by((): Series[] => {
    const totals = new Map<string, number>()
    for (const b of buckets) {
      for (const [family, tokens] of Object.entries(b.byModel || {})) {
        totals.set(family, (totals.get(family) || 0) + (tokens || 0))
      }
    }
    const ranked = [...totals.entries()].sort((a, b) => b[1] - a[1])
    const kept = ranked.slice(0, MAX_SERIES)
    const tail = ranked.slice(MAX_SERIES)
    const series: Series[] = kept.map(([family, total], i) => ({
      family,
      total,
      color: FAMILY_COLORS[i % FAMILY_COLORS.length],
    }))
    if (tail.length > 0) {
      series.push({
        family: 'other',
        total: tail.reduce((sum, [, t]) => sum + t, 0),
        color: OTHER_COLOR,
      })
    }
    return series
  })

  const keptFamilies = $derived(families.slice(0, MAX_SERIES).map((s) => s.family))

  // Per-bucket stack: cumulative token height of each series, bottom-up.
  const stack = $derived(
    buckets.map((b) => {
      const values = families.map((s) =>
        s.family === 'other'
          ? Object.entries(b.byModel || {})
              .filter(([family]) => !keptFamilies.includes(family))
              .reduce((sum, [, t]) => sum + (t || 0), 0)
          : b.byModel?.[s.family] || 0
      )
      const cumulative: number[] = []
      let acc = 0
      for (const v of values) {
        acc += v
        cumulative.push(acc)
      }
      return { bucket: b, cumulative, total: acc }
    })
  )

  const maxTotal = $derived(Math.max(1, ...stack.map((s) => s.total)))
  const hasData = $derived(stack.some((s) => s.total > 0))
  const step = $derived(buckets.length > 1 ? WIDTH / (buckets.length - 1) : WIDTH)
  const hourly = $derived(/^\d\d:\d\d$/.test(buckets[0]?.label || ''))

  function xAt(index: number): number {
    return buckets.length > 1 ? index * step : WIDTH / 2
  }

  function yAt(value: number): number {
    return HEIGHT - (value / maxTotal) * HEIGHT
  }

  // One area path per series, bounded by its own cumulative band, so the areas
  // stack without overlapping.
  function areaPath(band: number): string {
    if (stack.length === 0) return ''
    const top = stack.map((s, i) => `${xAt(i)},${yAt(s.cumulative[band] || 0)}`).join(' L ')
    const bottom = stack
      .map((s, i) => {
        const idx = stack.length - 1 - i
        const lower = band === 0 ? 0 : s.cumulative[band - 1] || 0
        return `${xAt(idx)},${yAt(lower)}`
      })
      .join(' L ')
    return `M ${top} L ${bottom} Z`
  }
</script>

<div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-3 p-4">
  <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
    <div>
      <h3 class="text-sm font-semibold text-text-main flex items-center gap-1.5">
        <span class="material-symbols-outlined text-[18px] text-brand-500" aria-hidden="true">show_chart</span>
        Usage Trend
      </h3>
      <p class="text-xs text-text-subtle">
        {viewMode === 'costs' ? 'Estimated cost' : 'Tokens'} per {hourly ? 'hour' : 'day'}, stacked by model family.
      </p>
    </div>
    <div class="inline-flex rounded-xl bg-surface border border-border p-1 shadow-sm self-start sm:self-auto" role="group" aria-label="Chart metric">
      <button
        type="button"
        onclick={() => (viewMode = 'costs')}
        class="rounded-lg px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {viewMode === 'costs'
          ? 'bg-brand-500 text-white shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Costs
      </button>
      <button
        type="button"
        onclick={() => (viewMode = 'tokens')}
        class="rounded-lg px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {viewMode === 'tokens'
          ? 'bg-brand-500 text-white shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Tokens
      </button>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center justify-center py-12 text-text-muted text-xs">
      <span class="material-symbols-outlined animate-spin text-[20px] mr-2" aria-hidden="true">progress_activity</span>
      Loading chart...
    </div>
  {:else if failed}
    <div class="py-10 text-center text-xs text-text-muted">Usage chart could not be loaded.</div>
  {:else if !hasData}
    <div class="py-10 text-center text-xs text-text-muted">No usage recorded in this period.</div>
  {:else}
    <div class="min-w-0">
      <svg viewBox={`0 0 ${WIDTH} ${HEIGHT}`} class="w-full h-auto" role="img" aria-label="Usage trend chart">
        {#each families as s, band (s.family)}
          <path d={areaPath(band)} fill={s.color} fill-opacity="0.45" stroke={s.color} stroke-width="1" />
        {/each}
        {#each stack as _point, i}
          <rect
            role="presentation"
            x={xAt(i) - step / 2}
            y="0"
            width={step}
            height={HEIGHT}
            fill="transparent"
            onmouseenter={() => (hovered = i)}
            onmouseleave={() => (hovered = null)}
          />
        {/each}
        {#if hovered !== null && stack[hovered]}
          <line
            x1={xAt(hovered)}
            x2={xAt(hovered)}
            y1="0"
            y2={HEIGHT}
            stroke="currentColor"
            stroke-width="1"
            stroke-dasharray="3 3"
            opacity="0.35"
          />
        {/if}
      </svg>
      {#if hovered !== null && stack[hovered]}
        <div class="mt-2 rounded-[10px] border border-border bg-surface-2 px-3 py-2 text-[11px] text-text-main">
          <div class="font-semibold text-text-muted">{stack[hovered].bucket.label}</div>
          <div class="mt-1 flex items-center justify-between gap-3">
            <span>Tokens</span>
            <span class="font-mono font-bold">{fmtTokens(stack[hovered].bucket.tokens)}</span>
          </div>
          <div class="flex items-center justify-between gap-3 text-warning">
            <span>Est. cost</span>
            <span class="font-mono font-bold">~${stack[hovered].bucket.cost.toFixed(2)}</span>
          </div>
          {#each families as s, band (s.family)}
            {@const value = stack[hovered].cumulative[band] - (band > 0 ? stack[hovered].cumulative[band - 1] : 0)}
            {#if value > 0}
              <div class="flex items-center justify-between gap-3">
                <span class="flex items-center gap-1.5">
                  <span class="w-2 h-2 rounded-sm" style="background: {s.color}"></span>
                  {s.family}
                </span>
                <span class="font-mono">{fmtTokens(value)}</span>
              </div>
            {/if}
          {/each}
        </div>
      {/if}
    </div>

    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-text-muted pt-2 border-t border-border-subtle">
      {#each families as s (s.family)}
        <span class="flex items-center gap-1.5">
          <span class="w-2.5 h-2.5 rounded-sm" style="background: {s.color}"></span>
          {s.family}
        </span>
      {/each}
      <span class="ml-auto font-mono">Peak {fmtTokens(maxTotal)}</span>
    </div>

    <div class="flex justify-between text-[10px] text-text-subtle">
      <span>{buckets[0]?.label}</span>
      <span>{buckets[buckets.length - 1]?.label}</span>
    </div>
  {/if}
</div>
