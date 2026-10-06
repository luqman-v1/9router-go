<script lang="ts">
  import { api, getAuthHeaders, normalizeLastError, type ProviderConnection, type ProviderNode } from '../../api/client'
  import { PROVIDER_CATALOG } from '../../lib/providers'
  import Card from '../../lib/ui/Card.svelte'
  import {
    fmt,
    timeAgo,
    PERIODS,
    periodLabel,
    normalizeCustomPeriod,
    type MainTab,
    type Period,
    type StatsData,
    type RequestDetailItem,
    type ActiveRequestItem,
    type RecentRequestItem
  } from './types'
  import SummaryKpiCards from './SummaryKpiCards.svelte'
  import UsageBreakdownTable from './UsageBreakdownTable.svelte'
  import RequestDetailsTab from './RequestDetailsTab.svelte'
  import ProviderTopologyCard from './ProviderTopologyCard.svelte'
  interface Props {
    connections?: ProviderConnection[]
    providerNodes?: ProviderNode[]
  }

  let { connections = [], providerNodes = [] }: Props = $props()

  let activeTab = $state<MainTab>('overview')
  let period = $state<Period>('today')
  let isFetching = $state(false)

  let showPeriodMenu = $state(false)
  let customPeriodInput = $state('')
  let customPeriodError = $state('')
  let periodMenuRoot: HTMLDivElement | null = $state(null)

  const isPresetPeriod = $derived(PERIODS.some((p) => p.value === period))
  const selectedLabel = $derived(periodLabel(period))

  function selectPeriod(next: Period) {
    period = next
    showPeriodMenu = false
    customPeriodError = ''
  }

  function applyCustomPeriod() {
    const normalized = normalizeCustomPeriod(customPeriodInput)
    if (!normalized) {
      customPeriodError = 'Enter a number of days or hours, like 14d or 12h.'
      return
    }
    selectPeriod(normalized)
  }

  function onPeriodMenuKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      showPeriodMenu = false
      return
    }
    if (event.key !== 'Tab') return
    // A menu that stays open behind the next control leaves the following
    // focusable element unreachable, so Tab closes it instead.
    showPeriodMenu = false
  }

  $effect(() => {
    if (!showPeriodMenu) return
    function handleDocClick(e: MouseEvent): void {
      const target = e.target as HTMLElement | null
      if (!target?.closest('#period-dropdown-root')) {
        showPeriodMenu = false
      }
    }
    document.addEventListener('click', handleDocClick)
    return () => document.removeEventListener('click', handleDocClick)
  })

  let stats = $state<StatsData>({})
  let activeRequests = $state<ActiveRequestItem[]>([])
  let pulseProvider = $state<string>('')
  let lastProvider = $state<string>('')
  let errorProvider = $state<string>('')

  // A failed stats read leaves the previous period's numbers on screen, and
  // those read as live. Since #148 the server answers 500 instead of a zeroed
  // body, so the failure arrives here as a thrown error: keep the message, and
  // let the template show the stale data as stale rather than as current.
  let statsError = $state('')
  let detailsError = $state('')
  let pulseTimer: ReturnType<typeof setTimeout> | null = null

  function triggerPulse(provider: string) {
    if (!provider) return
    pulseProvider = provider
    if (pulseTimer) clearTimeout(pulseTimer)
    pulseTimer = setTimeout(() => {
      pulseProvider = ''
    }, 3000)
  }
  // mergeRecent unions an incoming SSE list with what is on screen. The SSE
  // stream carries only this process's in-memory ring, so a plain replace
  // collapses the DB-backed list (20 rows after a REST load) down to the few
  // rows seen since the last restart — the list visibly blinks and rows below
  // vanish. Union + dedupe + newest-first keeps rows the stream has not seen.
  function mergeRecent(
    prev: RecentRequestItem[] | undefined,
    next: RecentRequestItem[] | undefined,
  ): RecentRequestItem[] {
    const byKey = new Map<string, RecentRequestItem>()
    const keyOf = (r: RecentRequestItem) =>
      `${r.model}|${r.provider}|${r.promptTokens}|${r.completionTokens}|${(r.timestamp || '').slice(0, 16)}`
    for (const r of prev || []) byKey.set(keyOf(r), r)
    for (const r of next || []) byKey.set(keyOf(r), r)
    return [...byKey.values()]
      .sort((a, b) => (b.timestamp || '').localeCompare(a.timestamp || ''))
      .slice(0, 20)
  }
  // Request details tab state
  let details = $state<RequestDetailItem[]>([])
  let detailsTotal = $state(0)
  let detailsPage = $state(1)
  let detailsLoading = $state(false)
  async function loadStats(targetPeriod: Period) {
    isFetching = true
    try {
      const res = await api.getUsageStats(targetPeriod)
      if (res) {
        statsError = ''
        stats = res
        if (Array.isArray(res.activeRequests)) {
          activeRequests = res.activeRequests
        }
        if (!lastProvider) {
          if (Array.isArray(res.activeRequests) && res.activeRequests.length > 0 && res.activeRequests[0].provider) {
            lastProvider = res.activeRequests[0].provider
          } else if (Array.isArray(res.recentRequests) && res.recentRequests.length > 0) {
            lastProvider = res.recentRequests[0].provider || ''
          }
        }
        if (res.errorProvider) {
          errorProvider = res.errorProvider
        }
      }
    } catch (err) {
      // A broken read used to arrive as a zeroed 200 and land on screen as
      // "no traffic this period". The server now answers 500, so the failure
      // has to be named here or it vanishes into the console while the
      // previous period's numbers keep being read as current.
      statsError = normalizeLastError(err) || 'Usage could not be loaded.'
      console.error('Failed to load usage stats:', err)
    } finally {
      isFetching = false
    }
  }

  async function loadDetails(page = 1) {
    detailsLoading = true
    detailsError = ''
    try {
      const limit = 20
      const offset = (page - 1) * limit
      const res = await api.getRequestDetails(limit, offset)
      if (res && Array.isArray(res.details)) {
        details = res.details
        detailsTotal = res.total || 0
        detailsPage = page
      }
    } catch (err) {
      detailsError = normalizeLastError(err) || 'Request details could not be loaded.'
      console.error('Failed to load request details:', err)
    } finally {
      detailsLoading = false
    }
  }

  $effect(() => {
    loadStats(period)
  })

  $effect(() => {
    if (activeTab === 'details') {
      loadDetails(detailsPage)
    }
  })

  // SSE real-time updates for activeRequests, recentRequests and error notifications
  $effect(() => {
    let isCancelled = false
    let controller: AbortController | null = null
    let reconnectTimeout: ReturnType<typeof setTimeout> | null = null

    const token = typeof localStorage !== 'undefined' ? localStorage.getItem('9router_key') || '' : ''
    let streamInitialized = false

    const connectStream = async () => {
      if (isCancelled) return
      controller = new AbortController()

      try {
        const streamUrl = token ? `/api/usage/stream?key=${encodeURIComponent(token)}` : '/api/usage/stream'
        const res = await fetch(streamUrl, {
          headers: getAuthHeaders(),
          signal: controller.signal,
        })

        if (!res.ok) {
          throw new Error(`usage stream failed: ${res.status}`)
        }

        const reader = res.body?.getReader()
        const decoder = new TextDecoder()
        if (!reader) return

        let buffer = ''
        while (!isCancelled) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            const trimmed = line.trim()
            if (!trimmed || trimmed.startsWith(':')) continue
            if (!trimmed.startsWith('data: ')) continue

            try {
              const data = JSON.parse(trimmed.slice(6))
              if (Array.isArray(data.recentRequests) && data.recentRequests.length > 0) {
                const prevTop = stats.recentRequests?.[0]
                const newTop = data.recentRequests[0]
                // Only pulse animation when a GENUINE new model request arrives AFTER stream initialization
                if (streamInitialized && prevTop) {
                  const isNewRequest =
                    newTop.timestamp !== prevTop.timestamp ||
                    newTop.model !== prevTop.model ||
                    newTop.tokens !== prevTop.tokens
                  if (isNewRequest && newTop.provider) {
                    lastProvider = newTop.provider
                    triggerPulse(newTop.provider)
                  }
                } else if (!lastProvider && newTop.provider) {
                  lastProvider = newTop.provider
                }
                streamInitialized = true
                stats = { ...stats, recentRequests: mergeRecent(stats.recentRequests, data.recentRequests) }
              }
              if (Array.isArray(data.activeRequests)) {
                activeRequests = data.activeRequests
                stats = { ...stats, activeRequests: data.activeRequests }
                if (data.activeRequests.length > 0 && data.activeRequests[0].provider) {
                  lastProvider = data.activeRequests[0].provider
                }
              }
              if (data.errorProvider) {
                errorProvider = data.errorProvider
              }
            } catch (err) {
              console.error('Failed to parse SSE usage stream:', err)
            }
          }
        }
      } catch (err) {
        if (!isCancelled) {
          reconnectTimeout = setTimeout(connectStream, 3000)
        }
      }
    }

    connectStream()

    // Auto-poll stats every 5s so KPI counters smoothly increment in real time
    const pollTimer = setInterval(() => {
      if (activeTab === 'overview' && (typeof document === 'undefined' || !document.hidden)) {
        loadStats(period)
      }
    }, 5000)

    return () => {
      isCancelled = true
      if (controller) controller.abort()
      if (reconnectTimeout) clearTimeout(reconnectTimeout)
      if (pulseTimer) clearTimeout(pulseTimer)
      clearInterval(pollTimer)
    }
  })
  let nodeNameById = $derived.by(() => {
    const m = new Map<string, string>()
    for (const n of providerNodes || []) {
      if (n?.id && n?.name) m.set(n.id, n.name)
    }
    return m
  })

  function topologyName(providerId: string, fallbackName?: string): string {
    const nodeName = nodeNameById.get(providerId)
    if (nodeName) return nodeName
    const cat = PROVIDER_CATALOG.find((p) => p.id === providerId || p.alias === providerId)
    if (cat?.name) return cat.name
    if (fallbackName && fallbackName !== providerId) {
      // Numeric key names (e.g. "12") are connection labels, not provider names —
      // fall back to the raw provider id so custom nodes never render as "12".
      if (!/^\d+$/.test(fallbackName.trim())) return fallbackName
      return providerId
    }
    return providerId
  }

  let topologyProviders = $derived.by(() => {
    const seen = new Set<string>()
    const list: { id: string; alias?: string; name: string; color?: string; type: string }[] = []

    const addProvider = (provId: string, type: string, customName?: string) => {
      if (!provId) return
      const canonical = provId.toLowerCase().trim()
      const cat = PROVIDER_CATALOG.find((p) => p.id.toLowerCase() === canonical || (p.alias && p.alias.toLowerCase() === canonical))
      // A provider the registry hides stays routable, but it has no place in a
      // map that reads as "who is on the bus" — the same reason it is kept out
      // of the provider list.
      if (cat?.hidden) return
      const targetId = cat?.id || canonical
      if (seen.has(targetId)) return
      seen.add(targetId)
      if (cat?.alias) seen.add(cat.alias.toLowerCase())
      seen.add(canonical)

      list.push({
        id: targetId,
        alias: cat?.alias,
        name: topologyName(targetId, customName),
        color: cat?.color || '#3B82F6',
        type
      })
    }

    // 1. Prioritize active & live providers so lines to models in use never get dropped
    for (const r of activeRequests) {
      if (r.provider) addProvider(r.provider, 'active')
    }
    if (pulseProvider) addProvider(pulseProvider, 'active')
    if (lastProvider) addProvider(lastProvider, 'recent')
    if (errorProvider) addProvider(errorProvider, 'error')

    // 2. Add recent requests
    for (const r of stats.recentRequests || []) {
      if (r.provider) addProvider(r.provider, 'recent')
    }

    // 3. Add active user-configured connections
    for (const c of connections) {
      if (c.isActive !== 0 && c.provider) {
        addProvider(c.provider, 'connection', c.name || undefined)
      }
    }

    // 4. Add historical providers with usage
    if (stats.byProvider) {
      for (const prov of Object.keys(stats.byProvider)) {
        addProvider(prov, 'stats')
      }
    }

    // 5. Free/no-auth defaults, but only once they have actually been used.
    //    Upstream #4615: no-auth providers store no connection, so the old
    //    unconditional pass drew every catalog free provider on a map that
    //    reads as "who is on the bus" — including ones never routed a request.
    //    Providers with real usage already entered above via stats.byProvider,
    //    so this only has to cover the ids whose usage key the map lacks.
    const usedInPeriod = (provId: string) =>
      (stats.byProvider?.[provId]?.requests || 0) > 0
    const FREE_DEFAULTS = ['antigravity', 'opencode', 'nvidia', 'openrouter', 'clinepass']
    for (const f of FREE_DEFAULTS) {
      if (usedInPeriod(f)) addProvider(f, 'default')
    }

    return list
  })
</script>

<div class="flex min-w-0 flex-col gap-6 px-1 sm:px-0">
  <!-- Tabs + Period Selector Row -->
  <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
    <div class="inline-flex rounded-xl bg-surface border border-border p-1 shadow-sm">
      <button
        type="button"
        onclick={() => (activeTab = 'overview')}
        class="rounded-lg px-4 py-1.5 text-xs sm:text-sm font-medium transition-colors cursor-pointer {activeTab === 'overview'
          ? 'bg-brand-500 text-white font-semibold shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Overview
      </button>
      <button
        type="button"
        onclick={() => (activeTab = 'details')}
        class="rounded-lg px-4 py-1.5 text-xs sm:text-sm font-medium transition-colors cursor-pointer {activeTab === 'details'
          ? 'bg-brand-500 text-white font-semibold shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Details
      </button>
    </div>

    {#if activeTab === 'overview'}
      <div id="period-dropdown-root" class="relative flex w-full items-center gap-1.5 sm:w-auto sm:self-auto">
        <button
          type="button"
          disabled={isFetching}
          aria-haspopup="listbox"
          aria-expanded={showPeriodMenu}
          onclick={() => (showPeriodMenu = !showPeriodMenu)}
          onkeydown={onPeriodMenuKeydown}
          class="inline-flex items-center gap-2 rounded-xl border border-border bg-surface px-3 py-1.5 text-xs font-medium text-text-main shadow-sm transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 disabled:opacity-50 sm:text-sm"
        >
          <span>{selectedLabel}</span>
          {#if !isPresetPeriod}
            <span class="rounded-md bg-surface-3 px-1.5 py-0.5 font-code text-[10px] text-text-muted">{period}</span>
          {/if}
          <span class="material-symbols-outlined text-[16px] text-text-muted" aria-hidden="true">
            {showPeriodMenu ? 'expand_less' : 'expand_more'}
          </span>
        </button>

        {#if showPeriodMenu}
          <div
            role="listbox"
            tabindex="-1"
            onkeydown={onPeriodMenuKeydown}
            class="absolute left-1/2 top-full z-30 mt-1 w-64 -translate-x-1/2 rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)] sm:left-auto sm:right-0 sm:translate-x-0"
          >
            {#each PERIODS as p (p.value)}
              <button
                type="button"
                role="option"
                aria-selected={period === p.value}
                onclick={() => selectPeriod(p.value)}
                class="flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-xs text-text-main transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 sm:text-sm"
              >
                <span>{p.label}</span>
                {#if period === p.value}
                  <span class="material-symbols-outlined text-[16px] text-brand-500" aria-hidden="true">check</span>
                {/if}
              </button>
            {/each}

            <div class="mt-1 border-t border-border-subtle px-3 pt-2 pb-1">
              <label for="custom-period" class="text-[11px] font-medium text-text-muted">Custom window</label>
              <div class="mt-1.5 flex items-center gap-1.5">
                <input
                  id="custom-period"
                  type="text"
                  placeholder="14d"
                  bind:value={customPeriodInput}
                  onkeydown={(e) => e.key === 'Enter' && applyCustomPeriod()}
                  aria-describedby={customPeriodError ? 'custom-period-error' : undefined}
                  aria-invalid={customPeriodError ? 'true' : undefined}
                  class="min-w-0 flex-1 rounded-lg border border-border bg-bg px-2 py-1.5 font-code text-xs text-text-main outline-none transition-colors placeholder:text-text-subtle focus:border-brand-500"
                />
                <button
                  type="button"
                  onclick={applyCustomPeriod}
                  class="shrink-0 rounded-lg bg-brand-500 px-2.5 py-1.5 text-xs font-semibold text-white transition-colors hover:bg-primary-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50"
                >
                  Set
                </button>
              </div>
              {#if customPeriodError}
                <p id="custom-period-error" class="mt-1.5 text-[11px] text-red-600 dark:text-red-400" role="alert">
                  {customPeriodError}
                </p>
              {:else}
                <p class="mt-1.5 text-[11px] text-text-muted">Days or hours, for example 14d or 12h.</p>
              {/if}
            </div>
          </div>
        {/if}

        {#if isFetching}
          <span class="w-2 h-2 rounded-full bg-brand-500 animate-ping"></span>
        {/if}
      </div>
    {/if}
  </div>

  <!-- A failed read leaves the previous period's numbers below, so say what
       happened and mark them stale rather than let them read as current. The
       retry is the same load the refresh button already calls. -->
  {#if activeTab === 'overview' && statsError}
    <div
      role="alert"
      class="flex flex-col gap-2 rounded-[14px] border border-red-500/30 bg-surface px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
    >
      <div class="flex min-w-0 items-start gap-2">
        <span class="material-symbols-outlined mt-px text-[18px] text-red-600 dark:text-red-400" aria-hidden="true">error</span>
        <div class="min-w-0">
          <p class="text-sm font-medium text-text-main">Usage could not be loaded. The figures below are from the last successful read.</p>
          <p class="mt-0.5 break-words text-[11px] text-text-muted">{statsError}</p>
        </div>
      </div>
      <button
        type="button"
        onclick={() => loadStats(period)}
        disabled={isFetching}
        class="shrink-0 self-start rounded-lg border border-border px-3 py-1.5 text-xs font-medium text-text-main transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 disabled:opacity-50 sm:self-auto"
      >
        {isFetching ? 'Retrying…' : 'Retry'}
      </button>
    </div>
  {/if}

  {#if activeTab === 'overview'}
    <!-- 5 Overview KPI Cards -->
    <SummaryKpiCards {stats} />

    <!-- Topology + Recent Requests -->
    <div class="grid min-w-0 grid-cols-1 items-stretch gap-2 lg:grid-cols-[minmax(0,2fr)_minmax(280px,1fr)]">
      <ProviderTopologyCard
        providers={topologyProviders}
        {activeRequests}
        {pulseProvider}
        {lastProvider}
        {errorProvider}
        onRefresh={() => loadStats(period)}
      />
      <!-- Recent Requests Card -->
      <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-4 flex min-w-0 flex-col overflow-hidden" style="height: 480px">
        <div class="px-1 py-2 border-b border-border shrink-0">
          <span class="text-xs font-semibold text-text-muted uppercase tracking-wide">Recent Requests</span>
        </div>

        {#if !stats.recentRequests || stats.recentRequests.length === 0}
          <div class="flex-1 flex items-center justify-center text-text-muted text-xs">
            No requests recorded yet.
          </div>
        {:else}
          <div class="flex-1 overflow-y-auto">
            <table class="w-full table-fixed min-w-[280px] border-collapse text-xs">
              <colgroup>
                <col class="w-[20px]" />
                <col />
                <col class="w-[96px]" />
                <col class="w-[56px]" />
              </colgroup>
              <thead class="sticky top-0 bg-bg z-10">
                <tr class="border-b border-border">
                  <th class="py-1.5 pl-3 text-left font-semibold text-text-muted"></th>
                  <th class="py-1.5 text-left font-semibold text-text-muted">Model</th>
                  <th class="py-1.5 text-right font-semibold text-text-muted whitespace-nowrap">In / Out</th>
                  <th class="py-1.5 pr-3 text-right font-semibold text-text-muted whitespace-nowrap">When</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-border/50 font-mono text-[11px]">
                {#each stats.recentRequests as req}
                  <tr class="hover:bg-bg-subtle transition-colors">
                    <td class="py-1.5 pl-3 align-middle">
                      <span class="mx-auto block w-1.5 h-1.5 rounded-full {req.status === 'ok' || req.status === 'success' ? 'bg-success' : 'bg-red-500'}"></span>
                    </td>
                    <td class="py-1.5 pr-2 min-w-0">
                      <span class="block truncate font-mono text-[11px]" title={req.model}>{req.model}</span>
                    </td>
                    <td class="py-1.5 pr-3 text-right whitespace-nowrap">
                      <span class="text-primary">{fmt(req.promptTokens)}↑</span>
                      <span class="text-success">{fmt(req.completionTokens)}↓</span>
                    </td>
                    <td class="py-1.5 pr-3 text-right text-text-muted whitespace-nowrap text-[10px]">
                      {timeAgo(req.timestamp)}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    </div>

    <!-- Breakdown Table -->
    <UsageBreakdownTable {stats} />
  {:else}
    <RequestDetailsTab
      {details}
      {detailsTotal}
      {detailsPage}
      {detailsLoading}
      {detailsError}
      onPageChange={loadDetails}
      onRefresh={() => loadDetails(detailsPage)}
      {providerNodes}
    />
  {/if}
</div>
