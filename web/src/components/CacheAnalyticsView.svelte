<script lang="ts">
  import { onMount, onDestroy, type Snippet } from 'svelte'
  import {
    api,
    type CacheStatsResponse,
    type CacheEntryMeta,
  } from '../api/client'
  import { notifications } from '../lib/notifications'
  import { copyToClipboard } from '../lib/clipboard'
  import ActionsMenu from './analytics/ActionsMenu.svelte'

  import MenuItem from './analytics/MenuItem.svelte'
  import ViewSelect from './analytics/ViewSelect.svelte'
  import type { ViewOption } from './analytics/types'

  type CacheView = 'prompt' | 'semantic'

  interface Props {
    /**
     * The Usage section picker, rendered in the left of the header so this
     * section's own controls sit beside it instead of in a row above
     * (issue #209).
     */
    headerLeft?: Snippet
  }

  let { headerLeft }: Props = $props()

  const VIEW_OPTIONS: ViewOption[] = [
    { value: 'prompt', label: 'Prompt Cache', icon: 'bolt' },
    { value: 'semantic', label: 'Semantic Cache', icon: 'psychology' },
  ]

  let activeView = $state<CacheView>('prompt')
  let loading = $state(true)
  let stats = $state<CacheStatsResponse | null>(null)
  let trendHours = $state(24)
  let autoRefresh = $state(false)
  let refreshTimer: ReturnType<typeof setInterval> | null = null

  // Semantic entries state
  let entries = $state<CacheEntryMeta[]>([])
  let entriesLoading = $state(false)
  let page = $state(1)
  let limit = 10
  let totalPages = $state(1)
  let totalEntries = $state(0)
  let search = $state('')
  let modelFilter = $state('')
  let invalidateModelInput = $state('')

  async function loadStats() {
    loading = true
    try {
      stats = await api.getCacheStats(trendHours)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      notifications.error(msg, 'Failed to load cache stats')
    } finally {
      loading = false
    }
  }

  async function loadEntries() {
    entriesLoading = true
    try {
      const res = await api.getCacheEntries({
        page,
        limit,
        search: search.trim() || undefined,
        model: modelFilter.trim() || undefined,
        sortBy: 'created_at',
        sortOrder: 'desc',
      })
      entries = res.entries || []
      totalPages = res.pagination?.totalPages || 1
      totalEntries = res.pagination?.total || 0
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      notifications.error(msg, 'Failed to load cache entries')
    } finally {
      entriesLoading = false
    }
  }

  async function handleClearAll() {
    if (!confirm('Clear all semantic cache entries?')) return
    try {
      await api.deleteCache()
      notifications.success('Semantic cache cleared successfully')
      await loadStats()
      await loadEntries()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      notifications.error(msg, 'Failed to clear cache')
    }
  }

  async function handleDeleteEntry(id: string) {
    try {
      await api.deleteCacheEntry(id)
      notifications.success('Cache entry deleted')
      await loadStats()
      await loadEntries()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      notifications.error(msg, 'Failed to delete entry')
    }
  }

  async function handleInvalidateByModel() {
    const m = invalidateModelInput.trim()
    if (!m) return
    try {
      const res = await api.deleteCache({ model: m })
      notifications.success(`Invalidated ${res.count ?? 0} entries for model ${m}`)
      invalidateModelInput = ''
      await loadStats()
      await loadEntries()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      notifications.error(msg, 'Failed to invalidate model')
    }
  }

  function handleSearchInput(e: Event) {
    const target = e.target as HTMLInputElement
    search = target.value
    page = 1
    loadEntries()
  }

  // Picking Semantic Cache has to load its entry list, which the Prompt view
  // never reads; the reverse needs nothing extra.
  function selectView(next: CacheView) {
    activeView = next
    if (next === 'semantic') loadEntries()
  }

  // One refresh for both halves of the section: the entries list is what the
  // Semantic view shows, the stats what everything else on this section reads.
  function refreshAll() {
    loadStats()
    if (activeView === 'semantic') loadEntries()
  }

  onMount(() => {
    loadStats()
    loadEntries()
  })

  onDestroy(() => {
    if (refreshTimer) clearInterval(refreshTimer)
  })

  function exportJSON() {
    if (!stats) return
    const blob = new Blob([JSON.stringify(stats, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `cache-analytics-${trendHours}h.json`
    a.click()
    URL.revokeObjectURL(url)
    notifications.success('Exported JSON successfully')
  }

  function exportCSV() {
    if (!stats) return
    const lines = ['Category,Name,TotalRequests,CachedRequests,CachedTokens,CreationTokens,HitRate']
    for (const p of providerRows) {
      lines.push(`Provider,${p.name},${p.totalRequests},${p.cachedRequests},${p.cachedTokens},${p.cacheCreationTokens},${p.rate.toFixed(1)}%`)
    }
    for (const m of modelRows) {
      lines.push(`Model,${m.name},${m.totalRequests},${m.cachedRequests},${m.cachedTokens},${m.cacheCreationTokens},${m.rate.toFixed(1)}%`)
    }
    const blob = new Blob([lines.join('\n')], { type: 'text/csv' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `cache-analytics-${trendHours}h.csv`
    a.click()
    URL.revokeObjectURL(url)
    notifications.success('Exported CSV successfully')
  }

  $effect(() => {
    if (autoRefresh) {
      if (!refreshTimer) {
        refreshTimer = setInterval(refreshAll, 15000)
      }
    } else {
      if (refreshTimer) {
        clearInterval(refreshTimer)
        refreshTimer = null
      }
    }
  })

  // Derived metrics
  let pc = $derived(stats?.promptCache)
  let sc = $derived(stats?.semanticCache)

  let cacheRate = $derived.by(() => {
    if (!pc || pc.totalRequests === 0) return 0
    return Math.min(100, Math.max(0, (pc.requestsWithCacheControl / pc.totalRequests) * 100))
  })

  let reuseRatio = $derived.by(() => {
    if (!pc || pc.totalInputTokens === 0) return 0
    return Math.min(100, Math.max(0, (pc.totalCachedTokens / pc.totalInputTokens) * 100))
  })

  let providerRows = $derived.by(() => {
    if (!pc?.byProvider) return []
    return Object.entries(pc.byProvider).map(([name, data]) => {
      const totalReq = data.totalRequests ?? 0
      const cachedReq = data.cachedRequests ?? 0
      const rate = totalReq > 0 ? (cachedReq / totalReq) * 100 : 0
      return {
        name,
        totalRequests: totalReq,
        cachedRequests: cachedReq,
        inputTokens: data.inputTokens ?? 0,
        cachedTokens: data.cachedTokens ?? 0,
        cacheCreationTokens: data.cacheCreationTokens ?? 0,
        rate,
      }
    }).sort((a, b) => b.cachedTokens - a.cachedTokens)
  })

  let modelRows = $derived.by(() => {
    if (!pc?.byModel) return []
    return Object.entries(pc.byModel).map(([name, data]) => {
      const totalReq = data.totalRequests ?? 0
      const cachedReq = data.cachedRequests ?? 0
      const rate = totalReq > 0 ? (cachedReq / totalReq) * 100 : 0
      return {
        name,
        totalRequests: totalReq,
        cachedRequests: cachedReq,
        inputTokens: data.inputTokens ?? 0,
        cachedTokens: data.cachedTokens ?? 0,
        cacheCreationTokens: data.cacheCreationTokens ?? 0,
        rate,
      }
    }).sort((a, b) => b.cachedTokens - a.cachedTokens)
  })

  let maxTrendRequests = $derived.by(() => {
    if (!stats?.trend || stats.trend.length === 0) return 1
    let m = 1
    for (const pt of stats.trend) {
      if (pt.requests > m) m = pt.requests
    }
    return m
  })
</script>

<div class="space-y-6">
  <!-- Section picker and sub-view picker on the left, the action menu on the
       right, and what the section reports under them. Auto-refresh, manual
       refresh, and both exports were four more controls in this row, which is
       what made it wrap on a narrow window; they moved into one menu
       (issue #209). -->
  <div class="space-y-2">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div class="flex min-w-0 flex-wrap items-center gap-2">
        {@render headerLeft?.()}

        <ViewSelect
          value={activeView}
          options={VIEW_OPTIONS}
          ariaLabel="Cache view"
          onChange={(next) => selectView(next as CacheView)}
        />
      </div>

      <div class="flex items-center gap-2">
        <ActionsMenu label="Cache analytics actions">
          <MenuItem
            label="Auto-refresh"
            icon="sync"
            checkbox
            note="15s"
            pressed={autoRefresh}
            onSelect={() => (autoRefresh = !autoRefresh)}
          />
          <MenuItem
            label="Refresh now"
            icon="refresh"
            disabled={loading}
            onSelect={refreshAll}
          />
          <div class="my-1 border-t border-border-subtle" role="separator"></div>
          <MenuItem label="Export CSV" icon="download" onSelect={exportCSV} />
          <MenuItem label="Export JSON" icon="data_object" onSelect={exportJSON} />
        </ActionsMenu>
      </div>
    </div>

    <p class="text-sm text-text-muted">
      Prompt caching and semantic deduplication tracking across all AI providers.
    </p>
  </div>

  {#if activeView === 'prompt'}
    <!-- Hero StatCards -->
    <div class="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
      <!-- Cache Rate -->
      <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
        <div class="flex items-center justify-between text-text-muted">
          <span class="text-xs font-medium uppercase tracking-wider">Cache Rate</span>
          <span class="material-symbols-outlined text-[20px] text-emerald-500">speed</span>
        </div>
        <div class="mt-3">
          <div class="text-2xl font-bold text-text-main">{cacheRate.toFixed(1)}%</div>
          <p class="text-xs text-text-subtle mt-1 truncate">
            {pc?.requestsWithCacheControl ?? 0} / {pc?.totalRequests ?? 0} requests
          </p>
        </div>
      </div>

      <!-- Reuse Ratio -->
      <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
        <div class="flex items-center justify-between text-text-muted">
          <span class="text-xs font-medium uppercase tracking-wider">Reuse Ratio</span>
          <span class="material-symbols-outlined text-[20px] text-blue-400">savings</span>
        </div>
        <div class="mt-3">
          <div class="text-2xl font-bold text-text-main">{reuseRatio.toFixed(1)}%</div>
          <p class="text-xs text-text-subtle mt-1 truncate">Cached vs input tokens</p>
        </div>
      </div>

      <!-- Cached Tokens Read -->
      <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
        <div class="flex items-center justify-between text-text-muted">
          <span class="text-xs font-medium uppercase tracking-wider">Cached Tokens</span>
          <span class="material-symbols-outlined text-[20px] text-cyan-400">token</span>
        </div>
        <div class="mt-3">
          <div class="text-2xl font-bold text-text-main">
            {(pc?.totalCachedTokens ?? 0).toLocaleString()}
          </div>
          <p class="text-xs text-text-subtle mt-1 truncate">Read from prompt cache</p>
        </div>
      </div>

      <!-- Cache Creation Tokens -->
      <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between">
        <div class="flex items-center justify-between text-text-muted">
          <span class="text-xs font-medium uppercase tracking-wider">Creation Tokens</span>
          <span class="material-symbols-outlined text-[20px] text-purple-400">upload</span>
        </div>
        <div class="mt-3">
          <div class="text-2xl font-bold text-text-main">
            {(pc?.totalCacheCreationTokens ?? 0).toLocaleString()}
          </div>
          <p class="text-xs text-text-subtle mt-1 truncate">Written to prompt cache</p>
        </div>
      </div>

      <!-- Est Cost Saved -->
      <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm flex flex-col justify-between col-span-2 sm:col-span-1">
        <div class="flex items-center justify-between text-text-muted">
          <span class="text-xs font-medium uppercase tracking-wider">Cost Saved</span>
          <span class="material-symbols-outlined text-[20px] text-emerald-400">attach_money</span>
        </div>
        <div class="mt-3">
          <div class="text-2xl font-bold text-emerald-500">
            ${(pc?.estimatedCostSaved ?? 0).toFixed(2)}
          </div>
          <p class="text-xs text-text-subtle mt-1 truncate">Estimated savings</p>
        </div>
      </div>
    </div>

    <!-- 24h Trend Chart -->
    <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm">
      <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between mb-4">
        <div>
          <h3 class="text-sm font-semibold text-text-main flex items-center gap-1.5">
            <span class="material-symbols-outlined text-[18px] text-brand-500">monitoring</span>
            Hourly Cache Trend
          </h3>
          <p class="text-xs text-text-subtle">Requests and cached tokens over time.</p>
        </div>
        <div class="flex items-center gap-1 text-xs">
          {#each [12, 24, 48, 72] as h}
            <button
              type="button"
              onclick={() => {
                trendHours = h
                loadStats()
              }}
              class={`rounded-lg px-2.5 py-1 font-medium transition-colors ${
                trendHours === h
                  ? 'bg-primary text-white'
                  : 'bg-surface-2 text-text-muted hover:text-text-main'
              }`}
            >
              {h}h
            </button>
          {/each}
        </div>
      </div>

      {#if !stats?.trend || stats.trend.length === 0}
        <div class="py-12 text-center text-xs text-text-subtle">
          No cache activity recorded in the last {trendHours} hours.
        </div>
      {:else}
        <div class="h-44 w-full flex items-end gap-1.5 pt-4 overflow-x-auto">
          {#each stats.trend as pt}
            {@const heightPct = Math.max(8, Math.round((pt.requests / maxTrendRequests) * 100))}
            {@const cachedRatio = pt.requests > 0 ? (pt.cachedRequests / pt.requests) * 100 : 0}
            <div class="flex-1 min-w-[20px] h-full flex flex-col justify-end items-center group relative">
              <!-- Tooltip -->
              <div class="absolute bottom-full mb-2 hidden group-hover:flex flex-col items-center z-20 pointer-events-none">
                <div class="rounded-lg bg-surface-3 border border-border p-2 text-[11px] shadow-lg whitespace-nowrap text-text-main">
                  <div class="font-semibold text-text-muted">{pt.timestamp.replace('T', ' ').replace(':00:00Z', ':00')}</div>
                  <div class="mt-1 flex items-center justify-between gap-3">
                    <span>Total Req:</span> <span class="font-bold">{pt.requests}</span>
                  </div>
                  <div class="flex items-center justify-between gap-3 text-emerald-500">
                    <span>Cached Req:</span> <span class="font-bold">{pt.cachedRequests}</span>
                  </div>
                  <div class="flex items-center justify-between gap-3 text-cyan-400">
                    <span>Cached Tokens:</span> <span class="font-bold">{pt.cachedTokens.toLocaleString()}</span>
                  </div>
                </div>
              </div>

              <!-- Bar visual -->
              <div
                class="w-full rounded-t transition-all duration-300 relative overflow-hidden bg-primary/20 hover:bg-primary/30"
                style="height: {heightPct}%"
              >
                <!-- Filled portion for cached requests -->
                <div
                  class="w-full bg-emerald-500 absolute bottom-0 left-0 transition-all duration-300"
                  style="height: {cachedRatio}%"
                ></div>
              </div>
            </div>
          {/each}
        </div>
        <div class="flex items-center justify-between text-[11px] text-text-subtle mt-2 pt-2 border-t border-border-subtle">
          <div class="flex items-center gap-4">
            <span class="inline-flex items-center gap-1.5">
              <span class="w-2.5 h-2.5 rounded-sm bg-primary/30"></span> Total Requests
            </span>
            <span class="inline-flex items-center gap-1.5">
              <span class="w-2.5 h-2.5 rounded-sm bg-emerald-500"></span> Cached Requests
            </span>
          </div>
          <span>Past {trendHours} Hours</span>
        </div>
      {/if}
    </div>

    <!-- Provider Breakdown Table -->
    <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm">
      <h3 class="text-sm font-semibold text-text-main mb-3 flex items-center gap-1.5">
        <span class="material-symbols-outlined text-[18px] text-brand-500">dns</span>
        Breakdown by Provider
      </h3>

      {#if providerRows.length === 0}
        <div class="py-8 text-center text-xs text-text-subtle">
          No provider prompt cache data recorded yet.
        </div>
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-left text-xs">
            <thead>
              <tr class="border-b border-border text-text-muted">
                <th class="py-2.5 px-3 font-semibold">Provider</th>
                <th class="py-2.5 px-3 font-semibold text-right">Cached / Total Req</th>
                <th class="py-2.5 px-3 font-semibold text-right">Cached Tokens</th>
                <th class="py-2.5 px-3 font-semibold text-right">Creation Tokens</th>
                <th class="py-2.5 px-3 font-semibold text-right">Hit Rate</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border-subtle">
              {#each providerRows as row}
                <tr class="hover:bg-surface-2/60 transition-colors">
                  <td class="py-2.5 px-3 font-semibold text-text-main flex items-center gap-2">
                    <span class="w-2 h-2 rounded-full bg-emerald-500"></span>
                    {row.name}
                  </td>
                  <td class="py-2.5 px-3 text-right text-text-muted">
                    <span class="text-text-main font-medium">{row.cachedRequests}</span> / {row.totalRequests}
                  </td>
                  <td class="py-2.5 px-3 text-right text-cyan-400 font-mono font-medium">
                    {row.cachedTokens.toLocaleString()}
                  </td>
                  <td class="py-2.5 px-3 text-right text-purple-400 font-mono font-medium">
                    {row.cacheCreationTokens.toLocaleString()}
                  </td>
                  <td class="py-2.5 px-3 text-right font-medium text-emerald-500">
                    {row.rate.toFixed(1)}%
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>

    <!-- Model Breakdown Table -->
    <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm">
      <h3 class="text-sm font-semibold text-text-main mb-3 flex items-center gap-1.5">
        <span class="material-symbols-outlined text-[18px] text-brand-500">model_training</span>
        Breakdown by Model
      </h3>

      {#if modelRows.length === 0}
        <div class="py-8 text-center text-xs text-text-subtle">
          No model prompt cache data recorded yet.
        </div>
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-left text-xs">
            <thead>
              <tr class="border-b border-border text-text-muted">
                <th class="py-2.5 px-3 font-semibold">Model</th>
                <th class="py-2.5 px-3 font-semibold text-right">Cached / Total Req</th>
                <th class="py-2.5 px-3 font-semibold text-right">Cached Tokens</th>
                <th class="py-2.5 px-3 font-semibold text-right">Creation Tokens</th>
                <th class="py-2.5 px-3 font-semibold text-right">Hit Rate</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border-subtle">
              {#each modelRows as row}
                <tr class="hover:bg-surface-2/60 transition-colors">
                  <td class="py-2.5 px-3 font-semibold text-text-main font-mono text-[11px]">
                    {row.name}
                  </td>
                  <td class="py-2.5 px-3 text-right text-text-muted">
                    <span class="text-text-main font-medium">{row.cachedRequests}</span> / {row.totalRequests}
                  </td>
                  <td class="py-2.5 px-3 text-right text-cyan-400 font-mono font-medium">
                    {row.cachedTokens.toLocaleString()}
                  </td>
                  <td class="py-2.5 px-3 text-right text-purple-400 font-mono font-medium">
                    {row.cacheCreationTokens.toLocaleString()}
                  </td>
                  <td class="py-2.5 px-3 text-right font-medium text-emerald-500">
                    {row.rate.toFixed(1)}%
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>

  {:else if activeView === 'semantic'}
    <!-- Semantic Cache View -->
    <div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
      <!-- Memory Entries -->
      <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm">
        <div class="flex items-center justify-between text-text-muted">
          <span class="text-xs font-medium uppercase tracking-wider">Memory Entries</span>
          <span class="material-symbols-outlined text-[20px] text-blue-400">memory</span>
        </div>
        <div class="mt-3">
          <div class="text-2xl font-bold text-text-main">{sc?.memoryEntries ?? 0}</div>
          <p class="text-xs text-text-subtle mt-1">In-memory LRU store</p>
        </div>
      </div>

      <!-- Hit Rate -->
      <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm">
        <div class="flex items-center justify-between text-text-muted">
          <span class="text-xs font-medium uppercase tracking-wider">Hit Rate</span>
          <span class="material-symbols-outlined text-[20px] text-emerald-500">percent</span>
        </div>
        <div class="mt-3">
          <div class="text-2xl font-bold text-emerald-500">{sc?.hitRate ?? '0.0'}%</div>
          <p class="text-xs text-text-subtle mt-1">{sc?.hits ?? 0} hits / {(sc?.hits ?? 0) + (sc?.misses ?? 0)} total</p>
        </div>
      </div>

      <!-- Cache Hits -->
      <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm">
        <div class="flex items-center justify-between text-text-muted">
          <span class="text-xs font-medium uppercase tracking-wider">Hits / Misses</span>
          <span class="material-symbols-outlined text-[20px] text-purple-400">sync_alt</span>
        </div>
        <div class="mt-3">
          <div class="text-2xl font-bold text-text-main">{sc?.hits ?? 0} / {sc?.misses ?? 0}</div>
          <p class="text-xs text-text-subtle mt-1">Exact & semantic matches</p>
        </div>
      </div>

      <!-- Tokens Saved -->
      <div class="rounded-2xl border border-border bg-surface p-4 shadow-sm">
        <div class="flex items-center justify-between text-text-muted">
          <span class="text-xs font-medium uppercase tracking-wider">Tokens Saved</span>
          <span class="material-symbols-outlined text-[20px] text-emerald-400">savings</span>
        </div>
        <div class="mt-3">
          <div class="text-2xl font-bold text-emerald-400">{(sc?.tokensSaved ?? 0).toLocaleString()}</div>
          <p class="text-xs text-text-subtle mt-1">From cached responses</p>
        </div>
      </div>
    </div>

    <!-- Management & Actions Bar -->
    <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm space-y-4">
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h3 class="text-sm font-semibold text-text-main flex items-center gap-1.5">
            <span class="material-symbols-outlined text-[18px] text-brand-500">tune</span>
            Cache Invalidation & Cleanup
          </h3>
          <p class="text-xs text-text-subtle">Invalidate cached responses by model or clear the entire store.</p>
        </div>

        <div class="flex items-center gap-2">
          <button
            type="button"
            onclick={handleClearAll}
            class="flex items-center gap-1.5 rounded-xl border border-red-500/30 bg-red-500/10 px-3.5 py-2 text-xs font-semibold text-red-500 hover:bg-red-500/20 transition-colors"
          >
            <span class="material-symbols-outlined text-[16px]">delete_sweep</span>
            Clear All Cache
          </button>
        </div>
      </div>

      <!-- Invalidate by Model Form -->
      <div class="flex flex-wrap items-center gap-2 pt-2 border-t border-border-subtle">
        <input
          type="text"
          placeholder="Invalidate by model (e.g. gpt-4o, claude-3-5)"
          bind:value={invalidateModelInput}
          class="flex-1 min-w-[240px] rounded-xl border border-border bg-surface-2 px-3 py-1.5 text-xs text-text-main focus:outline-none focus:border-brand-500"
        />
        <button
          type="button"
          onclick={handleInvalidateByModel}
          class="rounded-xl border border-border bg-surface px-3 py-1.5 text-xs font-semibold text-text-main hover:bg-surface-2 transition-colors"
        >
          Invalidate Model
        </button>
      </div>
    </div>

    <!-- Entries Table -->
    <div class="rounded-2xl border border-border bg-surface p-5 shadow-sm space-y-4">
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div class="flex items-center gap-2">
          <h3 class="text-sm font-semibold text-text-main flex items-center gap-1.5">
            <span class="material-symbols-outlined text-[18px] text-brand-500">table_rows</span>
            Cached Entries
          </h3>
          <span class="rounded-full bg-surface-2 px-2 py-0.5 text-[10px] font-semibold text-text-muted">
            {totalEntries}
          </span>
        </div>

        <!-- Search input -->
        <div class="flex items-center gap-2">
          <div class="relative">
            <span class="material-symbols-outlined absolute left-2.5 top-1/2 -translate-y-1/2 text-[16px] text-text-subtle">
              search
            </span>
            <input
              type="text"
              placeholder="Search signature/key..."
              value={search}
              oninput={handleSearchInput}
              class="w-48 sm:w-64 rounded-xl border border-border bg-surface-2 pl-8 pr-3 py-1.5 text-xs text-text-main focus:outline-none focus:border-brand-500"
            />
          </div>
        </div>
      </div>

      {#if entriesLoading}
        <div class="py-12 text-center text-xs text-text-subtle flex items-center justify-center gap-2">
          <span class="material-symbols-outlined animate-spin text-[18px]">sync</span>
          Loading entries...
        </div>
      {:else if entries.length === 0}
        <div class="py-12 text-center text-xs text-text-subtle">
          No cached entries found.
        </div>
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-left text-xs">
            <thead>
              <tr class="border-b border-border text-text-muted">
                <th class="py-2.5 px-3 font-semibold">Signature / Key</th>
                <th class="py-2.5 px-3 font-semibold">Model</th>
                <th class="py-2.5 px-3 font-semibold text-right">Hits</th>
                <th class="py-2.5 px-3 font-semibold text-right">Tokens Saved</th>
                <th class="py-2.5 px-3 font-semibold">Created</th>
                <th class="py-2.5 px-3 font-semibold text-right">Action</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border-subtle">
              {#each entries as entry (entry.id)}
                <tr class="hover:bg-surface-2/60 transition-colors">
                  <td class="py-2.5 px-3 font-mono text-[11px] text-text-main">
                    <div class="flex items-center gap-1.5 max-w-[280px]">
                      <span class="truncate" title={entry.signature}>{entry.signature}</span>
                      <button
                        type="button"
                        onclick={() => copyToClipboard(entry.signature)}
                        class="text-text-subtle hover:text-text-main shrink-0"
                        title="Copy signature"
                      >
                        <span class="material-symbols-outlined text-[14px]">content_copy</span>
                      </button>
                    </div>
                  </td>
                  <td class="py-2.5 px-3 font-medium text-text-muted">
                    <span class="rounded bg-surface-2 px-1.5 py-0.5 text-[11px] text-text-main font-mono">
                      {entry.model}
                    </span>
                  </td>
                  <td class="py-2.5 px-3 text-right font-medium text-emerald-500">
                    {entry.hit_count}
                  </td>
                  <td class="py-2.5 px-3 text-right font-mono text-cyan-400">
                    {entry.tokens_saved.toLocaleString()}
                  </td>
                  <td class="py-2.5 px-3 text-text-subtle whitespace-nowrap">
                    {entry.created_at ? entry.created_at.slice(0, 16).replace('T', ' ') : '-'}
                  </td>
                  <td class="py-2.5 px-3 text-right">
                    <button
                      type="button"
                      onclick={() => handleDeleteEntry(entry.id)}
                      class="text-text-subtle hover:text-red-500 transition-colors"
                      title="Delete entry"
                    >
                      <span class="material-symbols-outlined text-[16px]">delete</span>
                    </button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

        <!-- Pagination Controls -->
        {#if totalPages > 1}
          <div class="flex items-center justify-between pt-3 border-t border-border-subtle text-xs text-text-muted">
            <div>
              Page {page} of {totalPages}
            </div>
            <div class="flex items-center gap-1">
              <button
                type="button"
                disabled={page <= 1}
                onclick={() => {
                  page--
                  loadEntries()
                }}
                class="rounded-lg border border-border bg-surface px-2.5 py-1 text-xs disabled:opacity-40 hover:bg-surface-2"
              >
                Previous
              </button>
              <button
                type="button"
                disabled={page >= totalPages}
                onclick={() => {
                  page++
                  loadEntries()
                }}
                class="rounded-lg border border-border bg-surface px-2.5 py-1 text-xs disabled:opacity-40 hover:bg-surface-2"
              >
                Next
              </button>
            </div>
          </div>
        {/if}
      {/if}
    </div>
  {/if}
</div>
