<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import {
    api,
    normalizeLastError,
    type ConnectionUsageResponse,
    type CreateConnectionPayload,
    type FreebuffSessionStatusResponse,
    type ProviderConnection,
    type ProviderNode,
    type ModelCaps,
    type ProxyPool,
    type Settings
  } from '../../api/client'
  import { PROVIDER_CATALOG, PROVIDER_CATALOG_MAP, type ProviderCatalogItem } from '../../lib/providers'
  import {
    clearCallback,
    CODEX_REDIRECT_URI,
    clearPending,
    dashboardCallbackURL,
    loadPendings,
    matchPending,
    OAUTH_CALLBACK_KEY,
    OAUTH_CHANNEL,
    readCallback,
    savePending,
    oauthLoopbackCallbackURL,
    writeCallback,
  } from '../../lib/oauth-handoff'
  import { getModelsByProviderId, PROVIDER_ID_TO_ALIAS } from '../../lib/models'
  import { copyToClipboard } from '../../lib/clipboard'
  import { notifyCustomModelsChanged } from '../../lib/customModels'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'
  import { loadDeprecations, refreshDeprecations, subscribeDeprecations } from '../../lib/modelDeprecations.svelte'
  import DeprecatedBadge from '../../lib/ui/DeprecatedBadge.svelte'
  import Menu from '../../lib/ui/Menu.svelte'
  import MenuItem from '../../lib/ui/MenuItem.svelte'
  import {
    buildAvailableModels,
    fetchProviderModelsData,
    fetchSuggestedModels,
    getIconPath,
    isChatModel,
    type CustomModelData,
    type ModelItem,
    type SuggestedModel
  } from './types'
  import { proxyBadgeInfo } from './proxyBadge'
  import {
    formatCheckAllSummary,
    getBlockedModelIds,
    parseModelTestVerdict,
    type CheckAllSummary,
    type ModelTestStatus,
  } from './modelCheckAll'
  import {
    badgeFor,
    canProbeRow,
    probeOutcomeFrom,
    probeOutcomeFromError,
    testingStatus,
    type ProbeStatus
  } from './connectionProbe'
  import EditConnectionModal, { type ConnectionUpdate } from './EditConnectionModal.svelte'
  import AddConnectionModal from './AddConnectionModal.svelte'
  import AddCustomModelModal from './AddCustomModelModal.svelte'
  import ProviderHeaderOverridesModal from './ProviderHeaderOverridesModal.svelte'
  import EditCompatibleNodeModal from './EditCompatibleNodeModal.svelte'
  import FreebuffSessionBanner from './FreebuffSessionBanner.svelte'
  import ProviderIcon from './ProviderIcon.svelte'

  interface Props {
    providerId: string
    connections: ProviderConnection[]
    providerNodes: ProviderNode[]
    onBack: () => void
    onRefresh: () => void
    /** Called with the new id after a URL-suffix rename moves this node. */
    onSelectProvider?: (id: string) => void
  }

  let {
    providerId,
    connections = [],
    providerNodes = [],
    onBack,
    onRefresh,
    onSelectProvider
  }: Props = $props()

  // Catalog & Node resolution
  let selectedCatalogItem = $derived<ProviderCatalogItem | undefined>(
    PROVIDER_CATALOG.find((p) => p.id === providerId)
  )
  let selectedNode = $derived<ProviderNode | undefined>(
    providerNodes.find((n) => n.id === providerId)
  )
  let providerName = $derived(selectedNode?.name || selectedCatalogItem?.name || providerId)
  let providerColor = $derived(selectedCatalogItem?.color || '#f59e0b')
  let providerWebsite = $derived(
    selectedCatalogItem?.notice?.apiKeyUrl ||
      selectedCatalogItem?.notice?.signupUrl ||
      selectedCatalogItem?.website ||
      (providerId === 'antigravity' ? 'https://antigravity.google' : '')
  )
  // Upstream parity: "Get API Key" when the notice links straight to a key
  // page, otherwise "Sign up / Learn more".
  let providerWebsiteLabel = $derived(
    selectedCatalogItem?.notice?.apiKeyUrl ? 'Get API Key' : 'Sign up / Learn more'
  )
  let providerNoticeText = $derived(selectedCatalogItem?.notice?.text || '')
  let providerNoticeApiKeyUrl = $derived(selectedCatalogItem?.notice?.apiKeyUrl || '')
  let isOAuth = $derived(selectedCatalogItem?.category === 'oauth')
  let isClineOAuth = $derived(providerId === 'cline' || providerId === 'clinepass')
  let isPKCEOAuth = $derived(providerId === 'claude' || providerId === 'codex' || providerId === 'xai' || providerId === 'gitlab')
  let isAuthCodeOAuth = $derived(providerId === 'gemini-cli' || providerId === 'iflow')
  let isCustomOAuth = $derived(providerId === 'trae' || providerId === 'windsurf' || providerId === 'zed')
  // Upstream parity: providers with authModes ["apikey","oauth"] offer both an
  // OAuth login and a manual API Key button (data-driven, category-independent
  // so freeTier kimchi is covered too — mirrors upstream hasDualAuthModes).
  let authModes = $derived(selectedCatalogItem?.authModes || [])
  let hasDualAuthModes = $derived(
    !selectedNode && authModes.includes('oauth') && authModes.includes('apikey')
  )
  // Upstream per-provider button labels ([id]/page.js).
  let oauthButtonLabel = $derived(
    providerId === 'xai' ? 'Grok Build OAuth' : providerId === 'kimi' ? 'Kimi Coding OAuth' : 'OAuth'
  )
  let apiKeyButtonLabel = $derived(
    providerId === 'xai'
      ? 'xAI API Key'
      : providerId === 'kimi'
        ? 'Kimi API Key'
        : providerId === 'qoder'
          ? 'PAT'
          : 'API Key'
  )
  let isDeviceOAuth = $derived(
    providerId === 'qoder' || providerId === 'kilocode' || providerId === 'grok-cli' ||
    providerId === 'github' || providerId === 'kiro' || providerId === 'kimi' ||
    providerId === 'kimi-coding' || providerId === 'codebuddy-cn' || providerId === 'codebuddy-intl'
  )
  // Upstream parity: only the explicit noAuth flag hides the Connections card
  // ([id]/page.js isFreeNoAuth = !!FREE_PROVIDERS[id]?.noAuth). Category "free"
  // is NOT equivalent — kiro/gemini-cli are free with noAuth:false and still
  // need their Connect/OAuth + API Key buttons.
  let isNoAuth = $derived(selectedCatalogItem?.noAuth === true)
  let hasRiskNotice = $derived(providerId === 'antigravity' || Boolean(selectedCatalogItem?.notice?.text?.includes('RISK_NOTICE')))

  // Free provider proxy & rotation state
  let freeProxyPoolId = $state('none')
  let freeRotateStrategy = $state<'none' | 'round-robin' | 'random'>('none')
  let isSavingFreeProxy = $state(false)
  let savedFreeProxy = $state(false)
  // Quota & Cooldown tracking state
  let connectionQuotas = $state<Record<string, ConnectionUsageResponse>>({})
  let currentTime = $state(Date.now())

  onMount(() => {
    const timer = setInterval(() => {
      currentTime = Date.now()
    }, 1000)
    return () => {
      clearInterval(timer)
      if (freebuffPollTimer) {
        clearInterval(freebuffPollTimer)
        freebuffPollTimer = null
      }
    }
  })

  // Storage alias
  let storageAlias = $derived(
    selectedNode?.id || selectedCatalogItem?.alias || PROVIDER_ID_TO_ALIAS[providerId] || providerId
  )

  // Filtered connections for this provider, sorted by priority ASC
  let providerConnections = $derived(
    connections
      .filter((c) => c.provider === providerId)
      .sort((a, b) => (a.priority ?? 999999) - (b.priority ?? 999999))
  )
  // Upstream parity: compatible-node detection drives the details card,
  // bottom Add button, models section, and edit-node modal.
  let isOpenAICompatibleNode = $derived(!!selectedNode && selectedNode.type === 'openai-compatible')
  let isAnthropicCompatibleNode = $derived(!!selectedNode && selectedNode.type === 'anthropic-compatible')
  let isCompatibleNode = $derived(isOpenAICompatibleNode || isAnthropicCompatibleNode)
  let isResponsesNode = $derived(selectedNode?.apiType === 'responses')

  // Models state
  let customModels = $state<CustomModelData[]>([])
  let disabledModelIds = $state<string[]>([])
  let builtInModels = $derived(getModelsByProviderId(providerId).filter(isChatModel))
  let providerCustomModels = $derived(
    customModels.filter(
      (m) => (m.providerAlias === storageAlias || m.providerAlias === providerId) && isChatModel(m)
    )
  )
  let allAvailableModels = $derived<ModelItem[]>(
    buildAvailableModels(builtInModels, providerCustomModels)
  )
  // Registry order, like upstream: `models` there is getModelsByProviderId()
  // verbatim (providers/[id]/page.js:158) with custom models appended after the
  // catalog, so the rows read in the same sequence on both dashboards.
  let visibleModels = $derived(allAvailableModels.filter((m) => !disabledModelIds.includes(m.id)))
  // Suggested free models from the provider's public catalog (upstream parity).
  let suggestedModels = $state<SuggestedModel[]>([])
  let suggestedNotAdded = $derived(
    suggestedModels.filter(
      (m) =>
        !builtInModels.some((b) => b.id === m.id) &&
        !providerCustomModels.some((c) => c.id === m.id)
    )
  )
  let allDisabled = $derived(
    allAvailableModels.length > 0 && disabledModelIds.length >= allAvailableModels.length
  )
  let isSyncingModels = $state(false)
  let modelDeprecationCount = $state(0)

  // Capabilities + thinking levels are resolved server-side (GET /api/models/caps):
  // the catalog ships as a static bundle, but caps depend on the provider
  // registry, the capability tables and the synced models.dev catalog.
  let modelCaps = $state<Record<string, ModelCaps>>({})
  // Union of the thinking levels this provider's models accept, with the
  // explicit "auto" reset first — upstream providerThinkingLevels
  // (dashboard/providers/[id]/page.js:186). null hides the picker entirely for a
  // provider whose models have no reasoning.
  let providerThinkingLevels = $derived.by(() => {
    const levels = new Set<string>()
    for (const m of allAvailableModels) {
      for (const l of modelCaps[m.id]?.thinkingLevels ?? []) {
        if (l !== 'none') levels.add(l)
      }
    }
    return levels.size ? ['auto', ...levels] : null
  })
  // A picked level only applies to a model that actually supports it, so the
  // displayed id and the copied id never carry a level the provider rejects.
  function resolveThinkingSuffix(modelId: string): string | null {
    if (!thinkingLevel || thinkingLevel === 'auto') return null
    const levels = modelCaps[modelId]?.thinkingLevels
    return levels && levels.includes(thinkingLevel) ? thinkingLevel : null
  }
  // Compatible nodes (upstream CompatibleModelsSection): rows = custom models
  // + legacy aliases, both keyed by the node row id; display = node prefix.
  let modelAliases = $state<Record<string, string>>({})
  let newCompatibleModel = $state('')
  let isAddingCompatibleModel = $state(false)
  let isImportingCompatibleModels = $state(false)
  let isImportingLiveCatalogModels = $state(false)
  let compatibleTestId = $state<string | null>(null)
  let compatibleTestResults = $state<Record<string, 'ok' | 'error'>>({})
  let compatibleTestErrors = $state<Record<string, string | null>>({})
  let compatibleRows = $derived.by(() => {
    const rows: Array<{ id: string; source: 'custom' | 'legacyAlias'; alias?: string }> = []
    const seen = new Set<string>()
    for (const cm of customModels) {
      if (!cm.id || cm.providerAlias !== storageAlias) continue
      if ((cm.type || 'llm') !== 'llm') continue
      const full = `${storageAlias}/${cm.id}`
      if (seen.has(full)) continue
      seen.add(full)
      rows.push({ id: cm.id, source: 'custom' })
    }
    const prefix = `${storageAlias}/`
    for (const [alias, full] of Object.entries(modelAliases || {})) {
      if (typeof full !== 'string' || !full.startsWith(prefix)) continue
      const id = full.slice(prefix.length)
      if (!id || seen.has(full)) continue
      seen.add(full)
      rows.push({ id, source: 'legacyAlias', alias })
    }
    // Display-only A–Z sort (case-insensitive) so the list order is stable
    // instead of following the API response object's key order, which varies
    // between requests. rows is created above on every evaluation, so the
    // in-place sort is safe.
    return rows.sort((a, b) => a.id.localeCompare(b.id, undefined, { sensitivity: 'base' }))
  })
  let canImportCompatible = $derived(providerConnections.some((c) => c.isActive !== 0))

  // Settings & Strategies
  let settings = $state<Settings | null>(null)
  let isRoundRobin = $state(false)
  let stickyLimit = $state('1')
  let thinkingLevel = $state('auto')

  // Proxy Pools
  let proxyPools = $state<ProxyPool[]>([])
  let activeProxyPools = $derived(proxyPools.filter((p) => p.isActive))

  // Selection state
  let selectedConnIds = $state<string[]>([])
  let isAllSelected = $derived(
    providerConnections.length > 0 && selectedConnIds.length === providerConnections.length
  )

  // One-by-One Health Check state
  // ONE_BY_ONE_DELAY_MS mirrors upstream: a pause between connections so a
  // provider is not hit with back-to-back probes.
  const ONE_BY_ONE_DELAY_MS = 1000
  let isTestingOneByOne = $state(false)
  let isStoppingOneByOne = $state(false)
  let isStopTesting = $state(false)
  let oneByOneCurrentId = $state<string | null>(null)
  let oneByOneSummary = $state<
    { total: number; completed: number; passed: number; failed: number; stopped: boolean } | null
  >(null)
  // Written by both the sweep and the per-row refresh (PR #196); the shared
  // ProbeStatus keeps those two writers from drifting apart.
  let oneByOneStatuses = $state<Record<string, ProbeStatus>>({})

  // Row UI state
  let activeProxyDropdownId = $state<string | null>(null)
  let dropdownPos = $state<{ top: number; right: number }>({ top: 0, right: 0 })
  let updatingProxyConnId = $state<string | null>(null)
  let copiedModelId = $state<string | null>(null)
  let modelTestStatuses = $state<Record<string, ModelTestStatus>>({})
  let modelTestErrors = $state<Record<string, string | null>>({})
  let activeModelTestError = $state<string | null>(null)
  let checkAllSummary = $state<CheckAllSummary | null>(null)

  // Modals state
  let showRiskNoticeModal = $state(false)
  let showOAuthModal = $state(false)
  let oauthAuthUrl = $state('')
  let copiedAuthUrl = $state(false)
  let callbackInput = $state('')
  let oauthError = $state<string | null>(null)
  let isConnecting = $state(false)
  let clineCodeVerifier = $state('')
  let clineRedirectUri = $state('')
  let pkceCodeVerifier = $state('')
  let pkceState = $state('')
  let pkceRedirectUri = $state('')
  let gitlabBaseUrl = $state('')
  let gitlabClientId = $state('')
  let gitlabClientSecret = $state('')
  let customState = $state('')
  let customVerifier = $state('')
  let customSystemId = $state('')
  let deviceUserCode = $state('')
  let deviceCode = $state('')
  let deviceSession: Record<string, unknown> = $state({})
  let deviceInterval = $state(5)
  let devicePollTimer: ReturnType<typeof setInterval> | null = $state(null)
  // Kiro method selection (upstream KiroAuthModal/KiroOAuthWrapper parity):
  // builder-id | idc | api-key | import | import-cli-proxy | social-google | social-github
  let kiroMethod = $state<string | null>(null)
  let kiroIdcStartUrl = $state('')
  let kiroIdcRegion = $state('us-east-1')
  let kiroApiKey = $state('')
  let kiroApiKeyRegion = $state('us-east-1')
  let kiroRefreshToken = $state('')
  let kiroCliProxyJson = $state('')
  let kiroSocialAuthUrl = $state('')
  let kiroSocialCallback = $state('')
  // OAuth auto-handoff: callback tab writes to storage + BroadcastChannel,
  // this modal restores the pending session and auto-submits.
  let autoSubmitted = $state(false)
  // Codex completes its login on a server-owned loopback listener, so the
  // modal watches the server instead of the browser callback page.
  let codexPollTimer: ReturnType<typeof setInterval> | null = $state(null)

  function dashboardCallback(): string {
    return dashboardCallbackURL(dashboardOrigin())
  }

  function antigravityCallback(): string {
    if (typeof window === 'undefined') return 'http://localhost:8080/callback'
    return oauthLoopbackCallbackURL(window.location.port, window.location.protocol === 'https:')
  }

  function rememberPending(p: { state: string; verifier?: string; redirectUri?: string; extra?: Record<string, string> }) {
    if (typeof window === 'undefined') return
    try {
      savePending(window.localStorage, { provider: providerId, ...p })
    } catch {
      /* storage blocked — degrade to manual paste */
    }
  }

  function consumeOAuthCallback(): boolean {
    if (typeof window === 'undefined' || autoSubmitted || isConnecting || !showOAuthModal) return false
    let cb
    try {
      cb = readCallback(window.localStorage)
    } catch {
      return false
    }
    if (!cb || (!cb.raw && !cb.error)) return false
    if (cb.error) {
      oauthError = `Login failed: ${cb.error}${cb.errorDesc ? ` — ${cb.errorDesc}` : ''}`
      try {
        clearCallback(window.localStorage)
      } catch {
        /* noop */
      }
      return true
    }
    let pendings
    try {
      pendings = loadPendings(window.localStorage)
    } catch {
      return false
    }
    const pending = matchPending(pendings, providerId, cb.state)
    if (!pending || pending.provider !== providerId) return false
    // Restore session values saved at authorize time.
    if (pending.verifier) {
      if (isClineOAuth) clineCodeVerifier = pending.verifier
      else if (isPKCEOAuth) pkceCodeVerifier = pending.verifier
      else customVerifier = pending.verifier
    }
    if (pending.redirectUri) {
      if (isClineOAuth) clineRedirectUri = pending.redirectUri
      else pkceRedirectUri = pending.redirectUri
    }
    const extra = pending.extra || {}
    if (extra.state) {
      if (isCustomOAuth) customState = extra.state
      else pkceState = extra.state
    }
    if (providerId === 'gitlab') {
      if (extra.baseUrl) gitlabBaseUrl = extra.baseUrl
      if (extra.clientId) gitlabClientId = extra.clientId
      if (extra.clientSecret) gitlabClientSecret = extra.clientSecret
    }
    if (extra.systemId) customSystemId = extra.systemId
    callbackInput = cb.raw
    try {
      clearPending(window.localStorage, pending.provider, pending.state)
      clearCallback(window.localStorage)
    } catch {
      /* noop */
    }
    autoSubmitted = true
    void submitManualCallback()
    return true
  }

  // Listen for the callback tab while the OAuth modal is open.
  $effect(() => {
    if (!showOAuthModal || typeof window === 'undefined') return
    autoSubmitted = false
    consumeOAuthCallback()
    const onStorage = (e: StorageEvent) => {
      if (e.key === OAUTH_CALLBACK_KEY || e.key === null) consumeOAuthCallback()
    }
    window.addEventListener('storage', onStorage)
    const onWindowMessage = (e: MessageEvent) => {
      const trustedOrigin = e.origin === window.location.origin || e.origin === new URL(antigravityCallback()).origin
      if (!trustedOrigin) return
      if (e.data?.type === 'oauth_callback' && e.data?.data) {
        const data = e.data.data as {
          code?: string
          token?: string
          state?: string
          error?: string
          errorDescription?: string
        }
        if (data.error) {
          oauthError = `Login failed: ${data.error}${data.errorDescription ? ` — ${data.errorDescription}` : ''}`
          return
        }
        const raw = data.code || data.token || ''
        if (raw) {
          try {
            writeCallback(window.localStorage, { state: data.state || '', raw, error: '', errorDesc: '' })
          } catch {
            /* storage blocked */
          }
          consumeOAuthCallback()
        }
        return
      }
      if (e.data?.type === '9router-oauth-success' && e.data?.provider === 'antigravity') {
        showOAuthModal = false
        onRefresh()
      } else if (e.data?.type === '9router-oauth-error' && e.data?.provider === 'antigravity') {
        oauthError = `Login failed: ${e.data.error || 'Unknown error'}`
      }
    }
    window.addEventListener('message', onWindowMessage)
    let bc: BroadcastChannel | null = null
    try {
      bc = new BroadcastChannel(OAUTH_CHANNEL)
      bc.onmessage = (e) => {
        if (e.data?.provider === 'antigravity' && e.data?.success) {
          showOAuthModal = false
          onRefresh()
          return
        }
        if (e.data?.provider === 'antigravity' && e.data?.error) {
          oauthError = `Login failed: ${e.data.error}`
          return
        }
        consumeOAuthCallback()
      }
    } catch {
      /* BroadcastChannel unavailable — poll covers it */
    }
    const timer = setInterval(consumeOAuthCallback, 1500)
    return () => {
      window.removeEventListener('storage', onStorage)
      window.removeEventListener('message', onWindowMessage)
      try {
        bc?.close()
      } catch {
        /* noop */
      }
      clearInterval(timer)
    }
  })
  let isSpecialOAuth = $derived(
    providerId === 'cursor' || providerId === 'kimchi' || providerId === 'xiaomi-mimo'
  )
  let specialToken = $state('')
  let specialExtra = $state('')
  let specialBaseUrl = $state('')

  let showApplyProxyModal = $state(false)
  let isApplyingProxy = $state(false)
  // Non-null while a reorder request is in flight; the chevrons disable on it
  // so a second click cannot fire a swap computed from a stale list.
  let reorderingConnId = $state<string | null>(null)
  // Bulk-apply progress + abort flag for the Apply Proxy modal.
  let applyProxyDone = $state(0)
  let applyProxyAbort = $state(false)
  let proxyPoolSearch = $state('')

  let filteredProxyPools = $derived.by(() => {
    const q = proxyPoolSearch.trim().toLowerCase()
    if (!q) return proxyPools
    return proxyPools.filter(
      (p) => p.name.toLowerCase().includes(q) || p.proxyUrl.toLowerCase().includes(q)
    )
  })

  // The connection whose Edit Connection modal is open; null when closed. The
  // modal owns its own fields and payload (issue #158).
  let editingConnection = $state<ProviderConnection | null>(null)

  let showAddKeyModal = $state(false)
  let addConnectionError = $state('')
  let showAddCustomModelModal = $state(false)
  let showEditNodeModal = $state(false)
  let showHeaderOverrides = $state(false)
  // Freebuff specific state & session tracking
  let isFreebuff = $derived(
    providerId === 'freebuff' || storageAlias === 'fb' || storageAlias === 'freebuff'
  )
  // The session API has to be asked about an account that can actually serve.
  // Connections are ordered by priority, and a disabled one can sort first —
  // its rejected credential reports `banned`/`unauthorized` and blanks the
  // panel while the live seat sits on the next account.
  let selectedFreebuffConnId = $state<string>('')
  let freebuffSessions = $state<Record<string, FreebuffSessionStatusResponse>>({})
  let targetFreebuffConn = $derived.by(() => {
    if (selectedFreebuffConnId) {
      const found = providerConnections.find((c) => c.id === selectedFreebuffConnId)
      if (found) return found
    }
    return providerConnections.find((c) => c.isActive === 1) ?? providerConnections[0]
  })
  let freebuffConnection = $derived(targetFreebuffConn)
  let freebuffSession = $state<FreebuffSessionStatusResponse | null>(null)
  let isLoadingSession = $state(false)
  let isAuthorizingFreebuff = $state(false)
  let freebuffPollTimer = $state<ReturnType<typeof setInterval> | null>(null)
  let currentFreebuffInit = $state<{
    fingerprintId: string
    fingerprintHash: string
    expiresAt: number
    loginUrl: string
  } | null>(null)

  async function loadFreebuffSession() {
    if (!isFreebuff || providerConnections.length === 0) {
      freebuffSession = null
      return
    }
    const target = targetFreebuffConn
    if (!target) {
      freebuffSession = null
      return
    }
    isLoadingSession = true
    try {
      const res = await api.getFreebuffSessionStatus(target.id)
      freebuffSession = res
      if (res && target.id) {
        freebuffSessions = { ...freebuffSessions, [target.id]: res }
      }
      for (const c of providerConnections) {
        if (c.id !== target.id) {
          api.getFreebuffSessionStatus(c.id).then((st) => {
            if (st) {
              freebuffSessions = { ...freebuffSessions, [c.id]: st }
            }
          }).catch(() => {})
        }
      }
    } catch (err) {
      console.error('Failed to fetch Freebuff session status:', err)
      freebuffSession = null
    } finally {
      isLoadingSession = false
    }
  }

  function selectFreebuffAccount(connId: string) {
    selectedFreebuffConnId = connId
    loadFreebuffSession()
  }

  let sessionExpiresInMin = $derived.by(() => {
    if (!freebuffSession?.expiresAt) return null
    const exp = new Date(freebuffSession.expiresAt).getTime()
    const diffMs = exp - Date.now()
    return Math.max(0, Math.round(diffMs / 60000))
  })

  function checkIsActiveSession(modelId: string): boolean {
    if (!isFreebuff || freebuffSession?.status !== 'active' || !freebuffSession?.currentModel) {
      return false
    }
    const cur = freebuffSession.currentModel.toLowerCase().trim()
    const mid = modelId.toLowerCase().trim()
    return mid === cur || mid.endsWith('/' + cur) || cur.endsWith('/' + mid)
  }

  // Ends the active Freebuff session and re-admits it on the chosen model.
  // Freebuff serves one model per session and a session lives an hour even when
  // idle, so this is the only way to change models without waiting it out —
  // and it must stay an explicit user action (each switch spends a session).
  async function switchFreebuffModel(model: string) {
    const target = targetFreebuffConn
    const res = await api.switchFreebuffSession(model, target?.id)
    await loadFreebuffSession()
    onRefresh()
    return res
  }

  async function startFreebuffFlow() {
    try {
      isAuthorizingFreebuff = true
      oauthError = null
      callbackInput = ''
      copiedAuthUrl = false
      const init = await api.initiateFreebuff()
      currentFreebuffInit = {
        fingerprintId: init.fingerprintId,
        fingerprintHash: init.fingerprintHash,
        expiresAt: init.expiresAt,
        loginUrl: init.loginUrl
      }
      oauthAuthUrl = init.loginUrl
      showOAuthModal = true

      if (typeof window !== 'undefined' && init.loginUrl) {
        window.open(init.loginUrl, '_blank')
      }

      if (freebuffPollTimer) clearInterval(freebuffPollTimer)
      freebuffPollTimer = setInterval(async () => {
        try {
          if (!showOAuthModal) {
            if (freebuffPollTimer) {
              clearInterval(freebuffPollTimer)
              freebuffPollTimer = null
            }
            isAuthorizingFreebuff = false
            return
          }
          const res = await api.pollFreebuff(init.fingerprintId, init.fingerprintHash, init.expiresAt)
          if (res?.status === 'authorized') {
            if (freebuffPollTimer) {
              clearInterval(freebuffPollTimer)
              freebuffPollTimer = null
            }
            isAuthorizingFreebuff = false
            showOAuthModal = false
            onRefresh()
            loadFreebuffSession()
          }
        } catch {}
      }, 2500)
    } catch (err) {
      isAuthorizingFreebuff = false
      alert(`Failed to start Freebuff flow: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Load models, settings, proxy pools
  async function loadData() {
    try {
      const [modelsData, settingsData, poolsData, aliasesData, capsData] = await Promise.all([
        fetchProviderModelsData(providerId, storageAlias),
        api.getSettings().catch(() => ({})),
        api.getProxyPools().catch(() => []),
        api.getModelAliases().catch(() => ({ aliases: {} })),
        // Providers without a static catalog or compatible nodes do not have static caps.
        !isCompatibleNode && (PROVIDER_CATALOG_MAP.has(providerId) || PROVIDER_CATALOG_MAP.has(storageAlias))
          ? api.getModelCaps(providerId).catch(() => ({ caps: {} as Record<string, ModelCaps> }))
          : Promise.resolve({ caps: {} as Record<string, ModelCaps> }),
      ])
      customModels = modelsData.customModels
      disabledModelIds = modelsData.disabledModelIds
      modelAliases = aliasesData?.aliases || {}
      settings = settingsData
      proxyPools = poolsData
      modelCaps = capsData?.caps || {}

      // Suggested free models from the provider's public catalog (if configured in upstream registry).
      const fetcher = selectedCatalogItem?.modelsFetcher
      if (fetcher?.url && fetcher?.type) {
        fetchSuggestedModels(fetcher).then((list) => {
          suggestedModels = list
        })
      } else {
        suggestedModels = []
      }

      // Extract Round Robin strategy
      const strategy = settingsData?.providerStrategies?.[providerId]
      isRoundRobin = strategy?.fallbackStrategy === 'round-robin'
      stickyLimit = String(strategy?.stickyRoundRobinLimit ?? 1)

      // Extract Thinking Level
      const thinking = (settingsData as any)?.providerThinking?.[providerId]
      thinkingLevel = thinking?.mode || 'auto'
      // Extract free provider proxy & rotation settings
      if (strategy && !isSavingFreeProxy) {
        freeProxyPoolId = strategy.proxyPoolId || 'none'
        freeRotateStrategy = (strategy.rotateStrategy as 'none' | 'round-robin' | 'random') || 'none'
      }
      // Fetch quota info for each connection
      for (const c of providerConnections) {
        api.getConnectionUsage(c.id).then((usage) => {
          if (usage && !usage.error) {
            connectionQuotas[c.id] = usage
          }
        }).catch(() => {})
      }
      if (isFreebuff) {
        loadFreebuffSession()
      }
    } catch (err) {
      console.error('Failed to load provider details:', err)
    }
  }

  async function handleFreeProxyChange(newPool: string, newRotate: 'none' | 'round-robin' | 'random') {
    freeProxyPoolId = newPool
    freeRotateStrategy = newRotate
    isSavingFreeProxy = true
    try {
      const currentStrategies = { ...(settings?.providerStrategies || {}) }
      const currentStrat = { ...(currentStrategies[providerId] || {}) }
      if (newPool === 'none' || !newPool) {
        delete currentStrat.proxyPoolId
      } else {
        currentStrat.proxyPoolId = newPool
      }
      if (newRotate === 'none' || !newRotate) {
        delete currentStrat.rotateStrategy
      } else {
        currentStrat.rotateStrategy = newRotate
      }
      if (Object.keys(currentStrat).length === 0) {
        delete currentStrategies[providerId]
      } else {
        currentStrategies[providerId] = currentStrat
      }
      await api.updateSettings({ providerStrategies: currentStrategies })
      if (settings) settings.providerStrategies = currentStrategies
      savedFreeProxy = true
      setTimeout(() => (savedFreeProxy = false), 1500)
    } catch (err) {
      console.error('Failed to save proxy config:', err)
    } finally {
      isSavingFreeProxy = false
    }
  }
  function getCooldownInfo(conn: ProviderConnection): { label: string; title: string; isExhausted: boolean; isLock?: boolean } | null {
    // 1. Check modelLock_* and rateLimitedUntil across conn, conn.data, and providerSpecificData (matching upstream ⏱ {timeLeft})
    const dataObj = conn.providerSpecificData as Record<string, unknown> | undefined
    const rawData = (conn as unknown as { data?: Record<string, unknown> }).data
    const allProps = {
      ...(typeof rawData === 'object' && rawData ? rawData : {}),
      ...(typeof dataObj === 'object' && dataObj ? dataObj : {}),
      ...conn
    }
    const locks = Object.entries(allProps).filter(
      ([k, v]) => (k.startsWith('modelLock_') || k === 'rateLimitedUntil') && v && new Date(v as string).getTime() > currentTime
    )
    if (locks.length > 0) {
      let maxLock = 0
      for (const [_, v] of locks) {
        const t = new Date(v as string).getTime()
        if (t > maxLock) maxLock = t
      }
      const diff = Math.max(0, Math.floor((maxLock - currentTime) / 1000))
      const timeLeft =
        diff < 60
          ? `${diff}s`
          : diff < 3600
            ? `${Math.floor(diff / 60)}m ${diff % 60}s`
            : `${Math.floor(diff / 3600)}h ${Math.floor((diff % 3600) / 60)}m`
      return {
        label: `⏱ ${timeLeft}`,
        title: `Model rate limit lock active until ${new Date(maxLock).toLocaleTimeString()}`,
        isExhausted: false,
        isLock: true
      }
    }

    // 2. Check live quota data for exhausted models (remaining <= 0) and resetAt.
    //    A pool with total <= 0 is not an exhausted pool — it means the account
    //    has no allocation at all (Qoder credits report total 0, and its
    //    "expiresAt" is a 9999-12-31 sentinel), so counting it produced
    //    "Quota Exhausted (0%): Resets in 2912172d 1h" on a perfectly healthy
    //    connection. Only a pool that was actually granted something can run
    //    out of it.
    const usage = connectionQuotas[conn.id]
    if (usage?.quotas) {
      const quotaEntries = Object.entries(usage.quotas)
      const exhausted = quotaEntries.filter(([_, q]) => {
        const total = Number(q.total)
        if (!Number.isFinite(total) || total <= 0) return false
        return (
          (q.remainingPercentage !== undefined && Number(q.remainingPercentage) <= 0) ||
          (q.remaining !== undefined && Number(q.remaining) <= 0)
        )
      })
      if (exhausted.length > 0) {
        let earliestReset = 0
        let resetModel = ''
        for (const [m, q] of exhausted) {
          if (q.resetAt) {
            const t = new Date(q.resetAt).getTime()
            if (t > currentTime && (earliestReset === 0 || t < earliestReset)) {
              earliestReset = t
              resetModel = q.displayName || m
            }
          }
        }
        if (earliestReset > currentTime) {
          const diff = earliestReset - currentTime
          const hours = Math.floor(diff / (1000 * 60 * 60))
          const mins = Math.floor((diff % (1000 * 60 * 60)) / (1000 * 60))
          const secs = Math.floor((diff % (1000 * 60)) / 1000)
          const timeStr =
            hours >= 24
              ? `${Math.floor(hours / 24)}d ${hours % 24}h`
              : hours > 0
                ? `${hours}h ${mins}m`
                : `${mins}m ${secs}s`
          return {
            label: `Quota Exhausted (0%): Resets in ${timeStr}`,
            title: `Quota exhausted for ${resetModel}. Reset at ${new Date(earliestReset).toLocaleString()}`,
            isExhausted: true
          }
        }
      }
    }
    // 4. Check errorCode 429 or lastError indicating rate limit / quota
    const errCode = (conn as unknown as { errorCode?: number }).errorCode
    const lastErr = normalizeLastError(conn.lastError) || ''
    if (
      errCode === 429 ||
      lastErr.includes('429') ||
      lastErr.toLowerCase().includes('quota') ||
      lastErr.toLowerCase().includes('exhausted')
    ) {
      const match = lastErr.match(/Resets in ([^.]+)/i)
      if (match) {
        return {
          label: `Quota Exhausted (429): Resets in ${match[1].trim()}`,
          title: lastErr,
          isExhausted: true
        }
      }
      return {
        label: 'Quota Exhausted (429)',
        title: lastErr || 'HTTP 429 Rate Limit / Quota Exhausted',
        isExhausted: true
      }
    }

    return null
  }

  let lastLoadedProviderId = ''
  $effect(() => {
    const pid = providerId
    if (pid) {
      untrack(() => {
        if (lastLoadedProviderId !== pid) {
          lastLoadedProviderId = pid
          suggestedModels = []
        }
        loadData()
      })
    }
  })

  // Strategy saves
  async function saveProviderStrategy(fallback: 'round-robin' | null, sticky: string) {
    try {
      const current = settings?.providerStrategies || {}
      const updated = { ...current }
      if (fallback === 'round-robin') {
        // Upstream saves `Number(sticky) || 3`; the entry is merged (not
        // replaced) so the proxy/rotate keys saved by the free-provider card
        // survive a round-robin toggle.
        updated[providerId] = {
          ...(updated[providerId] || {}),
          fallbackStrategy: 'round-robin',
          stickyRoundRobinLimit: Number(sticky) || 3
        }
      } else {
        delete updated[providerId]
      }
      if (settings) settings.providerStrategies = updated
      await api.updateSettings({ providerStrategies: updated })
    } catch (err) {
      console.error('Error saving provider strategy:', err)
    }
  }

  async function toggleRoundRobin() {
    isRoundRobin = !isRoundRobin
    if (isRoundRobin && !stickyLimit) {
      stickyLimit = '1'
    }
    await saveProviderStrategy(isRoundRobin ? 'round-robin' : null, stickyLimit)
  }

  async function handleStickyLimitChange() {
    if (isRoundRobin) {
      await saveProviderStrategy('round-robin', stickyLimit)
    }
  }

  async function handleThinkingChange(event: Event) {
    const val = (event.target as HTMLSelectElement).value
    thinkingLevel = val
    try {
      const current = (settings as any)?.providerThinking || {}
      const updated = { ...current }
      if (val && val !== 'auto') {
        updated[providerId] = { mode: val }
      } else {
        delete updated[providerId]
      }
      if (settings) (settings as any).providerThinking = updated
      await api.updateSettings({ providerThinking: updated })
    } catch (err) {
      console.error('Error saving provider thinking:', err)
    }
  }

  // Selection handlers
  function toggleSelectAll() {
    if (isAllSelected) {
      selectedConnIds = []
    } else {
      selectedConnIds = providerConnections.map((c) => c.id)
    }
  }

  function toggleSelect(id: string) {
    if (selectedConnIds.includes(id)) {
      selectedConnIds = selectedConnIds.filter((x) => x !== id)
    } else {
      selectedConnIds = [...selectedConnIds, id]
    }
  }

  async function handleDeleteSelected() {
    if (selectedConnIds.length === 0) return
    if (!confirm(`Delete ${selectedConnIds.length} selected connection(s)? This cannot be undone.`)) return
    let failed = 0
    for (const id of selectedConnIds) {
      try {
        await api.deleteConnection(id)
      } catch (e) {
        console.error('Error deleting connection:', e)
        failed++
      }
    }
    selectedConnIds = []
    onRefresh()
    if (failed > 0) {
      alert(`Deleted with ${failed} failed request(s).`)
    }
  }

  // One-by-One Health Test (upstream handleRunOneByOneTest): sequential probes
  // with a pause between them, a live summary, and a cooperative stop.
  async function runOneByOneTest() {
    if (isTestingOneByOne || providerConnections.length === 0) return

    const queued: Record<string, { state: 'queued' | 'testing' | 'success' | 'failed'; error?: string | null }> = {}
    for (const c of providerConnections) {
      queued[c.id] = { state: 'queued', error: null }
    }

    isStopTesting = false
    isStoppingOneByOne = false
    oneByOneCurrentId = null
    oneByOneStatuses = queued
    oneByOneSummary = {
      total: providerConnections.length,
      completed: 0,
      passed: 0,
      failed: 0,
      stopped: false
    }
    isTestingOneByOne = true

    let passed = 0
    let failed = 0

    try {
      for (let index = 0; index < providerConnections.length; index += 1) {
        if (isStopTesting) {
          oneByOneSummary = {
            total: providerConnections.length,
            completed: index,
            passed,
            failed,
            stopped: true
          }
          break
        }

        const conn = providerConnections[index]
        oneByOneCurrentId = conn.id
        oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: testingStatus() }

        try {
          const res = await api.testConnection(conn.id)
          if (res?.valid) {
            passed += 1
          } else {
            failed += 1
          }
          oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: probeOutcomeFrom(res) }
        } catch (err) {
          failed += 1
          oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: probeOutcomeFromError(err) }
        }

        oneByOneSummary = {
          total: providerConnections.length,
          completed: index + 1,
          passed,
          failed,
          stopped: false
        }

        if (index < providerConnections.length - 1) {
          await new Promise((r) => setTimeout(r, ONE_BY_ONE_DELAY_MS))
        }
      }
    } finally {
      oneByOneCurrentId = null
      isTestingOneByOne = false
      isStoppingOneByOne = false
      isStopTesting = false
      onRefresh()
    }
  }

  function stopOneByOneTest() {
    if (!isTestingOneByOne) return
    isStopTesting = true
    isStoppingOneByOne = true
  }

  // Per-row refresh: probe one account straight from its row instead of
  // opening the edit modal. Reuses the one-by-one badge state so the row
  // shows the live testing/success/failed indicator for free.
  async function refreshOneConnection(conn: ProviderConnection) {
    if (!canProbeRow(isTestingOneByOne, oneByOneStatuses[conn.id])) return
    oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: testingStatus() }
    try {
      const res = await api.testConnection(conn.id)
      oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: probeOutcomeFrom(res) }
    } catch (err) {
      oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: probeOutcomeFromError(err) }
    } finally {
      onRefresh()
    }
  }

  // Priority reordering.
  // Single server-side transactional call instead of two independent PUTs:
  // a partial failure between those two writes left two rows sharing a
  // priority, and a stable sort over tied priorities made every later click a
  // literal no-op — the pair became permanently un-reorderable through the UI.
  // The in-flight flag also stops a second click from firing a swap computed
  // from the same stale list (last-write-wins, so the list appeared frozen).
  async function swapPriority(idx: number, delta: -1 | 1) {
    const conn = providerConnections[idx]
    if (!conn || reorderingConnId) return
    const target = providerConnections[idx + delta]
    if (!target) return

    reorderingConnId = conn.id
    try {
      await api.reorderConnection(conn.id, delta < 0 ? 'up' : 'down')
      onRefresh()
    } catch (err) {
      alert(`Reorder failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      reorderingConnId = null
    }
  }

  // Single connection toggling & deleting
  async function toggleConnectionActive(conn: ProviderConnection) {
    try {
      await api.updateConnection(conn.id, { isActive: conn.isActive === 1 ? 0 : 1 })
      onRefresh()
    } catch (err) {
      alert(`Toggle failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleDeleteConnection(conn: ProviderConnection) {
    if (!confirm(`Delete connection "${formatEmailLabel(conn.name || conn.id, $emailPrivacy)}"? This cannot be undone.`)) return
    try {
      await api.deleteConnection(conn.id)
      onRefresh()
    } catch (err) {
      alert(`Delete failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Per-row proxy badge (upstream ConnectionRow) — see proxyBadge.ts.
  function proxyBadgeFor(conn: ProviderConnection) {
    return proxyBadgeInfo(conn, proxyPools)
  }

  // Proxy assignment
  // The payload carries the binding in both places: upstream stores it under
  // providerSpecificData.proxyPoolId, while this backend's proxy resolver reads
  // the top-level field.
  function proxyAssignmentPayload(poolId: string | null) {
    return {
      proxyPoolId: poolId,
      providerSpecificData: { proxyPoolId: poolId }
    }
  }

  async function assignProxyPool(conn: ProviderConnection, poolId: string | null) {
    activeProxyDropdownId = null
    updatingProxyConnId = conn.id
    try {
      await api.updateConnection(conn.id, proxyAssignmentPayload(poolId))
      onRefresh()
    } catch (err) {
      alert(`Failed to update proxy: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      updatingProxyConnId = null
    }
  }

  // Upstream applies bulk proxy changes to every connection of the provider,
  // regardless of the checkbox selection.
  //
  // Cancel sets the abort flag and the loop stops on the next iteration, so
  // closing the modal no longer leaves requests firing in the background. The
  // running counter is shown in the modal: with 100+ connections a sequential
  // loop is slow enough that the previous label-less "Applying..." read as a
  // hang.
  async function applyProxyAssignments(assignments: Array<{ id: string; poolId: string | null }>) {
    isApplyingProxy = true
    applyProxyAbort = false
    applyProxyDone = 0
    let failed = 0
    try {
      for (const assignment of assignments) {
        if (applyProxyAbort) break
        try {
          await api.updateConnection(assignment.id, proxyAssignmentPayload(assignment.poolId))
        } catch {
          failed++
        }
        applyProxyDone++
      }
      onRefresh()
      showApplyProxyModal = false
      if (applyProxyAbort) {
        alert(`Cancelled after ${applyProxyDone} of ${assignments.length} connection(s).`)
      } else if (failed > 0) {
        alert(`Updated with ${failed} failed request(s).`)
      }
    } finally {
      isApplyingProxy = false
      applyProxyAbort = false
    }
  }

  function cancelApplyProxy() {
    if (isApplyingProxy) {
      applyProxyAbort = true
      return
    }
    showApplyProxyModal = false
  }

  async function handleApplyProxyPool(poolId: string | null) {
    await applyProxyAssignments(
      providerConnections.map((c) => ({ id: c.id, poolId }))
    )
  }

  async function handleApplyProxyRotate() {
    if (activeProxyPools.length === 0) {
      alert('No active proxy pools available.')
      return
    }
    // One-to-one (rotate): connection i gets active pool i, wrapping around.
    await applyProxyAssignments(
      providerConnections.map((c, i) => ({
        id: c.id,
        poolId: activeProxyPools[i % activeProxyPools.length].id
      }))
    )
  }

  // Edit connection modal. Every field, the probe and the payload live in
  // EditConnectionModal; this only decides when it opens and who refreshes.
  function openEditConnection(conn: ProviderConnection) {
    editingConnection = conn
  }

  async function saveEditingConnection(payload: ConnectionUpdate) {
    if (!editingConnection) return
    await api.updateConnection(editingConnection.id, payload)
    editingConnection = null
    onRefresh()
  }

  // Add Connection Button flow
  async function handleAddConnectionClick() {
    if (providerId === 'antigravity') {
      const confirmed = typeof window !== 'undefined' && localStorage.getItem('ag_risk_confirmed') === 'true'
      if (!confirmed) {
        showRiskNoticeModal = true
      } else {
        openAntigravityOAuth()
      }
    } else if (providerId === 'freebuff') {
      startFreebuffFlow()
    } else if (isClineOAuth) {
      openClineOAuth()
    } else if (isPKCEOAuth) {
      openPKCEOAuth()
    } else if (isAuthCodeOAuth) {
      openAuthCodeOAuth()
    } else if (isCustomOAuth) {
      openCustomOAuth()
    } else if (providerId === 'kiro') {
      openKiroOAuth()
    } else if (isDeviceOAuth) {
      // Device-code providers (qoder, grok-cli, github, kilocode, kimi,
      // codebuddy-*) have no authorize endpoint to redirect to: the backend
      // mints a PKCE/nonce pair locally and hands back a verification URL to
      // open, then polls for the token. Without this branch they fell through
      // to openGenericOAuth and the user was asked to paste a token by hand
      // instead of getting the login page — isDeviceOAuth was computed but
      // never read, and openDeviceOAuth was never called.
      openDeviceOAuth()
    } else if (isSpecialOAuth) {
      openSpecialOAuth()
    } else if (isOAuth) {
      openGenericOAuth()
    } else {
      showAddKeyModal = true
    }
  }

  function confirmRiskAndProceed() {
    if (typeof window !== 'undefined') {
      localStorage.setItem('ag_risk_confirmed', 'true')
    }
    showRiskNoticeModal = false
    openAntigravityOAuth()
  }

  async function openAntigravityOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    try {
      const redirectUri = antigravityCallback()
      const res = await api.getAntigravityAuthorizeUrl(redirectUri)
      oauthAuthUrl = res.authUrl || res.url || res.redirectUrl
      if (res.state) {
        rememberPending({
          state: res.state,
          redirectUri: res.redirectUri || redirectUri,
        })
      }
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function openPKCEOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    try {
      // Codex cannot use the dashboard callback: OpenAI only accepts the
      // redirect URI registered for the Codex CLI client.
      const cb = providerId === 'codex' ? CODEX_REDIRECT_URI : dashboardCallback()
      const res = await api.pkceAuthorize(
        providerId,
        providerId === 'gitlab'
          ? {
              redirectUri: cb,
              baseUrl: gitlabBaseUrl.trim() || undefined,
              clientId: gitlabClientId.trim() || undefined,
            }
          : { redirectUri: cb }
      )
      oauthAuthUrl = res.url || res.authUrl
      pkceCodeVerifier = res.codeVerifier || ''
      pkceState = res.state || ''
      pkceRedirectUri = res.redirectUri || cb
      if (providerId === 'codex') {
        await startCodexLoopback()
      }
      rememberPending({
        state: pkceState,
        verifier: pkceCodeVerifier,
        redirectUri: pkceRedirectUri,
        extra: {
          state: pkceState,
          baseUrl: gitlabBaseUrl.trim(),
          clientId: gitlabClientId.trim(),
          clientSecret: gitlabClientSecret.trim(),
        },
      })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Codex redirects the browser to a fixed loopback port, so the server needs
  // that listener running before the popup opens — otherwise the callback dies
  // on a closed port and the login can never complete.
  async function startCodexLoopback() {
    stopCodexPoll()
    const proxy = await api.codexStartProxy({
      appPort: window.location.port || (window.location.protocol === 'https:' ? '443' : '80'),
      state: pkceState,
      codeVerifier: pkceCodeVerifier,
      redirectUri: CODEX_REDIRECT_URI,
    })
    if (!proxy.success) {
      throw new Error(
        proxy.reason === 'port_busy'
          ? 'Port 1455 is in use; close the conflicting process and retry'
          : 'Could not start the local login listener',
      )
    }
    codexPollTimer = setInterval(pollCodexStatus, 1500)
  }

  async function pollCodexStatus() {
    try {
      const res = await api.codexPollStatus(pkceState)
      if (res.status === 'done') {
        stopCodexPoll()
        showOAuthModal = false
        onRefresh()
      } else if (res.status === 'error') {
        stopCodexPoll()
        oauthError = res.error || 'Authorization failed'
      } else if (res.status === 'unknown') {
        // The server no longer tracks this login. Stop polling, but say so:
        // silently freezing the modal on "Waiting for popup authorization…"
        // leaves the user with a login that can never finish and no way to
        // tell that apart from simply being slow.
        stopCodexPoll()
        oauthError = 'The local login listener is no longer running. Close this window and click Login again.'
      }
    } catch {
      // Biarkan polling berikutnya mencoba lagi.
    }
  }

  function stopCodexPoll() {
    if (codexPollTimer) {
      clearInterval(codexPollTimer)
      codexPollTimer = null
    }
  }

  // Dismissing the modal abandons the login, so any loopback listener and
  // poll loop it started have to be released with it.
  function closeOAuthModal() {
    showOAuthModal = false
    stopDevicePoll()
    stopCodexPoll()
    if (providerId === 'codex') {
      void api.codexStopProxy().catch(() => {})
    }
  }

  async function openAuthCodeOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    try {
      const res = await api.authcodeAuthorize(providerId, dashboardCallback())
      oauthAuthUrl = res.url || res.authUrl
      pkceState = res.state || ''
      pkceRedirectUri = res.redirectUri || ''
      pkceCodeVerifier = ''
      rememberPending({ state: pkceState, redirectUri: pkceRedirectUri, extra: { state: pkceState } })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function openCustomOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    try {
      const res = await api.customAuthorize(providerId as 'trae' | 'windsurf' | 'zed', dashboardCallback())
      oauthAuthUrl = res.url || res.authUrl
      customState = res.state || res.loginTraceId || ''
      customVerifier = res.codeVerifier || ''
      customSystemId = res.systemId || ''
      pkceRedirectUri = res.redirectUri || ''
      rememberPending({
        state: customState,
        verifier: customVerifier,
        redirectUri: pkceRedirectUri,
        extra: { state: customState, systemId: customSystemId },
      })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
  function resetKiroMethod() {
    kiroMethod = null
    kiroIdcStartUrl = ''
    kiroIdcRegion = 'us-east-1'
    kiroApiKey = ''
    kiroApiKeyRegion = 'us-east-1'
    kiroRefreshToken = ''
    kiroCliProxyJson = ''
    kiroSocialAuthUrl = ''
    kiroSocialCallback = ''
  }

  async function openKiroOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    stopDevicePoll()
    resetKiroMethod()
    showOAuthModal = true
  }

  async function startKiroDeviceFlow(authMethod: 'builder-id' | 'idc') {
    oauthError = null
    copiedAuthUrl = false
    stopDevicePoll()
    isConnecting = true
    try {
      const res = await api.deviceStart(
        'kiro',
        authMethod === 'idc'
          ? { startUrl: kiroIdcStartUrl.trim(), region: kiroIdcRegion.trim() || 'us-east-1', authMethod: 'idc' }
          : { authMethod: 'builder-id' }
      )
      oauthAuthUrl = res.verification_uri_complete || res.verification_uri || ''
      deviceUserCode = res.user_code || ''
      deviceCode = res.device_code || ''
      deviceSession = res.session || {}
      deviceInterval = res.interval && res.interval > 0 ? res.interval : 5
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank')
      }
      pollDeviceOnce()
      devicePollTimer = setInterval(pollDeviceOnce, deviceInterval * 1000)
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function submitKiroApiKey() {
    if (!kiroApiKey.trim()) {
      oauthError = 'Enter your Kiro/CodeWhisperer API key'
      return
    }
    oauthError = null
    isConnecting = true
    try {
      await api.kiroApiKey(kiroApiKey.trim(), kiroApiKeyRegion.trim() || 'us-east-1')
      showOAuthModal = false
      resetKiroMethod()
      onRefresh()
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function submitKiroImport() {
    if (!kiroRefreshToken.trim()) {
      oauthError = 'Enter the refresh token from Kiro IDE'
      return
    }
    oauthError = null
    isConnecting = true
    try {
      await api.kiroImport(kiroRefreshToken.trim())
      showOAuthModal = false
      resetKiroMethod()
      onRefresh()
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function submitKiroCliProxyImport() {
    if (!kiroCliProxyJson.trim()) {
      oauthError = 'Paste CLIProxyAPI auth JSON'
      return
    }
    oauthError = null
    isConnecting = true
    try {
      await api.kiroImportCliProxy(kiroCliProxyJson.trim())
      showOAuthModal = false
      resetKiroMethod()
      onRefresh()
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function startKiroAutoImport() {
    oauthError = null
    isConnecting = true
    try {
      const res = await api.kiroAutoImport()
      if (res?.found && res?.refreshToken) {
        kiroRefreshToken = res.refreshToken as string
      } else {
        oauthError = (res as { error?: string })?.error || 'No token detected on this host'
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function startKiroSocial(provider: 'google' | 'github') {
    oauthError = null
    kiroSocialAuthUrl = ''
    kiroSocialCallback = ''
    isConnecting = true
    try {
      const res = await api.kiroSocialAuthorize(provider)
      kiroSocialAuthUrl = res.authUrl || res.url || ''
      if (typeof window !== 'undefined' && kiroSocialAuthUrl) {
        window.open(kiroSocialAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function submitKiroSocialCallback() {
    const raw = kiroSocialCallback.trim()
    if (!raw) {
      oauthError = 'Paste the callback URL / code from the browser'
      return
    }
    // Accept a full kiro:// callback URL or a bare code (upstream
    // KiroSocialOAuthModal parity: code + state query params).
    let code = raw
    try {
      const url = new URL(raw)
      const errParam = url.searchParams.get('error')
      if (errParam) {
        oauthError = url.searchParams.get('error_description') || errParam
        return
      }
      code = url.searchParams.get('code') || raw
    } catch {
      // Not a URL — treat the input as a bare code.
    }
    if (!code) {
      oauthError = 'No authorization code found in the callback URL'
      return
    }
    oauthError = null
    isConnecting = true
    try {
      await api.kiroSocialExchange(code)
      showOAuthModal = false
      resetKiroMethod()
      onRefresh()
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }


  async function openDeviceOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    stopDevicePoll()
    try {
      const res = await api.deviceStart(providerId)
      oauthAuthUrl = res.verification_uri_complete || res.verification_uri || ''
      deviceUserCode = res.user_code || ''
      deviceCode = res.device_code || ''
      deviceSession = res.session || {}
      deviceInterval = res.interval && res.interval > 0 ? res.interval : 5
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank')
      }
      pollDeviceOnce()
      devicePollTimer = setInterval(pollDeviceOnce, deviceInterval * 1000)
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
      showOAuthModal = true
    }
  }

  async function pollDeviceOnce() {
    if (!showOAuthModal || !deviceCode) return
    try {
      const res = await api.devicePoll(providerId, deviceCode, deviceSession)
      if (res?.status === 'authorized') {
        stopDevicePoll()
        showOAuthModal = false
        onRefresh()
      } else if (res?.status === 'error') {
        stopDevicePoll()
        oauthError = res?.error || 'Authorization failed'
      }
    } catch {
      // Biarkan polling berikutnya mencoba lagi.
    }
  }

  function stopDevicePoll() {
    if (devicePollTimer) {
      clearInterval(devicePollTimer)
      devicePollTimer = null
    }
    deviceUserCode = ''
    deviceCode = ''
  }

  function openSpecialOAuth() {
    // cursor (import/auto-import), kimchi (browser token), xiaomi-mimo (ECDH).
    // gitlab PAT & iflow cookie reuse the generic modal + special submit.
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    specialToken = ''
    specialExtra = ''
    specialBaseUrl = ''
    customVerifier = ''
    oauthAuthUrl = ''
    if (providerId === 'kimchi' || providerId === 'xiaomi-mimo') {
      openSpecialAuthorize()
      return
    }
    showOAuthModal = true
  }

  async function openSpecialAuthorize() {
    try {
      const cb = dashboardCallback()
      const res = providerId === 'kimchi'
        ? await api.kimchiAuthorize(cb)
        : await api.mimoAuthorize(cb)
      oauthAuthUrl = res.url || res.authUrl
      customVerifier = (res as { codeVerifier?: string }).codeVerifier || ''
      rememberPending({
        state: (res as { state?: string }).state || '',
        verifier: customVerifier || undefined,
      })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank')
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
      showOAuthModal = true
    }
  }

  async function cursorAutoImportNow() {
    oauthError = null
    isConnecting = true
    try {
      const res = await api.cursorAutoImport()
      if (!res || (res as { error?: string })?.error || !(res as { success?: boolean })?.success) {
        oauthError = (res as { error?: string })?.error || 'Auto-import failed. Make sure Cursor IDE is installed and signed in on this host.'
      } else {
        showOAuthModal = false
        onRefresh()
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  function dashboardOrigin(): string {
    if (typeof window !== 'undefined' && window.location?.origin) return window.location.origin
    return 'http://localhost:20130'
  }

  function openGenericOAuth() {
    // No generic authorize endpoint: never redirect to Google/Antigravity.
    // Show the modal with manual-token import instructions instead.
    oauthError = null
    oauthAuthUrl = ''
    callbackInput = ''
    copiedAuthUrl = false
    clineCodeVerifier = ''
    clineRedirectUri = ''
    showOAuthModal = true
  }

  async function openClineOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    try {
      const res = await api.getClineAuthorizeUrl(providerId, dashboardCallback())
      oauthAuthUrl = res.url || res.authUrl
      clineCodeVerifier = res.codeVerifier || ''
      clineRedirectUri = res.redirectUri || ''
      rememberPending({ state: res.state || '', verifier: clineCodeVerifier, redirectUri: clineRedirectUri })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  function copyAuthUrl() {
    if (!oauthAuthUrl) return
    copyToClipboard(oauthAuthUrl)
    copiedAuthUrl = true
    setTimeout(() => (copiedAuthUrl = false), 2000)
  }

  async function submitManualCallback() {
    const raw = callbackInput.trim()
    isConnecting = true
    oauthError = null
    try {
      if (providerId === 'freebuff') {
        // Direct manual token input
        if (raw && !raw.includes('http') && !raw.includes('auth_code=') && raw.length >= 24 && !raw.includes('/')) {
          await api.createConnection({
            provider: 'freebuff',
            authType: 'oauth',
            name: 'Freebuff (manual)',
            apiKey: raw,
          })
          if (freebuffPollTimer) {
            clearInterval(freebuffPollTimer)
            freebuffPollTimer = null
          }
          isAuthorizingFreebuff = false
          showOAuthModal = false
          onRefresh()
          loadFreebuffSession()
          return
        }

        // Immediate poll verification
        if (currentFreebuffInit) {
          const res = await api.pollFreebuff(
            currentFreebuffInit.fingerprintId,
            currentFreebuffInit.fingerprintHash,
            currentFreebuffInit.expiresAt
          )
          if (res?.status === 'authorized') {
            if (freebuffPollTimer) {
              clearInterval(freebuffPollTimer)
              freebuffPollTimer = null
            }
            isAuthorizingFreebuff = false
            showOAuthModal = false
            onRefresh()
            loadFreebuffSession()
            return
          } else if (res?.status === 'pending') {
            oauthError = 'Status is still pending. If the Freebuff tab opened on the /onboard page, finish the onboarding step in that tab, then click Check & Connect again.'
          } else if (res?.status === 'expired') {
            oauthError = 'The authorization session has expired. Close this modal and click Authorize Freebuff CLI again.'
          } else {
            oauthError = `Status: ${res?.status || 'pending'}. Make sure the browser login is complete.`
          }
        } else {
          oauthError = 'The Freebuff session has not been started yet. Click Authorize Freebuff CLI again.'
        }
        return
      }

      if (!raw) return
      let code = raw.trim()
      let redirectUri: string | undefined
      if (code.includes('code=')) {
        try {
          const u = new URL(code)
          code = u.searchParams.get('code') || code
          redirectUri = `${u.origin}${u.pathname}`
        } catch {
          const match = code.match(/code=([^&]+)/)
          if (match) code = decodeURIComponent(match[1])
        }
      }
      while (code.includes('%')) {
        try {
          const decoded = decodeURIComponent(code)
          if (decoded === code) break
          code = decoded
        } catch {
          break
        }
      }
      if (isClineOAuth) {
        if (!clineCodeVerifier) {
          oauthError = 'The authorization session has not been started yet. Close this modal and click Log in again.'
          return
        }
        try {
          const res = await api.clineExchange(providerId, code, clineCodeVerifier, redirectUri || clineRedirectUri || undefined)
          if (!res || (res as { error?: string })?.error || (res as { status?: string })?.status === 'error') {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (isPKCEOAuth) {
        if (!pkceCodeVerifier) {
          oauthError = 'The authorization session has not been started yet. Close this modal and click Log in again.'
          return
        }
        try {
          const res = await api.pkceExchange({
            provider: providerId,
            code,
            codeVerifier: pkceCodeVerifier,
            redirectUri: redirectUri || pkceRedirectUri || undefined,
            state: pkceState || undefined,
            baseUrl: providerId === 'gitlab' ? gitlabBaseUrl.trim() || undefined : undefined,
            clientId: providerId === 'gitlab' ? gitlabClientId.trim() || undefined : undefined,
            clientSecret: providerId === 'gitlab' ? gitlabClientSecret.trim() || undefined : undefined,
          })
          if (!res || (res as { error?: string })?.error || (res as { status?: string })?.status === 'error') {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (isAuthCodeOAuth) {
        try {
          const res = await api.authcodeExchange({
            provider: providerId,
            code,
            redirectUri: redirectUri || pkceRedirectUri || undefined,
            state: pkceState || undefined,
          })
          if (!res || (res as { error?: string })?.error || (res as { status?: string })?.status === 'error') {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (isCustomOAuth) {
        if (!raw) return
        try {
          const res = await api.customExchange(providerId as 'trae' | 'windsurf' | 'zed', {
            code: raw,
            state: customState || undefined,
            codeVerifier: customVerifier || undefined,
            systemId: customSystemId || undefined,
          })
          if (!res || (res as { error?: string })?.error || (res as { status?: string })?.status === 'error') {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (isSpecialOAuth) {
        const tok = (specialToken || callbackInput).trim()
        if (!tok && providerId !== 'cursor') {
          oauthError = 'Tempel token / callback terlebih dahulu.'
          return
        }
        try {
          let res: { success?: boolean; status?: string; error?: string } | null = null
          if (providerId === 'cursor') {
            if (!tok || !specialExtra.trim()) {
              oauthError = 'Enter the access token and machine ID.'
              return
            }
            res = await api.cursorImport(tok, specialExtra.trim())
          } else if (providerId === 'kimchi') {
            res = await api.kimchiExchange(tok)
          } else if (providerId === 'xiaomi-mimo') {
            if (!customVerifier) {
              oauthError = 'The authorization session has not been started yet. Close this modal and click Log in again.'
              return
            }
            res = await api.mimoExchange(tok, customVerifier)
          }
          if (!res || res?.error || (res.status && res.status === 'error')) {
            oauthError = res?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (providerId === 'gitlab' && specialToken.trim()) {
        // GitLab PAT mode (disamping OAuth PKCE).
        try {
          const res = await api.gitlabPAT(specialToken.trim(), specialBaseUrl.trim() || undefined)
          if (!res || (res as { error?: string })?.error || !(res as { success?: boolean })?.success) {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (providerId === 'iflow' && specialToken.trim()) {
        // iFlow cookie mode (disamping OAuth authcode).
        try {
          const res = await api.iflowCookie(specialToken.trim())
          if (!res || (res as { error?: string })?.error || !(res as { success?: boolean })?.success) {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (providerId !== 'antigravity') {
        if (raw.includes('code=') || raw.includes('http')) {
          oauthError = `Direct OAuth login is not supported for ${providerName}. Paste an access token / refresh token instead.`
          return
        }
        try {
          const res = await api.importOAuthToken(providerId, raw)
          if (!res || (res as { error?: string })?.error) {
            oauthError = (res as { error?: string })?.error || 'Token import failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      let pending = null
      try {
        pending = matchPending(loadPendings(window.localStorage), providerId, '')
      } catch {
        pending = null
      }
      const exchangeRedirectUri = redirectUri || pending?.redirectUri || antigravityCallback()
      const res = await api.antigravityExchange(code, exchangeRedirectUri, pending?.state)
      if (res?.success === false || (res as { error?: string })?.error) {
        oauthError = (res as { error?: string })?.error || 'Authorization failed'
      } else {
        showOAuthModal = false
        onRefresh()
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  // Model actions
  function copyModelId(modelId: string) {
    const level = resolveThinkingSuffix(modelId)
    const full = `${storageAlias}/${modelId}${level ? `(${level})` : ''}`
    copyToClipboard(full)
    copiedModelId = modelId
    setTimeout(() => (copiedModelId = null), 2000)
  }

  async function testModel(modelId: string, silent = false) {
    // Probes take seconds; if the panel switches provider mid-flight the
    // verdict belongs to the old provider and must not be written here.
    const pid = providerId
    modelTestStatuses[modelId] = 'testing'
    modelTestErrors[modelId] = null
    if (!silent) activeModelTestError = null
    try {
      const res = await api.testModel(`${storageAlias}/${modelId}`)
      if (pid !== providerId) return
      const verdict = parseModelTestVerdict(res)
      modelTestStatuses[modelId] = verdict.status
      modelTestErrors[modelId] = verdict.error
      if (!silent && verdict.error) {
        activeModelTestError = `${modelId}: ${verdict.error}`
      }
    } catch (err) {
      if (pid !== providerId) return
      modelTestStatuses[modelId] = 'error'
      const msg = err instanceof Error ? err.message : 'Model test failed'
      modelTestErrors[modelId] = msg
      if (!silent) activeModelTestError = `${modelId}: ${msg}`
    }
  }

  async function handleDisableModel(modelId: string) {
    const updated = Array.from(new Set([...disabledModelIds, modelId]))
    disabledModelIds = updated
    try {
      await api.saveDisabledModels(storageAlias, updated)
    } catch (err) {
      console.error('Failed to disable model:', err)
    }
  }

  async function handleEnableModel(modelId: string) {
    const updated = disabledModelIds.filter((id) => id !== modelId)
    disabledModelIds = updated
    try {
      await api.saveDisabledModels(storageAlias, updated)
    } catch (err) {
      console.error('Failed to enable model:', err)
    }
  }

  async function handleToggleAllModels() {
    if (allDisabled) {
      disabledModelIds = []
      try {
        await api.saveDisabledModels(storageAlias, [])
      } catch (err) {
        console.error('Failed to enable all models:', err)
      }
    } else {
      if (!confirm(`Disable all ${allAvailableModels.length} model(s)?`)) return
      const updated = allAvailableModels.map((m) => m.id)
      disabledModelIds = updated
      try {
        await api.saveDisabledModels(storageAlias, updated)
      } catch (err) {
        console.error('Failed to disable all models:', err)
      }
    }
  }

  // Custom Model & Node handlers
  async function submitAddCustomModel(
    modelId: string,
    caps?: { vision?: boolean; reasoning?: boolean },
    limits?: { contextWindow?: number; maxOutput?: number },
  ) {
    try {
      await api.saveCustomModel(`${storageAlias}|${modelId}|llm`, {
        id: modelId,
        providerAlias: storageAlias,
        type: 'llm',
        ...(caps ? { caps } : {}),
        ...(limits?.contextWindow ? { contextWindow: limits.contextWindow } : {}),
        ...(limits?.maxOutput ? { maxOutput: limits.maxOutput } : {}),
      })
      showAddCustomModelModal = false
      const modelsData = await fetchProviderModelsData(providerId, storageAlias)
      customModels = modelsData.customModels
      notifyCustomModelsChanged()
    } catch (err) {
      alert(`Failed to add custom model: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleAddKeyConnection(payload: CreateConnectionPayload) {
    addConnectionError = ''
    try {
      await api.createConnection({
        ...payload,
        provider: payload.provider || providerId,
        name: payload.name || `${providerName} Key`
      })
      showAddKeyModal = false
      onRefresh()
    } catch (err) {
      // Upstream keeps the modal open and shows the error inline.
      addConnectionError = err instanceof Error ? err.message : String(err)
    }
  }

  function openAddKeyModal() {
    addConnectionError = ''
    showAddKeyModal = true
  }

  async function handleDeleteProviderNode(nodeId: string) {
    if (!confirm('Delete custom provider endpoint and all attached credentials?')) return
    try {
      await api.deleteProviderNode(nodeId)
      onBack()
      onRefresh()
    } catch (err) {
      alert(`Delete failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Upstream parity: PUT /api/provider-nodes/[id] then refresh the node list.
  //
  // A changed URL suffix renames the provider id, and this page is routed by
  // that id — so a rename has to navigate to the new one or the user lands on a
  // node that no longer exists.
  async function handleSaveEditedNode(data: {
    name: string
    prefix: string
    apiType?: string
    baseUrl: string
    urlSuffix: string
  }) {
    if (!selectedNode) return
    try {
      const res = await api.updateProviderNode(selectedNode.id, {
        name: data.name,
        prefix: data.prefix,
        ...(selectedNode.type === 'openai-compatible' && data.apiType ? { apiType: data.apiType } : {}),
        baseUrl: data.baseUrl,
        urlSuffix: data.urlSuffix || undefined
      })
      showEditNodeModal = false
      if (res?.node?.id && res.node.id !== selectedNode.id) {
        onSelectProvider?.(res.node.id)
      }
      onRefresh()
    } catch (err) {
      alert(`Save failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Compatible nodes: upstream CompatibleModelsSection handlers.
  function displayAlias(): string {
    return selectedNode?.prefix || providerId
  }

  async function refreshCompatibleModels() {
    try {
      const [modelsData, aliasesData] = await Promise.all([
        fetchProviderModelsData(providerId, storageAlias),
        api.getModelAliases().catch(() => ({ aliases: {} })),
      ])
      customModels = modelsData.customModels
      modelAliases = aliasesData?.aliases || {}
    } catch (err) {
      console.error('Failed to refresh compatible models:', err)
    }
  }

  async function handleAddCompatibleModel() {
    const modelId = newCompatibleModel.trim()
    if (!modelId || isAddingCompatibleModel) return
    if (compatibleRows.some((r) => r.id === modelId)) {
      alert('Model already exists for this provider.')
      return
    }
    isAddingCompatibleModel = true
    try {
      await api.saveCustomModel(`${storageAlias}|${modelId}|llm`, {
        id: modelId,
        providerAlias: storageAlias,
        type: 'llm',
      })
      newCompatibleModel = ''
      await refreshCompatibleModels()
      notifyCustomModelsChanged()
    } catch (err) {
      // A refusal (the id already addresses a combo or a model alias) has to
      // reach the user, like the other import paths on this page.
      alert(err instanceof Error ? err.message : 'Failed to add model')
      console.error('Error adding model:', err)
    } finally {
      isAddingCompatibleModel = false
    }
  }

  async function handleDeleteCompatibleModel(row: { id: string; source: 'custom' | 'legacyAlias'; alias?: string }) {
    try {
      if (row.source === 'custom') {
        await api.deleteCustomModel(`${storageAlias}|${row.id}|llm`)
      } else if (row.alias) {
        await api.deleteModelAlias(row.alias)
      }
      await refreshCompatibleModels()
      notifyCustomModelsChanged()
    } catch (err) {
      console.error('Error deleting model:', err)
    }
  }

  async function handleTestCompatibleModel(modelId: string) {
    if (compatibleTestId) return
    compatibleTestId = modelId
    compatibleTestErrors[modelId] = null
    try {
      const res = await api.testModel(`${storageAlias}/${modelId}`)
      if (res.ok) {
        compatibleTestResults[modelId] = 'ok'
        compatibleTestErrors[modelId] = null
      } else {
        compatibleTestResults[modelId] = 'error'
        const err = res.error || 'Model test failed'
        compatibleTestErrors[modelId] = err
        activeModelTestError = `${modelId}: ${err}`
      }
    } catch (err) {
      compatibleTestResults[modelId] = 'error'
      const msg = err instanceof Error ? err.message : 'Model test failed'
      compatibleTestErrors[modelId] = msg
      activeModelTestError = `${modelId}: ${msg}`
    } finally {
      compatibleTestId = null
    }
  }

  function copyCompatibleModel(modelId: string) {
    copyToClipboard(`${displayAlias()}/${modelId}`)
    copiedModelId = modelId
    setTimeout(() => (copiedModelId = null), 2000)
  }

  async function handleImportCompatibleModels() {
    if (isImportingCompatibleModels) return
    const active = providerConnections.find((c) => c.isActive !== 0)
    if (!active) return
    isImportingCompatibleModels = true
    try {
      const res = await api.getConnectionModels(active.id)
      const models = res.models || []
      if (models.length === 0) {
        alert('No models returned from /models.')
        return
      }
      let imported = 0
      for (const m of models) {
        const modelId = typeof m === 'string' ? m : (m.id || m.name || m.model || '')
        if (!modelId || compatibleRows.some((r) => r.id === modelId)) continue
        await api.saveCustomModel(`${storageAlias}|${modelId}|llm`, {
          id: modelId,
          providerAlias: storageAlias,
          type: 'llm',
        })
        imported += 1
      }
      await refreshCompatibleModels()
      if (imported > 0) notifyCustomModelsChanged()
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Failed to import models')
    } finally {
      isImportingCompatibleModels = false
    }
  }

  // Port of upstream ConnectionRow's secondaryDisplayName: the email when the
  // stored name differs from it, otherwise the displayName when that differs.
  // The Qoder device flow stores name === email, so the displayName ("Luqmanul
  // Hakim") is what upstream shows underneath — the same reason our card used to
  // show the email twice over.
  function secondaryConnLabel(c: {
    name?: string | null
    email?: string | null
    displayName?: string | null
  }): string {
    const name = c.name?.trim() ?? ''
    const email = c.email?.trim() ?? ''
    const display = c.displayName?.trim() ?? ''
    if (name && email && name !== email) return email
    if (name && display && name !== display) return display
    return ''
  }

  async function handleImportLiveCatalogModels() {
    if (isImportingLiveCatalogModels) return
    const active = providerConnections.find((c) => c.isActive !== 0)
    if (!active) {
      alert('Add an active connection first to fetch models.')
      return
    }
    isImportingLiveCatalogModels = true
    try {
      const res = await api.getConnectionModels(active.id)
      const models = res.models || []
      if (models.length === 0) {
        alert('No models returned from /models.')
        return
      }
      let imported = 0
      for (const m of models) {
        const modelId = typeof m === 'string' ? m : (m.id || m.name || m.model || '')
        if (!modelId) continue
        const alreadyBuiltin = builtInModels.some((b) => b.id === modelId)
        const alreadyCustom = providerCustomModels.some((c) => c.id === modelId)
        if (alreadyBuiltin || alreadyCustom) continue
        const caps =
          typeof m === 'object' &&
          m !== null &&
          'capabilities' in m &&
          m.capabilities &&
          typeof m.capabilities === 'object'
            ? (m.capabilities as { vision?: boolean; reasoning?: boolean })
            : undefined
        // An endpoint that reports its own limits is the authoritative source;
        // saving them means /v1/models keeps publishing them after the row
        // outlives this import.
        const declaredCtxLen =
          typeof m === 'object' && m !== null && 'context_length' in m
            ? Number(m.context_length) || 0
            : 0
        const declaredMaxOut =
          typeof m === 'object' && m !== null && 'max_completion_tokens' in m
            ? Number(m.max_completion_tokens) || 0
            : 0
        await api.saveCustomModel(`${storageAlias}|${modelId}|llm`, {
          id: modelId,
          providerAlias: storageAlias,
          type: 'llm',
          ...(caps ? { caps } : {}),
          ...(declaredCtxLen > 0 ? { contextWindow: declaredCtxLen } : {}),
          ...(declaredMaxOut > 0 ? { maxOutput: declaredMaxOut } : {}),
        })
        imported++
      }
      const modelsData = await fetchProviderModelsData(providerId, storageAlias)
      customModels = modelsData.customModels
      if (imported > 0) notifyCustomModelsChanged()
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Failed to import models')
    } finally {
      isImportingLiveCatalogModels = false
    }
  }

  // --- Feature: check latest models / accessibility / prune unusable ----------

  let latestModels = $state<Array<{ id: string; name?: string }>>([])
  let isCheckingLatest = $state(false)
  let latestError = $state<string | null>(null)
  let isCheckingAll = $state(false)
  let checkAllProgress = $state({ done: 0, total: 0 })

  // ConnectionsView renders this panel without a {#key providerId}, so the
  // instance survives a provider switch. Everything computed for the old
  // provider is dropped before the new provider's DOM commits — otherwise the
  // stale "new models" list stays clickable and Add would save a model from
  // provider A into provider B's store.
  $effect.pre(() => {
    if (!providerId) return
    latestModels = []
    latestError = null
    isCheckingLatest = false
    isCheckingAll = false
    checkAllProgress = { done: 0, total: 0 }
    modelTestStatuses = {}
    modelTestErrors = {}
    checkAllSummary = null
    activeModelTestError = null
  })

  // Providers whose /models the backend can enumerate
  // (internal/providers.modelsListURL + compatible nodes + live-catalog
  // providers). Kept in sync so the button only renders where it can work.
  const ModelsListURLs = new Set([
    'tokenharbor',
    'dahl',
    'atria',
    'agnes',
    'bai',
    'muse',
  ])

  // canListLiveModels mirrors the backend's list of providers with a live
  // catalogue (HandleGetConnectionModels): a button that cannot work should
  // not render.
  let canListLiveModels = $derived(
    providerId.startsWith('openai-compatible-') ||
      isCompatibleNode ||
      ['antigravity', 'gemini-cli', 'cline', 'clinepass', 'qoder', 'qoder-cn'].includes(providerId) ||
      ModelsListURLs.has(providerId)
  )

  async function handleCheckLatestModels() {
    if (isCheckingLatest) return
    // Captured up front: a provider switch mid-fetch invalidates the response.
    const pid = providerId
    const active = providerConnections.find((c) => c.isActive !== 0)
    if (!active) {
      alert('Add an active connection first to fetch models.')
      return
    }
    isCheckingLatest = true
    latestError = null
    latestModels = []
    try {
      const res = await api.getConnectionModels(active.id)
      if (pid !== providerId) return
      const known = new Set<string>([
        ...builtInModels.map((m) => m.id),
        ...providerCustomModels.map((m) => m.id),
      ])
      for (const m of res.models || []) {
        const item = typeof m === 'string'
          ? { id: m }
          : { id: (m.id || m.model || m.name || '') as string, name: (m.name || '') as string | undefined }
        if (!item.id || known.has(item.id)) continue
        latestModels.push(item)
      }
      latestModels.sort((a, b) => a.id.localeCompare(b.id))
      if (latestModels.length === 0) {
        latestError = 'Catalog is up to date — no new models found.'
      }
    } catch (err) {
      if (pid !== providerId) return
      latestError = err instanceof Error ? err.message : 'Failed to fetch models'
    } finally {
      if (pid === providerId) isCheckingLatest = false
    }
  }

  async function handleAddLatestModel(id: string, name?: string) {
    try {
      await api.saveCustomModel(`${storageAlias}|${id}|llm`, {
        id,
        providerAlias: storageAlias,
        type: 'llm',
        ...(name ? { name } : {}),
      })
      latestModels = latestModels.filter((m) => m.id !== id)
      const modelsData = await fetchProviderModelsData(providerId, storageAlias)
      customModels = modelsData.customModels
      notifyCustomModelsChanged()
    } catch (err) {
      alert(`Failed to add model: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Bounded concurrency pool: each probe is a real chat completion that can
  // take seconds, so running all of them at once would hammer the provider and
  // the browser (a ~30-model provider fires 30 simultaneous fetches).
  const TEST_CONCURRENCY = 6

  // Runs ids through the bounded pool, then publishes the sweep summary. A
  // run that outlives its provider panel publishes nothing: the verdicts it
  // produced belong to a provider this panel no longer shows.
  async function runModelSweep(ids: string[]) {
    const pid = providerId
    isCheckingAll = true
    checkAllProgress = { done: 0, total: ids.length }
    try {
      let cursor = 0
      const workers = Array.from({ length: Math.min(TEST_CONCURRENCY, ids.length) }, async () => {
        while (cursor < ids.length && pid === providerId) {
          const i = cursor++
          await testModel(ids[i], true)
          if (pid !== providerId) return
          checkAllProgress = { ...checkAllProgress, done: checkAllProgress.done + 1 }
        }
      })
      await Promise.allSettled(workers)
      if (pid !== providerId) return
      const summary = formatCheckAllSummary(modelTestStatuses)
      checkAllSummary = summary
      activeModelTestError = summary.message
    } finally {
      if (pid === providerId) isCheckingAll = false
    }
  }

  async function handleCheckAllModels() {
    if (isCheckingAll) return
    // Reset previous verdicts so the run is not confused with stale ones.
    // Only the full sweep clears them — a retry must keep the passed and
    // failed verdicts it is not re-testing.
    modelTestStatuses = {}
    modelTestErrors = {}
    checkAllSummary = null
    await runModelSweep(visibleModels.map((m) => m.id))
  }

  // A blocked model was never asked anything, so re-testing only those is
  // cheap. It reuses the same bounded pool: a serial loop would leave the
  // button looking hung for as long as the cooldown, which is exactly the
  // wait the operator is sitting through.
  async function handleRetryBlockedModels() {
    if (isCheckingAll) return
    const blockedIds = getBlockedModelIds(modelTestStatuses)
    if (blockedIds.length === 0) return
    await runModelSweep(blockedIds)
  }

  // Delete is only real for custom models; registry models are static, so an
  // unusable built-in model is disabled instead (the existing disable path).
  async function handleDeleteModel(modelId: string) {
    const custom = providerCustomModels.find((m) => m.id === modelId)
    if (custom) {
      if (!confirm(`Delete custom model "${modelId}"?`)) return
      try {
        await api.deleteCustomModel(`${storageAlias}|${modelId}|llm`)
        const modelsData = await fetchProviderModelsData(providerId, storageAlias)
        customModels = modelsData.customModels
        notifyCustomModelsChanged()
      } catch (err) {
        alert(`Failed to delete model: ${err instanceof Error ? err.message : String(err)}`)
      }
      return
    }
    await handleDisableModel(modelId)
  }

  // Deprecated models of this provider, so the section can say how many of
  // the listed models are dead upstream instead of leaving the operator to
  // count the badges.
  async function reloadDeprecations() {
    const pid = providerId
    const deps = await loadDeprecations()
    if (pid !== providerId) return
    modelDeprecationCount = Object.values(deps).filter(
      (d) => d.provider === providerId && visibleModels.some((m) => m.id === d.model)
    ).length
  }

  /**
   * Re-reads the provider's live catalogue and clears the badge from any model
   * it still serves. Only revival is decided here: a model missing from the
   * catalogue is not badged on that evidence, because catalogues are routinely
   * partial and a healthy model would be blacklisted with it.
   */
  async function handleSyncModelDeprecations() {
    if (isSyncingModels) return
    const pid = providerId
    isSyncingModels = true
    try {
      const res = await api.syncProviderModels(providerId)
      if (pid !== providerId) return
      await refreshDeprecations()
      if (res.revived > 0) {
        await reloadDeprecations()
        alert(`Sync complete — ${res.revived} model(s) are serving again and no longer marked Deprecated.`)
      } else {
        await reloadDeprecations()
        alert('Sync complete. Models the provider no longer serves stay marked until a live request confirms they are gone.')
      }
    } catch (err) {
      alert(`Model sync failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      if (pid === providerId) isSyncingModels = false
    }
  }

  $effect(() => {
    const unsubscribe = subscribeDeprecations(reloadDeprecations)
    void reloadDeprecations()
    return unsubscribe
  })
</script>

<div class="flex min-w-0 flex-col gap-6 px-1 sm:gap-8 sm:px-0">
  <!-- 0. Header: Back button + Provider Icon & Name -->
  <div class="min-w-0">
    <button
      type="button"
      onclick={onBack}
      class="inline-flex items-center gap-1 text-sm text-text-muted hover:text-primary transition-colors mb-4 cursor-pointer"
    >
      <span class="material-symbols-outlined text-lg">arrow_back</span>
      Back to Providers
    </button>

    <div class="flex min-w-0 items-center gap-3 sm:gap-4">
      <div
        class="flex size-12 shrink-0 items-center justify-center rounded-lg"
        style="background-color: {providerColor}15;"
      >
        <ProviderIcon
          id={providerId}
          apiType={selectedNode?.apiType}
          size="lg"
          class="border-transparent bg-transparent"
        />
      </div>

      <div class="min-w-0">
        <div class="flex items-center gap-3 flex-wrap">
          <h1 class="truncate text-2xl font-semibold tracking-tight sm:text-3xl">
            {providerName}
          </h1>
          {#if providerWebsite}
            <a
              href={providerWebsite}
              target="_blank"
              rel="noopener noreferrer"
              class="text-xs text-primary hover:underline inline-flex items-center gap-1"
            >
              <span class="material-symbols-outlined text-sm">open_in_new</span>
              {providerWebsiteLabel}
            </a>
          {/if}
        </div>
        <p class="text-text-muted">
          {providerConnections.length} connection{providerConnections.length === 1 ? '' : 's'}
        </p>
      </div>
    </div>
  </div>

  <!-- 1. Risk Notice (for OAuth / subscription providers) -->
  {#if hasRiskNotice}
    <div class="flex items-center gap-2 px-3 py-2 rounded-lg bg-yellow-500/10 border border-yellow-500/30">
      <span class="material-symbols-outlined text-[16px] text-yellow-500 mt-0.5 shrink-0">warning</span>
      <p class="text-xs text-red-600 dark:text-yellow-400 leading-relaxed">
        ⚠️ Risk Notice: This provider uses a subscription/OAuth session not officially licensed for proxy/router use. Account may be restricted or banned. Use at your own risk.
      </p>
    </div>
  {/if}

  <!-- 1b. Provider notice from upstream registry (apiKeyUrl → Get API Key button) -->
  {#if providerNoticeText}
    <div class="flex flex-col gap-2 rounded-lg border border-blue-500/30 bg-blue-500/10 px-3 py-2 sm:flex-row sm:items-center">
      <span class="material-symbols-outlined text-[16px] text-blue-500 shrink-0">info</span>
      <p class="min-w-0 flex-1 text-xs leading-relaxed text-blue-600 dark:text-blue-400">{providerNoticeText}</p>
      {#if providerNoticeApiKeyUrl}
        <a
          href={providerNoticeApiKeyUrl}
          target="_blank"
          rel="noopener noreferrer"
          class="inline-flex justify-center rounded bg-blue-500 px-2 py-1 text-xs font-medium text-white transition-colors hover:bg-blue-600 sm:py-0.5"
        >
          Get API Key →
        </a>
      {/if}
    </div>
  {/if}

  <!-- 2. Compatible Node details (if custom node) — upstream parity: endpoint line + Add API Key / Edit / Delete -->
  {#if selectedNode}
    <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6">
      <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div class="min-w-0">
          <h2 class="text-lg font-semibold">
            {isAnthropicCompatibleNode ? 'Anthropic Compatible Details' : 'OpenAI Compatible Details'}
          </h2>
          <p class="break-all text-sm text-text-muted">
            {isAnthropicCompatibleNode
              ? `Messages API · ${(selectedNode.baseUrl || '').replace(/\/$/, '')}/messages`
              : `${isResponsesNode ? 'Responses API' : 'Chat Completions'} · ${(selectedNode.baseUrl || '').replace(/\/$/, '')}/${isResponsesNode ? 'responses' : 'chat/completions'}`}
          </p>
        </div>
        <div class="grid grid-cols-1 gap-2 sm:flex sm:items-center">
          <button
            type="button"
            onclick={openAddKeyModal}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
          >
            <span class="material-symbols-outlined text-[18px]">add</span>
            Add API Key
          </button>
          <button
            type="button"
            onclick={() => (showEditNodeModal = true)}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
          >
            <span class="material-symbols-outlined text-[18px]">edit</span>
            Edit
          </button>
          <button
            type="button"
            onclick={() => handleDeleteProviderNode(selectedNode!.id)}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
          >
            <span class="material-symbols-outlined text-[18px]">delete</span>
            Delete
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if isNoAuth}
    <!-- Free Provider: NoAuthProxyCard -->
    <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6 flex flex-col gap-4">
      <div class="flex items-start gap-3">
        <span class="material-symbols-outlined text-[20px] text-primary mt-0.5">lock_open</span>
        <div class="flex-1 min-w-0">
          <p class="text-sm font-medium text-text-main">No authentication required</p>
          <p class="text-xs text-text-muted mt-0.5">
            This provider is ready to use. Optionally route requests through a proxy pool to bypass IP-based limits.
          </p>
        </div>
        {#if savedFreeProxy}
          <span class="px-2 py-0.5 rounded text-xs bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/30">
            Saved
          </span>
        {/if}
      </div>

      <!-- Proxy Pool Selector -->
      <div class="flex flex-col gap-1.5">
        <label for="free-proxy-pool" class="text-sm font-medium text-text-main">Proxy Pool</label>
        <select
          id="free-proxy-pool"
          value={freeProxyPoolId}
          onchange={(e) => handleFreeProxyChange(e.currentTarget.value, freeRotateStrategy)}
          disabled={isSavingFreeProxy || freeRotateStrategy !== 'none'}
          class="w-full bg-surface-2 border border-border rounded-lg px-3 py-2 text-sm text-text-main focus:outline-none focus:border-primary disabled:opacity-50 cursor-pointer"
        >
          <option value="none">None (direct)</option>
          {#each activeProxyPools as pool}
            <option value={pool.id}>{pool.name}</option>
          {/each}
        </select>
        {#if freeRotateStrategy !== 'none'}
          <p class="text-xs text-text-muted">
            Pool selector is ignored when rotation is active — all active pools are used.
          </p>
        {/if}
      </div>

      <!-- Rotation Strategy -->
      <div class="flex flex-col gap-1.5">
        <span class="text-sm font-medium text-text-main">Rotation Strategy</span>
        <div class="flex items-center gap-2 flex-wrap">
          <button
            type="button"
            onclick={() => handleFreeProxyChange(freeProxyPoolId, 'none')}
            class="px-3 py-1.5 rounded-lg text-xs font-medium border transition-colors cursor-pointer {freeRotateStrategy === 'none'
              ? 'bg-primary text-white border-primary'
              : 'bg-surface-2 text-text-main border-border hover:bg-surface-3'}"
          >
            None (single pool)
          </button>
          <button
            type="button"
            onclick={() => handleFreeProxyChange(freeProxyPoolId, 'round-robin')}
            disabled={activeProxyPools.length < 2}
            class="px-3 py-1.5 rounded-lg text-xs font-medium border transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed {freeRotateStrategy === 'round-robin'
              ? 'bg-primary text-white border-primary'
              : 'bg-surface-2 text-text-main border-border hover:bg-surface-3'}"
          >
            Round-robin
          </button>
          <button
            type="button"
            onclick={() => handleFreeProxyChange(freeProxyPoolId, 'random')}
            disabled={activeProxyPools.length < 2}
            class="px-3 py-1.5 rounded-lg text-xs font-medium border transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed {freeRotateStrategy === 'random'
              ? 'bg-primary text-white border-primary'
              : 'bg-surface-2 text-text-main border-border hover:bg-surface-3'}"
          >
            Random
          </button>
        </div>
        {#if activeProxyPools.length < 2}
          <p class="text-xs text-text-muted">
            Need at least 2 active proxy pools for rotation.
          </p>
        {/if}
      </div>
    </div>
  {:else}
  <!-- 3. Connections Card -->
  <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6">
    <!-- Header -->
    <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <h2 class="text-lg font-semibold">Connections</h2>
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:gap-4">
        {#if selectedConnIds.length > 0}
          <button
            type="button"
            onclick={handleDeleteSelected}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-red-500/10 hover:bg-red-500/20 text-red-600 dark:text-red-400 border border-red-500/30 h-7 px-3 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">delete</span>
            Delete Selected ({selectedConnIds.length})
          </button>
        {/if}

        {#if providerConnections.length > 0 && proxyPools.length > 0}
          <button
            type="button"
            onclick={() => (showApplyProxyModal = true)}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] disabled:opacity-50 disabled:cursor-not-allowed bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">lan</span>
            Apply Proxy
          </button>
        {/if}

        <button
          type="button"
          onclick={runOneByOneTest}
          disabled={isTestingOneByOne || providerConnections.length === 0}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] disabled:opacity-50 disabled:cursor-not-allowed bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px]"
        >
          <span class="material-symbols-outlined text-[18px] {isTestingOneByOne ? 'animate-spin text-primary' : ''}">sync</span>
          {isTestingOneByOne ? 'Testing Connection One-by-One...' : 'Test Connection One-by-One'}
        </button>

        {#if isTestingOneByOne}
          <button
            type="button"
            onclick={stopOneByOneTest}
            disabled={isStoppingOneByOne}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] disabled:opacity-50 disabled:cursor-not-allowed bg-transparent hover:bg-surface-2 text-text-muted hover:text-text-main h-7 px-3 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">stop</span>
            {isStoppingOneByOne ? 'Stopping...' : 'Stop'}
          </button>
        {/if}

        <div class="flex flex-wrap items-center gap-2">
          <span class="text-xs text-text-muted font-medium">Round Robin</span>
          <button
            type="button"
            role="switch"
            aria-label="Toggle Round Robin"
            aria-checked={isRoundRobin}
            onclick={toggleRoundRobin}
            class="relative inline-flex shrink-0 cursor-pointer rounded-full transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-brand-500/30 {isRoundRobin ? 'bg-brand-500' : 'bg-surface-3'} w-11 h-6"
          >
            <span
              class="pointer-events-none inline-block rounded-full bg-white shadow-sm transform transition duration-200 ease-in-out {isRoundRobin ? 'translate-x-5' : 'translate-x-0.5'} size-5 mt-0.5"
            ></span>
          </button>
          {#if isRoundRobin}
            <div class="flex items-center gap-1.5">
              <span class="text-xs text-text-muted">Sticky:</span>
              <input
                type="number"
                min="1"
                bind:value={stickyLimit}
                onchange={handleStickyLimitChange}
                placeholder="1"
                class="w-14 px-2 py-1 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary"
              />
            </div>
          {/if}

          <button
            type="button"
            onclick={() => (showHeaderOverrides = true)}
            class="inline-flex h-7 items-center gap-1.5 rounded-[8px] px-3 text-xs font-medium text-text-muted transition-colors hover:bg-surface-2 hover:text-text-main"
          >
            <span class="material-symbols-outlined text-[18px]" aria-hidden="true">tune</span>
            Custom headers
          </button>
        </div>
      </div>
    </div>

    <!-- One-by-One summary (upstream parity) -->
    {#if oneByOneSummary}
      <div class="mb-4 rounded-lg border border-black/10 bg-black/[0.02] px-3 py-2 text-xs text-text-muted dark:border-white/10 dark:bg-white/[0.03]">
        <div class="flex flex-wrap items-center gap-3">
          <span>Total: {oneByOneSummary.total}</span>
          <span>Completed: {oneByOneSummary.completed}</span>
          <span>Passed: {oneByOneSummary.passed}</span>
          <span>Failed: {oneByOneSummary.failed}</span>
          {#if oneByOneSummary.stopped}
            <span class="text-amber-600 dark:text-amber-400">Stopped</span>
          {/if}
          {#if isTestingOneByOne && oneByOneCurrentId}
            <span>
              Running: {formatEmailLabel(providerConnections.find((conn) => conn.id === oneByOneCurrentId)?.name || oneByOneCurrentId, $emailPrivacy)}
            </span>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Select All Checkbox & Sensor Email Toggle -->
    {#if providerConnections.length > 0}
      <div class="mb-3 flex items-center justify-between gap-2 border-b border-black/[0.03] pb-2 dark:border-white/[0.03]">
        <label class="flex cursor-pointer items-center gap-1.5 text-xs text-text-muted hover:text-primary">
          <input
            type="checkbox"
            checked={isAllSelected}
            onchange={toggleSelectAll}
            class="h-3.5 w-3.5 rounded border-gray-300 text-primary focus:ring-primary cursor-pointer"
          />
          Select All
        </label>

        <!-- Sensor Email Toggle Button -->
        <button
          type="button"
          onclick={() => emailPrivacy.toggle()}
          class="flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors cursor-pointer {$emailPrivacy ? 'border-brand-500/40 bg-brand-500/10 text-brand-500 font-medium' : 'border-border-subtle bg-surface text-text-muted hover:text-text-main hover:bg-surface-2'}"
          title={$emailPrivacy ? 'Tampilkan email lengkap' : 'Sensor / sembunyikan email'}
        >
          <span class="material-symbols-outlined text-[15px]">
            {$emailPrivacy ? 'visibility_off' : 'visibility'}
          </span>
          <span>{$emailPrivacy ? 'Email Disensor' : 'Sensor Email'}</span>
        </button>
      </div>
    {/if}

    <!-- Connections List -->
      {#if providerConnections.length === 0}
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div class="flex items-center gap-3">
          <div class="inline-flex items-center justify-center w-9 h-9 rounded-full bg-primary/10 text-primary shrink-0">
            <span class="material-symbols-outlined text-[18px]">{isOAuth ? 'lock' : 'key'}</span>
          </div>
          <div class="min-w-0">
            <p class="text-sm text-text-muted">No connections yet</p>
            {#if hasDualAuthModes}
              <p class="text-xs text-text-muted">
                Choose {oauthButtonLabel} or {apiKeyButtonLabel}.
              </p>
            {/if}
          </div>
        </div>
        <div class="flex gap-2">
        {#if providerId === 'freebuff'}
          <button
            type="button"
            onclick={startFreebuffFlow}
            disabled={isAuthorizingFreebuff}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">vpn_key</span>
            {isAuthorizingFreebuff ? 'Polling Authorization...' : 'Authorize Freebuff CLI'}
          </button>
        {:else if providerId === 'antigravity'}
          <button
            type="button"
            onclick={handleAddConnectionClick}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">login</span>
            Connect Google Account
          </button>
        {:else if providerId === 'kiro'}
          <button
            type="button"
            onclick={handleAddConnectionClick}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">login</span>
            Connect Kiro
          </button>
        {:else if hasDualAuthModes}
          <button
            type="button"
            onclick={handleAddConnectionClick}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">lock</span>
            {oauthButtonLabel}
          </button>
          <button
            type="button"
            onclick={openAddKeyModal}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">key</span>
            {apiKeyButtonLabel}
          </button>
        {:else}
          <button
            type="button"
            onclick={handleAddConnectionClick}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">add</span>
            Add Connection
          </button>
        {/if}
        </div>
      </div>
    {:else}
      <!-- relative z-50: the open row proxy dropdown paints a fixed inset-0
           click-away backdrop over the whole viewport, which otherwise swallows
           the first click on any priority chevron. -->
      <div class="relative z-50 flex min-w-0 flex-col divide-y divide-black/[0.03] dark:divide-white/[0.03] max-h-[500px] overflow-y-auto pr-1">
        {#each providerConnections as conn, idx (conn.id)}
          {@const isFirst = idx === 0}
          {@const isLast = idx === providerConnections.length - 1}
          {@const isSelected = selectedConnIds.includes(conn.id)}
          {@const status = oneByOneStatuses[conn.id]}
          {@const specificData = conn.providerSpecificData as Record<string, unknown> | undefined}
          {@const assignedPoolId = (typeof specificData?.proxyPoolId === 'string' ? specificData.proxyPoolId : null)}
          {@const proxyBadge = proxyBadgeFor(conn)}
          {@const lastErr = normalizeLastError(conn.lastError || status?.error) || ''}
          {@const priorityNum = conn.priority ?? idx + 1}
          {@const isConnActive = conn.isActive === 1}
          {@const cooldownInfo = getCooldownInfo(conn)}
          <!-- badgeFor owns the precedence between the live probe and the
               connection's persisted testStatus; the markup below only renders
               the verdict it returns. -->
          {@const connBadge = badgeFor(status, conn.testStatus, lastErr)}
          <div class="flex min-w-0 items-stretch">
            <!-- Multi-select checkbox -->
            <div class="flex shrink-0 items-center pl-1 sm:pl-2">
              <input
                type="checkbox"
                checked={isSelected}
                onchange={() => toggleSelect(conn.id)}
                class="h-4 w-4 rounded border-gray-300 text-primary focus:ring-primary cursor-pointer"
              />
            </div>

            <!-- Connection Row Content -->
            <div class="flex-1 min-w-0">
              <div class="group flex min-w-0 flex-col gap-3 rounded-lg p-2 transition-colors hover:bg-black/[0.02] dark:hover:bg-white/[0.02] sm:flex-row sm:items-center sm:justify-between">
                <!-- Left info -->
                <div class="flex min-w-0 flex-1 items-start gap-2 sm:items-center sm:gap-3">
                  <!-- Reorder buttons. z-[60] sits above the row proxy
                       dropdown's fixed inset-0 click-away backdrop (z-40): a
                       positioned backdrop paints above unpositioned content, so
                       without this the first chevron click after opening a
                       dropdown is always swallowed. -->
                  <div class="relative z-[60] flex shrink-0 flex-col">
                    <button
                      type="button"
                      disabled={isFirst || !!reorderingConnId}
                      title={isFirst ? 'Already first' : 'Move up'}
                      onclick={() => swapPriority(idx, -1)}
                      class="p-1 rounded disabled:opacity-40 {isFirst || reorderingConnId ? 'text-text-muted/30 cursor-not-allowed' : 'hover:bg-sidebar text-text-muted hover:text-primary cursor-pointer'}"
                    >
                      <span class="material-symbols-outlined text-sm">keyboard_arrow_up</span>
                    </button>
                    <button
                      type="button"
                      disabled={isLast || !!reorderingConnId}
                      title={isLast ? 'Already last' : 'Move down'}
                      onclick={() => swapPriority(idx, 1)}
                      class="p-1 rounded disabled:opacity-40 {isLast || reorderingConnId ? 'text-text-muted/30 cursor-not-allowed' : 'hover:bg-sidebar text-text-muted hover:text-primary cursor-pointer'}"
                    >
                      <span class="material-symbols-outlined text-sm">keyboard_arrow_down</span>
                    </button>
                  </div>

                  <!-- Lock icon -->
                  <span class="material-symbols-outlined shrink-0 text-base text-text-muted">lock</span>

                  <!-- Title & badges -->
                  <div class="flex-1 min-w-0">
                    <p class="text-sm font-medium truncate">
                      {formatEmailLabel(conn.name || conn.email || (conn.authType === 'oauth' ? 'OAuth Account' : 'API Key Slot'), $emailPrivacy)}
                    </p>
                    <!-- Secondary label: upstream ConnectionRow shows the email
                         under a name, or the displayName when the stored name is
                         the email (which is what the Qoder device flow writes). -->
                    {#if secondaryConnLabel(conn)}
                      <p class="truncate text-xs text-text-muted">{formatEmailLabel(secondaryConnLabel(conn), $emailPrivacy)}</p>
                    {/if}
                    <div class="mt-1 flex min-w-0 flex-wrap items-center gap-1.5 sm:gap-2">
                      {#if connBadge === 'queued'}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-surface-2 text-text-muted px-2 py-0.5 text-[10px]"
                          title="Waiting for its turn"
                        >
                          <span class="size-1.5 rounded-full bg-text-muted/50"></span>
                          queued
                        </span>
                      {:else if connBadge === 'testing'}
                        <span class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-blue-500/10 text-blue-600 dark:text-blue-400 px-2 py-0.5 text-[10px]">
                          <span class="material-symbols-outlined text-[10px] animate-spin">progress_activity</span>
                          testing
                        </span>
                      {:else if connBadge === 'error'}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-red-500/10 text-red-600 dark:text-red-400 px-2 py-0.5 text-[10px]"
                          title={status?.state === 'failed' && status.error ? `failed: ${status.error}` : undefined}
                        >
                          <span class="size-1.5 rounded-full bg-red-500"></span>
                          error
                        </span>
                      {:else}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-green-500/10 text-green-600 dark:text-green-400 px-2 py-0.5 text-[10px]"
                          title={status?.state === 'success' ? 'last probe passed' : undefined}
                        >
                          <span class="size-1.5 rounded-full bg-green-500"></span>
                          active
                        </span>
                      {/if}
                      <!-- Cooldown & Exhausted Quota badge with live timer -->
                      {#if cooldownInfo}
                        {#if cooldownInfo.isLock}
                          <span class="text-xs text-orange-500 font-mono" title={cooldownInfo.title}>
                            {cooldownInfo.label}
                          </span>
                        {:else}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] border {cooldownInfo.isExhausted
                              ? 'bg-rose-500/10 text-rose-600 dark:text-rose-400 border-rose-500/30'
                              : 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/30'}"
                            title={cooldownInfo.title}
                          >
                            <span class="material-symbols-outlined text-[12px] animate-pulse">hourglass_top</span>
                            {cooldownInfo.label}
                          </span>
                        {/if}
                      {/if}

                      <!-- Auth type badge -->
                      <span class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-surface-2 text-text-muted px-2 py-0.5 text-[10px]">
                        {conn.authType === 'oauth' ? 'OAuth' : 'API Key'}
                      </span>
                      <!-- Freebuff Session status badge -->
                      {#if isFreebuff}
                        {@const fbSess = freebuffSessions[conn.id]}
                        {@const boundModel = fbSess?.currentModel || (specificData?.freebuffModel as string) || (specificData?.assignedModel as string)}
                        {#if fbSess?.status === 'active' || boundModel}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20"
                            title="Active session bound to {boundModel}"
                          >
                            <span>🔒</span>
                            <span class="font-mono">{boundModel}</span>
                          </span>
                        {:else if fbSess?.status === 'queued'}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] bg-amber-500/10 text-amber-600 dark:text-amber-400 border border-amber-500/20"
                            title="Waiting in queue"
                          >
                            <span>⏳</span> queued
                          </span>
                        {:else if fbSess?.status === 'banned'}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] bg-red-500/10 text-red-600 dark:text-red-400 border border-red-500/20"
                            title="Account banned"
                          >
                            <span>🚫</span> banned
                          </span>
                        {:else if fbSess}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-normal px-2 py-0.5 text-[10px] bg-surface-2 text-text-muted border border-border/50"
                            title="No active session held"
                          >
                            no session
                          </span>
                        {/if}
                      {/if}
                      <!-- Proxy badge (upstream: green when the bound pool is active,
                           red when bound to a missing/inactive pool or a legacy proxy) -->
                      {#if proxyBadge.hasAnyProxy}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold px-2 py-0.5 text-[10px] {proxyBadge.variant === 'success'
                            ? 'bg-green-500/10 text-green-600 dark:text-green-400'
                            : 'bg-red-500/10 text-red-600 dark:text-red-400'}"
                          title={proxyBadge.displayText}
                        >
                          <span class="size-1.5 rounded-full {proxyBadge.variant === 'success' ? 'bg-green-500' : 'bg-red-500'}"></span>
                          Proxy
                        </span>
                      {/if}
                      <!-- Last error tooltip -->
                      {#if lastErr && lastErr !== 'Provider test not supported'}
                        <span class="max-w-full truncate text-xs text-red-500 sm:max-w-[300px]" title={lastErr}>
                          {lastErr.length > 50 ? lastErr.slice(0, 50) + '...' : lastErr}
                        </span>
                      {/if}

                      <!-- Priority tag -->
                      <span class="text-xs text-text-muted">#{priorityNum}</span>
                    </div>
                    <!-- Error message block -->
                    {#if lastErr && lastErr !== 'Provider test not supported' && (status?.state === 'failed' || conn.testStatus === 'failed' || conn.testStatus === 'error')}
                      <div class="mt-1.5 flex items-start gap-1.5 text-xs text-red-500 bg-red-500/10 px-2.5 py-1.5 rounded-md border border-red-500/20 max-w-full">
                        <span class="material-symbols-outlined text-sm shrink-0 mt-0.5">error</span>
                        <span class="break-words font-medium leading-relaxed">{lastErr}</span>
                      </div>
                    {/if}
                    <!-- Proxy detail line: pool/legacy label, masked endpoint, no_proxy -->
                    {#if proxyBadge.hasAnyProxy}
                      <div class="mt-1 flex min-w-0 flex-wrap items-center gap-2">
                        <span
                          class="max-w-full truncate text-[11px] text-text-muted sm:max-w-[420px]"
                          title={proxyBadge.displayText}
                        >
                          {proxyBadge.displayText}
                        </span>
                        {#if proxyBadge.maskedProxyUrl}
                          <code
                            class="max-w-full truncate rounded bg-black/5 px-1 py-0.5 font-mono text-[10px] text-text-muted dark:bg-white/5 sm:max-w-[260px]"
                          >
                            {proxyBadge.maskedProxyUrl}
                          </code>
                        {/if}
                        {#if proxyBadge.noProxyText}
                          <span
                            class="max-w-full truncate text-[11px] text-text-muted sm:max-w-[320px]"
                            title={proxyBadge.noProxyText}
                          >
                            no_proxy: {proxyBadge.noProxyText}
                          </span>
                        {/if}
                      </div>
                    {/if}
                  </div>
                </div>

                <!-- Right actions -->
                <div class="flex w-full items-center justify-between gap-2 sm:w-auto sm:justify-end">
                  <!-- The proxy picker stays a control of its own: it is a
                       two-tap choice between pools, not a verb. Everything that
                       runs and is done moved into one menu, because four to five
                       labelled buttons in a row left the connection name almost
                       no width (issue #224). -->
                  <div class="grid flex-1 grid-flow-col auto-cols-fr gap-1 sm:flex sm:flex-none">
                    <!-- Proxy dropdown -->
                    <div class="relative">
                      <button
                        type="button"
                        onclick={(e) => {
                          e.stopPropagation()
                          if (activeProxyDropdownId === conn.id) {
                            activeProxyDropdownId = null
                          } else {
                            activeProxyDropdownId = conn.id
                            const rect = e.currentTarget.getBoundingClientRect()
                            dropdownPos = {
                              top: rect.bottom + 4,
                              right: Math.max(8, window.innerWidth - rect.right)
                            }
                          }
                        }}
                        disabled={updatingProxyConnId === conn.id}
                        class="flex w-full flex-col items-center rounded px-2 py-1 transition-colors hover:bg-black/5 dark:hover:bg-white/5 disabled:opacity-60 {proxyBadge.hasAnyProxy ? 'text-primary' : 'text-text-muted hover:text-primary'} cursor-pointer"
                      >
                        <span class="material-symbols-outlined text-[18px] {updatingProxyConnId === conn.id ? 'animate-spin' : ''}">
                          {updatingProxyConnId === conn.id ? 'progress_activity' : 'lan'}
                        </span>
                        <span class="text-[10px] leading-tight">Proxy</span>
                      </button>

                      {#if activeProxyDropdownId === conn.id}
                        <!-- Backdrop -->
                        <div
                          class="fixed inset-0 z-[80]"
                          onclick={() => (activeProxyDropdownId = null)}
                          role="presentation"
                        ></div>
                        <div
                          class="fixed z-[85] max-w-[78vw] min-w-[180px] rounded-lg border border-border bg-bg py-1 shadow-lg"
                          style="top: {dropdownPos.top}px; right: {dropdownPos.right}px;"
                        >
                          {#if proxyPools.length === 0}
                            <div class="px-3 py-2 text-xs text-text-muted">
                              <p>No proxy pools available.</p>
                              <a
                                href="/dashboard/proxypools"
                                class="mt-1.5 inline-flex items-center gap-1 font-medium text-primary hover:underline"
                              >
                                <span class="material-symbols-outlined text-sm">add</span>
                                Create proxy pool
                              </a>
                            </div>
                          {:else}
                            <button
                              type="button"
                              onclick={() => assignProxyPool(conn, null)}
                              class="flex w-full items-center px-3 py-1.5 text-xs hover:bg-surface-2 transition-colors cursor-pointer {!assignedPoolId ? 'text-primary font-medium' : 'text-text-main'}"
                            >
                              None
                            </button>
                            {#each proxyPools as pool}
                              <button
                                type="button"
                                onclick={() => assignProxyPool(conn, pool.id)}
                                class="flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-surface-2 transition-colors cursor-pointer {assignedPoolId === pool.id ? 'text-primary font-medium' : 'text-text-main'}"
                              >
                                <span class="truncate">{pool.name}</span>
                                {#if !pool.isActive}
                                  <span class="text-[10px] text-text-muted shrink-0">(inactive)</span>
                                {/if}
                              </button>
                            {/each}
                          {/if}
                        </div>
                      {/if}
                    </div>
                    <Menu
                      label="Actions for this connection"
                      triggerIcon="more_horiz"
                      hideLabel
                      minWidth="13rem"
                    >
                      {#if isFreebuff}
                        <MenuItem
                          label="Manage session"
                          icon="lock_clock"
                          onSelect={() => selectFreebuffAccount(conn.id)}
                        />
                        <div class="my-1 border-t border-border-subtle" role="separator"></div>
                      {/if}

                      <MenuItem
                        label="Refresh"
                        icon="sync"
                        disabled={isTestingOneByOne || status?.state === 'testing'}
                        onSelect={() => refreshOneConnection(conn)}
                      />
                      <MenuItem label="Edit" icon="edit" onSelect={() => openEditConnection(conn)} />

                      <div class="my-1 border-t border-border-subtle" role="separator"></div>

                      <MenuItem label="Delete" icon="delete" danger onSelect={() => handleDeleteConnection(conn)} />
                    </Menu>
                  </div>

                  <!-- Active toggle switch -->
                  <div class="flex items-center gap-3">
                    <button
                      type="button"
                      role="switch"
                      aria-label="Toggle connection active"
                      aria-checked={isConnActive}
                      onclick={() => toggleConnectionActive(conn)}
                      class="relative inline-flex shrink-0 cursor-pointer rounded-full transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-brand-500/30 {isConnActive ? 'bg-brand-500' : 'bg-surface-3'} w-8 h-4"
                    >
                      <span
                        class="pointer-events-none inline-block rounded-full bg-white shadow-sm transform transition duration-200 ease-in-out {isConnActive ? 'translate-x-4' : 'translate-x-0.5'} size-3 mt-0.5"
                      ></span>
                    </button>
                  </div>
                </div>
              </div>
            </div>
          </div>
        {/each}
      </div>
    {/if}

    <!-- Bottom Add Button (upstream: compatible nodes add keys only via the details card) -->
    {#if !isCompatibleNode}
    <div class="mt-4 grid grid-cols-1 gap-2 sm:flex">
      {#if providerId === 'freebuff'}
        <button
          type="button"
          onclick={startFreebuffFlow}
          disabled={isAuthorizingFreebuff}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">vpn_key</span>
          {isAuthorizingFreebuff ? 'Polling Authorization...' : 'Authorize Freebuff CLI'}
        </button>
      {:else if providerId === 'kiro'}
        <button
          type="button"
          onclick={handleAddConnectionClick}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">login</span>
          Connect Kiro
        </button>
      {:else if hasDualAuthModes}
        <button
          type="button"
          onclick={handleAddConnectionClick}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">lock</span>
          {oauthButtonLabel}
        </button>
        <button
          type="button"
          onclick={openAddKeyModal}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">key</span>
          {apiKeyButtonLabel}
        </button>
      {:else}
        <button
          type="button"
          onclick={handleAddConnectionClick}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">add</span>
          Add
        </button>
      {/if}
    </div>
    {/if}
  </div>
  {/if}

  <!-- 4. Models Card: compatible nodes use upstream CompatibleModelsSection layout -->
  {#if isCompatibleNode}
  <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6">
    <div class="flex flex-col gap-4">
      <p class="text-sm text-text-muted">
        Add {isAnthropicCompatibleNode ? 'Anthropic' : 'OpenAI'}-compatible models manually or import them from the /models endpoint.
      </p>
      <div class="flex items-end gap-2 flex-wrap">
        <div class="flex-1 min-w-[240px]">
          <label for="new-compatible-model-input" class="text-xs text-text-muted mb-1 block">Model ID</label>
          <input
            id="new-compatible-model-input"
            type="text"
            bind:value={newCompatibleModel}
            onkeydown={(e) => { if (e.key === 'Enter') handleAddCompatibleModel() }}
            placeholder={isAnthropicCompatibleNode ? 'claude-3-opus-20240229' : 'gpt-4o'}
            class="w-full px-3 py-2 text-sm border border-border rounded-lg bg-background focus:outline-none focus:border-primary"
          />
        </div>
        <button
          type="button"
          onclick={handleAddCompatibleModel}
          disabled={!newCompatibleModel.trim() || isAddingCompatibleModel}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px] disabled:opacity-50 disabled:cursor-not-allowed"
        >
          <span class="material-symbols-outlined text-[18px]">add</span>
          {isAddingCompatibleModel ? 'Adding...' : 'Add'}
        </button>
        <button
          type="button"
          onclick={handleImportCompatibleModels}
          disabled={!canImportCompatible || isImportingCompatibleModels}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-8 px-4 text-xs rounded-[8px] disabled:opacity-50 disabled:cursor-not-allowed"
        >
          <span class="material-symbols-outlined text-[18px]">download</span>
          {isImportingCompatibleModels ? 'Importing...' : 'Import from /models'}
        </button>
      </div>
      {#if !canImportCompatible}
        <p class="text-xs text-text-muted">Add a connection to enable importing models.</p>
      {/if}
      {#if compatibleRows.length > 0}
        <div class="flex flex-col gap-3">
          {#each compatibleRows as row (row.source + ':' + row.id)}
            {@const tStatus = compatibleTestResults[row.id]}
            {@const tError = compatibleTestErrors[row.id]}
            {@const isTestingRow = compatibleTestId === row.id}
            <div class="flex items-start gap-3 p-3 rounded-lg border {tStatus === 'ok' ? 'border-green-500/40' : tStatus === 'error' ? 'border-red-500/40' : 'border-border'} hover:bg-sidebar/50">
              <span
                class="material-symbols-outlined text-base text-text-muted mt-0.5"
                style={tStatus === 'ok' ? 'color:#22c55e' : tStatus === 'error' ? 'color:#ef4444' : undefined}
              >
                {tStatus === 'ok' ? 'check_circle' : tStatus === 'error' ? 'cancel' : 'smart_toy'}
              </span>
              <div class="flex-1 min-w-0">
                <p class="text-sm font-medium truncate">{row.id}</p>
                <div class="flex items-center gap-1 mt-1">
                  <code class="text-xs text-text-muted font-mono bg-sidebar px-1.5 py-0.5 rounded">{displayAlias()}/{row.id}</code>
                  <div class="relative group/btn">
                    <button
                      type="button"
                      onclick={() => copyCompatibleModel(row.id)}
                      class="p-0.5 hover:bg-sidebar rounded text-text-muted hover:text-primary cursor-pointer"
                    >
                      <span class="material-symbols-outlined text-sm">{copiedModelId === row.id ? 'check' : 'content_copy'}</span>
                    </button>
                    <span class="pointer-events-none absolute top-5 left-1/2 -translate-x-1/2 text-[10px] text-text-muted whitespace-nowrap opacity-0 group-hover/btn:opacity-100 transition-opacity">
                      {copiedModelId === row.id ? 'Copied!' : 'Copy'}
                    </span>
                  </div>
                  {#if providerConnections.length > 0}
                    <div class="relative group/btn">
                      <button
                        type="button"
                        onclick={() => handleTestCompatibleModel(row.id)}
                        disabled={isTestingRow}
                        class="p-0.5 hover:bg-sidebar rounded text-text-muted hover:text-primary transition-colors cursor-pointer"
                      >
                        <span class="material-symbols-outlined text-sm" style={isTestingRow ? 'animation: spin 1s linear infinite' : undefined}>
                          {isTestingRow ? 'progress_activity' : 'science'}
                        </span>
                      </button>
                      <span class="pointer-events-none absolute top-5 left-1/2 -translate-x-1/2 text-[10px] text-text-muted whitespace-nowrap opacity-0 group-hover/btn:opacity-100 transition-opacity">
                        {isTestingRow ? 'Testing...' : 'Test'}
                      </span>
                    </div>
                  {/if}
                </div>
                {#if tError}
                  <div class="mt-2 flex items-start gap-1.5 text-xs text-red-500 bg-red-500/10 px-2.5 py-1.5 rounded-md border border-red-500/20">
                    <span class="material-symbols-outlined text-sm shrink-0 mt-0.5">error</span>
                    <span class="break-words font-medium leading-relaxed">{tError}</span>
                  </div>
                {/if}
              </div>
              <button
                type="button"
                onclick={() => handleDeleteCompatibleModel(row)}
                class="p-1 hover:bg-red-50 rounded text-red-500 cursor-pointer"
                title="Remove model"
              >
                <span class="material-symbols-outlined text-sm">delete</span>
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>
  {:else}
  <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] p-6">
    <!-- Header -->
    <div class="mb-4 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
      <div class="flex items-center gap-3">
        <h2 class="text-lg font-semibold">Available Models</h2>
        {#if modelDeprecationCount > 0}
          <span
            class="inline-flex items-center gap-1 rounded-full bg-warning/10 px-2 py-0.5 text-[11px] font-medium text-warning border border-warning/25"
            title="These models were retired by the provider and cannot serve a request"
          >
            <span class="material-symbols-outlined text-[13px] leading-none" aria-hidden="true">error</span>
            {modelDeprecationCount} deprecated
          </span>
        {/if}
        {#if providerThinkingLevels}
        <select
          title="Appends (level) suffix to copied model names"
          value={thinkingLevel}
          onchange={handleThinkingChange}
          class="rounded-md border border-border bg-background px-2 py-1 text-xs focus:border-primary focus:outline-none cursor-pointer"
        >
          {#each providerThinkingLevels as opt (opt)}
            <option value={opt}>Thinking: {opt.charAt(0).toUpperCase() + opt.slice(1)}</option>
          {/each}
        </select>
        {/if}
      </div>

      <div class="flex gap-2">
        <button
          type="button"
          onclick={handleToggleAllModels}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px]"
        >
          <span class="material-symbols-outlined text-[18px]">block</span>
          {allDisabled ? 'Enable All' : 'Disable All'}
        </button>
      </div>
    </div>

    {#if activeModelTestError}
      {@const isBlockedSeverity = checkAllSummary?.severity === 'blocked'}
      {@const blockedCount = getBlockedModelIds(modelTestStatuses).length}
      <div class="mb-3 flex items-start gap-2.5 rounded-lg border {isBlockedSeverity ? 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400' : 'border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400'} p-3 text-xs">
        <span class="material-symbols-outlined shrink-0 text-base">{isBlockedSeverity ? 'schedule' : 'error'}</span>
        <div class="flex-1 font-medium leading-relaxed flex items-center justify-between gap-2 flex-wrap">
          <span>{activeModelTestError}</span>
          {#if blockedCount > 0}
            <button
              type="button"
              onclick={handleRetryBlockedModels}
              disabled={isCheckingAll}
              class="px-2 py-0.5 rounded bg-amber-500/20 hover:bg-amber-500/30 text-amber-700 dark:text-amber-300 font-semibold text-xs cursor-pointer disabled:opacity-50"
            >
              Retry blocked ({blockedCount})
            </button>
          {/if}
        </div>
        <button
          type="button"
          onclick={() => { activeModelTestError = null; checkAllSummary = null; }}
          class="hover:opacity-75 cursor-pointer shrink-0"
          title="Dismiss"
        >
          <span class="material-symbols-outlined text-sm">close</span>
        </button>
      </div>
    {/if}
    {#if isFreebuff}
      <div class="mb-4">
        <FreebuffSessionBanner
          session={freebuffSession}
          isLoading={isLoadingSession}
          expiresInMin={sessionExpiresInMin}
          onRefresh={loadFreebuffSession}
          models={visibleModels.map((m) => ({ id: m.id, name: m.name }))}
          onSwitch={switchFreebuffModel}
          connections={providerConnections.map((c) => {
            const specific = c.providerSpecificData as Record<string, any> | undefined
            return {
              id: c.id,
              name: formatEmailLabel(c.name || c.email || 'Freebuff Account', $emailPrivacy),
              isActive: c.isActive === 1,
              currentModel: freebuffSessions[c.id]?.currentModel || (specific?.freebuffModel as string) || (specific?.assignedModel as string),
              status: freebuffSessions[c.id]?.status,
            }
          })}
          selectedConnectionId={targetFreebuffConn?.id}
          onSelectConnection={selectFreebuffAccount}
        />
      </div>
    {/if}
    <!-- Models flex-wrap list matching upstream -->
    <div class="flex flex-wrap gap-3">
      {#each visibleModels as model (model.id)}
        {@const level = resolveThinkingSuffix(model.id)}
        {@const fullModelId = `${storageAlias}/${model.id}${level ? `(${level})` : ''}`}
        {@const rowCaps = modelCaps[model.id] ?? model.caps}
        {@const testStatus = modelTestStatuses[model.id]}
        {@const isTestingThis = testStatus === 'testing'}
        {@const isSessionActive = checkIsActiveSession(model.id)}
        <div
          class="group min-w-0 max-w-full rounded-lg border px-3 py-2 {testStatus === 'ok' ? 'border-green-500/40' : testStatus === 'blocked' ? 'border-amber-500/40' : testStatus === 'error' ? 'border-red-500/40' : 'border-border'} hover:bg-sidebar/50 transition-colors"
        >
          <div class="flex min-w-0 items-start gap-2 sm:items-center">
            <span class="material-symbols-outlined shrink-0 text-base text-text-muted">smart_toy</span>
            <div class="flex min-w-0 flex-1 flex-col gap-1">
              <div class="flex items-center gap-1.5 flex-wrap">
                <code class="max-w-[72vw] truncate rounded bg-sidebar px-1.5 py-0.5 font-mono text-xs text-text-muted sm:max-w-[360px]">
                  {fullModelId}
                </code>
                {#if isSessionActive}
                  <span class="inline-flex items-center gap-1 text-[10px] font-semibold px-2 py-0.5 rounded-full bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border border-emerald-500/30">
                    <span class="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse"></span>
                    Active Session
                  </span>
                {/if}
                <DeprecatedBadge provider={providerId} model={model.id} />
              </div>
              <span class="flex min-w-0 items-center text-[9px] gap-1 pl-1">
                <span class="truncate text-[9px] italic text-text-muted/70">{model.name}</span>
                <span class="inline-flex items-center gap-0.5">
                  {#if rowCaps?.vision}
                    <div class="relative inline-flex group/tt">
                      <span class="material-symbols-outlined leading-none cursor-help text-text-muted/70" style="font-size: 12px;">visibility</span>
                      <div class="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 z-50 w-max max-w-56 rounded px-2 py-1 text-[11px] leading-snug bg-gray-900 text-white opacity-0 group-hover/tt:opacity-100 transition-opacity duration-150 whitespace-normal shadow-lg">
                        Vision — Supports image input
                      </div>
                    </div>
                  {/if}
                  {#if rowCaps?.reasoning}
                    <div class="relative inline-flex group/tt">
                      <span class="material-symbols-outlined leading-none cursor-help text-text-muted/70" style="font-size: 12px;">neurology</span>
                      <div class="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 z-50 w-max max-w-56 rounded px-2 py-1 text-[11px] leading-snug bg-gray-900 text-white opacity-0 group-hover/tt:opacity-100 transition-opacity duration-150 whitespace-normal shadow-lg">
                        Reasoning — Supports reasoning / thinking
                      </div>
                    </div>
                  {/if}
                </span>
              </span>
            </div>
              {#if modelTestErrors[model.id]}
                <span class="text-[9px] {testStatus === 'blocked' ? 'text-amber-500 dark:text-amber-400' : 'text-red-500 dark:text-red-400'} font-medium pl-1 truncate max-w-[280px]" title={modelTestErrors[model.id]}>
                  {modelTestErrors[model.id]}
                </span>
              {/if}

            <!-- Test button -->
            <div class="relative shrink-0 group/btn">
              <button
                type="button"
                onclick={() => testModel(model.id)}
                disabled={isTestingThis}
                class="rounded p-0.5 text-text-muted transition-opacity hover:bg-sidebar hover:text-primary opacity-100 sm:opacity-0 sm:group-hover:opacity-100 cursor-pointer"
                title={modelTestErrors[model.id] || (testStatus === 'ok' ? 'Test Passed' : 'Test')}
              >
                {#if isTestingThis}
                  <span class="material-symbols-outlined text-sm animate-spin text-primary">progress_activity</span>
                {:else if testStatus === 'ok'}
                  <span class="material-symbols-outlined text-sm text-green-500">check</span>
                {:else if testStatus === 'blocked'}
                  <span class="material-symbols-outlined text-sm text-amber-500">schedule</span>
                {:else if testStatus === 'error'}
                  <span class="material-symbols-outlined text-sm text-red-500">error</span>
                {:else}
                  <span class="material-symbols-outlined text-sm">science</span>
                {/if}
              </button>
              <span class="pointer-events-none absolute mt-1 top-5 left-1/2 -translate-x-1/2 text-[10px] text-text-muted whitespace-nowrap opacity-0 group-hover/btn:opacity-100 transition-opacity z-20 bg-surface-2 px-1 rounded shadow border border-border">
                {#if isTestingThis}
                  Testing...
                {:else if testStatus === 'ok'}
                  Passed
                {:else if testStatus === 'blocked'}
                  {modelTestErrors[model.id] || 'Blocked — account in cooldown'}
                {:else if testStatus === 'error'}
                  {modelTestErrors[model.id] || 'Failed'}
                {:else}
                  Test
                {/if}
              </span>
            </div>
            <!-- Copy button -->
            <div class="relative shrink-0 group/btn">
              <button
                type="button"
                onclick={() => copyModelId(model.id)}
                class="rounded p-0.5 text-text-muted hover:bg-sidebar hover:text-primary cursor-pointer"
              >
                {#if copiedModelId === model.id}
                  <span class="material-symbols-outlined text-sm text-green-500">check</span>
                {:else}
                  <span class="material-symbols-outlined text-sm">content_copy</span>
                {/if}
              </button>
              <span class="pointer-events-none absolute mt-1 top-5 left-1/2 -translate-x-1/2 text-[10px] text-text-muted whitespace-nowrap opacity-0 group-hover/btn:opacity-100 transition-opacity">
                Copy
              </span>
            </div>

            <!-- Disable button -->
            <button
              type="button"
              onclick={() => handleDisableModel(model.id)}
              class="ml-auto rounded p-0.5 text-text-muted opacity-100 transition-opacity hover:bg-red-500/10 hover:text-red-500 sm:opacity-0 sm:group-hover:opacity-100 cursor-pointer"
              title="Disable this model"
            >
              <span class="material-symbols-outlined text-sm">close</span>
            </button>
            {#if testStatus === 'error'}
              <!-- Unusable model: delete it. Custom models are removed from the
                   store; built-in registry models are static, so those are
                   disabled instead (the closest equivalent). -->
              <button
                type="button"
                onclick={() => handleDeleteModel(model.id)}
                class="rounded p-0.5 text-red-500 opacity-100 transition-opacity hover:bg-red-500/10 hover:text-red-600 sm:opacity-0 sm:group-hover:opacity-100 cursor-pointer"
                title={model.isCustom ? 'Delete unusable model' : 'Unusable — disable this registry model'}
              >
                <span class="material-symbols-outlined text-sm">delete</span>
              </button>
            {/if}
          </div>
        </div>
      {/each}

      <!-- Add Model button inside the same flex-wrap -->
      <button
        type="button"
        onclick={() => (showAddCustomModelModal = true)}
        class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-dashed border-primary/40 px-3 py-2 text-xs text-primary transition-colors hover:border-primary hover:bg-primary/5 sm:w-auto cursor-pointer"
      >
        <span class="material-symbols-outlined text-sm">add</span>
        Add Model
      </button>

      {#if (providerId === 'cline' || providerId === 'clinepass' || providerId === 'qoder' || providerId === 'qoder-cn') && providerConnections.some((c) => c.isActive !== 0)}
        <button
          type="button"
          onclick={handleImportLiveCatalogModels}
          disabled={isImportingLiveCatalogModels}
          class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 hover:bg-surface-3 px-3 py-2 text-xs text-text-main transition-colors sm:w-auto cursor-pointer disabled:opacity-50"
        >
          <span class="material-symbols-outlined text-sm {isImportingLiveCatalogModels ? 'animate-spin' : ''}">
            {isImportingLiveCatalogModels ? 'progress_activity' : 'download'}
          </span>
          {isImportingLiveCatalogModels
            ? 'Fetching...'
            : providerId === 'qoder' || providerId === 'qoder-cn'
              ? 'Fetch Qoder Models'
              : 'Import from /models'}
        </button>
      {/if}

      {#if visibleModels.length > 0}
        <button
          type="button"
          onclick={handleCheckAllModels}
          disabled={isCheckingAll}
          class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 hover:bg-surface-3 px-3 py-2 text-xs text-text-main transition-colors sm:w-auto cursor-pointer disabled:opacity-50"
          title="Send a minimal request to every model and mark the ones that fail"
        >
          <span class="material-symbols-outlined text-sm">{isCheckingAll ? 'progress_activity' : 'troubleshoot'}</span>
          {isCheckingAll
            ? `Checking ${checkAllProgress.done}/${checkAllProgress.total}...`
            : `Check All Models (${visibleModels.length})`}
        </button>
      {/if}

      {#if canListLiveModels && providerConnections.some((c) => c.isActive !== 0)}
        <button
          type="button"
          onclick={handleCheckLatestModels}
          disabled={isCheckingLatest}
          class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 hover:bg-surface-3 px-3 py-2 text-xs text-text-main transition-colors sm:w-auto cursor-pointer disabled:opacity-50"
          title="Fetch the provider's live catalog and list models you have not added yet"
        >
          <span class="material-symbols-outlined text-sm">{isCheckingLatest ? 'progress_activity' : 'cached'}</span>
          {isCheckingLatest ? 'Checking...' : 'Check Latest Models'}
        </button>
      {/if}

      {#if providerConnections.some((c) => c.isActive !== 0)}
        <button
          type="button"
          onclick={handleSyncModelDeprecations}
          disabled={isSyncingModels}
          class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 hover:bg-surface-3 px-3 py-2 text-xs text-text-main transition-colors sm:w-auto cursor-pointer disabled:opacity-50"
          title="Re-read the provider's live catalog and clear the Deprecated badge from any model it still serves"
        >
          <span class="material-symbols-outlined text-sm {isSyncingModels ? 'animate-spin' : ''}">
            {isSyncingModels ? 'progress_activity' : 'sync'}
          </span>
          {isSyncingModels ? 'Syncing...' : 'Sync Models'}
        </button>
      {/if}
    </div>

    {#if latestModels.length > 0 || latestError}
      <div class="w-full mt-2 rounded-lg border border-border bg-surface-2 p-3">
        {#if latestModels.length > 0}
          <p class="text-xs text-text-muted mb-2">{latestModels.length} new model(s) not yet added:</p>
          <div class="flex flex-col gap-1.5">
            {#each latestModels as m (m.id)}
              <div class="flex items-center gap-2">
                <code class="flex-1 truncate text-xs font-mono bg-sidebar px-1.5 py-1 rounded">{m.id}</code>
                <button
                  type="button"
                  onclick={() => handleAddLatestModel(m.id, m.name)}
                  class="shrink-0 inline-flex items-center gap-1 rounded px-2 py-1 text-xs text-primary hover:bg-primary/10 border border-primary/40 cursor-pointer"
                >
                  <span class="material-symbols-outlined text-[13px]">add</span>
                  Add
                </button>
              </div>
            {/each}
          </div>
        {:else}
          <p class="text-xs text-text-muted">{latestError}</p>
        {/if}
      </div>
    {/if}
    <!-- Suggested models from provider API — show only models not yet added -->
    {#if suggestedNotAdded.length > 0}
      <div class="w-full mt-2">
        <p class="text-xs text-text-muted mb-2">Suggested free models (≥200k context):</p>
        <div class="flex flex-wrap gap-2">
          {#each suggestedNotAdded as m (m.id)}
            <button
              type="button"
              onclick={() => submitAddCustomModel(m.id)}
              class="flex items-center gap-1 px-2.5 py-1.5 rounded-lg border border-black/10 dark:border-white/10 text-xs text-text-muted hover:text-primary hover:border-primary/40 hover:bg-primary/5 transition-colors cursor-pointer"
              title={m.contextLength ? `${m.name || m.id} · ${Math.round(m.contextLength / 1000)}k ctx` : (m.name || m.id)}
            >
              <span class="material-symbols-outlined text-[13px]">add</span>
              {m.id.split('/').pop()}
            </button>
          {/each}
        </div>
      </div>
    {/if}

    <!-- Disabled Models pills -->
    {#if disabledModelIds.length > 0}
      <div class="w-full mt-4">
        <p class="text-xs text-text-muted mb-2">Disabled models ({disabledModelIds.length}):</p>
        <div class="flex flex-wrap gap-2">
          {#each disabledModelIds as dId}
            <button
              type="button"
              onclick={() => handleEnableModel(dId)}
              class="flex items-center gap-1 px-2.5 py-1.5 rounded-lg border border-black/10 dark:border-white/10 text-xs text-text-muted hover:text-primary hover:border-primary/40 hover:bg-primary/5 transition-colors cursor-pointer"
            >
              <span class="material-symbols-outlined text-[13px]">add</span>
              {dId.split('/').pop()}
            </button>
          {/each}
        </div>
      </div>
    {/if}
  </div>
  {/if}
</div>

<!-- Modals -->

<!-- 1. Risk Notice Modal -->
{#if showRiskNoticeModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 backdrop-blur-[2px] fade-in"
      onclick={() => (showRiskNoticeModal = false)}
      role="presentation"
    ></div>
    <div class="relative w-full bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] fade-in max-w-md p-6">
      <div class="flex items-center justify-between pb-3 border-b border-border-subtle mb-4">
        <h2 class="text-lg font-semibold text-text-main">Risk Notice</h2>
        <button
          type="button"
          onclick={() => (showRiskNoticeModal = false)}
          class="p-1 rounded text-text-muted hover:text-text-main cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>
      <p class="text-xs text-red-600 dark:text-yellow-400 leading-relaxed mb-6">
        ⚠️ Risk Notice: This provider uses a subscription/OAuth session not officially licensed for proxy/router use. Account may be restricted or banned. Use at your own risk.
      </p>
      <div class="flex gap-2 justify-end">
        <button
          type="button"
          onclick={() => (showRiskNoticeModal = false)}
          class="px-3 py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
        >
          Cancel
        </button>
        <button
          type="button"
          onclick={confirmRiskAndProceed}
          class="px-3 py-1.5 text-xs font-semibold rounded-[8px] bg-red-600 hover:bg-red-700 text-white shadow-sm cursor-pointer"
        >
          I Understand, Continue
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- 2. OAuth / Antigravity Connect Modal -->
{#if showOAuthModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
      <div
        class="absolute inset-0 bg-black/50 backdrop-blur-[2px] fade-in"
        onclick={closeOAuthModal}
        role="presentation"
      ></div>
    <div class="relative w-full bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] fade-in max-w-lg p-6">
      <div class="flex items-center justify-between pb-3 border-b border-border-subtle mb-4">
        <h2 class="text-lg font-semibold text-text-main">Connect {providerName}</h2>
        <button
          type="button"
          onclick={closeOAuthModal}
          class="p-1 rounded text-text-muted hover:text-text-main cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>
      {#if providerId === 'kiro' && !kiroMethod && !deviceUserCode}
        <p class="text-sm text-text-muted mb-3">Choose your authentication method:</p>
        <div class="space-y-2 mb-4">
          <button type="button" onclick={() => { kiroMethod = 'builder-id'; startKiroDeviceFlow('builder-id') }} disabled={isConnecting} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors disabled:opacity-50 cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">shield</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">AWS Builder ID</h3><p class="text-xs text-text-muted">Recommended. Free AWS account, device-code flow.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'idc' }} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">business</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">AWS IAM Identity Center</h3><p class="text-xs text-text-muted">Enterprise with your organization start URL.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'api-key' }} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">key</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">API Key</h3><p class="text-xs text-text-muted">Long-lived headless Kiro/CodeWhisperer key.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'social-google'; startKiroSocial('google') }} disabled={isConnecting} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors disabled:opacity-50 cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">account_circle</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">Google Account</h3><p class="text-xs text-text-muted">Log in with Google, paste callback manually.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'social-github'; startKiroSocial('github') }} disabled={isConnecting} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors disabled:opacity-50 cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">code</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">GitHub Account</h3><p class="text-xs text-text-muted">Log in with GitHub, paste callback manually.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'import' }} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">file_upload</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">Import Token</h3><p class="text-xs text-text-muted">Paste refresh token from Kiro IDE.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'import-cli-proxy' }} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">data_object</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">Import CLIProxyAPI JSON</h3><p class="text-xs text-text-muted">external_idp auth JSON (Microsoft login).</p></div>
            </div>
          </button>
        </div>
      {/if}
      {#if providerId === 'kiro' && kiroMethod === 'idc' && !deviceUserCode}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          <div>
            <p class="text-xs font-medium mb-1">IDC Start URL <span class="text-red-500">*</span></p>
            <input bind:value={kiroIdcStartUrl} placeholder="https://your-org.awsapps.com/start" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          </div>
          <div>
            <p class="text-xs font-medium mb-1">AWS Region</p>
            <input bind:value={kiroIdcRegion} placeholder="us-east-1" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          </div>
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={() => startKiroDeviceFlow('idc')} disabled={isConnecting || !kiroIdcStartUrl.trim()} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Connecting…' : 'Continue'}</button>
          </div>
        </div>
      {/if}
      {#if providerId === 'kiro' && kiroMethod === 'api-key'}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          <input bind:value={kiroApiKey} type="password" placeholder="Kiro / CodeWhisperer API key" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          <input bind:value={kiroApiKeyRegion} placeholder="Region (default us-east-1)" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={submitKiroApiKey} disabled={isConnecting} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Saving…' : 'Save'}</button>
          </div>
        </div>
      {/if}
      {#if providerId === 'kiro' && kiroMethod === 'import'}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          <button type="button" onclick={startKiroAutoImport} disabled={isConnecting} class="w-full py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 border border-border disabled:opacity-50 cursor-pointer">{isConnecting ? 'Reading…' : 'Auto-detect from this host'}</button>
          <input bind:value={kiroRefreshToken} placeholder="Refresh token (aorAAAAAG…)" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={submitKiroImport} disabled={isConnecting} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Importing…' : 'Import'}</button>
          </div>
        </div>
      {/if}
      {#if providerId === 'kiro' && kiroMethod === 'import-cli-proxy'}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          <textarea bind:value={kiroCliProxyJson} rows={4} placeholder="Paste CLIProxyAPI auth JSON (external_idp)" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"></textarea>
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={submitKiroCliProxyImport} disabled={isConnecting} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Importing…' : 'Import'}</button>
          </div>
        </div>
      {/if}
      {#if providerId === 'kiro' && (kiroMethod === 'social-google' || kiroMethod === 'social-github')}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          {#if kiroSocialAuthUrl}
            <p class="text-xs text-text-muted">Open this URL, log in, then paste the callback URL (kiro://…) below:</p>
            <div class="flex gap-2">
              <input readonly value={kiroSocialAuthUrl} class="flex-1 px-2.5 py-1.5 text-xs border border-border rounded-md bg-background text-text-muted select-all font-mono" />
              <button type="button" onclick={() => window.open(kiroSocialAuthUrl, '_blank')} class="px-3 py-1.5 text-xs font-semibold rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Open</button>
            </div>
          {/if}
          <input bind:value={kiroSocialCallback} placeholder="kiro://kiro.kiroAgent/authenticate-success?code=…" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null; kiroSocialAuthUrl = '' }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={submitKiroSocialCallback} disabled={isConnecting} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Exchanging…' : 'Exchange code'}</button>
          </div>
        </div>
      {/if}
      {#if providerId !== 'kiro' || kiroMethod || deviceUserCode}
      <div class="flex items-center gap-2 px-3 py-2 border border-border rounded-lg bg-sidebar/50 mb-4">
        <span class="material-symbols-outlined text-base text-primary animate-spin">progress_activity</span>
        <span class="text-sm">
          {providerId === 'freebuff'
            ? 'Waiting for Freebuff authorization… (auto-polling active)'
            : deviceUserCode
              ? `Waiting for device authorization… (auto-check every ${deviceInterval}s)`
              : 'Waiting for popup authorization…'}
        </span>
      </div>

      <div class="flex items-center gap-3 my-3">
        <div class="flex-1 h-px bg-border"></div>
        <span class="text-xs text-text-muted uppercase tracking-wider">
          {providerId === 'freebuff'
            ? 'Authorization link & manual check'
            : isClineOAuth
              ? 'Log in via Cline, then paste the callback'
              : oauthAuthUrl
                ? 'Or paste callback URL manually'
                : 'Manual token import'}
        </span>
        <div class="flex-1 h-px bg-border"></div>
      </div>

      <div class="space-y-4">
        {#if providerId === 'cursor'}
          <div class="space-y-2 p-3 border border-border rounded-md bg-sidebar/50">
            <p class="text-[11px] text-text-muted">Get it from Cursor IDE's <span class="font-mono">state.vscdb</span> (<span class="font-mono">cursorAuth/accessToken</span> + <span class="font-mono">storage.serviceMachineId</span>):</p>
            <input
              bind:value={specialToken}
              placeholder="Access token (cursorAuth/accessToken)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
            <input
              bind:value={specialExtra}
              placeholder="Machine ID (storage.serviceMachineId)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
            <button
              type="button"
              onclick={cursorAutoImportNow}
              disabled={isConnecting}
              class="w-full py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border disabled:opacity-50 cursor-pointer"
            >
              {isConnecting ? 'Reading…' : 'Auto-import from Cursor on this host'}
            </button>
          </div>
        {/if}
        {#if providerId === 'gitlab'}
          <div class="space-y-2 p-3 border border-border rounded-md bg-sidebar/50">
            <p class="text-[11px] text-text-muted">Or use a Personal Access Token (in addition to the OAuth login above):</p>
            <input
              bind:value={specialToken}
              type="password"
              placeholder="GitLab PAT (scope api, read_user)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
            <input
              bind:value={specialBaseUrl}
              placeholder="Base URL (default https://gitlab.com)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
          </div>
        {/if}
        {#if providerId === 'iflow'}
          <div class="space-y-2 p-3 border border-border rounded-md bg-sidebar/50">
            <p class="text-[11px] text-text-muted">Or use a platform.iflow.cn cookie (in addition to the OAuth login above):</p>
            <input
              bind:value={specialToken}
              placeholder="Cookie (must contain BXAuth=...)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
          </div>
        {/if}
        {#if deviceUserCode}
          <div class="p-3 border border-border rounded-md bg-sidebar/50 text-center">
            <p class="text-[11px] text-text-muted mb-1">Enter this code on the login page that opened:</p>
            <p class="text-2xl font-mono font-bold tracking-[0.3em] text-text-main select-all">{deviceUserCode}</p>
          </div>
        {/if}
        {#if oauthAuthUrl}
          <div>
            <p class="text-sm font-medium mb-1">Step 1: Open this URL in your browser</p>
          <div class="flex gap-2">
            <input
              readonly
              value={oauthAuthUrl}
              class="flex-1 px-2.5 py-1.5 text-xs border border-border rounded-md bg-background text-text-muted select-all font-mono"
            />
            <button
              type="button"
              onclick={() => window.open(oauthAuthUrl, '_blank')}
              class="flex items-center gap-1 px-3 py-1.5 text-xs font-semibold rounded-md border border-border bg-surface-2 hover:bg-surface-3 text-text-main cursor-pointer"
            >
              <span class="material-symbols-outlined text-sm">open_in_new</span>
              Open
            </button>
            <button
              type="button"
              onclick={copyAuthUrl}
              class="flex items-center gap-1 px-3 py-1.5 text-xs font-semibold rounded-md border border-border bg-surface-2 hover:bg-surface-3 text-text-main cursor-pointer"
            >
              <span class="material-symbols-outlined text-sm">{copiedAuthUrl ? 'check' : 'content_copy'}</span>
              {copiedAuthUrl ? 'Copied' : 'Copy'}
            </button>
          </div>
        </div>
        {/if}

        {#if providerId === 'gitlab'}
          <div class="grid grid-cols-1 gap-2 p-3 border border-border rounded-md bg-sidebar/50 mb-1">
            <p class="text-[11px] text-text-muted">Self-hosted GitLab / own OAuth app (optional — defaults to gitlab.com without a client):</p>
            <input
              bind:value={gitlabBaseUrl}
              placeholder="Base URL (default https://gitlab.com)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
            <div class="grid grid-cols-2 gap-2">
              <input
                bind:value={gitlabClientId}
                placeholder="OAuth Client ID"
                class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
              />
              <input
                bind:value={gitlabClientSecret}
                type="password"
                placeholder="OAuth Client Secret"
                class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
              />
            </div>
          </div>
        {/if}
        <div>
          <p class="text-sm font-medium mb-1">
            {providerId === 'freebuff'
              ? 'Step 2: Finish in the browser / paste URL / Code / Token'
              : isClineOAuth
                ? 'Step 2: Paste the callback URL here'
                : oauthAuthUrl
                  ? 'Step 2: Paste the callback URL here'
                  : 'Manual token import (no browser login for this provider)'}
          </p>
          <input
            bind:value={callbackInput}
            placeholder={providerId === 'freebuff'
              ? 'https://freebuff.com/onboard?auth_code=... or paste authToken'
              : `${dashboardCallback()}?code=...&state=...`}
            class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
          />
          <p class="text-[11px] text-text-muted mt-1">
            {providerId === 'freebuff'
              ? 'If the browser lands on /onboard, finish onboarding in the Freebuff tab, then paste the URL above or click Check & Connect.'
              : isClineOAuth
                ? 'Log in via the Cline tab that opened — the connection completes automatically. If it fails, copy the redirect URL (containing code=...) here and click Connect.'
                : oauthAuthUrl
                  ? 'Complete the login in the browser tab — the connection completes automatically. If it fails, copy the full URL from the browser here.'
                  : 'Paste the access token here, then click Connect.'}
          </p>
        </div>

        {#if oauthError}
          <p class="text-xs text-red-500">{oauthError}</p>
        {/if}

        <div class="flex gap-2 pt-2">
          <button
            type="button"
            onclick={() => {
              if (deviceUserCode) {
                pollDeviceOnce()
              } else {
                submitManualCallback()
              }
            }}
            disabled={isConnecting}
            class="flex-1 py-1.5 text-xs font-semibold rounded-[8px] bg-brand-500 hover:bg-brand-600 text-white shadow-sm disabled:opacity-50 cursor-pointer"
          >
            {isConnecting ? 'Checking…' : deviceUserCode ? 'Check now' : providerId === 'freebuff' ? 'Check & Connect' : 'Connect'}
          </button>
          <button
            type="button"
            onclick={() => {
              showOAuthModal = false
              stopDevicePoll()
              if (freebuffPollTimer) {
                clearInterval(freebuffPollTimer)
                freebuffPollTimer = null
              }
              isAuthorizingFreebuff = false
            }}
            class="flex-1 py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
          >
            Cancel
          </button>
        </div>
      </div>
      {/if}
    </div>
  </div>
{/if}

<!-- 3. Apply Proxy Modal -->
{#if showApplyProxyModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 backdrop-blur-[2px] fade-in"
      onclick={cancelApplyProxy}
      role="presentation"
    ></div>
    <div class="relative flex max-h-[85vh] w-full flex-col bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] fade-in max-w-lg">
      <div class="flex shrink-0 items-center justify-between px-6 pt-5 pb-3 border-b border-border-subtle">
        <h2 class="text-lg font-semibold text-text-main">
          Apply Proxy ({providerConnections.length} connections)
        </h2>
        <button
          type="button"
          onclick={cancelApplyProxy}
          disabled={isApplyingProxy}
          aria-label="Close"
          class="p-1 rounded text-text-muted hover:text-text-main disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>

      <!-- The pool list scrolls on its own. With 100+ imported pools an
           unbounded list grew this centred panel past the viewport, which
           clipped the title and every action above the fold. -->
      <div class="min-h-0 flex-1 space-y-2 overflow-y-auto custom-scrollbar px-6 py-4">
        <button
          type="button"
          onclick={handleApplyProxyRotate}
          disabled={isApplyingProxy || activeProxyPools.length === 0}
          class="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left border border-border hover:bg-surface-2 transition-colors disabled:opacity-50 cursor-pointer"
        >
          <span class="material-symbols-outlined text-primary text-lg">sync_alt</span>
          <div>
            <div class="text-xs font-medium text-text-main">One-to-one (rotate)</div>
            <div class="text-[11px] text-text-muted">Distribute active proxy pools round-robin</div>
          </div>
        </button>

        <button
          type="button"
          onclick={() => handleApplyProxyPool(null)}
          disabled={isApplyingProxy}
          class="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left border border-border hover:bg-surface-2 transition-colors cursor-pointer"
        >
          <span class="material-symbols-outlined text-red-500 text-lg">link_off</span>
          <div>
            <div class="text-xs font-medium text-text-main">None (unbind all)</div>
            <div class="text-[11px] text-text-muted">Remove proxy pool from connections</div>
          </div>
        </button>

        <input
          type="search"
          bind:value={proxyPoolSearch}
          placeholder="Filter {proxyPools.length} pools by name or URL"
          disabled={isApplyingProxy}
          class="w-full rounded-lg border border-border bg-surface-2 px-3 py-2 text-xs text-text-main placeholder:text-text-muted focus:border-primary focus:outline-none disabled:opacity-50"
        />

        {#each filteredProxyPools as pool}
          <button
            type="button"
            onclick={() => handleApplyProxyPool(pool.id)}
            disabled={isApplyingProxy || !pool.isActive}
            class="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left border border-border hover:bg-surface-2 transition-colors disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
          >
            <span class="material-symbols-outlined text-text-muted text-lg">lan</span>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <div class="text-xs font-medium text-text-main truncate">{pool.name}</div>
                {#if !pool.isActive}
                  <span class="text-[10px] text-text-muted">(inactive)</span>
                {/if}
              </div>
              <div class="text-[11px] text-text-muted truncate">{pool.proxyUrl}</div>
            </div>
          </button>
        {:else}
          <p class="py-6 text-center text-xs text-text-muted">No proxy pool matches “{proxyPoolSearch}”.</p>
        {/each}
      </div>

      <div class="flex shrink-0 items-center justify-between gap-3 px-6 py-4 border-t border-border-subtle">
        <p class="text-xs text-text-muted" aria-live="polite">
          {#if isApplyingProxy}
            Applying {applyProxyDone} / {providerConnections.length}…
          {:else}
            {filteredProxyPools.length} of {proxyPools.length} pools
          {/if}
        </p>
        <button
          type="button"
          onclick={cancelApplyProxy}
          class="px-3 py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
        >
          {isApplyingProxy ? 'Stop' : 'Cancel'}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- 4. Edit Connection Modal -->
{#if editingConnection}
  <EditConnectionModal
    connection={editingConnection}
    onClose={() => (editingConnection = null)}
    onSave={saveEditingConnection}
    onSaveError={(message) => alert(message)}
  />
{/if}

<!-- 5. Add Custom Model Modal -->
{#if !isCompatibleNode}
<AddCustomModelModal
  isOpen={showAddCustomModelModal}
  providerAlias={storageAlias}
  onClose={() => (showAddCustomModelModal = false)}
  onSave={submitAddCustomModel}
/>
{/if}

<!-- 5a. Per-provider header overrides (upstream CustomConfigCard parity) -->
<ProviderHeaderOverridesModal
  isOpen={showHeaderOverrides}
  providerId={providerId}
  onClose={() => (showHeaderOverrides = false)}
/>

<!-- 5b. Edit Compatible Node Modal (upstream EditCompatibleNodeModal parity) -->
<EditCompatibleNodeModal
  isOpen={showEditNodeModal && isCompatibleNode}
  node={selectedNode}
  isAnthropic={isAnthropicCompatibleNode}
  onClose={() => (showEditNodeModal = false)}
  onSave={handleSaveEditedNode}
/>

<!-- 6. Add Key Connection Modal (for non-oauth providers) -->
<AddConnectionModal
  isOpen={showAddKeyModal}
  providerId={providerId}
  providerName={providerName}
  isCompatible={!!selectedNode}
  isAnthropic={selectedNode?.apiType === 'responses'}
  existingNames={connections.map((c) => c.name).filter((n): n is string => !!n)}
  {proxyPools}
  error={addConnectionError}
  onClose={() => {
    addConnectionError = ''
    showAddKeyModal = false
  }}
  onSubmit={handleAddKeyConnection}
  onBulkDone={onRefresh}
/>
