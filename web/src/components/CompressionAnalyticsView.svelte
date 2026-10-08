<script lang="ts">
  import { onMount, onDestroy, type Snippet } from 'svelte'
  import {
    api,
    type CompressionAnalyticsSummary,
  } from '../api/client'
  import { notifications } from '../lib/notifications'
  import { copyToClipboard } from '../lib/clipboard'
  import PeriodSelect from './analytics/PeriodSelect.svelte'
  import type { PeriodPreset } from './analytics/types'
  import ActionsMenu from './analytics/ActionsMenu.svelte'
  import MenuItem from './analytics/MenuItem.svelte'

  interface Props {
    /**
     * The Usage section picker, rendered in the left of the header so this
     * section's own controls sit beside it instead of in a row above
     * (issue #209).
     */
    headerLeft?: Snippet
  }

  let { headerLeft }: Props = $props()

  type SinceOption = '24h' | '7d' | '30d' | 'all'

  // The endpoint resolves exactly these four windows (see
  // DashboardHandler.HandleGetCompressionAnalytics) and answers anything else
  // with its 24h default, so the dropdown offers no custom input here: an
  // arbitrary window would be accepted and then silently read as 24h.
  const SINCE_OPTIONS: { value: PeriodPreset; label: string }[] = [
    { value: '24h', label: 'Last 24 hours' },
    { value: '7d', label: 'Last 7 days' },
    { value: '30d', label: 'Last 30 days' },
    { value: 'all', label: 'All time' },
  ]

  let since = $state<SinceOption>('24h')
  let loading = $state(true)
  let stats = $state<CompressionAnalyticsSummary | null>(null)
  let autoRefresh = $state(false)
  let refreshTimer: ReturnType<typeof setInterval> | null = null

  async function loadData() {
    loading = true
    try {
      stats = await api.getCompressionAnalytics(since)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      notifications.error(msg, 'Failed to load compression analytics')
    } finally {
      loading = false
    }
  }

  function exportJSON() {
    if (!stats) return
    const blob = new Blob([JSON.stringify(stats, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `compression-analytics-${since}.json`
    a.click()
    URL.revokeObjectURL(url)
    notifications.success('Exported JSON successfully')
  }

  function exportCSV() {
    if (!stats) return
    const lines = ['Category,Key,Requests,TokensSaved,AvgSavingsPct']
    for (const m of modeList) {
      lines.push(`Mode,${m.mode},${m.count},${m.tokensSaved},${m.avgSavingsPct}%`)
    }
    for (const p of providerList) {
      lines.push(`Provider,${p.provider},${p.count},${p.tokensSaved},0%`)
    }
    for (const m of modelList) {
      lines.push(`Model,${m.model},${m.count},${m.tokensSaved},${m.avgSavingsPct}%`)
    }
    if (stats.topSavers && stats.topSavers.length > 0) {
      lines.push('')
      lines.push('TopSavers,RequestID,Timestamp,Provider,Model,Mode,OriginalTokens,CompressedTokens,TokensSaved,SavingsPct,DurationMs,EstUSD')
      for (const s of stats.topSavers) {
        lines.push(`TopSaver,${s.requestId},${s.timestamp},${s.provider},${s.model},${s.mode},${s.originalTokens},${s.compressedTokens},${s.tokensSaved},${s.savingsPct}%,${s.durationMs}ms,$${s.estimatedUsd}`)
      }
    }
    const blob = new Blob([lines.join('\n')], { type: 'text/csv' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `compression-analytics-${since}.csv`
    a.click()
    URL.revokeObjectURL(url)
    notifications.success('Exported CSV successfully')
  }

  onMount(() => {
    loadData()
  })

  onDestroy(() => {
    if (refreshTimer) clearInterval(refreshTimer)
  })

  $effect(() => {
    if (autoRefresh) {
      if (!refreshTimer) {
        refreshTimer = setInterval(() => {
          loadData()
        }, 15000)
      }
    } else {
      if (refreshTimer) {
        clearInterval(refreshTimer)
        refreshTimer = null
      }
    }
  })

  // Derived calculations
  let totalAttempts = $derived.by(() => {
    if (!stats) return 0
    return (stats.totalRequests ?? 0) + (stats.totalSkipped ?? 0)
  })

  let modeList = $derived.by(() => {
    if (!stats?.byMode) return []
    const total = totalAttempts || 1
    return Object.entries(stats.byMode).map(([mode, data]) => {
      const count = data.count ?? 0
      const pct = Math.min(100, Math.round((count / total) * 100))
      return {
        mode,
        count,
        tokensSaved: data.tokensSaved ?? 0,
        avgSavingsPct: data.avgSavingsPct ?? 0,
        skipped: data.skipped ?? 0,
        pct,
      }
    }).sort((a, b) => b.tokensSaved - a.tokensSaved)
  })

  let providerList = $derived.by(() => {
    if (!stats?.byProvider) return []
    const total = totalAttempts || 1
    return Object.entries(stats.byProvider).map(([provider, data]) => {
      const count = data.count ?? 0
      const pct = Math.min(100, Math.round((count / total) * 100))
      return {
        provider,
        count,
        tokensSaved: data.tokensSaved ?? 0,
        pct,
      }
    }).sort((a, b) => b.tokensSaved - a.tokensSaved)
  })

  let modelList = $derived.by(() => {
    if (!stats?.byModel) return []
    const total = totalAttempts || 1
    return Object.entries(stats.byModel).map(([model, data]) => {
      const count = data.count ?? 0
      const pct = Math.min(100, Math.round((count / total) * 100))
      return {
        model,
        count,
        tokensSaved: data.tokensSaved ?? 0,
        avgSavingsPct: data.avgSavingsPct ?? 0,
        pct,
      }
    }).sort((a, b) => b.tokensSaved - a.tokensSaved)
  })

  let maxTrendTokens = $derived.by(() => {
    if (!stats?.last24h || stats.last24h.length === 0) return 1
    let m = 1
    for (const b of stats.last24h) {
      if (b.tokensSaved > m) m = b.tokensSaved
    }
    return m
  })
</script>

<div class="space-y-6">
  <!-- Section picker and window selector on the left, the action menu on the
       right, and what the section reports under them. Auto-refresh, manual
       refresh, and both exports were four more controls in this row, which is
       what made it wrap on a narrow window; they moved into one menu
       (issue #209). -->
  <div class="space-y-2">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div class="flex min-w-0 flex-wrap items-center gap-2">
        {@render headerLeft?.()}

        <!-- Window selector: the shared Usage dropdown, not a second strip -->
        <PeriodSelect
          value={since}
          options={SINCE_OPTIONS}
          showCustom={false}
          busy={loading}
          onChange={(next) => {
            since = next as SinceOption
            loadData()
          }}
        />
      </div>

      <ActionsMenu label="Compression analytics actions">
        <MenuItem
          label="Auto-refresh"
          icon="sync"
          checkbox
          note="15s"
          pressed={autoRefresh}
          onSelect={() => (autoRefresh = !autoRefresh)}
        />
        <MenuItem label="Refresh now" icon="refresh" disabled={loading} onSelect={loadData} />
        <div class="my-1 border-t border-border-subtle" role="separator"></div>
        <MenuItem label="Export CSV" icon="download" onSelect={exportCSV} />
        <MenuItem label="Export JSON" icon="data_object" onSelect={exportJSON} />
      </ActionsMenu>
    </div>

    <p class="text-sm text-text-muted">
      Prompt token reduction, engine execution efficiency, and savings telemetry.
    </p>
  </div>

  <!-- Hero StatCards -->
  <!-- Seven cards over track counts that divide them evenly. `lg:grid-cols-7`
       with six cards inside left the ROI card outside the grid, where it spanned
       nothing and left an empty column on wide screens (issue #200). -->
  <div class="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-7">
    <!-- Total Requests -->
    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
      <div class="flex items-center justify-between text-text-muted">
        <span class="text-xs font-medium uppercase tracking-wider">Total Requests</span>
        <span class="material-symbols-outlined text-[20px] text-brand-500">compress</span>
      </div>
      <div class="mt-3">
        <div class="text-2xl font-bold text-text-main">{totalAttempts.toLocaleString()}</div>
        <p class="text-xs text-text-subtle mt-1 truncate">
          {stats?.totalSkipped ? `${stats.totalSkipped} skipped` : 'Processed runs'}
        </p>
      </div>
    </div>

    <!-- Tokens Saved -->
    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
      <div class="flex items-center justify-between text-text-muted">
        <span class="text-xs font-medium uppercase tracking-wider">Tokens Saved</span>
        <span class="material-symbols-outlined text-[20px] text-emerald-500">token</span>
      </div>
      <div class="mt-3">
        <div class="text-2xl font-bold text-emerald-500">
          {(stats?.totalTokensSaved ?? 0).toLocaleString()}
        </div>
        <p class="text-xs text-text-subtle mt-1 truncate">Prompt reduction</p>
      </div>
    </div>

    <!-- Avg Savings -->
    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
      <div class="flex items-center justify-between text-text-muted">
        <span class="text-xs font-medium uppercase tracking-wider">Avg Savings</span>
        <span class="material-symbols-outlined text-[20px] text-blue-400">percent</span>
      </div>
      <div class="mt-3">
        <div class="text-2xl font-bold text-text-main">
          {stats?.avgSavingsPct ? `${stats.avgSavingsPct}%` : '0%'}
        </div>
        <p class="text-xs text-text-subtle mt-1 truncate">Per request savings</p>
      </div>
    </div>

    <!-- Avg Duration -->
    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
      <div class="flex items-center justify-between text-text-muted">
        <span class="text-xs font-medium uppercase tracking-wider">Avg Duration</span>
        <span class="material-symbols-outlined text-[20px] text-purple-400">timer</span>
      </div>
      <div class="mt-3">
        <div class="text-2xl font-bold text-text-main">
          {stats?.avgDurationMs ?? 0}ms
        </div>
        <p class="text-xs text-text-subtle mt-1 truncate">Compression overhead</p>
      </div>
    </div>

    <!-- Usage Receipts -->
    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
      <div class="flex items-center justify-between text-text-muted">
        <span class="text-xs font-medium uppercase tracking-wider">Receipts</span>
        <span class="material-symbols-outlined text-[20px] text-cyan-400">receipt_long</span>
      </div>
      <div class="mt-3">
        <div class="text-2xl font-bold text-text-main">
          {(stats?.realUsage?.requestsWithReceipts ?? 0).toLocaleString()}
        </div>
        <p class="text-xs text-text-subtle mt-1 truncate">
          {(stats?.realUsage?.totalTokens ?? 0).toLocaleString()} real tokens
        </p>
      </div>
    </div>

    <!-- Est. Cost Saved -->
    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
      <div class="flex items-center justify-between text-text-muted">
        <span class="text-xs font-medium uppercase tracking-wider">Est. Saved</span>
        <span class="material-symbols-outlined text-[20px] text-emerald-400">attach_money</span>
      </div>
      <div class="mt-3">
        <div class="text-2xl font-bold text-emerald-500">
          ${(stats?.realUsage?.estimatedUsdSaved ?? 0).toFixed(2)}
        </div>
        <p class="text-xs text-text-subtle mt-1 truncate">Estimated USD saved</p>
      </div>
    </div>
    <!-- ROI Speed -->
    <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between col-span-2 sm:col-span-1 lg:col-span-2 xl:col-span-1">
      <div class="flex items-center justify-between text-text-muted">
        <span class="text-xs font-medium uppercase tracking-wider">ROI Speed</span>
        <span class="material-symbols-outlined text-[20px] text-emerald-400">bolt</span>
      </div>
      <div class="mt-3">
        <div class="text-2xl font-bold text-emerald-500">
          {(stats?.roiTokensPerMs ?? 0).toLocaleString()} <span class="text-xs font-normal text-text-muted">tok/ms</span>
        </div>
        <p class="text-xs text-text-subtle mt-1 truncate">
          ~{Math.round((stats?.roiTokensPerMs ?? 0) * 1000).toLocaleString()} tokens/sec
        </p>
      </div>
    </div>
  </div>

  <!-- Hourly Trend Chart -->
  <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm">
    <div class="flex flex-col gap-1 mb-4">
      <h3 class="text-sm font-semibold text-text-main flex items-center gap-1.5">
        <span class="material-symbols-outlined text-[18px] text-brand-500">monitoring</span>
        Hourly Token Savings Trend
      </h3>
      <p class="text-xs text-text-subtle">Tokens saved over recent hours.</p>
    </div>

    {#if !stats?.last24h || stats.last24h.length === 0}
      <div class="py-12 text-center text-xs text-text-subtle">
        No compression activity recorded for the selected window.
      </div>
    {:else}
      <div class="h-44 w-full flex items-end gap-1.5 pt-4 overflow-x-auto">
        {#each stats.last24h as b}
          {@const heightPct = Math.max(8, Math.round((b.tokensSaved / maxTrendTokens) * 100))}
          <div class="flex-1 min-w-[20px] h-full flex flex-col justify-end items-center group relative">
            <!-- Tooltip -->
            <div class="absolute bottom-full mb-2 hidden group-hover:flex flex-col items-center z-20 pointer-events-none">
              <div class="rounded-lg bg-surface-3 border border-border p-2 text-[11px] shadow-lg whitespace-nowrap text-text-main">
                <div class="font-semibold text-text-muted">{b.hour.replace('T', ' ').replace(':00:00Z', ':00')}</div>
                <div class="mt-1 flex items-center justify-between gap-3">
                  <span>Runs:</span> <span class="font-bold">{b.count}</span>
                </div>
                <div class="flex items-center justify-between gap-3 text-emerald-500">
                  <span>Tokens Saved:</span> <span class="font-bold">{b.tokensSaved.toLocaleString()}</span>
                </div>
              </div>
            </div>

            <!-- Bar -->
            <div
              class="w-full rounded-t transition-all duration-300 bg-emerald-500 hover:bg-emerald-400 cursor-pointer"
              style="height: {heightPct}%"
            ></div>
          </div>
        {/each}
      </div>
      <div class="flex items-center justify-between text-[11px] text-text-subtle mt-2 pt-2 border-t border-border-subtle">
        <span>Hourly Buckets</span>
        <span>Window: {since.toUpperCase()}</span>
      </div>
    {/if}
  </div>

  <!-- Breakdown Grid: Modes, Providers, Models -->
  <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
    <!-- Breakdown by Mode -->
    <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm space-y-4">
      <div class="flex items-center justify-between">
        <h3 class="text-sm font-semibold text-text-main flex items-center gap-1.5">
          <span class="material-symbols-outlined text-[18px] text-brand-500">tune</span>
          Breakdown by Mode
        </h3>
        <span class="text-xs text-text-muted">{modeList.length} mode(s)</span>
      </div>

      {#if modeList.length === 0}
        <div class="py-8 text-center text-xs text-text-subtle">
          No compression mode data recorded yet.
        </div>
      {:else}
        <div class="space-y-4">
          {#each modeList as m}
            <div class="space-y-1.5">
              <div class="flex items-center justify-between text-xs">
                <span class="font-semibold text-text-main uppercase font-mono">{m.mode}</span>
                <span class="text-text-muted">
                  <span class="text-text-main font-medium">{m.count}</span> runs •
                  <span class="text-emerald-500 font-mono">{m.tokensSaved.toLocaleString()}</span> tokens
                  {#if m.avgSavingsPct > 0}
                    • {m.avgSavingsPct}% avg
                  {/if}
                  {#if m.skipped > 0}
                    • <span class="text-amber-500">{m.skipped} skipped</span>
                  {/if}
                </span>
              </div>
              <div class="h-2 w-full rounded-full bg-surface-2 overflow-hidden">
                <div
                  class="h-full rounded-full bg-primary transition-all duration-300"
                  style="width: {m.pct}%"
                ></div>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- Breakdown by Provider -->
    <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm space-y-4">
      <div class="flex items-center justify-between">
        <h3 class="text-sm font-semibold text-text-main flex items-center gap-1.5">
          <span class="material-symbols-outlined text-[18px] text-brand-500">dns</span>
          Breakdown by Provider
        </h3>
        <span class="text-xs text-text-muted">{providerList.length} provider(s)</span>
      </div>

      {#if providerList.length === 0}
        <div class="py-8 text-center text-xs text-text-subtle">
          No provider compression data recorded yet.
        </div>
      {:else}
        <div class="space-y-4">
          {#each providerList as p}
            <div class="space-y-1.5">
              <div class="flex items-center justify-between text-xs">
                <span class="font-semibold text-text-main flex items-center gap-2">
                  <span class="w-2 h-2 rounded-full bg-emerald-500"></span>
                  {p.provider}
                </span>
                <span class="text-text-muted">
                  <span class="text-text-main font-medium">{p.count}</span> runs •
                  <span class="text-cyan-400 font-mono font-medium">{p.tokensSaved.toLocaleString()}</span> tokens
                </span>
              </div>
              <div class="h-2 w-full rounded-full bg-surface-2 overflow-hidden">
                <div
                  class="h-full rounded-full bg-emerald-500 transition-all duration-300"
                  style="width: {p.pct}%"
                ></div>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- Breakdown by Model -->
    <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm space-y-4">
      <div class="flex items-center justify-between">
        <h3 class="text-sm font-semibold text-text-main flex items-center gap-1.5">
          <span class="material-symbols-outlined text-[18px] text-brand-500">model_training</span>
          Breakdown by Model
        </h3>
        <span class="text-xs text-text-muted">{modelList.length} model(s)</span>
      </div>

      {#if modelList.length === 0}
        <div class="py-8 text-center text-xs text-text-subtle">
          No model compression data recorded yet.
        </div>
      {:else}
        <div class="space-y-4">
          {#each modelList as m}
            <div class="space-y-1.5">
              <div class="flex items-center justify-between text-xs">
                <span class="font-semibold text-text-main font-mono text-[11px] truncate max-w-[140px]" title={m.model}>
                  {m.model}
                </span>
                <span class="text-text-muted text-[11px]">
                  <span class="text-text-main font-medium">{m.count}</span> runs •
                  <span class="text-cyan-400 font-mono font-medium">{m.tokensSaved.toLocaleString()}</span> tokens
                  {#if m.avgSavingsPct > 0}
                    • {m.avgSavingsPct}%
                  {/if}
                </span>
              </div>
              <div class="h-2 w-full rounded-full bg-surface-2 overflow-hidden">
                <div
                  class="h-full rounded-full bg-blue-500 transition-all duration-300"
                  style="width: {m.pct}%"
                ></div>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>

  <!-- Top 10 Biggest Savers Table -->
  <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm space-y-4">
    <div class="flex items-center justify-between">
      <div>
        <h3 class="text-sm font-semibold text-text-main flex items-center gap-1.5">
          <span class="material-symbols-outlined text-[18px] text-brand-500">trophy</span>
          Top 10 Biggest Token Savers
        </h3>
        <p class="text-xs text-text-subtle">Individual requests with the highest prompt token reduction.</p>
      </div>
      <span class="rounded-full bg-surface-2 px-2.5 py-1 text-xs font-semibold text-text-muted">
        {stats?.topSavers?.length ?? 0} request(s)
      </span>
    </div>

    {#if !stats?.topSavers || stats.topSavers.length === 0}
      <div class="py-8 text-center text-xs text-text-subtle">
        No recorded top compression runs yet.
      </div>
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full text-left text-xs">
          <thead>
            <tr class="border-b border-border text-text-muted">
              <th class="py-2.5 px-3 font-semibold">Request ID</th>
              <th class="py-2.5 px-3 font-semibold">Timestamp</th>
              <th class="py-2.5 px-3 font-semibold">Provider / Model</th>
              <th class="py-2.5 px-3 font-semibold">Mode</th>
              <th class="py-2.5 px-3 font-semibold text-right">Original &rarr; Compressed</th>
              <th class="py-2.5 px-3 font-semibold text-right">Tokens Saved</th>
              <th class="py-2.5 px-3 font-semibold text-right">Savings %</th>
              <th class="py-2.5 px-3 font-semibold text-right">Overhead</th>
              <th class="py-2.5 px-3 font-semibold text-right">Est. USD</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border-subtle">
            {#each stats.topSavers as s (s.requestId || s.timestamp)}
              <tr class="hover:bg-surface-2/60 transition-colors">
                <td class="py-2.5 px-3 font-mono text-[11px] text-text-main">
                  <div class="flex items-center gap-1.5 max-w-[140px]">
                    <span class="truncate" title={s.requestId || '-'}>{s.requestId || '-'}</span>
                    {#if s.requestId}
                      <button
                        type="button"
                        onclick={() => copyToClipboard(s.requestId)}
                        class="text-text-subtle hover:text-text-main shrink-0"
                        title="Copy Request ID"
                      >
                        <span class="material-symbols-outlined text-[13px]">content_copy</span>
                      </button>
                    {/if}
                  </div>
                </td>
                <td class="py-2.5 px-3 text-text-subtle whitespace-nowrap">
                  {s.timestamp ? s.timestamp.slice(0, 16).replace('T', ' ') : '-'}
                </td>
                <td class="py-2.5 px-3 text-text-muted">
                  <span class="font-medium text-text-main">{s.provider}</span> / <span class="font-mono text-[11px]">{s.model}</span>
                </td>
                <td class="py-2.5 px-3">
                  <span class="rounded bg-surface-2 px-1.5 py-0.5 text-[10px] font-mono font-semibold uppercase text-brand-500">
                    {s.mode}
                  </span>
                </td>
                <td class="py-2.5 px-3 text-right font-mono text-text-muted">
                  {s.originalTokens.toLocaleString()} &rarr; {s.compressedTokens.toLocaleString()}
                </td>
                <td class="py-2.5 px-3 text-right font-mono font-semibold text-cyan-400">
                  {s.tokensSaved.toLocaleString()}
                </td>
                <td class="py-2.5 px-3 text-right font-medium text-emerald-500">
                  {s.savingsPct}%
                </td>
                <td class="py-2.5 px-3 text-right text-purple-400 font-mono">
                  {s.durationMs}ms
                </td>
                <td class="py-2.5 px-3 text-right font-medium text-emerald-400">
                  ${s.estimatedUsd.toFixed(3)}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>

  <!-- Real Usage Receipts & Telemetry Details -->
  <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm">
    <h3 class="text-sm font-semibold text-text-main mb-3 flex items-center gap-1.5">
      <span class="material-symbols-outlined text-[18px] text-brand-500">receipt_long</span>
      Upstream Usage Receipts Telemetry
    </h3>

    <div class="grid grid-cols-2 gap-4 sm:grid-cols-4 pt-2">
      <div class="p-3 rounded-xl bg-surface-2/60 border border-border-subtle">
        <div class="text-[11px] font-medium text-text-muted uppercase">Prompt Tokens</div>
        <div class="text-lg font-bold text-text-main mt-1 font-mono">
          {(stats?.realUsage?.promptTokens ?? 0).toLocaleString()}
        </div>
      </div>
      <div class="p-3 rounded-xl bg-surface-2/60 border border-border-subtle">
        <div class="text-[11px] font-medium text-text-muted uppercase">Completion Tokens</div>
        <div class="text-lg font-bold text-text-main mt-1 font-mono">
          {(stats?.realUsage?.completionTokens ?? 0).toLocaleString()}
        </div>
      </div>
      <div class="p-3 rounded-xl bg-surface-2/60 border border-border-subtle">
        <div class="text-[11px] font-medium text-text-muted uppercase">Total Tokens</div>
        <div class="text-lg font-bold text-text-main mt-1 font-mono">
          {(stats?.realUsage?.totalTokens ?? 0).toLocaleString()}
        </div>
      </div>
      <div class="p-3 rounded-xl bg-surface-2/60 border border-border-subtle">
        <div class="text-[11px] font-medium text-text-muted uppercase">Cache Read Tokens</div>
        <div class="text-lg font-bold text-cyan-400 mt-1 font-mono">
          {(stats?.realUsage?.cacheReadTokens ?? 0).toLocaleString()}
        </div>
      </div>
    </div>
  </div>
</div>
