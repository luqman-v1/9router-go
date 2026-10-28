<script lang="ts">
  // Usage trend chart for the Overview section (issue #280: visualization under
  // Recent Requests, above the usage breakdown filter). Port of
  // techysy/10router's UsageChart, drawn as plain SVG — the dashboard ships no
  // chart dependency, matching CacheAnalyticsView / CompressionAnalyticsView.
  import { api } from '../../api/client'
  import { fmtTokens } from '../../lib/compactNumber'
  import { costIn } from '../../lib/currency.svelte'

  interface Props {
    period?: string
    /**
     * Which stack the chart draws. Owned by the parent because the switch that
     * picks it sits in the panel header, and the header label describes it.
     */
    viewMode?: 'tokens' | 'costs'
    /** Whether the buckets are hourly — the same fact the header label needs. */
    hourly?: boolean
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

  let {
    period = '7d',
    viewMode = $bindable('tokens'),
    hourly = $bindable(false),
  }: Props = $props()

  let buckets = $state<ChartBucket[]>([])
  let loading = $state(true)
  let failed = $state(false)
  let hovered = $state<number | null>(null)

  const WIDTH = 640
  const HEIGHT = 200
  // At most six plotted families, mirroring 10router's legend cap so every
  // plotted color has a named entry and nothing is left unexplained.
  const MAX_SERIES = 6
  const FAMILY_COLORS = ['#6366f1', '#f59e0b', '#10b981', '#ef4444', '#8b5cf6', '#06b6d4', '#f43f5e', '#84cc16']
  const OTHER_COLOR = '#64748b'

  // A read that never answers leaves the panel spinning with no way out, so
  // the wait is bounded: the chart says it could not be loaded, and the
  // reader collapses and expands the panel to try again.
  const CHART_TIMEOUT_MS = 20000

  $effect(() => {
    const wanted = period
    let cancelled = false
    let settled = false
    loading = true
    failed = false
    const timer = setTimeout(() => {
      if (settled || cancelled) return
      settled = true
      failed = true
      loading = false
    }, CHART_TIMEOUT_MS)
    api
      .getUsageChart(wanted)
      .then((data) => {
        if (cancelled || settled) return
        settled = true
        clearTimeout(timer)
        buckets = Array.isArray(data) ? data : []
        // The server buckets hourly for short windows; the header label repeats
        // that fact, so it is reported rather than re-derived up there.
        hourly = /^\d\d:\d\d$/.test(buckets[0]?.label || '')
        loading = false
      })
      .catch(() => {
        if (cancelled || settled) return
        settled = true
        clearTimeout(timer)
        buckets = []
        failed = true
        loading = false
      })
    return () => {
      cancelled = true
      clearTimeout(timer)
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

  function xAt(index: number): number {
    return buckets.length > 1 ? index * step : WIDTH / 2
  }

  function yAt(value: number): number {
    return HEIGHT - (value / maxTotal) * HEIGHT
  }

  // One area path per series, bounded by its own cumulative band, so the areas
  // stack without overlapping.
  // Arrow keys walk the same points the mouse reveals. A tooltip reachable only
  // by hovering is unreadable to anyone driving the page from the keyboard, and
  // the table below is the non-visual equivalent of the drawn series.
  //
  // One tab stop, not one per point: the keydown fires on the focused element,
  // so a per-point binding always reads its own index and ArrowRight moves the
  // tooltip without moving focus — the second press computes the same index
  // again. Focus follows the arrow instead, and the point that is focused is
  // the only one with tabindex 0.
  let focusedPoint = $state(0)

  function handlePointKey(event: KeyboardEvent) {
    const index = focusedPoint
    let next = index
    if (event.key === 'ArrowRight') next = index + 1
    else if (event.key === 'ArrowLeft') next = index - 1
    else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = stack.length - 1
    else if (event.key === 'Escape') {
      event.preventDefault()
      hovered = null
      return
    } else return

    event.preventDefault()
    next = Math.max(0, Math.min(stack.length - 1, next))
    focusedPoint = next
    hovered = next
    // Focus moves with the tooltip. The tabindex re-render lands on the next
    // microtask, so the lookup is deferred rather than done inline.
    const el = event.currentTarget as SVGElement
    const svg = el.ownerSVGElement
    svg?.querySelectorAll<SVGRectElement>('rect')?.[next]?.focus()
  }

  // The table lives inside <details>, which hides its rows visually but still
  // builds them. An all-time window is thousands of buckets, so one <tr> and
  // three cells per bucket is a cost a 1-vCPU box pays while the table is shut.
  let tableOpen = $state(false)

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

<div class="flex min-w-0 flex-col gap-3">
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
            tabindex={i === focusedPoint ? 0 : -1}
            aria-label="{stack[i].bucket.label}: {fmtTokens(stack[i].bucket.tokens)} tokens"
            x={xAt(i) - step / 2}
            y="0"
            width={step}
            height={HEIGHT}
            fill="transparent"
            onmouseenter={() => (hovered = i)}
            onmouseleave={() => (hovered = null)}
            onfocus={() => {
              focusedPoint = i
              hovered = i
            }}
            onblur={() => (hovered = null)}
            onkeydown={handlePointKey}
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
        <!-- role=status so a screen reader hears the point the keyboard just
             moved to; the same node is the pointer tooltip. -->
        <div
          role="status"
          aria-live="polite"
          class="mt-2 rounded-[10px] border border-border bg-surface-2 px-3 py-2 text-[11px] text-text-main"
        >
          <div class="font-semibold text-text-muted">{stack[hovered].bucket.label}</div>
          <div class="mt-1 flex items-center justify-between gap-3">
            <span>Tokens</span>
            <span class="font-mono font-bold">{fmtTokens(stack[hovered].bucket.tokens)}</span>
          </div>
          <div class="flex items-center justify-between gap-3 text-warning">
            <span>Est. cost</span>
            <span class="font-mono font-bold">~{costIn(stack[hovered].bucket.cost ?? 0)}</span>
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

    <!-- The drawn areas carry no text a screen reader can read and the tooltip
         only opens under a pointer, so the same numbers are here as a table. -->
    <details class="text-[11px] text-text-muted" bind:open={tableOpen}>
      <summary class="cursor-pointer select-none">View table</summary>
      {#if tableOpen}
        <table class="mt-2 w-full border-collapse text-left" aria-label="Usage trend table">
          <thead>
            <tr class="border-b border-border-subtle">
              <th scope="col" class="py-1 pr-2 font-medium">{hourly ? 'Hour' : 'Day'}</th>
              <th scope="col" class="py-1 pr-2 text-right font-medium">Tokens</th>
              <th scope="col" class="py-1 text-right font-medium">Est. cost</th>
            </tr>
          </thead>
          <tbody>
            <!-- The label is not a unique key: a daily bucket is labelled "Jan 2",
                 so a period long enough to cross a year boundary repeats it and
                 Svelte throws each_key_duplicate, which takes the usage page
                 down with it. The index is the key here — the same choice the
                 plotted points above make — and it is unique by construction. -->
            {#each stack as point, i (i)}
              <tr class="border-b border-border-subtle/60">
                <th scope="row" class="py-1 pr-2 font-normal text-text-main">{point.bucket.label}</th>
                <td class="py-1 pr-2 text-right font-mono">{fmtTokens(point.bucket.tokens)}</td>
                <td class="py-1 text-right font-mono">~{costIn(point.bucket.cost ?? 0)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </details>
  {/if}
</div>
