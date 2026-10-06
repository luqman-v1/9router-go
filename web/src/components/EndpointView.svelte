<script lang="ts">
  import { onMount } from 'svelte'
  import {
    AlertCircle,
    AlertTriangle,
    Check,
    CloudUpload,
    Copy,
    ExternalLink,
    Eye,
    EyeOff,
    Key,
    Loader2,
    Plus,
    Power,
    Radio,
    Shield,
    Trash2,
    X
  } from 'lucide-svelte'
  import Card from '../lib/ui/Card.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import { copyToClipboard } from '../lib/clipboard'
  import { api, type APIKey, type Settings, type TunnelStatusResponse } from '../api/client'

  interface Props {
    apiKeys?: APIKey[]
    settings?: Settings
    onRefresh?: () => void
  }

  let {
    apiKeys = [],
    settings = {},
    onRefresh
  }: Props = $props()

  // Local state for keys & settings
  let localKeys = $state<APIKey[]>([])
  let requireApiKey = $state(false)
  let requireLogin = $state(true)
  let hasPassword = $state(true)
  let tunnelDashboardAccess = $state(false)
  let copiedId = $state<string | null>(null)
  let shownKeyIds = $state<Set<string>>(new Set())

  // Origin resolution (SSR fallback uses the Go default port 20130)
  let localOrigin = $state('http://localhost:20130')
  let localEndpoint = $derived(`${localOrigin}/v1`)
  onMount(() => {
    if (typeof window !== 'undefined') {
      localOrigin = window.location.origin
    }
  })
  // Tunnel state
  let tunnelEnabled = $state(false)
  let tunnelRunning = $state(false)
  let tunnelReachable = $state(false)
  let tunnelEverReachable = $state(false)
  let tunnelMissCount = 0
  let tunnelUrl = $state('')
  let publicUrl = $state('')
  let isTunnelLoading = $state(false)
  let tunnelStatusText = $state('')
  let tunnelError = $state<string | null>(null)
  let showEnableTunnelModal = $state(false)
  let showDisableTunnelModal = $state(false)

  // Tailscale state
  let tailscaleEnabled = $state(false)
  let tailscaleRunning = $state(false)
  let tailscaleReachable = $state(false)
  let tailscaleEverReachable = $state(false)
  let tailscaleMissCount = 0
  let tailscaleUrl = $state('')
  let isTailscaleLoading = $state(false)
  let tailscaleStatusText = $state('')
  let tailscaleAuthUrl = $state('')
  let tailscaleError = $state<string | null>(null)
  let tailscaleInstalled = $state<boolean | null>(null)
  let showTailscaleModal = $state(false)
  let showDisableTailscaleModal = $state(false)

  // Keys modal state
  let isCreateKeyOpen = $state(false)
  let newKeyName = $state('')
  let isSubmittingKey = $state(false)
  let newlyCreatedKey = $state<string | null>(null)

  // Confirmation modal
  let confirmModal = $state<{
    title: string
    message: string
    confirmLabel?: string
    isDanger?: boolean
    onConfirm: () => void
  } | null>(null)

  // Sync props to state
  $effect(() => {
    if (apiKeys && apiKeys.length > 0) {
      localKeys = [...apiKeys]
    }
  })
  $effect(() => {
    if (settings) {
      requireApiKey = !!settings.requireApiKey
      tunnelDashboardAccess = !!settings.tunnelDashboardAccess
      if (typeof settings.requireLogin === 'boolean') requireLogin = settings.requireLogin
      if (typeof settings.hasPassword === 'boolean') hasPassword = settings.hasPassword
    }
  })

  // Browser-side health probe: must reach origin (not just CF/TS edge).
  // /api/health sets Access-Control-Allow-Origin: * so CORS works through tunnel.
  async function clientPingUrl(url: string): Promise<boolean> {
    if (!url) return false
    try {
      const controller = new AbortController()
      const timeoutId = setTimeout(() => controller.abort(), 5000)
      const res = await fetch(`${url}/api/health`, {
        mode: 'cors',
        cache: 'no-store',
        signal: controller.signal,
      })
      clearTimeout(timeoutId)
      return res.ok
    } catch {
      return false
    }
  }

  function markReachable(ok: boolean, kind: 'tunnel' | 'tailscale') {
    // Debounce reachable=false: the server may briefly return false during a
    // background refresh, so only flip after 5 consecutive misses (upstream).
    if (kind === 'tunnel') {
      if (ok) {
        tunnelMissCount = 0
        tunnelReachable = true
        tunnelEverReachable = true
      } else if (++tunnelMissCount >= 5) {
        tunnelReachable = false
      }
    } else {
      if (ok) {
        tailscaleMissCount = 0
        tailscaleReachable = true
        tailscaleEverReachable = true
      } else if (++tailscaleMissCount >= 5) {
        tailscaleReachable = false
      }
    }
  }

  // Load status
  async function loadStatus() {
    try {
      const [keysRes, settingsRes, tunnelRes] = await Promise.all([
        api.getApiKeys().catch(() => null),
        api.getSettings().catch(() => null),
        api.getTunnelStatus().catch(() => null)
      ])

      if (keysRes) {
        localKeys = keysRes
      }
      if (settingsRes) {
        requireApiKey = !!settingsRes.requireApiKey
        tunnelDashboardAccess = !!settingsRes.tunnelDashboardAccess
        if (typeof settingsRes.requireLogin === 'boolean') requireLogin = settingsRes.requireLogin
        if (typeof settingsRes.hasPassword === 'boolean') hasPassword = settingsRes.hasPassword
      }
      if (tunnelRes) {
        applyTunnelStatus(tunnelRes)
      }
    } catch {
      // silent
    }
  }

  function applyTunnelStatus(status: TunnelStatusResponse) {
    if (status.tunnel) {
      tunnelEnabled = !!(status.tunnel.settingsEnabled ?? status.tunnel.enabled)
      tunnelRunning = !!status.tunnel.running
      tunnelUrl = status.tunnel.tunnelUrl || ''
      publicUrl = status.tunnel.publicUrl || ''
    }
    if (status.tailscale) {
      tailscaleEnabled = !!(status.tailscale.settingsEnabled ?? status.tailscale.enabled)
      tailscaleRunning = !!status.tailscale.running
      tailscaleUrl = status.tailscale.tunnelUrl || ''
    }
  }

  onMount(() => {
    loadStatus()
    // Status poll only while degraded; healthy tunnels rely on the browser
    // ping below (upstream STATUS_POLL_FAST_MS behaviour).
    const statusTimer = setInterval(async () => {
      if (typeof document !== 'undefined' && document.hidden) return
      const tunnelHealthy = !tunnelEnabled || tunnelReachable
      const tsHealthy = !tailscaleEnabled || tailscaleReachable
      if (tunnelHealthy && tsHealthy) return
      try {
        const res = await api.getTunnelStatus()
        if (res) applyTunnelStatus(res)
      } catch {}
    }, 5000)
    // Browser-side ping: probe tunnel/tailscale URLs directly every 10s.
    const pingTimer = setInterval(async () => {
      if (typeof document !== 'undefined' && document.hidden) return
      if (tunnelEnabled && (tunnelUrl || publicUrl)) {
        const direct = tunnelUrl ? clientPingUrl(tunnelUrl) : Promise.resolve(false)
        const pub = publicUrl ? clientPingUrl(publicUrl) : Promise.resolve(false)
        markReachable((await direct) || (await pub), 'tunnel')
      }
      if (tailscaleEnabled && tailscaleUrl) {
        markReachable(await clientPingUrl(tailscaleUrl), 'tailscale')
      }
    }, 10000)
    return () => {
      clearInterval(statusTimer)
      clearInterval(pingTimer)
    }
  })

  function copy(text: string, id: string) {
    copyToClipboard(text)
    copiedId = id
    setTimeout(() => {
      if (copiedId === id) copiedId = null
    }, 2000)
  }

  function toggleShowKey(id: string) {
    const next = new Set(shownKeyIds)
    if (next.has(id)) {
      next.delete(id)
    } else {
      next.add(id)
    }
    shownKeyIds = next
  }

  function maskKey(key: string): string {
    if (!key) return ''
    if (key.length <= 10) return key
    return key.slice(0, 6) + '••••••' + key.slice(-4)
  }

  // Toggle Require API Key
  async function toggleRequireApiKey(value: boolean) {
    requireApiKey = value
    try {
      await api.updateSettings({ requireApiKey: value })
      onRefresh?.()
    } catch (err) {
      requireApiKey = !value
      alert(`Failed to update requireApiKey: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Toggle Tunnel Dashboard Access
  async function toggleTunnelDashboardAccess(value: boolean) {
    tunnelDashboardAccess = value
    try {
      await api.updateSettings({ tunnelDashboardAccess: value })
      onRefresh?.()
    } catch (err) {
      tunnelDashboardAccess = !value
      alert(`Failed to update tunnelDashboardAccess: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Security gate: block remote exposure while dashboard uses default
  // password or login is off (upstream isLoginUnsafe).
  let isLoginUnsafe = $derived(!requireLogin || !hasPassword)
  let unsafeReason = $derived(
    !requireLogin
      ? 'Enable "Require login" and set a custom password before activating the tunnel.'
      : 'Change the default dashboard password before activating the tunnel.'
  )

  // Start Cloudflare Tunnel
  async function startTunnel() {
    showEnableTunnelModal = false
    isTunnelLoading = true
    tunnelStatusText = 'Creating tunnel...'
    tunnelError = null

    try {
      const res = await api.enableTunnel()
      if (res.error) {
        tunnelError = res.error
        return
      }
      if (res.tunnelUrl) {
        tunnelUrl = res.tunnelUrl
        publicUrl = res.publicUrl || ''
        tunnelEnabled = true
        tunnelRunning = true
      }
    } catch (err) {
      tunnelError = err instanceof Error ? err.message : String(err)
    } finally {
      isTunnelLoading = false
      tunnelStatusText = ''
    }
  }

  // Disable Cloudflare Tunnel
  async function stopTunnel() {
    showDisableTunnelModal = false
    isTunnelLoading = true
    try {
      await api.disableTunnel()
      tunnelEnabled = false
      tunnelRunning = false
      tunnelUrl = ''
      publicUrl = ''
    } catch (err) {
      tunnelError = err instanceof Error ? err.message : String(err)
    } finally {
      isTunnelLoading = false
    }
  }

  // Tailscale handlers
  async function handleTailscaleClick() {
    if (isLoginUnsafe) {
      tailscaleError = `Security required: ${unsafeReason}`
      return
    }
    tailscaleError = null
    try {
      const check = await api.checkTailscale()
      tailscaleInstalled = !!check.installed
      if (check.installed) {
        startTailscale()
      } else {
        showTailscaleModal = true
      }
    } catch {
      showTailscaleModal = true
    }
  }

  async function startTailscale() {
    showTailscaleModal = false
    isTailscaleLoading = true
    tailscaleStatusText = 'Connecting to Tailscale...'
    tailscaleError = null

    try {
      const res = await api.enableTailscale()
      if (res.error) {
        tailscaleError = res.error
        return
      }
      if (res.needsLogin && res.authUrl) {
        tailscaleAuthUrl = res.authUrl
        tailscaleStatusText = 'Login required'
        return
      }
      if (res.tunnelUrl) {
        tailscaleUrl = res.tunnelUrl
        tailscaleEnabled = true
        tailscaleRunning = true
      }
    } catch (err) {
      tailscaleError = err instanceof Error ? err.message : String(err)
    } finally {
      isTailscaleLoading = false
      tailscaleStatusText = ''
    }
  }

  async function stopTailscale() {
    showDisableTailscaleModal = false
    isTailscaleLoading = true
    try {
      await api.disableTailscale()
      tailscaleEnabled = false
      tailscaleRunning = false
      tailscaleUrl = ''
    } catch (err) {
      tailscaleError = err instanceof Error ? err.message : String(err)
    } finally {
      isTailscaleLoading = false
    }
  }

  // Keys management
  async function handleCreateKey(e: SubmitEvent) {
    e.preventDefault()
    if (!newKeyName.trim()) return

    isSubmittingKey = true
    try {
      const res = await api.createApiKey({ name: newKeyName.trim() })
      if (res.key) {
        newlyCreatedKey = res.key
        localStorage.setItem('9router_key', res.key)
        newKeyName = ''
        isCreateKeyOpen = false
        await loadStatus()
        onRefresh?.()
      }
    } catch (err) {
      alert(`Failed to create key: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSubmittingKey = false
    }
  }

  async function handleToggleKey(key: APIKey, nextActive: boolean) {
    if (!nextActive && (key.isActive === 1 || key.isActive === true)) {
      confirmModal = {
        title: 'Pause API Key',
        message: `Pause API key "${key.name || 'API Key'}"? This key will stop working immediately but can be resumed later.`,
        confirmLabel: 'Pause Key',
        isDanger: false,
        onConfirm: async () => {
          confirmModal = null
          await executeToggleKey(key.id, false)
        }
      }
    } else {
      await executeToggleKey(key.id, nextActive)
    }
  }

  async function executeToggleKey(id: string, active: boolean) {
    try {
      await api.toggleApiKey(id, active)
      localKeys = localKeys.map((k) => (k.id === id ? { ...k, isActive: active ? 1 : 0 } : k))
      onRefresh?.()
    } catch (err) {
      alert(`Failed to toggle key: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  function handleDeleteKey(key: APIKey) {
    confirmModal = {
      title: 'Delete API Key',
      message: `Are you sure you want to permanently delete API key "${key.name || 'API Key'}"?`,
      confirmLabel: 'Delete Key',
      isDanger: true,
      onConfirm: async () => {
        confirmModal = null
        try {
          await api.deleteApiKey(key.id)
          localKeys = localKeys.filter((k) => k.id !== key.id)
          onRefresh?.()
        } catch (err) {
          alert(`Failed to delete key: ${err instanceof Error ? err.message : String(err)}`)
        }
      }
    }
  }

  const tunnelValueProps = [
    { icon: 'public', title: 'Access Anywhere', desc: 'Use your API from any network' },
    { icon: 'group', title: 'Share Endpoint', desc: 'Share URL with team members' },
    { icon: 'code', title: 'Use in Cursor/Cline', desc: 'Connect AI tools remotely' },
    { icon: 'lock', title: 'Encrypted', desc: 'End-to-end TLS via Cloudflare' },
  ]
</script>

<div class="flex flex-col gap-6">
  <!-- TOP SECTION: API Endpoint Card -->
  <Card padding="md" class="space-y-4">
    <div class="flex items-center gap-2 mb-2">
      <div class="p-2 rounded-lg bg-brand-500/10 text-brand-500">
        <Radio class="w-5 h-5" />
      </div>
      <div>
        <h2 class="text-lg font-semibold text-text-main flex items-center gap-2">
          API Endpoint
        </h2>
        <p class="text-xs text-text-muted">Direct, secure connection points to your local AI router</p>
      </div>
    </div>

    <div class="flex flex-col gap-3">
      <!-- Local Endpoint Field -->
      <div class="flex items-center gap-2">
        <span class="text-xs font-mono px-2 py-1 rounded shrink-0 min-w-[90px] text-center bg-surface-2 text-text-muted font-semibold border border-border">
          Local
        </span>
        <input
          type="text"
          value={localEndpoint}
          readonly
          class="flex-1 font-mono text-sm bg-bg border border-border rounded-lg px-3 py-2 text-text-main selection:bg-brand-500/30"
        />
        <button
          type="button"
          onclick={() => copy(localEndpoint, 'local_url')}
          class="p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-brand-500 transition-colors shrink-0 cursor-pointer border border-border"
          title="Copy Local URL"
        >
          {#if copiedId === 'local_url'}
            <Check class="w-4 h-4 text-success" />
          {:else}
            <Copy class="w-4 h-4" />
          {/if}
        </button>
      </div>

      <!-- Tunnel (Cloudflare Quick Tunnel) -->
      <div class="flex items-center gap-2">
        <span
          class="text-xs font-mono px-2 py-1 rounded shrink-0 min-w-[90px] text-center font-semibold border transition-colors {tunnelRunning && tunnelUrl
            ? 'bg-brand-500/15 text-brand-500 border-brand-500/30'
            : 'bg-surface-2 text-text-muted border-border'}"
        >
          Tunnel
        </span>

        {#if tunnelEnabled && !isTunnelLoading && tunnelReachable}
          <input
            type="text"
            value="{publicUrl || tunnelUrl}/v1"
            readonly
            class="flex-1 font-mono text-sm bg-bg border border-border rounded-lg px-3 py-2 text-text-main selection:bg-brand-500/30"
          />
          <button
            type="button"
            onclick={() => copy(`${publicUrl || tunnelUrl}/v1`, 'tunnel_url')}
            class="p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-brand-500 transition-colors shrink-0 cursor-pointer border border-border"
            title="Copy Tunnel URL"
          >
            {#if copiedId === 'tunnel_url'}
              <Check class="w-4 h-4 text-success" />
            {:else}
              <Copy class="w-4 h-4" />
            {/if}
          </button>
          <button
            type="button"
            onclick={() => (showDisableTunnelModal = true)}
            class="p-2 hover:bg-danger/10 rounded-lg text-red-600 dark:text-red-400 transition-colors shrink-0 cursor-pointer border border-danger/20"
            title="Disable Tunnel"
          >
            <Power class="w-4 h-4" />
          </button>
        {:else if tunnelEnabled && !isTunnelLoading && !tunnelReachable}
          <div class="flex-1 flex items-center gap-2 px-3 py-2 rounded-lg border border-amber-500/25 bg-amber-500/5 text-sm text-amber-600 dark:text-amber-400">
            <Loader2 class="w-4 h-4 animate-spin text-amber-500" />
            <span>{tunnelEverReachable ? 'Tunnel reconnecting...' : 'Tunnel checking...'}</span>
          </div>
          <button
            type="button"
            onclick={() => (showDisableTunnelModal = true)}
            class="p-2 hover:bg-danger/10 rounded-lg text-red-600 dark:text-red-400 transition-colors shrink-0 cursor-pointer border border-danger/20"
            title="Disable Tunnel"
          >
            <Power class="w-4 h-4" />
          </button>
        {:else if isTunnelLoading}
          <div class="flex-1 flex items-center gap-2 px-3 py-2 rounded-lg border border-border bg-surface-2 text-sm text-text-muted">
            <Loader2 class="w-4 h-4 animate-spin text-brand-500" />
            <span>{tunnelStatusText || 'Creating tunnel...'}</span>
          </div>
          <button
            type="button"
            onclick={() => (isTunnelLoading = false)}
            class="p-2 hover:bg-danger/10 rounded-lg text-red-600 dark:text-red-400 transition-colors shrink-0 cursor-pointer border border-border"
            title="Cancel"
          >
            <Power class="w-4 h-4" />
          </button>
        {:else if tunnelError}
          <div class="flex-1 flex items-center gap-2 px-3 py-2 rounded-lg border border-danger/30 bg-danger/10 text-sm text-red-600 dark:text-red-400">
            <AlertCircle class="w-4 h-4 shrink-0" />
            <span class="truncate">{tunnelError}</span>
          </div>
          <button
            type="button"
            onclick={() => (showEnableTunnelModal = true)}
            class="flex items-center gap-1.5 px-3 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs transition cursor-pointer shadow-sm"
          >
            <CloudUpload class="w-3.5 h-3.5" />
            <span>Enable</span>
          </button>
        {:else}
          <div class="flex-1 flex items-center px-3 py-2 text-sm text-text-muted font-mono bg-bg border border-border rounded-lg">
            <span>Not connected</span>
          </div>
          <button
            type="button"
            onclick={() => {
              if (isLoginUnsafe) {
                tunnelError = `Security required: ${unsafeReason}`
              } else if (!requireApiKey) {
                tunnelError = 'Security required: Enable "Require API key" before activating the tunnel.'
              } else {
                showEnableTunnelModal = true
              }
            }}
            class="flex items-center gap-1.5 px-3 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs transition cursor-pointer shadow-sm"
          >
            <CloudUpload class="w-3.5 h-3.5" />
            <span>Enable</span>
          </button>
        {/if}
      </div>

      <!-- Tailscale (Serve / Funnel) -->
      <div class="flex items-center gap-2">
        <span
          class="text-xs font-mono px-2 py-1 rounded shrink-0 min-w-[90px] text-center font-semibold border transition-colors {tailscaleRunning && tailscaleUrl
            ? 'bg-indigo-500/15 text-indigo-400 border-indigo-500/30'
            : 'bg-surface-2 text-text-muted border-border'}"
        >
          Tailscale
        </span>

        {#if tailscaleEnabled && !isTailscaleLoading && tailscaleReachable}
          <input
            type="text"
            value="{tailscaleUrl}/v1"
            readonly
            class="flex-1 font-mono text-sm bg-bg border border-border rounded-lg px-3 py-2 text-text-main selection:bg-indigo-500/30"
          />
          <button
            type="button"
            onclick={() => copy(`${tailscaleUrl}/v1`, 'ts_url')}
            class="p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-indigo-400 transition-colors shrink-0 cursor-pointer border border-border"
            title="Copy Tailscale URL"
          >
            {#if copiedId === 'ts_url'}
              <Check class="w-4 h-4 text-success" />
            {:else}
              <Copy class="w-4 h-4" />
            {/if}
          </button>
          <button
            type="button"
            onclick={() => (showDisableTailscaleModal = true)}
            class="p-2 hover:bg-danger/10 rounded-lg text-red-600 dark:text-red-400 transition-colors shrink-0 cursor-pointer border border-danger/20"
            title="Disable Tailscale"
          >
            <Power class="w-4 h-4" />
          </button>
        {:else if tailscaleEnabled && !isTailscaleLoading && !tailscaleReachable}
          <div class="flex-1 flex items-center gap-2 px-3 py-2 rounded-lg border border-amber-500/25 bg-amber-500/5 text-sm text-amber-600 dark:text-amber-400">
            <Loader2 class="w-4 h-4 animate-spin text-amber-500" />
            <span>{tailscaleEverReachable ? 'Tailscale reconnecting...' : 'Tailscale checking...'}</span>
          </div>
          <button
            type="button"
            onclick={() => (showDisableTailscaleModal = true)}
            class="p-2 hover:bg-danger/10 rounded-lg text-red-600 dark:text-red-400 transition-colors shrink-0 cursor-pointer border border-danger/20"
            title="Disable Tailscale"
          >
            <Power class="w-4 h-4" />
          </button>
        {:else if isTailscaleLoading}
          <div class="flex-1 flex items-center gap-2 px-3 py-2 rounded-lg border border-border bg-surface-2 text-sm text-text-muted">
            <Loader2 class="w-4 h-4 animate-spin text-indigo-400" />
            <span>{tailscaleStatusText || 'Connecting...'}</span>
          </div>
          {#if tailscaleAuthUrl}
            <button
              type="button"
              onclick={() => window.open(tailscaleAuthUrl, '_blank')}
              class="flex items-center gap-1.5 px-3 py-2 rounded-lg bg-indigo-600 hover:bg-indigo-700 text-white font-semibold text-xs transition cursor-pointer"
            >
              <ExternalLink class="w-3.5 h-3.5" />
              <span>Login</span>
            </button>
          {/if}
          <button
            type="button"
            onclick={() => (isTailscaleLoading = false)}
            class="p-2 hover:bg-danger/10 rounded-lg text-red-600 dark:text-red-400 transition-colors shrink-0 cursor-pointer border border-border"
            title="Cancel"
          >
            <Power class="w-4 h-4" />
          </button>
        {:else if tailscaleError}
          <div class="flex-1 flex items-center gap-2 px-3 py-2 rounded-lg border border-danger/30 bg-danger/10 text-sm text-red-600 dark:text-red-400">
            <AlertCircle class="w-4 h-4 shrink-0" />
            <span class="truncate">{tailscaleError}</span>
          </div>
          <button
            type="button"
            onclick={handleTailscaleClick}
            class="flex items-center gap-1.5 px-3 py-2 rounded-lg bg-gradient-to-r from-indigo-500 to-purple-500 hover:from-indigo-600 hover:to-purple-600 text-white font-semibold text-xs transition cursor-pointer shadow-sm"
          >
            <Shield class="w-3.5 h-3.5" />
            <span>Enable</span>
          </button>
        {:else}
          <div class="flex-1 flex items-center px-3 py-2 text-sm text-text-muted font-mono bg-bg border border-border rounded-lg">
            <span>Not connected</span>
          </div>
          <button
            type="button"
            onclick={() => {
              if (isLoginUnsafe) {
                tailscaleError = `Security required: ${unsafeReason}`
              } else {
                handleTailscaleClick()
              }
            }}
            class="flex items-center gap-1.5 px-3 py-2 rounded-lg bg-gradient-to-r from-indigo-500 to-purple-500 hover:from-indigo-600 hover:to-purple-600 text-white font-semibold text-xs transition cursor-pointer shadow-sm"
          >
            <Shield class="w-3.5 h-3.5" />
            <span>Enable</span>
          </button>
        {/if}
      </div>
    </div>

    <!-- Pre-enable security gate banner (upstream isLoginUnsafe) -->
    {#if isLoginUnsafe && !tunnelEnabled && !tailscaleEnabled}
      <div class="mt-4 flex items-center gap-2 px-3 py-2 rounded-lg bg-amber-500/10 border border-amber-500/20 text-amber-700 dark:text-amber-400 text-xs">
        <AlertTriangle class="w-4 h-4 shrink-0 text-amber-500" />
        <p class="flex-1 leading-relaxed">{unsafeReason}</p>
        <a href="/dashboard/profile" class="font-semibold underline hover:opacity-80 shrink-0">
          Open settings
        </a>
      </div>
    {/if}

    <!-- Security warnings when tunnel or tailscale is active -->
    {#if (tunnelEnabled || tailscaleEnabled) && (!requireApiKey || isLoginUnsafe)}
      <div class="mt-4 flex flex-col gap-2">
        {#if !requireApiKey}
          <div class="flex items-center gap-2 px-3 py-2 rounded-lg bg-amber-500/10 border border-amber-500/20 text-amber-700 dark:text-amber-400 text-xs">
            <AlertTriangle class="w-4 h-4 shrink-0 text-amber-500" />
            <p class="flex-1 leading-relaxed">
              Require API key is disabled — your endpoint is publicly accessible without authentication.
            </p>
            <button
              type="button"
              onclick={() => toggleRequireApiKey(true)}
              class="font-semibold underline hover:opacity-80 shrink-0 cursor-pointer"
            >
              Enable
            </button>
          </div>
        {/if}
        {#if isLoginUnsafe}
          <div class="flex items-center gap-2 px-3 py-2 rounded-lg bg-amber-500/10 border border-amber-500/20 text-amber-700 dark:text-amber-400 text-xs">
            <AlertTriangle class="w-4 h-4 shrink-0 text-amber-500" />
            <p class="flex-1 leading-relaxed">
              {!requireLogin
                ? 'Require login is disabled — anyone can access your dashboard via tunnel.'
                : 'Dashboard uses the default password — change it in Profile settings.'}
            </p>
            <a href="/dashboard/profile" class="font-semibold underline hover:opacity-80 shrink-0">
              {!requireLogin ? 'Enable' : 'Change password'}
            </a>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Allow dashboard access via tunnel toggle -->
    {#if tunnelRunning || tailscaleRunning}
      <div class="mt-4 pt-4 border-t border-border flex items-center justify-between">
        <div class="space-y-0.5 pr-4">
          <p class="font-medium text-sm text-text-main">Allow dashboard access via tunnel</p>
          <p class="text-xs text-text-muted leading-relaxed">
            When enabled, the dashboard can be accessed through your tunnel or Tailscale URL (login still required).
          </p>
        </div>
        <Toggle
          checked={tunnelDashboardAccess}
          label="Allow dashboard access via tunnel"
          onChange={toggleTunnelDashboardAccess}
        />
      </div>
    {/if}
  </Card>

  <!-- BOTTOM SECTION: API Keys Card -->
  <Card padding="md" class="space-y-4">
    <!-- Header with Create Key button -->
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-2">
        <div class="p-2 rounded-lg bg-brand-500/10 text-brand-500">
          <Key class="w-5 h-5" />
        </div>
        <div>
          <h2 class="text-lg font-semibold text-text-main flex items-center gap-2">
            API Keys
          </h2>
          <p class="text-xs text-text-muted">Manage Bearer tokens for clients connecting to this endpoint</p>
        </div>
      </div>

      <button
        type="button"
        onclick={() => (isCreateKeyOpen = true)}
        class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs shadow-md shadow-brand-500/20 transition cursor-pointer"
      >
        <Plus class="w-4 h-4" />
        <span>Create Key</span>
      </button>
    </div>

    <!-- Master Switch: Require API key -->
    <div class="flex items-center justify-between pb-4 pt-2 border-b border-border">
      <div class="space-y-0.5 pr-4">
        <p class="font-medium text-sm text-text-main">Require API key</p>
        <p class="text-xs text-text-muted">Requests without a valid key will be rejected</p>
      </div>
      <Toggle
        checked={requireApiKey}
        label="Require API key"
        onChange={toggleRequireApiKey}
      />
    </div>

    <!-- Warning if disabled -->
    {#if !requireApiKey}
      <div class="flex items-center gap-2 px-3 py-2 rounded-lg bg-amber-500/10 border border-amber-500/20 text-amber-700 dark:text-amber-400 text-xs">
        <AlertTriangle class="w-4 h-4 shrink-0 text-amber-500" />
        <span>Endpoint is exposed without an API key. Anyone who can reach this host can make requests.</span>
      </div>
    {/if}

    <!-- Keys List / Table -->
    {#if localKeys.length === 0}
      <div class="text-center py-12 space-y-3">
        <div class="inline-flex items-center justify-center w-14 h-14 rounded-full bg-brand-500/10 text-brand-500">
          <Key class="w-7 h-7" />
        </div>
        <div class="space-y-1">
          <p class="text-text-main font-medium text-sm">No API keys yet</p>
          <p class="text-xs text-text-muted">Create your first API key to get started</p>
        </div>
        <button
          type="button"
          onclick={() => (isCreateKeyOpen = true)}
          class="inline-flex items-center gap-1.5 px-4 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs shadow-sm transition cursor-pointer"
        >
          <Plus class="w-4 h-4" />
          <span>Create Key</span>
        </button>
      </div>
    {:else}
      <div class="flex flex-col divide-y divide-border/40">
        {#each localKeys as key (key.id)}
          {@const isShown = shownKeyIds.has(key.id)}
          {@const isActive = key.isActive === 1 || key.isActive === true}
          <div class="group flex items-center justify-between py-3.5 transition {isActive ? '' : 'opacity-60'}">
            <div class="flex-1 min-w-0 pr-4">
              <p class="text-sm font-semibold text-text-main">{key.name || 'Default Key'}</p>
              <div class="flex items-center gap-2 mt-1">
                <code class="text-xs text-text-muted font-mono bg-surface-2 px-2 py-0.5 rounded border border-border/50 select-all">
                  {isShown ? key.key : maskKey(key.key)}
                </code>

                <!-- Eye Toggle -->
                <button
                  type="button"
                  onclick={() => toggleShowKey(key.id)}
                  class="p-1 hover:bg-surface-2 rounded text-text-muted hover:text-text-main transition-colors cursor-pointer"
                  title={isShown ? 'Hide key' : 'Show key'}
                >
                  {#if isShown}
                    <EyeOff class="w-3.5 h-3.5" />
                  {:else}
                    <Eye class="w-3.5 h-3.5" />
                  {/if}
                </button>

                <!-- Copy -->
                <button
                  type="button"
                  onclick={() => copy(key.key, key.id)}
                  class="p-1 hover:bg-surface-2 rounded text-text-muted hover:text-brand-500 transition-colors cursor-pointer"
                  title="Copy key"
                >
                  {#if copiedId === key.id}
                    <Check class="w-3.5 h-3.5 text-success" />
                  {:else}
                    <Copy class="w-3.5 h-3.5" />
                  {/if}
                </button>
              </div>

              <div class="flex items-center gap-2 mt-1.5 text-xs text-text-subtle">
                <span>Created {key.createdAt ? new Date(key.createdAt).toLocaleDateString() : '—'}</span>
                {#if !isActive}
                  <span>•</span>
                  <span class="text-amber-500 font-medium">Paused</span>
                {/if}
              </div>
            </div>

            <div class="flex items-center gap-3 shrink-0">
              <!-- Active Switch -->
              <Toggle
                checked={isActive}
                size="sm"
                label={isActive ? 'Pause key' : 'Resume key'}
                title={isActive ? 'Pause key' : 'Resume key'}
                onChange={(nextActive) => handleToggleKey(key, nextActive)}
              />

              <!-- Delete Button -->
              <button
                type="button"
                onclick={() => handleDeleteKey(key)}
                class="p-2 hover:bg-danger/10 rounded-lg text-text-subtle hover:text-red-600 dark:hover:text-red-400 opacity-100 sm:opacity-0 sm:group-hover:opacity-100 transition-all cursor-pointer"
                title="Delete key"
              >
                <Trash2 class="w-4 h-4" />
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </Card>
</div>

<!-- MODAL: Create API Key -->
{#if isCreateKeyOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
    <div class="w-full max-w-md p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-3 border-b border-border">
        <h3 class="text-base font-bold text-text-main">Create API Key</h3>
        <button
          type="button"
          onclick={() => {
            isCreateKeyOpen = false
            newKeyName = ''
          }}
          class="text-text-muted hover:text-text-main cursor-pointer"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <form onsubmit={handleCreateKey} class="space-y-4">
        <div class="space-y-1">
          <label for="key-name-input" class="block text-xs font-semibold text-text-muted">
            Key Name
          </label>
          <input
            id="key-name-input"
            type="text"
            bind:value={newKeyName}
            placeholder="Production Key"
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-sm text-text-main focus:outline-none focus:border-brand-500"
          />
        </div>

        <div class="flex gap-2 pt-2">
          <button
            type="submit"
            disabled={!newKeyName.trim() || isSubmittingKey}
            class="flex-1 py-2 px-4 rounded-lg bg-brand-500 hover:bg-brand-600 disabled:opacity-50 text-white font-semibold text-xs transition cursor-pointer flex items-center justify-center gap-1.5"
          >
            {#if isSubmittingKey}
              <Loader2 class="w-3.5 h-3.5 animate-spin" />
              <span>Creating...</span>
            {:else}
              <span>Create</span>
            {/if}
          </button>
          <button
            type="button"
            onclick={() => {
              isCreateKeyOpen = false
              newKeyName = ''
            }}
            class="flex-1 py-2 px-4 rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main font-semibold text-xs transition cursor-pointer border border-border"
          >
            Cancel
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

<!-- MODAL: API Key Created -->
{#if newlyCreatedKey}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
    <div class="w-full max-w-md p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-3 border-b border-border">
        <h3 class="text-base font-bold text-text-main">API Key Created</h3>
      </div>

      <div class="bg-amber-500/10 border border-amber-500/20 rounded-xl p-4 space-y-1 text-amber-700 dark:text-amber-300">
        <p class="text-sm font-bold">Save this key now!</p>
        <p class="text-xs leading-relaxed">
          This is the only time you will see this key. Store it securely.
        </p>
      </div>

      <div class="flex items-center gap-2">
        <input
          type="text"
          value={newlyCreatedKey}
          readonly
          class="flex-1 font-mono text-xs bg-bg border border-border rounded-lg px-3 py-2 text-text-main select-all"
        />
        <button
          type="button"
          onclick={() => copy(newlyCreatedKey || '', 'created_key')}
          class="flex items-center gap-1.5 px-3 py-2 rounded-lg bg-surface-2 hover:bg-surface-3 border border-border text-xs font-semibold text-text-main cursor-pointer"
        >
          {#if copiedId === 'created_key'}
            <Check class="w-3.5 h-3.5 text-success" />
            <span>Copied!</span>
          {:else}
            <Copy class="w-3.5 h-3.5" />
            <span>Copy</span>
          {/if}
        </button>
      </div>

      <div class="pt-2">
        <button
          type="button"
          onclick={() => (newlyCreatedKey = null)}
          class="w-full py-2 px-4 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs transition cursor-pointer"
        >
          Done
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- MODAL: Confirmation (Delete / Pause) -->
{#if confirmModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
    <div class="w-full max-w-md p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <h3 class="text-base font-bold text-text-main">{confirmModal.title}</h3>
        <button
          type="button"
          onclick={() => (confirmModal = null)}
          class="text-text-muted hover:text-text-main cursor-pointer"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <p class="text-sm text-text-muted leading-relaxed">
        {confirmModal.message}
      </p>

      <div class="flex gap-2 pt-2">
        <button
          type="button"
          onclick={confirmModal.onConfirm}
          class="flex-1 py-2 px-4 rounded-lg text-white font-semibold text-xs transition cursor-pointer {confirmModal.isDanger
            ? 'bg-danger hover:bg-danger/90'
            : 'bg-brand-500 hover:bg-brand-600'}"
        >
          {confirmModal.confirmLabel || 'Confirm'}
        </button>
        <button
          type="button"
          onclick={() => (confirmModal = null)}
          class="flex-1 py-2 px-4 rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main font-semibold text-xs transition cursor-pointer border border-border"
        >
          Cancel
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- MODAL: Enable Cloudflare Tunnel -->
{#if showEnableTunnelModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
    <div class="w-full max-w-lg p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <h3 class="text-base font-bold text-text-main">Enable Tunnel</h3>
        <button
          type="button"
          onclick={() => (showEnableTunnelModal = false)}
          class="text-text-muted hover:text-text-main cursor-pointer"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <!-- Cloudflare Tunnel Info Box -->
      <div class="bg-surface-2 border border-border rounded-xl p-4 flex items-start gap-3">
        <CloudUpload class="w-5 h-5 text-brand-500 shrink-0 mt-0.5" />
        <div class="space-y-1 text-xs">
          <p class="font-bold text-text-main">Cloudflare Quick Tunnel</p>
          <p class="text-text-muted leading-relaxed">
            Expose your local 9router-go to the internet. No port forwarding, no static IP needed. Share endpoint URL with your team or use it in Cursor, Cline, and other AI tools from anywhere.
          </p>
        </div>
      </div>

      <!-- Feature Grid -->
      <div class="grid grid-cols-2 gap-2.5">
        {#each tunnelValueProps as prop (prop.title)}
          <div class="p-3 rounded-xl bg-surface-2/60 border border-border/50 text-center space-y-1">
            <p class="text-xs font-bold text-text-main">{prop.title}</p>
            <p class="text-[11px] text-text-muted">{prop.desc}</p>
          </div>
        {/each}
      </div>

      <p class="text-xs text-text-subtle">
        Requires outbound port 7844 (TCP/UDP). Connection may take 10-30s.
      </p>

      <div class="flex gap-2 pt-2">
        <button
          type="button"
          onclick={startTunnel}
          class="flex-1 py-2 px-4 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs transition cursor-pointer shadow-sm"
        >
          Start Tunnel
        </button>
        <button
          type="button"
          onclick={() => (showEnableTunnelModal = false)}
          class="flex-1 py-2 px-4 rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main font-semibold text-xs transition cursor-pointer border border-border"
        >
          Cancel
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- MODAL: Disable Cloudflare Tunnel -->
{#if showDisableTunnelModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
    <div class="w-full max-w-md p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <h3 class="text-base font-bold text-text-main">Disable Tunnel</h3>
        <button
          type="button"
          onclick={() => (showDisableTunnelModal = false)}
          class="text-text-muted hover:text-text-main cursor-pointer"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <p class="text-sm text-text-muted leading-relaxed">
        The Cloudflare tunnel will be disconnected. Remote access via tunnel URL will stop working.
      </p>

      <div class="flex gap-2 pt-2">
        <button
          type="button"
          onclick={stopTunnel}
          class="flex-1 py-2 px-4 rounded-lg bg-danger hover:bg-danger/90 text-white font-semibold text-xs transition cursor-pointer"
        >
          Disable Tunnel
        </button>
        <button
          type="button"
          onclick={() => (showDisableTunnelModal = false)}
          class="flex-1 py-2 px-4 rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main font-semibold text-xs transition cursor-pointer border border-border"
        >
          Cancel
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- MODAL: Tailscale Funnel -->
{#if showTailscaleModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
    <div class="w-full max-w-md p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <h3 class="text-base font-bold text-text-main">Tailscale Funnel</h3>
        <button
          type="button"
          onclick={() => (showTailscaleModal = false)}
          class="text-text-muted hover:text-text-main cursor-pointer"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      {#if tailscaleInstalled === false}
        <div class="space-y-3">
          <p class="text-sm text-text-muted leading-relaxed">
            Tailscale CLI is not installed or detected on your system path. Install Tailscale to enable Funnel.
          </p>
          <div class="p-3 rounded-lg bg-surface-2 border border-border font-mono text-xs text-text-main select-all">
            curl -fsSL https://tailscale.com/install.sh | sh
          </div>
        </div>
      {:else}
        <div class="space-y-3">
          <p class="text-sm text-text-muted leading-relaxed">
            Tailscale is installed. Click Connect to expose your 9router-go via Tailscale Funnel.
          </p>
        </div>
      {/if}

      <div class="flex gap-2 pt-2">
        {#if tailscaleInstalled}
          <button
            type="button"
            onclick={startTailscale}
            class="flex-1 py-2 px-4 rounded-lg bg-indigo-600 hover:bg-indigo-700 text-white font-semibold text-xs transition cursor-pointer"
          >
            Connect
          </button>
        {/if}
        <button
          type="button"
          onclick={() => (showTailscaleModal = false)}
          class="flex-1 py-2 px-4 rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main font-semibold text-xs transition cursor-pointer border border-border"
        >
          Cancel
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- MODAL: Disable Tailscale -->
{#if showDisableTailscaleModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
    <div class="w-full max-w-md p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <h3 class="text-base font-bold text-text-main">Disable Tailscale</h3>
        <button
          type="button"
          onclick={() => (showDisableTailscaleModal = false)}
          class="text-text-muted hover:text-text-main cursor-pointer"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <p class="text-sm text-text-muted leading-relaxed">
        Tailscale Funnel will be stopped. Remote access via Tailscale URL will stop working.
      </p>

      <div class="flex gap-2 pt-2">
        <button
          type="button"
          onclick={stopTailscale}
          class="flex-1 py-2 px-4 rounded-lg bg-danger hover:bg-danger/90 text-white font-semibold text-xs transition cursor-pointer"
        >
          Disable Tailscale
        </button>
        <button
          type="button"
          onclick={() => (showDisableTailscaleModal = false)}
          class="flex-1 py-2 px-4 rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main font-semibold text-xs transition cursor-pointer border border-border"
        >
          Cancel
        </button>
      </div>
    </div>
  </div>
{/if}
