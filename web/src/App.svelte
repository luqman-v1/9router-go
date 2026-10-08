<script lang="ts">
  import { onMount } from 'svelte'
  import { Loader2 } from 'lucide-svelte'
  import {
    api,
    getStoredAPIKey,
    onUnauthorized,
    type APIKey,
    type Combo,
    type ProviderConnection,
    type ProviderNode,
    type Settings
  } from './api/client'
  import AnalyticsView from './components/analytics/AnalyticsView.svelte'
  import CliToolsView from './components/CliToolsView.svelte'
  import CombosView from './components/combos/CombosView.svelte'
  import ConnectionsView from './components/connections/ConnectionsView.svelte'
  import OAuthCallbackView from './components/connections/OAuthCallbackView.svelte'
  import EndpointView from './components/EndpointView.svelte'
  import LoginView from './components/LoginView.svelte'
  import MediaKindView from './components/media/MediaKindView.svelte'
  import MediaProviderDetail from './components/media/MediaProviderDetail.svelte'
  import ProxyPoolsView from './components/ProxyPoolsView.svelte'
  import ProfileSettingsView from './components/ProfileSettingsView.svelte'
  import MediaWebView from './components/media/MediaWebView.svelte'
  import QuotaTrackerView from './components/QuotaTrackerView.svelte'
  import SkillsView from './components/SkillsView.svelte'
  import SettingsView from './components/SettingsView.svelte'
  import SecurityPanel from './components/security/SecurityPanel.svelte'
  import Sidebar from './components/Sidebar.svelte'
  import UpdateModal from './components/UpdateModal.svelte'
  import type { SystemVersionInfo } from './api/client'
  import Toasts from './lib/ui/Toasts.svelte'
  import TerminalView from './components/TerminalView.svelte'
  import TokenSaverView from './components/TokenSaverView.svelte'
  import TopBar from './components/TopBar.svelte'
  import { parseMediaProvider, parseProviderId, pathToTab, providerPath, mediaProviderPath, TAB_ROUTES, type ActiveTab, type MediaProviderRoute } from './lib/router'
  import { PROVIDER_CATALOG } from './lib/providers'
  import { getIconPath } from './components/connections/types'

  let activeTab = $state<ActiveTab>(
    typeof window !== 'undefined' ? pathToTab(window.location.pathname) : 'endpoint'
  )
  let connections = $state<ProviderConnection[]>([])
  let providerNodes = $state<ProviderNode[]>([])
  let combos = $state<Combo[]>([])
  let apiKeys = $state<APIKey[]>([])
  let settings = $state<Settings>({})
  let isLoading = $state(true)
  let isAuthChecking = $state(true)
  let isAuthenticatedState = $state(false)
  let requireLogin = $state(false)
  let isCreateComboOpen = $state(false)
  let isMobileMenuOpen = $state(false)
  let selectedProviderId = $state<string | null>(
    typeof window !== 'undefined' ? parseProviderId(window.location.pathname) : null
  )
  let selectedMedia = $state<MediaProviderRoute | null>(
    typeof window !== 'undefined' ? parseMediaProvider(window.location.pathname) : null
  )
  // OAuth callback tab (provider redirect target): standalone page, no login
  // gate — it only hands the code over to the dashboard tab via storage.
  let isOAuthCallback = $state(
    typeof window !== 'undefined' && window.location.pathname.replace(/\/+$/, '') === '/callback'
  )

  function navigate(tab: ActiveTab, replace = false, providerId?: string | null) {
    activeTab = tab
    selectedMedia = null
    if (tab === 'connections') {
      selectedProviderId = providerId || null
      const path = providerId ? providerPath(providerId) : TAB_ROUTES.connections
      if (typeof window !== 'undefined' && window.location.pathname !== path) {
        const snapshot = { tab, providerId: providerId ?? null }
        if (replace) {
          window.history.replaceState(snapshot, '', path)
        } else {
          window.history.pushState(snapshot, '', path)
        }
      }
      return
    }
    selectedProviderId = null
    const path = TAB_ROUTES[tab]
    if (typeof window !== 'undefined' && window.location.pathname !== path) {
      const snapshot = { tab }
      if (replace) {
        window.history.replaceState(snapshot, '', path)
      } else {
        window.history.pushState(snapshot, '', path)
      }
    }
  }

  function navigateMediaProvider(kind: string, providerId: string, replace = false) {
    const path = mediaProviderPath(kind, providerId)
    activeTab = pathToTab(path)
    selectedMedia = { kind, providerId }
    selectedProviderId = null
    if (typeof window !== 'undefined' && window.location.pathname !== path) {
      const snapshot = { tab: activeTab, mediaKind: kind, mediaProviderId: providerId }
      if (replace) {
        window.history.replaceState(snapshot, '', path)
      } else {
        window.history.pushState(snapshot, '', path)
      }
    }
  }

  async function loadData() {
    try {
      const [connsRes, nodesRes, combosRes, keysRes, settingsRes] = await Promise.all([
        api.getConnections().catch(() => []),
        api.getProviderNodes().catch(() => []),
        api.getCombos().catch(() => []),
        api.getApiKeys().catch(() => []),
        api.getSettings().catch(() => ({})),
      ])
      connections = connsRes
      providerNodes = nodesRes
      combos = combosRes
      apiKeys = keysRes
      settings = settingsRes
    } finally {
      isLoading = false
    }
  }

  async function checkAuth() {
    try {
      const authStatus = await api.checkRequireLogin()
      requireLogin = !!authStatus.requireLogin
      if (requireLogin) {
        // When login is required, only the server-verified session cookie is authoritative.
        isAuthenticatedState = !!authStatus.authenticated
        if (!isAuthenticatedState) {
          sessionStorage.removeItem('9router_auth')
          localStorage.removeItem('9router_auth')
        }
      } else {
        isAuthenticatedState = true
      }
    } catch {
      requireLogin = false
      isAuthenticatedState = true
    } finally {
      isAuthChecking = false
    }
  }

  onMount(() => {
    const unsubscribeUnauthorized = onUnauthorized(() => {
      isAuthenticatedState = false
      requireLogin = true
      if (activeTab !== 'login') {
        navigate('login', true)
      }
    })

    checkAuth().then(() => {
      if (requireLogin && !isAuthenticatedState) {
        if (activeTab !== 'login') {
          navigate('login', true)
        }
      } else {
        if (activeTab === 'login') {
          navigate('endpoint', true)
        }
        loadData()
      }
    })

    const rawPath = window.location.pathname.replace(/\/+$/, '') || '/'
    if (isAuthenticatedState && (rawPath === '/' || rawPath === '/dashboard')) {
      window.history.replaceState({ tab: activeTab }, '', TAB_ROUTES[activeTab])
    }

    function handlePopState() {
      activeTab = pathToTab(window.location.pathname)
      selectedProviderId = parseProviderId(window.location.pathname)
      selectedMedia = parseMediaProvider(window.location.pathname)
    }
    window.addEventListener('popstate', handlePopState)

    const interval = setInterval(async () => {
      if (typeof document !== 'undefined' && document.hidden) return
      if (requireLogin && !isAuthenticatedState) return
      try {
        const [connsRes, nodesRes] = await Promise.all([
          api.getConnections().catch(() => null),
          api.getProviderNodes().catch(() => null)
        ])
        if (connsRes) connections = connsRes
        if (nodesRes) providerNodes = nodesRes
      } catch {
        // silent refresh error
      }
    }, 10000)

    return () => {
      clearInterval(interval)
      window.removeEventListener('popstate', handlePopState)
      unsubscribeUnauthorized()
    }
  })

  let activeConnectionsCount = $derived(connections.filter((c) => c.isActive === 1).length)
  let selectedMediaCatalogItem = $derived.by(() => {
    if (!selectedMedia) return null
    return PROVIDER_CATALOG.find((p) => p.id === selectedMedia!.providerId) || null
  })
  let selectedProviderMeta = $derived.by(() => {
    if (selectedMedia && selectedMediaCatalogItem) {
      return {
        id: selectedMedia.providerId,
        name: selectedMediaCatalogItem.name || selectedMedia.providerId,
        icon: getIconPath(selectedMedia.providerId)
      }
    }
    if (activeTab !== 'connections' || !selectedProviderId) return null
    const node = providerNodes.find((n) => n.id === selectedProviderId)
    const cat = PROVIDER_CATALOG.find((p) => p.id === selectedProviderId)
    return {
      id: selectedProviderId,
      name: node?.name || cat?.name || selectedProviderId,
      icon: getIconPath(selectedProviderId, node?.apiType)
    }
  })
  const pageMeta: Record<ActiveTab, { title: string; description: string }> = {
    login: { title: 'Login', description: 'Authenticate to access 9router-go' },
    endpoint: { title: 'Endpoint & Key', description: 'API endpoint and key configuration' },
    connections: { title: 'Providers & Endpoints', description: 'Manage your AI provider connections' },
    combos: { title: 'Combo & Routing', description: 'Model combos and failover strategies' },
    // The two moved tabs are sections of the Usage page, so the TopBar keeps
    // the Usage title and the section's own header below it names the section.
    'usage-cache': { title: 'Usage & Analytics', description: 'Monitor your API usage, token consumption, and request logs' },
    'usage-compression': { title: 'Usage & Analytics', description: 'Monitor your API usage, token consumption, and request logs' },
    analytics: { title: 'Usage & Analytics', description: 'Monitor your API usage, token consumption, and request logs' },
    quota: { title: 'Quota Tracker', description: 'Track and manage your API quota limits' },
    'token-saver': { title: 'Token Saver', description: 'Compress prompts and outputs to save tokens' },
    'cli-tools': { title: 'CLI Tools', description: 'Configure CLI tools and API keys' },
    'media-embedding': { title: 'Embedding Models', description: 'Vector embeddings and semantic retrieval' },
    'media-image': { title: 'Text to Image', description: 'Image generation and transformation models' },
    'media-tts': { title: 'Text to Speech', description: 'Voice synthesis and audio generation models' },
    'media-stt': { title: 'Speech to Text', description: 'Audio transcription and speech recognition models' },
    'media-systemone': { title: 'System One', description: 'Structured state evaluation models' },
    'media-web': { title: 'Web Fetch & Search', description: 'Configure web search and scrape tools' },
    'proxy-pools': { title: 'Proxy Pools', description: 'Manage your proxy pool configurations' },
    skills: { title: 'Agent Skills', description: 'Copy a link and paste to your AI to use 9router-go — no install needed' },
    'console-log': { title: 'Console Log', description: 'Live server console output' },
    terminal: { title: 'Console Log', description: 'Live server console output' },
    settings: { title: 'Settings', description: 'Manage your preferences and configuration' },
  }

  function handleOpenNewCombo() {
    navigate('combos')
    isCreateComboOpen = true
  }
  let isUpdateModalOpen = $state(false)
  let systemUpdateInfo = $state<SystemVersionInfo | null>(null)
  let systemVersion = $state('')
</script>

{#if isOAuthCallback}
  <OAuthCallbackView />
{:else if isAuthChecking}
  <div class="min-h-screen flex items-center justify-center bg-bg p-4">
    <div class="text-center">
      <div class="inline-block animate-spin rounded-full h-8 w-8 border-b-2 border-primary"></div>
      <p class="text-text-muted mt-4">Loading...</p>
    </div>
  </div>
{:else if (requireLogin && !isAuthenticatedState) || activeTab === 'login'}
  <LoginView
    onSuccess={() => {
      isAuthenticatedState = true
      loadData()
      if (activeTab === 'login') {
        navigate('endpoint')
      }
    }}
  />
{:else}
  <div class="flex h-screen w-full overflow-hidden bg-bg text-text-main font-body transition-colors duration-300">
    <Toasts />
    <!-- Mobile sidebar drawer backdrop -->
    {#if isMobileMenuOpen}
      <div
        class="fixed inset-0 z-40 bg-black/50 backdrop-blur-sm lg:hidden"
        onclick={() => (isMobileMenuOpen = false)}
        onkeydown={(e) => e.key === 'Escape' && (isMobileMenuOpen = false)}
        role="button"
        tabindex="-1"
        aria-label="Close menu backdrop"
      ></div>
    {/if}

    <!-- Left Sidebar -->
    <div
      class="fixed inset-y-0 left-0 z-50 lg:static transition-transform duration-300 transform lg:transform-none {isMobileMenuOpen
        ? 'translate-x-0'
        : '-translate-x-full lg:translate-x-0'}"
    >
      <Sidebar
        bind:activeTab
        navigate={(tab, replace) => {
          navigate(tab, replace, null)
        }}
        activeConnections={activeConnectionsCount}
        totalConnections={connections.length}
        onClose={() => (isMobileMenuOpen = false)}
        bind:showUpdateModal={isUpdateModalOpen}
        bind:updateInfo={systemUpdateInfo}
        bind:version={systemVersion}
      />
    </div>

    <!-- Global Update Modal & Disconnected Overlay outside of transform containers -->
    <UpdateModal
      bind:isOpen={isUpdateModalOpen}
      updateInfo={systemUpdateInfo}
      currentVersion={systemVersion}
    />

    <!-- Main Viewport (TopBar + Scrollable Canvas) -->
    <div class="flex-1 flex flex-col min-w-0 h-full relative isolate">
      <!-- Faint grid background (upstream landing-grid) -->
      <div class="landing-grid absolute inset-0 pointer-events-none -z-10" aria-hidden="true"></div>

      <TopBar
        {activeTab}
        pageTitle={pageMeta[activeTab]?.title}
        pageDescription={pageMeta[activeTab]?.description}
        selectedProvider={selectedProviderMeta}
        onBackToProviders={() => navigate('connections', false, null)}
        onMenuClick={() => (isMobileMenuOpen = !isMobileMenuOpen)}
        onLogout={() => {
          isAuthenticatedState = false
          navigate('login')
        }}
      />

      <main class="flex-1 overflow-y-auto custom-scrollbar p-6 lg:p-10">
        <div class="max-w-7xl mx-auto">
          {#if isLoading}
            <div class="flex flex-col items-center justify-center h-[70vh] gap-3 text-text-muted">
              <Loader2 class="w-7 h-7 animate-spin text-brand-500" />
              <span class="font-code text-xs">Connecting to 9router-go Localhost Gateway (:20130)...</span>
            </div>
          {:else}
            {#if activeTab === 'endpoint'}
              <EndpointView {apiKeys} {settings} onRefresh={loadData} />
            {:else if activeTab === 'connections'}
              <ConnectionsView
                {connections}
                {providerNodes}
                onRefresh={loadData}
                bind:selectedProviderId
                onSelectProvider={(id) => navigate('connections', false, id)}
                onBackToOverview={() => navigate('connections', false, null)}
              />
            {:else if activeTab === 'combos'}
              <CombosView {combos} {connections} {providerNodes} onRefresh={loadData} bind:isCreatingOpen={isCreateComboOpen} />
            {:else if activeTab === 'analytics' || activeTab === 'usage-cache' || activeTab === 'usage-compression'}
              <!-- Cache and Compression Analytics are Usage sections (#200);
                   AnalyticsView renders all four sections under one header. -->
              <AnalyticsView {connections} {providerNodes} tab={activeTab} onNavigate={(next) => navigate(next)} />
            {:else if activeTab === 'quota'}
              <QuotaTrackerView {connections} />
            {:else if activeTab === 'token-saver'}
              <TokenSaverView {settings} onRefresh={loadData} />
            {:else if activeTab === 'cli-tools'}
              <CliToolsView {apiKeys} onRefresh={loadData} />
            {:else if activeTab === 'media-embedding'}
              {#if selectedMedia && selectedMediaCatalogItem}
                <MediaProviderDetail
                  provider={selectedMediaCatalogItem}
                  kind="embedding"
                  {connections}
                  {apiKeys}
                  {settings}
                  onBack={() => {
                    selectedMedia = null
                    navigate('media-embedding')
                  }}
                  onRefresh={loadData}
                />
              {:else}
                <MediaKindView
                  kind="embedding"
                  {connections}
                  {apiKeys}
                  {settings}
                  {combos}
                  onRefresh={loadData}
                  onSelectProvider={(k, id) => navigateMediaProvider(k, id)}
                />
              {/if}
            {:else if activeTab === 'media-image'}
              {#if selectedMedia && selectedMediaCatalogItem}
                <MediaProviderDetail
                  provider={selectedMediaCatalogItem}
                  kind="image"
                  {connections}
                  {apiKeys}
                  {settings}
                  onBack={() => {
                    selectedMedia = null
                    navigate('media-image')
                  }}
                  onRefresh={loadData}
                />
              {:else}
                <MediaKindView
                  kind="image"
                  {connections}
                  {apiKeys}
                  {settings}
                  {combos}
                  onRefresh={loadData}
                  onSelectProvider={(k, id) => navigateMediaProvider(k, id)}
                />
              {/if}
            {:else if activeTab === 'media-tts'}
              {#if selectedMedia && selectedMediaCatalogItem}
                <MediaProviderDetail
                  provider={selectedMediaCatalogItem}
                  kind="tts"
                  {connections}
                  {apiKeys}
                  {settings}
                  onBack={() => {
                    selectedMedia = null
                    navigate('media-tts')
                  }}
                  onRefresh={loadData}
                />
              {:else}
                <MediaKindView
                  kind="tts"
                  {connections}
                  {apiKeys}
                  {settings}
                  {combos}
                  onRefresh={loadData}
                  onSelectProvider={(k, id) => navigateMediaProvider(k, id)}
                />
              {/if}
            {:else if activeTab === 'media-stt'}
              {#if selectedMedia && selectedMediaCatalogItem}
                <MediaProviderDetail
                  provider={selectedMediaCatalogItem}
                  kind="stt"
                  {connections}
                  {apiKeys}
                  {settings}
                  onBack={() => {
                    selectedMedia = null
                    navigate('media-stt')
                  }}
                  onRefresh={loadData}
                />
              {:else}
                <MediaKindView
                  kind="stt"
                  {connections}
                  {apiKeys}
                  {settings}
                  {combos}
                  onRefresh={loadData}
                  onSelectProvider={(k, id) => navigateMediaProvider(k, id)}
                />
              {/if}
            {:else if activeTab === 'media-video'}
              {#if selectedMedia && selectedMediaCatalogItem}
                <MediaProviderDetail
                  provider={selectedMediaCatalogItem}
                  kind="video"
                  {connections}
                  {apiKeys}
                  {settings}
                  onBack={() => {
                    selectedMedia = null
                    navigate('media-video')
                  }}
                  onRefresh={loadData}
                />
              {:else}
                <MediaKindView
                  kind="video"
                  {connections}
                  {apiKeys}
                  {settings}
                  {combos}
                  onRefresh={loadData}
                  onSelectProvider={(k, id) => navigateMediaProvider(k, id)}
                />
              {/if}
            {:else if activeTab === 'media-systemone'}
              {#if selectedMedia && selectedMediaCatalogItem}
                <MediaProviderDetail
                  provider={selectedMediaCatalogItem}
                  kind={selectedMedia.kind as any}
                  {connections}
                  {apiKeys}
                  {settings}
                  onBack={() => {
                    selectedMedia = null
                    navigate('media-systemone')
                  }}
                  onRefresh={loadData}
                />
              {:else}
                <MediaKindView
                  kind="systemone"
                  {connections}
                  {apiKeys}
                  {settings}
                  {combos}
                  onRefresh={loadData}
                  onSelectProvider={(k, id) => navigateMediaProvider(k, id)}
                />
              {/if}
            {:else if activeTab === 'media-web'}
              {#if selectedMedia && selectedMediaCatalogItem}
                <MediaProviderDetail
                  provider={selectedMediaCatalogItem}
                  kind={selectedMedia.kind as any}
                  {connections}
                  {apiKeys}
                  {settings}
                  onBack={() => {
                    selectedMedia = null
                    navigate('media-web')
                  }}
                  onRefresh={loadData}
                />
              {:else}
                <MediaWebView
                  {connections}
                  {combos}
                  onRefresh={loadData}
                  onSelectProvider={(kind, id) => navigateMediaProvider(kind, id)}
                />
              {/if}
            {:else if activeTab === 'proxy-pools'}
              <ProxyPoolsView />
            {:else if activeTab === 'skills'}
              <SkillsView />
            {:else if activeTab === 'console-log' || activeTab === 'terminal'}
              <TerminalView />
            {:else if activeTab === 'settings'}
              <ProfileSettingsView {settings} onRefresh={loadData} />
            {:else if activeTab === 'security'}
              <SecurityPanel />
            {/if}
          {/if}
        </div>
      </main>
    </div>
  </div>
{/if}
