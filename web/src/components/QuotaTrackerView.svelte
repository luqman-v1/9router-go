<script lang="ts">
  // Port of Next.js ProviderLimits page logic for the Go dashboard quota page.
  // Matches upstream behavior: paginated provider fetch, per-connection quota
  // fetch + localStorage cache, auto-refresh w/ countdown + page visibility,
  // Claude throttling, provider/account filters, codex sort, expiring-first,
  // bulk turn off empty / on available, quota hide/show via settings,
  // and per-connection refresh/toggle/delete actions.
  import { onMount } from 'svelte'
  import {
    api,
    type CodexResetCredit,
    type ConnectionUsageResponse,
    type ProviderConnection,
  } from '../api/client'
  import {
    getCodexResetCreditExpiryLabel,
    getResetCreditConfirmation,
    getResetCreditWindowTitle,
    newResetCreditIdempotencyKey,
  } from '../lib/codexResetCredit'
  import { PROVIDER_CATALOG } from '../lib/providers'
  import { copyToClipboard } from '../lib/clipboard'
  import { emailPrivacy, formatEmailLabel } from '../lib/privacy'
  import Toggle from '../lib/ui/Toggle.svelte'
  import { getIconPath } from './connections/types'
  import EditConnectionModal, { type ConnectionUpdate } from './connections/EditConnectionModal.svelte'
  import QuotaTable from './quota/QuotaTable.svelte'
  import {
    ACCOUNT_FILTER_OPTIONS,
    ACCOUNT_PAGE_SIZE_MAX,
    ACCOUNT_PAGE_SIZE_OPTIONS,
    AUTO_REFRESH_STORAGE_KEY,
    CLAUDE_REFRESH_INTERVAL_MS,
    DEPLETED_QUOTA_THRESHOLD,
    QUOTA_SORT_OPTIONS,
    REFRESH_INTERVAL_MS,
    buildLoadingState,
    calculatePercentage,
    filterQuotaStateByConnections,
    filterQuotasByVisibility,
    getConnectionsEmptyMessage,
    getConnectionsPaginationSummary,
    getHiddenQuotaRows,
    getProviderOptions,
    getQuotaCache,
    getQuotaVisibilityKey,
    getSafePagination,
    getSafeTotals,
    parseQuotaData,
    setQuotaCache,
    shouldResetPage,
    sortVisibleConnections,
    type Pagination,
    type ProviderConnectionLike,
    type QuotaEntry,
    type QuotaVisibility,
    type Totals,
  } from './quota/types'
  import {
    QUOTA_FETCH_CONCURRENCY,
    runQuotaFetchCycle,
    type QuotaFetchTarget,
  } from './quota/fetch'

  const CONNECTIONS_PAGE_SIZE = 20

  interface Props {
    connections?: ProviderConnection[]
  }

  let { connections: initialConns = [] }: Props = $props()

  // ─── State ─────────────────────────────────────────────────────────────────
  let connections = $state<ProviderConnection[]>([])
  let quotaData = $state<Record<string, QuotaEntry>>({})
  let quotaLoading = $state<Record<string, boolean>>({})
  let quotaErrors = $state<Record<string, string>>({})
  let autoRefresh = $state(true)
  let hasHydratedAutoRefresh = $state(false)
  let lastUpdated = $state<Date | null>(null)
  let refreshingAll = $state(false)
  let countdown = $state(60)
  let connectionsLoading = $state(true)
  let deletingId = $state<string | null>(null)
  let togglingId = $state<string | null>(null)
  let bulkToggling = $state(false)

  // Filters
  let providerFilter = $state('all')
  let providerOptions = $state<string[]>([])
  let providerMenuOpen = $state(false)
  let accountFilter = $state('all')
  let quotaSortMode = $state('default')
  let expiringFirst = $state(false)

  // Pagination
  let page = $state(1)
  let pageSize = $state(CONNECTIONS_PAGE_SIZE)
  let customPageSizeInput = $state(String(CONNECTIONS_PAGE_SIZE))
  let pagination = $state<Pagination>({
    page: 1,
    pageSize: CONNECTIONS_PAGE_SIZE,
    total: 0,
    totalPages: 1,
  })
  let totals = $state<Totals>({ eligibleConnections: 0, providerFilteredConnections: 0 })

  // Quota visibility (hidden rows), persisted via /api/settings quotaVisibility
  let quotaVisibility = $state<QuotaVisibility>({})
  let autoPingMaps = $state<{ claude: Record<string, boolean>; codex: Record<string, boolean> }>({
    claude: {},
    codex: {},
  })

  // Edit modal state. The modal itself owns its fields and payload (issue #158).
  let editingConnection = $state<ProviderConnection | null>(null)
  let copiedArnId = $state<string | null>(null)

  // Timers
  let intervalTimer: ReturnType<typeof setInterval> | null = null
  let countdownTimer: ReturnType<typeof setInterval> | null = null
  let tickCount = 0
  let initialLoadDone = false

  // ─── Helpers ───────────────────────────────────────────────────────────────

  // The controller of the quota cycle currently in flight, if any. Kept out of
  // $state: it is a cancellation handle, not something the view renders, so
  // putting it in the reactive graph would only add renders nobody reads.
  let quotaCycleController: AbortController | null = null

  // Runs one full pass over `targets`. Starting a pass supersedes any pass
  // still running: its reads are aborted and its late answers are dropped, so
  // a page change mid-refresh cannot leave rows blank or overwrite the new page.
  async function fetchQuotasFor(
    targets: QuotaFetchTarget[],
    visible: ProviderConnection[],
    force = false,
  ): Promise<void> {
    quotaErrors = filterQuotaStateByConnections(quotaErrors, visible)
    quotaData = filterQuotaStateByConnections(quotaData, visible)
    quotaLoading = buildLoadingState(targets)

    // Supersede the pass in flight before starting this one. Its reads are
    // aborted, so the browser stops waiting on answers no row will use and the
    // late ones can no longer overwrite the rows below.
    quotaCycleController?.abort()
    const controller = new AbortController()
    quotaCycleController = controller

    await runQuotaFetchCycle<ConnectionUsageResponse>({
      targets,
      signal: controller.signal,
      concurrency: QUOTA_FETCH_CONCURRENCY,
      force,
      fetch: (target, isForced) =>
        api.getConnectionUsage(target.id, isForced, controller.signal),
      onResult: (target, data) => {
        const quotaEntry: QuotaEntry = {
          quotas: parseQuotaData(target.provider, data),
          plan: data?.plan || null,
          message: data?.message || null,
          raw: data,
        }
        quotaData = { ...quotaData, [target.id]: quotaEntry }
        quotaErrors = { ...quotaErrors, [target.id]: '' }
        quotaLoading = { ...quotaLoading, [target.id]: false }
        setQuotaCache(target.id, quotaEntry)
      },
      onError: (target, error) => {
        quotaLoading = { ...quotaLoading, [target.id]: false }
        const message = error instanceof Error ? error.message : String(error)
        // 404-style: the connection was deleted underneath the page, which is
        // not a failure worth showing on a row that is about to disappear.
        if (message.toLowerCase().includes('not found')) return
        quotaErrors = { ...quotaErrors, [target.id]: message || 'Failed to fetch quota' }
      },
      onComplete: () => {
        if (quotaCycleController === controller) quotaCycleController = null
        lastUpdated = new Date()
        refreshingAll = false
      },
    })
  }

  const KIRO_METHOD_LABELS: Record<string, string> = {
    'builder-id': 'AWS Builder ID',
    idc: 'IAM Identity Center',
    google: 'Google',
    github: 'GitHub',
    imported: 'Imported Token',
    api_key: 'API Key',
  }

  const AUTO_PING_SETTINGS_KEYS: Record<string, string> = {
    claude: 'claudeAutoPing',
    codex: 'codexAutoPing',
  }

  const AUTO_PING_TOOLTIPS: Record<string, string> = {
    claude: 'When your 5h quota runs out, auto-sends a request the moment it resets so a new window starts right away.',
    codex: 'Auto-starts the next 5h Codex window after reset by sending a tiny gpt-5.5 request. Consumes a small amount of quota.',
  }

  function kiroMethodLabel(conn: ProviderConnectionLike): string {
    const m = (conn.providerSpecificData as Record<string, unknown> | undefined)?.authMethod as string | undefined
    if (m && KIRO_METHOD_LABELS[m]) return KIRO_METHOD_LABELS[m]
    return conn.authType === 'api_key' ? 'API Key' : 'OAuth'
  }

  function kiroRegion(conn: ProviderConnectionLike): string {
    const r = (conn.providerSpecificData as Record<string, unknown> | undefined)?.region as string | undefined
    if (r) return r
    const arn = (conn.providerSpecificData as Record<string, unknown> | undefined)?.profileArn
    const seg = typeof arn === 'string' ? arn.split(':')[3] : ''
    return seg || ''
  }

  function getConnectionSecondaryLabel(conn: ProviderConnectionLike): string | null {
    if (conn.name?.trim() && conn.email?.trim() && conn.name.trim() !== conn.email.trim()) {
      return conn.email.trim()
    }
    if (conn.name?.trim() && conn.displayName?.trim() && conn.name.trim() !== conn.displayName.trim()) {
      return conn.displayName.trim()
    }
    return null
  }

  function getCodexResetCreditCount(quota?: QuotaEntry): number {
    const value = (quota?.raw as { resetCredits?: { availableCount?: unknown } } | undefined)?.resetCredits?.availableCount
    const count = typeof value === 'number' ? value : Number(value)
    return Number.isFinite(count) ? Math.max(0, count) : 0
  }

  // Codex reset credits. The counter on the row only says how many are left;
  // opening the chooser is what lists them, so the button costs nothing until
  // it is used.
  let resetCreditConn = $state<ProviderConnection | null>(null)
  let resetCreditList = $state<CodexResetCredit[]>([])
  let resetCreditSelected = $state('')
  let resetCreditLoading = $state(false)
  let resetCreditConsuming = $state(false)
  let resetCreditError = $state('')
  // One key per open session, reused across a retry so a double submit or a
  // retried request cannot redeem the same credit twice.
  let resetCreditIdempotencyKey = $state('')
  // availableCount drives the empty-state wording: a positive count with an
  // empty list means the details could not be read, which is a different
  // situation from having no credits at all.
  let resetCreditCount = $state(0)
  let resetCreditConfirming = $state(false)
  let resetCreditPending = $state<CodexResetCredit | null>(null)

  // Redeeming spends the credit for good, so the chooser is two-step: pick a
  // credit, then confirm the irreversible action. Ported from OmniRoute's
  // CodexResetCreditsModal.
  function requestResetCredit(credit: CodexResetCredit) {
    if (resetCreditConsuming) return
    resetCreditPending = credit
    resetCreditConfirming = true
    resetCreditError = ''
  }

  function backToResetCreditList() {
    if (resetCreditConsuming) return
    resetCreditConfirming = false
    resetCreditPending = null
    resetCreditError = ''
  }


  function closeResetCredits() {
    if (resetCreditConsuming) return
    resetCreditConn = null
    resetCreditList = []
    resetCreditSelected = ''
    resetCreditError = ''
    resetCreditCount = 0
    resetCreditConfirming = false
    resetCreditPending = null
    resetCreditIdempotencyKey = ''
  }

  async function openResetCredits(conn: ProviderConnection) {
    resetCreditConn = conn
    resetCreditList = []
    resetCreditSelected = ''
    resetCreditError = ''
    resetCreditCount = 0
    resetCreditConfirming = false
    resetCreditPending = null
    // Mint here, not at declare time: an empty key is not "no key", it is a
    // key the server replaces with a fresh one per request, which is exactly
    // the double-redeem this was meant to prevent. Every attempt within one
    // open session then shares a key, so a retried or double-submitted confirm
    // cannot spend a second credit.
    resetCreditIdempotencyKey = newResetCreditIdempotencyKey()
    resetCreditLoading = true
    try {
      const res = await api.listCodexResetCredits(conn.id)
      if (res?.error) {
        resetCreditError = res.error
        resetCreditList = []
        return
      }
      resetCreditList = res?.credits ?? []
      resetCreditCount = res?.availableCount ?? 0
      // The server returns them soonest-expiry first, so the default choice is
      // the one that frees the quota soonest.
      resetCreditSelected = resetCreditList[0]?.selectionToken ?? ''
    } catch (err) {
      resetCreditError = err instanceof Error ? err.message : 'Failed to load reset credits.'
    } finally {
      resetCreditLoading = false
    }
  }

  async function confirmResetCredit() {
    const conn = resetCreditConn
    const credit = resetCreditPending
    // The token is carried from the confirm step, not the radio selection, so
    // exactly the credit the warning was shown for is the one that gets spent.
    // An empty key would let the server mint a fresh one per request, which is
    // the double-spend this guard exists to prevent — refuse rather than send one.
    if (!conn || !credit || resetCreditConsuming || !resetCreditIdempotencyKey) return
    resetCreditConsuming = true
    resetCreditError = ''
    try {
      const res = await api.consumeCodexResetCredit(conn.id, credit.selectionToken, resetCreditIdempotencyKey)
      if (res?.error) {
        resetCreditError = res.error
        return
      }
      resetCreditConn = null
      resetCreditList = []
      resetCreditSelected = ''
      // The quota windows are what the user reset, so re-read them rather than
      // leaving a stale reading on screen.
      await refreshProvider(conn.id, conn.provider)
    } catch (err) {
      resetCreditError = err instanceof Error ? err.message : 'Failed to redeem the reset credit.'
    } finally {
      resetCreditConsuming = false
    }
  }

  async function toggleAutoPing(connId: string, provider: string, enabled: boolean) {
    const key = AUTO_PING_SETTINGS_KEYS[provider]
    if (!key) return
    const currentMap = autoPingMaps[provider as 'claude' | 'codex'] || {}
    const nextMap = { ...currentMap, [connId]: enabled }
    autoPingMaps = {
      ...autoPingMaps,
      [provider]: nextMap,
    }
    try {
      await api.updateSettingsRaw({
        [key]: nextMap,
      })
    } catch (err) {
      console.error('Failed to update autoPing:', err)
    }
  }

  // Edit modal. Fields, probe and payload live in EditConnectionModal (issue
  // #158); this only decides when it opens and who refreshes the list.
  function openEditModal(conn: ProviderConnection) {
    editingConnection = conn
  }

  async function saveEditingConnection(payload: ConnectionUpdate) {
    if (!editingConnection) return
    await api.updateConnection(editingConnection.id, payload)
    editingConnection = null
    await fetchConnections(page)
  }

  function copyArn(text?: string, id?: string) {
    if (!text || !id) return
    copyToClipboard(text)
    copiedArnId = id
    setTimeout(() => {
      if (copiedArnId === id) copiedArnId = null
    }, 2000)
  }

  function providerLabel(providerId: string): string {
    const cat = PROVIDER_CATALOG.find((p) => p.id === providerId || p.alias === providerId)
    return cat?.name || providerId
  }

  function isConnectionDepleted(conn: ProviderConnection): boolean {
    const quotas = quotaData[conn.id]?.quotas
    if (!quotas?.length) return false
    return quotas.some((q) => {
      if (!q.total || q.total <= 0) return false
      return calculatePercentage(q.used, q.total) <= DEPLETED_QUOTA_THRESHOLD
    })
  }

  function isActiveConn(conn: ProviderConnection): boolean {
    return conn.isActive === 1 || conn.isActive === true
  }

  function getConnectionLabel(conn: ProviderConnectionLike): string | null {
    return conn.name?.trim() || conn.email?.trim() || conn.displayName?.trim() || null
  }
  // ─── Data loading ──────────────────────────────────────────────────────────
  async function fetchConnections(targetPage = page): Promise<ProviderConnection[]> {
    try {
      const params = new URLSearchParams({
        page: String(targetPage),
        pageSize: String(pageSize),
        accountStatus: accountFilter,
        sort: 'priority',
      })
      if (providerFilter !== 'all') {
        params.set('provider', providerFilter)
      }
      const data = await api.getProvidersClientPage(params.toString())
      const connectionList = data.connections || []
      connections = connectionList
      providerOptions = getProviderOptions(data.providerOptions)
      pagination = getSafePagination(data.pagination, pageSize)
      totals = getSafeTotals(data.totals, connectionList.length, pagination.total)
      page = data.pagination?.page || targetPage
      return connectionList
    } catch (error) {
      console.error('Error fetching connections:', error)
      connections = []
      providerOptions = []
      pagination = { page: 1, pageSize, total: 0, totalPages: 1 }
      totals = { eligibleConnections: 0, providerFilteredConnections: 0 }
      return []
    }
  }

  // Refreshes one connection on its own. The per-row button must not cancel the
  // page-wide pass still working through the other rows, so this read owns its
  // controller and nothing else aborts it.
  async function fetchQuota(
    connectionId: string,
    provider: string,
    { force = false }: { force?: boolean } = {},
  ): Promise<void> {
    quotaLoading = { ...quotaLoading, [connectionId]: true }
    quotaErrors = { ...quotaErrors, [connectionId]: '' }
    const controller = new AbortController()

    await runQuotaFetchCycle<ConnectionUsageResponse>({
      targets: [{ id: connectionId, provider }],
      signal: controller.signal,
      force,
      fetch: (target, isForced) =>
        api.getConnectionUsage(target.id, isForced, controller.signal),
      onResult: (target, data) => {
        const quotaEntry: QuotaEntry = {
          quotas: parseQuotaData(target.provider, data),
          plan: data?.plan || null,
          message: data?.message || null,
          raw: data,
        }
        quotaData = { ...quotaData, [target.id]: quotaEntry }
        quotaErrors = { ...quotaErrors, [target.id]: '' }
        quotaLoading = { ...quotaLoading, [target.id]: false }
        setQuotaCache(target.id, quotaEntry)
      },
      onError: (target, error) => {
        quotaLoading = { ...quotaLoading, [target.id]: false }
        const message = error instanceof Error ? error.message : String(error)
        // 404-style: the connection is gone, which is not a failure worth
        // showing on a row that is about to disappear.
        if (message.toLowerCase().includes('not found')) return
        quotaErrors = { ...quotaErrors, [target.id]: message || 'Failed to fetch quota' }
      },
    })
  }

  async function refreshProvider(connectionId: string, provider: string): Promise<void> {
    await fetchQuota(connectionId, provider, { force: true })
    lastUpdated = new Date()
  }

  async function refreshAll(force = false): Promise<void> {
    if (refreshingAll) return
    refreshingAll = true
    countdown = REFRESH_INTERVAL_MS / 1000

    // Throttle Claude: poll its quota every Nth auto-tick (manual force bypasses)
    const tick = ++tickCount
    const claudeEvery = Math.round(CLAUDE_REFRESH_INTERVAL_MS / REFRESH_INTERVAL_MS)
    const shouldFetch = (conn: ProviderConnection) =>
      force || conn.provider !== 'claude' || tick % claudeEvery === 0

    try {
      const visibleConnections = await fetchConnections(page)
      await fetchQuotasFor(visibleConnections.filter(shouldFetch), visibleConnections, force)
    } catch (error) {
      console.error('Error refreshing all providers:', error)
      refreshingAll = false
    }
  }

  // ─── Connection actions ────────────────────────────────────────────────────
  async function handleDeleteConnection(id: string): Promise<void> {
    if (!window.confirm('Delete this connection?')) return
    deletingId = id
    try {
      await api.deleteConnection(id)
      const cache = getQuotaCache()
      if (cache[id]) {
        delete cache[id]
        try {
          window.localStorage.setItem('quotaCacheData', JSON.stringify(cache))
        } catch (e) {
          console.error('Error deleting cache entry:', e)
        }
      }
      // Reconcile page after delete (stay on page or move back if empty)
      await fetchConnections(page)
    } catch (error) {
      console.error('Error deleting connection:', error)
    } finally {
      deletingId = null
    }
  }

  async function handleToggleConnectionActive(id: string, nextActive: boolean): Promise<void> {
    togglingId = id
    try {
      await api.updateConnection(id, { isActive: nextActive ? 1 : 0 })
      connections = connections.map((c) =>
        c.id === id ? { ...c, isActive: nextActive ? 1 : 0 } : c,
      )
    } catch (error) {
      console.error('Error updating connection status:', error)
    } finally {
      togglingId = null
    }
  }

  async function bulkSetActive(targetIds: string[], nextActive: boolean): Promise<void> {
    if (!targetIds.length || bulkToggling) return
    bulkToggling = true
    try {
      await Promise.allSettled(
        targetIds.map((id) =>
          api.updateConnection(id, { isActive: nextActive ? 1 : 0 }).then(() => {
            connections = connections.map((c) =>
              c.id === id ? { ...c, isActive: nextActive ? 1 : 0 } : c,
            )
          }),
        ),
      )
    } catch (error) {
      console.error('Error bulk toggling connections:', error)
    } finally {
      bulkToggling = false
    }
  }

  function handleDisableDepleted(): void {
    const ids = connections
      .filter((c) => isActiveConn(c) && isConnectionDepleted(c))
      .map((c) => c.id)
    void bulkSetActive(ids, false)
  }

  function handleEnableAvailable(): void {
    const ids = connections
      .filter((c) => !isActiveConn(c) && !isConnectionDepleted(c))
      .map((c) => c.id)
    void bulkSetActive(ids, true)
  }

  // ─── Quota visibility ──────────────────────────────────────────────────────
  async function updateQuotaVisibility(
    nextVisibility: QuotaVisibility,
    previousVisibility: QuotaVisibility,
  ): Promise<void> {
    quotaVisibility = nextVisibility
    try {
      await api.patchSettings({ quotaVisibility: nextVisibility })
    } catch (error) {
      console.error('Error updating quota visibility:', error)
      quotaVisibility = previousVisibility
    }
  }

  function antigravityFamilyUnhide(hidden: Set<string>, key: string): void {
    // Hiding an antigravity family row unhides its member rows
    if (key === 'gemini') {
      for (const k of hidden) {
        if (k.startsWith('gemini-') && !k.includes('image')) hidden.delete(k)
      }
    } else if (key === 'claude') {
      for (const k of hidden) {
        if (k.startsWith('claude-')) hidden.delete(k)
      }
    }
  }

  function handleHideQuota(provider: string, quotaRow: { modelKey?: string; name?: string }): void {
    const key = getQuotaVisibilityKey(quotaRow)
    if (!provider || !key) return
    const previous = quotaVisibility
    const providerVisibility = previous[provider] || {}
    const hidden = new Set(providerVisibility.hidden || [])
    hidden.add(key)
    antigravityFamilyUnhide(hidden, key)
    const next: QuotaVisibility = {
      ...previous,
      [provider]: { ...providerVisibility, hidden: [...hidden] },
    }
    void updateQuotaVisibility(next, previous)
  }

  function handleShowQuota(provider: string, quotaRow: { modelKey?: string; name?: string }): void {
    const key = getQuotaVisibilityKey(quotaRow)
    if (!provider || !key) return
    const previous = quotaVisibility
    const providerVisibility = previous[provider] || {}
    const hidden = new Set(providerVisibility.hidden || [])
    hidden.delete(key)
    antigravityFamilyUnhide(hidden, key)
    const next: QuotaVisibility = {
      ...previous,
      [provider]: { ...providerVisibility, hidden: [...hidden] },
    }
    void updateQuotaVisibility(next, previous)
  }

  // ─── Derived ───────────────────────────────────────────────────────────────
  let sortedConnections = $derived.by<ProviderConnection[]>(() =>
    sortVisibleConnections(
      connections as ProviderConnectionLike[],
      quotaData,
      expiringFirst,
      providerFilter,
      quotaSortMode,
    ) as ProviderConnection[],
  )

  let hasEligibleConnections = $derived(totals.eligibleConnections > 0)
  let hasVisibleConnections = $derived(sortedConnections.length > 0)
  let emptyState = $derived(getConnectionsEmptyMessage(totals, providerFilter, accountFilter))
  let connectionsPageSummary = $derived(getConnectionsPaginationSummary(pagination))
  let isCustomPageSize = $derived(!ACCOUNT_PAGE_SIZE_OPTIONS.includes(pageSize))
  let selectedProviderLabel = $derived(
    providerFilter === 'all' ? 'All providers' : providerLabel(providerFilter),
  )

  // ─── Auto-refresh lifecycle ────────────────────────────────────────────────
  function stopTimers(): void {
    if (intervalTimer) {
      clearInterval(intervalTimer)
      intervalTimer = null
    }
    if (countdownTimer) {
      clearInterval(countdownTimer)
      countdownTimer = null
    }
  }

  function startTimers(): void {
    stopTimers()
    intervalTimer = setInterval(() => {
      void refreshAll()
    }, REFRESH_INTERVAL_MS)
    countdownTimer = setInterval(() => {
      countdown = countdown <= 1 ? REFRESH_INTERVAL_MS / 1000 : countdown - 1
    }, 1000)
  }

  // Persist auto-refresh preference
  $effect(() => {
    if (!hasHydratedAutoRefresh) return
    window.localStorage.setItem(AUTO_REFRESH_STORAGE_KEY, String(autoRefresh))
  })

  // Start/stop timers on auto-refresh changes
  $effect(() => {
    if (!hasHydratedAutoRefresh) return
    if (autoRefresh) {
      startTimers()
    } else {
      stopTimers()
    }
    return () => stopTimers()
  })

  // Refetch connections when pagination/filter inputs change (skip first run;
  // the initial load happens in onMount)
  $effect(() => {
    void page
    void pageSize
    void accountFilter
    void providerFilter
    if (!initialLoadDone) return
    void fetchConnections(page)
  })

  onMount(() => {
    // Seed from props to avoid a flash of empty state
    if (initialConns.length > 0 && connections.length === 0) {
      connections = initialConns
    }

  // A filter is not navigation, so the URL is rewritten rather than pushed:
  // the back button must not walk through every provider the operator clicked
  // through. 'all' removes the parameter instead of pinning it.
  $effect(() => {
    void providerFilter
    if (!initialLoadDone || typeof window === 'undefined') return
    const url = new URL(window.location.href)
    if (providerFilter && providerFilter !== 'all') {
      url.searchParams.set('provider', providerFilter)
    } else {
      url.searchParams.delete('provider')
    }
    if (url.href !== window.location.href) {
      window.history.replaceState(window.history.state, '', url)
    }
  })

  // An unknown provider yields an empty list and an empty dropdown, so the
  // deep link is released once the real option list is known.
  $effect(() => {
    if (!initialLoadDone || providerFilter === 'all') return
    if (providerOptions.length === 0) return
    if (providerOptions.includes(providerFilter)) return
    providerFilter = 'all'
  })

    // A ?provider= deep link must survive a reload and be shareable, so the
    // filter starts from the URL rather than always 'all'. An unknown value is
    // kept until the option list arrives, then dropped — ignoring it up front
    // would silently un-filter the page for anyone holding an old bookmark.
    const deepLinkProvider = new URLSearchParams(window.location.search).get('provider')
    if (deepLinkProvider) providerFilter = deepLinkProvider

    // Hydrate auto-refresh preference
    const stored = window.localStorage.getItem(AUTO_REFRESH_STORAGE_KEY)
    autoRefresh = stored === null ? true : stored === 'true'
    hasHydratedAutoRefresh = true

    // Load quota visibility settings
    api
      .getSettings()
      .then((s) => {
        const raw = s as unknown as Record<string, unknown>
        quotaVisibility = (raw?.quotaVisibility as QuotaVisibility) || {}
        autoPingMaps = {
          claude: (raw?.claudeAutoPing as Record<string, boolean>) || {},
          codex: (raw?.codexAutoPing as Record<string, boolean>) || {},
        }
      })
      .catch(() => {})

    // Initial data load
    void (async () => {
      connectionsLoading = true
      const visibleConnections = await fetchConnections(page)
      connectionsLoading = false
      initialLoadDone = true
      await fetchQuotasFor(visibleConnections, visibleConnections)
    })()

    // Pause auto-refresh when tab hidden (Page Visibility API)
    function handleVisibilityChange(): void {
      if (document.hidden) {
        stopTimers()
      } else if (autoRefresh && hasHydratedAutoRefresh) {
        startTimers()
      }
    }
    document.addEventListener('visibilitychange', handleVisibilityChange)

    // Close provider dropdown on outside click
    function handleDocClick(e: MouseEvent): void {
      const target = e.target as HTMLElement | null
      if (!target?.closest('#provider-dropdown-container')) {
        providerMenuOpen = false
      }
    }
    document.addEventListener('click', handleDocClick)

    return () => {
      stopTimers()
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      document.removeEventListener('click', handleDocClick)
    }
  })

  // ─── Page-size handlers ────────────────────────────────────────────────────
  function handlePageSizeChange(nextPageSize: number): void {
    page = 1
    pageSize = nextPageSize
    customPageSizeInput = String(nextPageSize)
  }

  function commitCustomPageSize(): void {
    const parsed = Number.parseInt(customPageSizeInput, 10)
    if (!Number.isFinite(parsed)) {
      customPageSizeInput = String(pageSize)
      return
    }
    const next = Math.min(ACCOUNT_PAGE_SIZE_MAX, Math.max(1, parsed))
    page = 1
    pageSize = next
    customPageSizeInput = String(next)
  }

  function handleCustomPageSizeKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Enter') return
    commitCustomPageSize()
  }

  function handleAccountFilterChange(): void {
    page = 1
  }
</script>

<div class="space-y-6">
  <!-- Header Controls -->
  <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-end">
    <div class="flex flex-wrap items-center gap-1.5">
      <!-- Provider filter dropdown -->
      <div class="relative" id="provider-dropdown-container">
        <button
          type="button"
          onclick={() => (providerMenuOpen = !providerMenuOpen)}
          class="flex h-8 items-center justify-between gap-1 rounded-lg border border-border-subtle bg-surface-2 px-2 text-xs text-text-main transition-colors hover:bg-surface-3"
          aria-haspopup="menu"
          aria-expanded={providerMenuOpen}
          title="Filter quota providers"
        >
          <span class="flex min-w-0 items-center gap-1.5">
            {#if providerFilter === 'all'}
              <span class="material-symbols-outlined text-[14px] text-text-muted">apps</span>
            {:else}
              <img
                src={getIconPath(providerFilter)}
                alt={providerFilter}
                class="size-[18px] rounded object-contain"
                onerror={(e) => {
                  ;(e.currentTarget as HTMLImageElement).style.display = 'none'
                }}
              />
            {/if}
            <span class="hidden truncate lg:inline">{selectedProviderLabel}</span>
          </span>
          <span class="material-symbols-outlined text-[14px] text-text-muted">expand_more</span>
        </button>

        {#if providerMenuOpen}
          <button
            type="button"
            class="fixed inset-0 z-30 cursor-default bg-transparent"
            aria-label="Close provider filter"
            onclick={() => (providerMenuOpen = false)}
          ></button>
          <div
            class="absolute left-0 top-full z-40 mt-2 w-64 overflow-hidden rounded-2xl border border-border-subtle bg-surface p-1.5 shadow-xl sm:w-72"
          >
            <button
              type="button"
              onclick={() => {
                if (shouldResetPage(providerFilter, 'all')) page = 1
                providerFilter = 'all'
                providerMenuOpen = false
              }}
              class="flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left text-sm transition-colors {providerFilter ===
              'all'
                ? 'bg-brand-500/10 text-brand-500'
                : 'text-text-main hover:bg-surface-2'}"
            >
              <span class="material-symbols-outlined text-[22px]">apps</span>
              <span class="font-medium">All providers</span>
              {#if providerFilter === 'all'}
                <span class="material-symbols-outlined ml-auto text-[20px]">check</span>
              {/if}
            </button>
            <div class="my-1 h-px bg-border-subtle"></div>
            <div class="max-h-72 overflow-y-auto pr-1">
              {#each providerOptions as prov (prov)}
                <button
                  type="button"
                  onclick={() => {
                    if (shouldResetPage(providerFilter, prov)) page = 1
                    providerFilter = prov
                    providerMenuOpen = false
                  }}
                  class="flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left text-sm transition-colors {providerFilter ===
                  prov
                    ? 'bg-brand-500/10 text-brand-500'
                    : 'text-text-main hover:bg-surface-2'}"
                >
                  <img
                    src={getIconPath(prov)}
                    alt={prov}
                    class="size-6 rounded-md object-contain"
                    onerror={(e) => {
                      ;(e.currentTarget as HTMLImageElement).style.display = 'none'
                    }}
                  />
                  <span class="font-medium">{providerLabel(prov)}</span>
                  {#if providerFilter === prov}
                    <span class="material-symbols-outlined ml-auto text-[20px]">check</span>
                  {/if}
                </button>
              {/each}
            </div>
          </div>
        {/if}
      </div>

      <!-- Account status filter -->
      <select
        bind:value={accountFilter}
        onchange={handleAccountFilterChange}
        class="h-8 rounded-lg border border-border-subtle bg-surface-2 px-2 text-xs text-text-main outline-none transition-colors hover:bg-surface-3"
        aria-label="Filter accounts by status"
      >
        {#each ACCOUNT_FILTER_OPTIONS as option (option.value)}
          <option value={option.value}>{option.label}</option>
        {/each}
      </select>

      <!-- Codex quota sort -->
      {#if providerFilter === 'codex'}
        <select
          bind:value={quotaSortMode}
          class="h-8 rounded-lg border border-border-subtle bg-surface-2 px-2 text-xs text-text-main outline-none transition-colors hover:bg-surface-3"
          aria-label="Sort Codex quotas by remaining"
        >
          {#each QUOTA_SORT_OPTIONS as option (option.value)}
            <option value={option.value}>{option.label}</option>
          {/each}
        </select>
      {/if}

      <!-- Sensor email toggle -->
      <button
        type="button"
        onclick={() => emailPrivacy.toggle()}
        aria-pressed={$emailPrivacy}
        class="flex h-8 shrink-0 items-center gap-1 rounded-lg border px-2 text-xs transition-colors cursor-pointer {$emailPrivacy
          ? 'border-brand-500/40 bg-brand-500/10 text-brand-500 font-medium'
          : 'border-border-subtle bg-surface text-text-main hover:bg-surface-2'}"
        title={$emailPrivacy ? 'Tampilkan email lengkap' : 'Sensor / sembunyikan email'}
      >
        <span class="material-symbols-outlined text-[14px]">
          {$emailPrivacy ? 'visibility_off' : 'visibility'}
        </span>
        <span class="hidden sm:inline">{$emailPrivacy ? 'Email Disensor' : 'Sensor Email'}</span>
      </button>

      <!-- Expiring first -->
      <button
        type="button"
        onclick={() => (expiringFirst = !expiringFirst)}
        aria-pressed={expiringFirst}
        class="flex h-8 shrink-0 items-center gap-1 rounded-lg border px-2 text-xs transition-colors {expiringFirst
          ? 'border-amber-500/40 bg-amber-500/10 text-amber-500'
          : 'border-border-subtle bg-surface text-text-main hover:bg-surface-2'}"
        title="Sort accounts by earliest quota reset time"
      >
        <span class="material-symbols-outlined text-[14px]">hourglass_top</span>
        <span class="hidden sm:inline">Expiring first</span>
      </button>

      <!-- Bulk: disable depleted -->
      <button
        type="button"
        onclick={handleDisableDepleted}
        disabled={bulkToggling}
        class="flex h-8 shrink-0 items-center gap-1 rounded-lg border border-red-500/30 px-2 text-xs text-red-500 transition-colors hover:bg-red-500/10 disabled:opacity-50"
        title="Disable connections with depleted quota on the current page"
      >
        <span class="material-symbols-outlined text-[14px]">block</span>
        <span class="hidden sm:inline">Turn off Empty</span>
      </button>

      <!-- Bulk: enable available -->
      <button
        type="button"
        onclick={handleEnableAvailable}
        disabled={bulkToggling}
        class="flex h-8 shrink-0 items-center gap-1 rounded-lg border border-emerald-500/30 px-2 text-xs text-emerald-500 transition-colors hover:bg-emerald-500/10 disabled:opacity-50"
        title="Enable connections that still have quota on the current page"
      >
        <span class="material-symbols-outlined text-[14px]">check_circle</span>
        <span class="hidden sm:inline">Turn on Available</span>
      </button>

      <!-- Auto-refresh toggle -->
      <button
        type="button"
        onclick={() => (autoRefresh = !autoRefresh)}
        class="flex h-8 shrink-0 items-center gap-1 rounded-lg border border-border-subtle bg-surface px-2 text-xs transition-colors hover:bg-surface-2"
        title={autoRefresh ? 'Disable auto-refresh' : 'Enable auto-refresh'}
      >
        <span
          class="material-symbols-outlined text-[14px] {autoRefresh
            ? 'text-brand-500'
            : 'text-text-muted'}"
        >
          {autoRefresh ? 'toggle_on' : 'toggle_off'}
        </span>
        <span class="hidden text-text-main sm:inline">Auto-refresh</span>
        {#if autoRefresh}
          <span class="text-[10px] tabular-nums text-text-muted">({countdown}s)</span>
        {/if}
      </button>

      <!-- Refresh all -->
      <button
        type="button"
        onclick={() => refreshAll(true)}
        disabled={refreshingAll}
        class="flex h-8 shrink-0 items-center gap-1 rounded-lg border border-border-subtle bg-surface px-2 text-xs text-text-main transition-colors hover:bg-surface-2 disabled:opacity-50"
        title="Refresh all"
      >
        <span class="material-symbols-outlined text-[14px] {refreshingAll ? 'animate-spin' : ''}">
          refresh
        </span>
      </button>
    </div>
  </div>

  <!-- Expiring-first notice -->
  {#if expiringFirst}
    <div
      class="rounded-xl border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
    >
      Expiring-first currently reorders accounts inside the current page. Cross-page ordering still
      follows backend pagination.
    </div>
  {/if}

  <!-- Empty states -->
  {#if !connectionsLoading && !hasEligibleConnections}
    <div
      class="rounded-xl border border-border-subtle bg-surface p-12 text-center shadow-[var(--shadow-soft)]"
    >
      <span class="material-symbols-outlined text-[64px] text-text-muted opacity-20">
        {emptyState.icon}
      </span>
      <h3 class="mt-4 text-lg font-semibold text-text-main">{emptyState.title}</h3>
      <p class="mx-auto mt-2 max-w-md text-sm text-text-muted">{emptyState.description}</p>
    </div>
  {:else if !connectionsLoading && !hasVisibleConnections}
    <div
      class="rounded-xl border border-border-subtle bg-surface p-12 text-center shadow-[var(--shadow-soft)]"
    >
      <span class="material-symbols-outlined text-[64px] text-text-muted opacity-20">
        {emptyState.icon}
      </span>
      <h3 class="mt-4 text-lg font-semibold text-text-main">{emptyState.title}</h3>
      <p class="mx-auto mt-2 max-w-md text-sm text-text-muted">{emptyState.description}</p>
    </div>
  {:else}
    <!-- Provider cards: 2 columns, compact -->
    <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
      {#each sortedConnections as conn (conn.id)}
        {@const isActive = isActiveConn(conn)}
        {@const quota = quotaData[conn.id]}
        {@const isLoading = quotaLoading[conn.id]}
        {@const error = quotaErrors[conn.id]}
        {@const rowBusy = deletingId === conn.id || togglingId === conn.id}
        {@const rawQuotas = quota?.quotas || []}
        {@const visibleQuotas = filterQuotasByVisibility(conn.provider, rawQuotas, quotaVisibility)}
        {@const hiddenQuotaRows = getHiddenQuotaRows(conn.provider, rawQuotas, quotaVisibility)}

        <div
          class="flex min-w-0 flex-col overflow-hidden rounded-[14px] border border-border-subtle bg-surface shadow-[var(--shadow-soft)] {isActive
            ? ''
            : 'opacity-60'}"
        >
          <!-- Card header -->
          <div class="border-b border-border-subtle px-3 py-2">
            <div class="flex items-center justify-between gap-2">
              <div class="flex min-w-0 items-center gap-2">
                <div
                  class="flex h-8 w-8 shrink-0 items-center justify-center overflow-hidden rounded-md"
                >
                  <img
                    src={getIconPath(conn.provider)}
                    alt={conn.provider}
                    class="size-8 object-contain"
                    onerror={(e) => {
                      ;(e.currentTarget as HTMLImageElement).style.display = 'none'
                    }}
                  />
                </div>
                <div class="min-w-0">
                  <h3 class="truncate text-sm font-semibold text-text-main">
                    {providerLabel(conn.provider)}
                  </h3>
                  {#if getConnectionLabel(conn)}
                    <p class="truncate text-xs text-text-muted">{formatEmailLabel(getConnectionLabel(conn), $emailPrivacy)}</p>
                  {/if}
                  {#if getConnectionSecondaryLabel(conn)}
                    <p class="truncate text-[11px] text-text-muted/80">{formatEmailLabel(getConnectionSecondaryLabel(conn), $emailPrivacy)}</p>
                  {/if}
                  {#if conn.provider === 'kiro'}
                    <div class="mt-1 flex flex-wrap items-center gap-1">
                      <span class="rounded-full bg-brand-500/10 px-2 py-0.5 text-[10px] font-semibold text-brand-600 dark:text-brand-300">
                        {kiroMethodLabel(conn)}
                      </span>
                      {#if kiroRegion(conn)}
                        <span class="rounded-full bg-blue-500/10 px-2 py-0.5 text-[10px] font-semibold text-blue-600 dark:text-blue-400">
                          {kiroRegion(conn)}
                        </span>
                      {/if}
                      <span
                        class="rounded-full px-2 py-0.5 text-[10px] font-semibold {!isActive
                          ? 'bg-surface-3 text-text-muted'
                          : conn.testStatus === 'active' || conn.testStatus === 'success'
                            ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
                            : conn.testStatus === 'error' ||
                                conn.testStatus === 'expired' ||
                                conn.testStatus === 'unavailable'
                              ? 'bg-red-500/10 text-red-600 dark:text-red-400'
                              : 'bg-surface-3 text-text-muted'}"
                      >
                        {!isActive ? 'disabled' : conn.testStatus || 'unknown'}
                      </span>
                      {#if (conn.providerSpecificData as Record<string, unknown> | undefined)?.profileArn}
                        {@const profileArn = String((conn.providerSpecificData as Record<string, unknown>).profileArn)}
                        <button
                          type="button"
                          onclick={() => copyArn(profileArn, conn.id)}
                          title={profileArn}
                          class="inline-flex max-w-full items-center gap-1 rounded-full border border-border-subtle px-2 py-0.5 text-[10px] text-text-muted transition-colors hover:text-primary cursor-pointer"
                        >
                          <span class="material-symbols-outlined text-[12px]">
                            {copiedArnId === conn.id ? 'check' : 'content_copy'}
                          </span>
                          <code class="truncate font-mono">{profileArn}</code>
                        </button>
                      {/if}
                    </div>
                  {/if}
                </div>
              </div>

              <div class="flex shrink-0 items-center gap-1">
                {#if conn.provider === 'codex'}
                  {@const resetCreditCount = getCodexResetCreditCount(quota)}
                  <button
                    type="button"
                    disabled={isLoading || rowBusy || resetCreditConsuming}
                    onclick={() => openResetCredits(conn)}
                    aria-label="Redeem a Codex reset credit"
                    title={resetCreditCount > 0
                      ? `Redeem a Codex reset credit (${resetCreditCount} available)`
                      : 'Check Codex reset credits'}
                    class="flex h-8 min-w-10 items-center justify-center gap-1 rounded-lg border px-2 text-[11px] font-medium tabular-nums transition-colors disabled:cursor-not-allowed disabled:opacity-60 {resetCreditCount > 0 ? 'border-primary/30 bg-primary/5 text-primary hover:bg-primary/10' : 'border-border-subtle bg-surface-2 text-text-muted'}"
                  >
                    <span class="material-symbols-outlined text-[15px]">restart_alt</span>
                    <span>{resetCreditCount}</span>
                  </button>
                {/if}

                {#if (conn.provider === 'claude' || conn.provider === 'codex') && conn.authType === 'oauth'}
                  {@const isAutoPingActive = autoPingMaps[conn.provider]?.[conn.id] === true}
                  <button
                    type="button"
                    onclick={() => toggleAutoPing(conn.id, conn.provider, !isAutoPingActive)}
                    title={AUTO_PING_TOOLTIPS[conn.provider]}
                    aria-label="Toggle auto-ping"
                    class="flex h-8 w-8 items-center justify-center rounded-lg transition-colors hover:bg-surface-3 cursor-pointer {isAutoPingActive ? 'text-primary' : 'text-text-muted'}"
                  >
                    <span class="material-symbols-outlined text-[18px]">bolt</span>
                  </button>
                {/if}

                <!-- Refresh quota -->
                <button
                  type="button"
                  onclick={() => refreshProvider(conn.id, conn.provider)}
                  disabled={isLoading || rowBusy}
                  aria-label="Refresh quota"
                  title="Refresh quota"
                  class="flex h-8 w-8 items-center justify-center rounded-lg text-text-muted transition-colors hover:bg-surface-3 hover:text-text-main disabled:opacity-50 cursor-pointer"
                >
                  <span
                    class="material-symbols-outlined text-[18px] {isLoading
                      ? 'animate-spin'
                      : ''}">refresh</span
                  >
                </button>

                <!-- Edit connection -->
                <button
                  type="button"
                  onclick={() => openEditModal(conn)}
                  disabled={rowBusy}
                  aria-label="Edit connection"
                  title="Edit connection"
                  class="flex h-8 w-8 items-center justify-center rounded-lg text-text-muted transition-colors hover:bg-surface-3 hover:text-primary disabled:opacity-50 cursor-pointer"
                >
                  <span class="material-symbols-outlined text-[18px]">edit</span>
                </button>

                <!-- Delete connection -->
                <button
                  type="button"
                  onclick={() => handleDeleteConnection(conn.id)}
                  disabled={rowBusy}
                  aria-label="Delete connection"
                  title="Delete connection"
                  class="flex h-8 w-8 items-center justify-center rounded-lg text-text-muted transition-colors hover:bg-red-500/10 hover:text-red-500 disabled:opacity-50 cursor-pointer"
                >
                  <span
                    class="material-symbols-outlined text-[18px] {deletingId === conn.id
                      ? 'animate-pulse'
                      : ''}">delete</span
                  >
                </button>

                <div
                  class="inline-flex items-center pl-0.5"
                  title={isActive ? 'Disable connection' : 'Enable connection'}
                >
                  <Toggle
                    size="sm"
                    checked={isActive}
                    disabled={rowBusy}
                    onChange={(nextActive) => handleToggleConnectionActive(conn.id, nextActive)}
                  />
                </div>
              </div>
            </div>
          </div>

          <!-- Card body: quota rows -->
          <div class="px-2 py-1.5">
            {#if isLoading}
              <div class="py-5 text-center text-text-muted">
                <span class="material-symbols-outlined text-[28px] animate-spin">
                  progress_activity
                </span>
              </div>
            {:else if error}
              <div class="py-5 text-center">
                <p class="text-xs text-text-muted">{error}</p>
              </div>
            {:else if quota?.message}
              <div class="py-5 text-center">
                <p class="text-xs text-text-muted">{quota.message}</p>
              </div>
            {:else}
              <QuotaTable
                quotas={visibleQuotas}
                compact
                sortMode={conn.provider === 'codex' && quotaSortMode !== 'default'
                  ? (quotaSortMode as 'remaining-asc' | 'remaining-desc')
                  : 'default'}
                showSortLabel={conn.provider === 'codex' && quotaSortMode !== 'default'}
                onHideQuota={(quotaRow) => handleHideQuota(conn.provider, quotaRow)}
              />
              {#if visibleQuotas.length === 0 && rawQuotas.length === 0}
                <div class="py-3 text-center text-xs text-text-muted">
                  Account active. No quota limits tracked.
                </div>
              {/if}
            {/if}
            {#if hiddenQuotaRows.length > 0}
              <div
                class="mt-2 flex min-w-0 items-center gap-1 border-t border-border-subtle/60 pt-2 text-[10px] text-text-muted"
              >
                <span class="material-symbols-outlined shrink-0 text-[14px]">visibility_off</span>
                <span class="shrink-0">Hidden:</span>
                <div
                  class="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto whitespace-nowrap pb-2"
                >
                  {#each hiddenQuotaRows as quotaRow (getQuotaVisibilityKey(quotaRow))}
                    <button
                      type="button"
                      onclick={() => handleShowQuota(conn.provider, quotaRow)}
                      class="shrink-0 rounded-md border border-border-subtle px-1.5 py-0.5 transition-colors hover:bg-surface-3 hover:text-text-main"
                      title="Show this quota row"
                    >
                      {quotaRow.name}
                    </button>
                  {/each}
                </div>
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </div>

    <!-- Pagination footer -->
    <div class="rounded-xl border border-border-subtle bg-surface-2/60 px-3 py-2">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <span class="text-xs text-text-muted">{connectionsPageSummary}</span>
        <div class="flex flex-wrap items-center gap-2">
          <select
            value={isCustomPageSize ? 'custom' : String(pageSize)}
            onchange={(e) => {
              const nextValue = (e.currentTarget as HTMLSelectElement).value
              if (nextValue === 'custom') return
              const nextPageSize = Number.parseInt(nextValue, 10)
              if (Number.isFinite(nextPageSize)) handlePageSizeChange(nextPageSize)
            }}
            class="h-8 rounded-lg border border-border-subtle bg-surface-2 px-2 text-xs text-text-main outline-none transition-colors hover:bg-surface-3"
            aria-label="Accounts per page"
          >
            {#each ACCOUNT_PAGE_SIZE_OPTIONS as option (option)}
              <option value={String(option)}>{option} / page</option>
            {/each}
            <option value="custom">Custom</option>
          </select>
          <input
            type="number"
            min="1"
            max={String(ACCOUNT_PAGE_SIZE_MAX)}
            inputmode="numeric"
            bind:value={customPageSizeInput}
            onblur={commitCustomPageSize}
            onkeydown={handleCustomPageSizeKeydown}
            class="h-8 w-20 rounded-lg border border-border-subtle bg-surface-2 px-2 text-xs text-text-main outline-none transition-colors hover:bg-surface-3"
            aria-label="Custom accounts per page"
            placeholder="Custom"
          />
          <span class="text-xs text-text-muted">
            Page {pagination.page} / {pagination.totalPages}
          </span>
        </div>
        <div class="flex items-center gap-1.5">
          <button
            type="button"
            onclick={() => (page = 1)}
            disabled={pagination.page <= 1 || connectionsLoading || refreshingAll}
            class="flex h-8 items-center rounded-lg border border-border-subtle px-3 text-xs text-text-main transition-colors hover:bg-surface-3 disabled:cursor-not-allowed disabled:opacity-40"
          >
            First Page
          </button>
          <button
            type="button"
            onclick={() => (page = Math.max(1, page - 1))}
            disabled={pagination.page <= 1 || connectionsLoading || refreshingAll}
            aria-label="Previous accounts page"
            class="flex h-8 w-8 items-center justify-center rounded-lg border border-border-subtle text-text-main transition-colors hover:bg-surface-3 disabled:cursor-not-allowed disabled:opacity-40"
          >
            <span class="material-symbols-outlined text-[16px]">chevron_left</span>
          </button>
          <button
            type="button"
            onclick={() => (page = Math.min(pagination.totalPages, page + 1))}
            disabled={pagination.page >= pagination.totalPages || connectionsLoading || refreshingAll}
            aria-label="Next accounts page"
            class="flex h-8 w-8 items-center justify-center rounded-lg border border-border-subtle text-text-main transition-colors hover:bg-surface-3 disabled:cursor-not-allowed disabled:opacity-40"
          >
            <span class="material-symbols-outlined text-[16px]">chevron_right</span>
          </button>
          <button
            type="button"
            onclick={() => (page = pagination.totalPages)}
            disabled={pagination.page >= pagination.totalPages || connectionsLoading || refreshingAll}
            class="flex h-8 items-center rounded-lg border border-border-subtle px-3 text-xs text-text-main transition-colors hover:bg-surface-3 disabled:cursor-not-allowed disabled:opacity-40"
          >
            Last Page
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>

{#if editingConnection}
  <EditConnectionModal
    connection={editingConnection}
    onClose={() => (editingConnection = null)}
    onSave={saveEditingConnection}
    onSaveError={(message) => alert(message)}
    testLabel="Test"
  />
{/if}

{#if resetCreditConn}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 backdrop-blur-[2px]"
      onclick={closeResetCredits}
      role="presentation"
    ></div>
    <div class="relative w-full max-w-md bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] p-6">
      <div class="flex items-center justify-between pb-3 border-b border-border-subtle mb-4">
        <h2 class="text-lg font-semibold text-text-main">Redeem a reset credit</h2>
        <button
          type="button"
          onclick={closeResetCredits}
          disabled={resetCreditConsuming}
          class="text-text-muted hover:text-text-main transition-colors disabled:opacity-50 cursor-pointer"
          aria-label="Close"
        >
          <span class="material-symbols-outlined">close</span>
        </button>
      </div>

      {#if resetCreditConfirming && resetCreditPending}
        <div class="rounded-lg border border-amber-500/30 bg-amber-500/10 p-4">
          <div class="flex items-start gap-3">
            <span class="material-symbols-outlined text-amber-500">warning</span>
            <div class="min-w-0">
              <p class="font-semibold text-text-main">Redeem this reset credit?</p>
              <p class="mt-1 text-sm text-text-muted">{getResetCreditConfirmation(resetCreditPending)}</p>
            </div>
          </div>
        </div>
        <div class="mt-3 rounded-lg border border-border-subtle px-3 py-2.5">
          <p class="text-sm font-medium text-text-main">{getResetCreditWindowTitle(resetCreditPending)}</p>
          {#if getCodexResetCreditExpiryLabel(resetCreditPending.expiresAt).relative}
            <p class="mt-0.5 text-[11px] text-text-muted">
              Expires in {getCodexResetCreditExpiryLabel(resetCreditPending.expiresAt).relative}
            </p>
          {/if}
        </div>
        {#if resetCreditError}
          <p class="mt-3 text-xs text-red-500">{resetCreditError}</p>
        {/if}
      {:else}
      <p class="text-xs text-text-muted mb-4">
        Redeeming a credit resets the Codex usage window immediately instead of waiting for the
        countdown to run out.
      </p>

      {#if resetCreditLoading}
        <p class="text-sm text-text-muted py-4 text-center">Loading reset credits…</p>
      {:else if resetCreditError && resetCreditList.length === 0}
        <p class="text-sm text-red-500 py-4 text-center">{resetCreditError}</p>
      {:else if resetCreditList.length === 0 && resetCreditCount > 0}
        <p class="text-sm text-text-muted py-4 text-center">
          Credit details are currently unavailable. Refresh and try again.
        </p>
      {:else if resetCreditList.length === 0}
        <p class="text-sm text-text-muted py-4 text-center">No reset credits are available on this account.</p>
      {:else}
        <div class="space-y-2 max-h-64 overflow-y-auto">
          {#each resetCreditList as credit, index (credit.selectionToken)}
            <div
              class="flex flex-col gap-3 rounded-lg border px-3 py-2.5 sm:flex-row sm:items-center {resetCreditSelected ===
              credit.selectionToken
                ? 'border-primary/40 bg-primary/5'
                : 'border-border-subtle'}"
            >
              <span class="min-w-0 flex-1">
                <span class="block text-sm font-medium text-text-main">
                  {getResetCreditWindowTitle(credit)}
                  {#if index === 0}
                    <span class="ml-1 text-[10px] font-semibold text-primary">recommended</span>
                  {/if}
                </span>
                {#if getCodexResetCreditExpiryLabel(credit.expiresAt).relative}
                  <span class="block text-[11px] text-text-muted">
                    Expires in {getCodexResetCreditExpiryLabel(credit.expiresAt).relative} · {getCodexResetCreditExpiryLabel(credit.expiresAt).absolute}
                  </span>
                {:else}
                  <span class="block text-[11px] text-text-muted">No expiry date</span>
                {/if}
                {#if credit.description}
                  <span class="block text-[11px] text-text-subtle mt-0.5">{credit.description}</span>
                {/if}
              </span>
              <button
                type="button"
                onclick={() => requestResetCredit(credit)}
                disabled={resetCreditConsuming}
                class="shrink-0 px-2.5 py-1.5 rounded-lg bg-primary text-[11px] font-semibold text-white hover:bg-primary-hover transition-colors disabled:opacity-50 cursor-pointer"
              >
                Redeem
              </button>
            </div>
          {/each}
        </div>

        {#if resetCreditError}
          <p class="mt-3 text-xs text-red-500">{resetCreditError}</p>
        {/if}
      {/if}
      {/if}

      <div class="flex justify-end gap-2 mt-5">
        {#if resetCreditConfirming && resetCreditPending}
          <button
            type="button"
            onclick={backToResetCreditList}
            disabled={resetCreditConsuming}
            class="px-3 py-2 rounded-lg border border-border text-xs font-semibold text-text-main hover:bg-surface-2 transition-colors disabled:opacity-50 cursor-pointer"
          >
            Back
          </button>
          <button
            type="button"
            onclick={confirmResetCredit}
            disabled={resetCreditConsuming}
            class="px-3 py-2 rounded-lg bg-primary text-xs font-semibold text-white hover:bg-primary-hover transition-colors disabled:opacity-50 cursor-pointer"
          >
            {resetCreditConsuming ? 'Redeeming…' : 'Redeem credit'}
          </button>
        {:else}
          <button
            type="button"
            onclick={closeResetCredits}
            disabled={resetCreditConsuming}
            class="px-3 py-2 rounded-lg border border-border text-xs font-semibold text-text-main hover:bg-surface-2 transition-colors disabled:opacity-50 cursor-pointer"
          >
            Close
          </button>
        {/if}
      </div>
    </div>
  </div>
{/if}
