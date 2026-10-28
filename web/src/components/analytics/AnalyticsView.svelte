<script lang="ts">
  import { api, getAuthHeaders, normalizeLastError, type ProviderConnection, type ProviderNode } from '../../api/client'
  import { PROVIDER_CATALOG } from '../../lib/providers'
  import {
    USAGE_SECTION_BY_TAB,
    USAGE_SECTION_BY_VALUE,
    type ActiveTab,
    type UsageSection,
  } from '../../lib/router'
  import {
    fmt,
    timeAgo,
    type Period,
    type StatsData,
    type RequestDetailItem,
    type ActiveRequestItem,
    type RecentRequestItem
  } from './types'
  import CacheAnalyticsView from '../CacheAnalyticsView.svelte'
  import CompressionAnalyticsView from '../CompressionAnalyticsView.svelte'
  import PeriodSelect from './PeriodSelect.svelte'
  import SectionMenu from './SectionMenu.svelte'
  import SummaryKpiCards from './SummaryKpiCards.svelte'
  import UsageBreakdownTable from './UsageBreakdownTable.svelte'
  import RequestDetailsTab from './RequestDetailsTab.svelte'
  import UsageChart from './UsageChart.svelte'
  import ProviderTopologyCard from './ProviderTopologyCard.svelte'
  import ComboFlowCard from './ComboFlowCard.svelte'
  import { usageBlocks, isPanelVisible } from '../../lib/usagePanel.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'
  interface Props {
    connections?: ProviderConnection[]
    providerNodes?: ProviderNode[]
    tab?: ActiveTab
    onNavigate?: (tab: ActiveTab) => void
  }

  let { connections = [], providerNodes = [], tab = 'analytics', onNavigate = () => {} }: Props = $props()

  // Cache and Compression Analytics became sections of this page (issue #200),
  // each with a tab of its own, so a reload on /dashboard/usage/cache restores
  // that section. Overview and Details share one path and keep their state
  // here instead.
  // The initial value is read once, on purpose: `tab` is the tab the router
  // resolved on mount, and re-reading it later would yank the user off a
  // section they picked after a browser-back navigation restored the old path.
  // svelte-ignore state_referenced_locally
  let section = $state<UsageSection>(USAGE_SECTION_BY_TAB[tab] ?? 'overview')
  let period = $state<Period>('today')
  let isFetching = $state(false)

  // The two optional Overview panels start collapsed and say so with a chevron,
  // the same affordance the Details panels use. Collapsed is a view state, not a
  // preference: the switch that hides a panel outright is the arrangement's own
  // visibility toggle, and that one also skips the combo query below.
  let comboFlowOpen = $state(false)
  let usageTrendOpen = $state(false)
  // The trend's metric switch lives in the panel header, and the header label
  // describes the active metric — so both the mode and the hourly fact the
  // label repeats are owned here (UsageChart reports the bucket shape back).
  let trendMode = $state<'tokens' | 'costs'>('tokens')
  let trendHourly = $state(false)
  const trendLabel = $derived(
    trendMode === 'costs'
      ? `Estimated cost per ${trendHourly ? 'hour' : 'day'}, stacked by model family.`
      : `Tokens per ${trendHourly ? 'hour' : 'day'}, stacked by model family.`
  )
  // The stats read and the live stream fill the Overview's data panels: KPI
  // cards, topology map, Recent Requests, the breakdown table, and Combo flow.
  // The Usage trend is not one of them — it reads its own chart endpoint, and
  // only while it is expanded — so a section that shows nothing but the trend
  // must not poll what none of its cards draw (issue #506).
  const OVERVIEW_LIVE_PANELS = [
    'overview.kpi',
    'overview.topology',
    'overview.recent',
    'overview.breakdown',
    'overview.comboFlow',
  ] as const
  const overviewLive = $derived(OVERVIEW_LIVE_PANELS.some((id) => isPanelVisible(id)))

  // Choosing a section has to move the URL when that section owns one, or the
  // next reload silently drops back to Overview.
  $effect(() => {
    const desired = USAGE_SECTION_BY_VALUE[section]
    if (desired && desired !== tab) onNavigate(desired)
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
  // Combo definitions for the live map. The SSE already reports which combo a
  // request asked for (requestedModel); these supply the members it resolved
  // to, so the card can list models and providers instead of guessing.
  let comboDefs = $state<{ name: string; members: string[]; strategy?: string }[]>([])
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

  async function loadDetails(page = 1, window: Period = period) {
    detailsLoading = true
    detailsError = ''
    try {
      const limit = 20
      const offset = (page - 1) * limit
      const res = await api.getRequestDetails(limit, offset, window)
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

  // The usage and details fetches follow the Overview and Details sections.
  // The moved Cache and Compression sections load their own data, so the
  // Overview's read must not run behind them.
  $effect(() => {
    if (section === 'overview' && overviewLive) loadStats(period)
  })

  $effect(() => {
    // The paged request table is one of the panels the arrangement can hide.
    // With it hidden the section draws no rows, so the read would only cost a
    // request and the page weight of a table nobody sees (issue #506).
    if (section === 'details' && isPanelVisible('details.requests')) {
      loadDetails(detailsPage, period)
    }
  })

  // Loaded once: editing a combo is rare, and a reload picks up the change. The
  // query is skipped outright while the Combo flow panel is hidden — the panel
  // draws nothing then, so the definitions would only cost a request and memory
  // (upstream #429). `isPanelVisible` reads the arrangement, so turning the panel
  // back on in Settings refetches without a reload.
  $effect(() => {
    if (!isPanelVisible('overview.comboFlow')) {
      comboDefs = []
      return
    }
    let cancelled = false
    api
      .getCombos()
      .then((rows: any[]) => {
        if (cancelled) return
        comboDefs = (Array.isArray(rows) ? rows : [])
          .map((c: any) => {
            let members: string[] = []
            try {
              const raw = typeof c?.models === 'string' ? JSON.parse(c.models) : c?.models
              if (Array.isArray(raw)) members = raw.map((m: any) => String(m))
            } catch {
              members = []
            }
            return { name: String(c?.name ?? ''), members, strategy: String(c?.strategy ?? '') }
          })
          .filter((c: { name: string }) => c.name !== '')
      })
      .catch(() => {
        if (!cancelled) comboDefs = []
      })
    return () => {
      cancelled = true
    }
  })

  // SSE real-time updates for activeRequests, recentRequests and error notifications
  $effect(() => {
    // The live stream fills the Overview's data panels and nothing else. With
    // every one of them hidden, the connection would stream numbers into a
    // page that draws none of them (issue #506).
    if (!overviewLive) return
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
      if (section === 'overview' && (typeof document === 'undefined' || !document.hidden)) {
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
    // The fallback is a connection name, and for OAuth connections that is
    // the account email — mask it the same way the connection lists do.
    const nodeName = formatEmailLabel(nodeNameById.get(providerId) ?? '', $emailPrivacy)
    if (nodeName) return nodeName
    const cat = PROVIDER_CATALOG.find((p) => p.id === providerId || p.alias === providerId)
    if (cat?.name) return cat.name
    // Only fall back to the connection label if it isn't a numeric ID (a
    // connection label like "12") and isn't an OAuth account email: an email
    // is the connection's identity, not the provider's name, so it must never
    // stand in as the topology node label even with privacy masking off.
    if (fallbackName && fallbackName !== providerId) {
      const trimmed = fallbackName.trim()
      if (!/^\d+$/.test(trimmed) && !trimmed.includes('@')) return formatEmailLabel(fallbackName, $emailPrivacy)
      return providerId
    }
    return providerId
  }

  let topologyProviders = $derived.by(() => {
    const seen = new Set<string>()
    const list: { id: string; alias?: string; name: string; color?: string; type: string }[] = []

    // "Who is on the bus" is a usage question, so every source below is
    // measured against the same window. The backend keys usage by the
    // canonical provider id, while a connection row can hold either that or
    // the published alias, so both are tried before a provider counts as idle.
    const usedInPeriod = (provId: string) => {
      const canonical = provId.toLowerCase().trim()
      const cat = PROVIDER_CATALOG.find((p) => p.id.toLowerCase() === canonical || p.alias?.toLowerCase() === canonical)
      const keys = [canonical, cat?.id, cat?.alias].filter(Boolean) as string[]
      return keys.some((k) => (stats.byProvider?.[k]?.requests || 0) > 0)
    }
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

    // 3. Add user-configured connections, but only the ones that actually
    //    carried traffic in the selected window. Every non-inactive row used
    //    to be drawn here, and since that list is not usage-windowed the node
    //    outlived both the period and the connection itself: a provider the
    //    operator had merely configured sat on the map indefinitely as if it
    //    were serving (#280). Gating it on the same window as every other
    //    source is what makes the map mean what it claims to mean.
    for (const c of connections) {
      if (c.isActive !== 0 && c.provider && usedInPeriod(c.provider)) {
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
    const FREE_DEFAULTS = ['antigravity', 'opencode', 'nvidia', 'openrouter', 'clinepass']
    for (const f of FREE_DEFAULTS) {
      if (usedInPeriod(f)) addProvider(f, 'default')
    }

    return list
  })
</script>

<!-- The section picker, defined once here and handed to the sections that put
     it in their own header row beside their own controls (issue #209). -->
{#snippet sectionMenu()}
  <SectionMenu {section} onSectionChange={(next) => (section = next)} />
{/snippet}

<!-- Both optional Overview panels are collapsed by default and say so with the
     same chevron the Details panels use, so a reader recognizes the affordance
     without learning a second one. -->
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


<div class="flex min-w-0 flex-col gap-6 px-1 sm:px-0">
  <!-- The section picker and the window selector. Cache Analytics and
       Compression Analytics each put the picker in their own header, beside the
       controls they own (issue #209). Details shares this row's control, which
       windows the request table through ?period; the panels above it keep the
       window they label in place (lifetime is all time, Node Health is 7 days),
       so no number here changes meaning silently. -->
  {#if section === 'overview' || section === 'details'}
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      {@render sectionMenu()}

      <PeriodSelect value={period} busy={isFetching} onChange={(next) => (period = next)} />
    </div>
  {/if}

  <!-- A failed read leaves the previous period's numbers below, so say what
       happened and mark them stale rather than let them read as current. The
       retry is the same load the refresh button already calls. -->
  {#if section === 'overview' && statsError}
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

  {#if section === 'overview'}
    <!-- The panels of this section, in the arrangement Settings holds. A block
         is one panel, except for the map + Recent Requests row: those two share
         one grid row while both are visible and adjacent, and fall apart into
         full-width blocks once the arrangement separates them. -->
    {#each usageBlocks('overview') as block (block.key)}
      {#if block.key === 'overview.kpi'}
        <SummaryKpiCards {stats} />
      {:else if block.key === 'overview.comboFlow'}
        <!-- Combo flow: router -> combo -> members, only while a combo is in use
             (or just finished). The provider map keeps its own animation. The
             panel starts collapsed: the flow is only interesting while a request
             is moving, and an expanded empty tree reads as a broken panel. The
             header is the Details panels' own — same fonts, same chevron, with
             the panel's icon in front of the title. -->
        <Card padding="none" class="overflow-hidden border border-border">
          <div
            class="flex flex-col gap-3 bg-surface-2 px-5 py-3 sm:flex-row sm:items-center sm:justify-between {comboFlowOpen
              ? 'border-b border-border'
              : ''}"
          >
            <div class="flex min-w-0 items-center gap-2">
              <span class="material-symbols-outlined text-[18px] text-text-muted" aria-hidden="true"
                >account_tree</span
              >
              <div class="min-w-0">
                <h2 class="font-headline text-sm font-bold text-text-main">Combo flow</h2>
                <p class="font-body text-xs text-text-muted">Router → combo → members, live</p>
              </div>
            </div>
            {@render panelToggle(comboFlowOpen, 'Combo flow', () => (comboFlowOpen = !comboFlowOpen))}
          </div>
          {#if comboFlowOpen}
            <div class="p-5">
              <ComboFlowCard
                combos={comboDefs}
                {activeRequests}
                recentRequests={stats.recentRequests || []}
                {errorProvider}
              />
            </div>
          {/if}
        </Card>
      {:else if block.key === 'overview.topology+overview.recent'}
        <div class="grid min-w-0 grid-cols-1 items-stretch gap-2 lg:grid-cols-[minmax(0,2fr)_minmax(280px,1fr)]">
          {@render topologyMap()}
          {@render recentRequestsCard()}
        </div>
      {:else if block.key === 'overview.topology'}
        {@render topologyMap()}
      {:else if block.key === 'overview.recent'}
        {@render recentRequestsCard()}
      {:else if block.key === 'overview.trend'}
        <!-- Usage trend: the same collapsed-by-default shape as Combo flow, so the
             two optional panels read alike. The metric switch lives here, in the
             header, beside the chevron — the label below it names what is drawn,
             so the two cannot disagree. -->
        <Card padding="none" class="overflow-hidden border border-border">
          <div
            class="flex flex-col gap-3 bg-surface-2 px-5 py-3 sm:flex-row sm:items-center sm:justify-between {usageTrendOpen
              ? 'border-b border-border'
              : ''}"
          >
            <div class="flex min-w-0 items-center gap-2">
              <span class="material-symbols-outlined text-[18px] text-text-muted" aria-hidden="true"
                >monitoring</span
              >
              <div class="min-w-0">
                <h2 class="font-headline text-sm font-bold text-text-main">Usage trend</h2>
                <p class="font-body text-xs text-text-muted">{trendLabel}</p>
              </div>
            </div>
            <div class="flex items-center gap-2 self-start sm:self-auto">
              <div
                class="inline-flex rounded-xl bg-surface border border-border p-1 shadow-sm"
                role="group"
                aria-label="Chart metric"
              >
                <button
                  type="button"
                  onclick={() => (trendMode = 'costs')}
                  class="rounded-lg px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {trendMode ===
                  'costs'
                    ? 'bg-brand-500 text-white shadow-sm'
                    : 'text-text-muted hover:text-text-main'}"
                >
                  Costs
                </button>
                <button
                  type="button"
                  onclick={() => (trendMode = 'tokens')}
                  class="rounded-lg px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {trendMode ===
                  'tokens'
                    ? 'bg-brand-500 text-white shadow-sm'
                    : 'text-text-muted hover:text-text-main'}"
                >
                  Tokens
                </button>
              </div>
              {@render panelToggle(usageTrendOpen, 'Usage trend', () => (usageTrendOpen = !usageTrendOpen))}
            </div>
          </div>
          {#if usageTrendOpen}
            <div class="p-5">
              <UsageChart {period} bind:viewMode={trendMode} bind:hourly={trendHourly} />
            </div>
          {/if}
        </Card>
      {:else if block.key === 'overview.breakdown'}
        <UsageBreakdownTable {stats} />
      {/if}
    {/each}
  {:else if section === 'cache'}
    <CacheAnalyticsView>
      {#snippet headerLeft()}
        {@render sectionMenu?.()}
      {/snippet}
    </CacheAnalyticsView>
  {:else if section === 'compression'}
    <CompressionAnalyticsView>
      {#snippet headerLeft()}
        {@render sectionMenu?.()}
      {/snippet}
    </CompressionAnalyticsView>
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
      {connections}
    />
  {/if}
</div>
{#snippet topologyMap()}
  <ProviderTopologyCard
    providers={topologyProviders}
    {activeRequests}
    {pulseProvider}
    {lastProvider}
    {errorProvider}
    onRefresh={() => loadStats(period)}
  />
{/snippet}

{#snippet recentRequestsCard()}
  <div
    class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-4 flex min-w-0 flex-col overflow-hidden"
    style="height: 480px"
  >
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
                  <span
                    class="mx-auto block w-1.5 h-1.5 rounded-full {req.status === 'ok' || req.status === 'success'
                      ? 'bg-success'
                      : 'bg-red-500'}"
                  ></span>
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
{/snippet}
