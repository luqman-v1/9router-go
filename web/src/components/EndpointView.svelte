<script lang="ts">
  import { onMount } from 'svelte'
  import {
    AlertCircle,
    AlertTriangle,
    Check,
    CloudUpload,
    Copy,
    Eye,
    EyeOff,
    ExternalLink,
    Loader2,
    Radio,
    RotateCcw,
    Settings as SettingsIcon,
    Wrench,
    Shield,
    X
  } from 'lucide-svelte'
  import Card from '../lib/ui/Card.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import Menu from '../lib/ui/Menu.svelte'
  import MenuItem from '../lib/ui/MenuItem.svelte'
  import { copyToClipboard } from '../lib/clipboard'
  import type { Snippet } from 'svelte'
  import ApiKeysTable from './keys/ApiKeysTable.svelte'
  import { api, type APIKey, type Settings, type TunnelStatusResponse } from '../api/client'
  import type { ActiveTab } from '../lib/router'
  interface Props {
    apiKeys?: APIKey[]
    settings?: Settings
    onRefresh?: () => void
    navigate?: (tab: ActiveTab) => void
  }

  let {
    apiKeys = [],
    settings = {},
    onRefresh,
    navigate,
  }: Props = $props()

  // Local state for the endpoint + tunnel settings. The key list owns its own
  // state in ApiKeysTable (issue #199); nothing here mirrors it any more.
  let requireApiKey = $state(false)
  let requireLogin = $state(true)
  let hasPassword = $state(true)
  let tunnelDashboardAccess = $state(false)
  // copiedId is also used for the endpoint/tunnel URL buttons below, so it
  // stays here rather than moving into the table.
  let copiedId = $state<string | null>(null)
  // The mapping service the gateway uses when the operator has not set one.
  // Kept in sync with internal/tunnel.defaultWorkerURL — the backend restores
  // this same value when the setting is cleared, so the reset button and the
  // server agree on what "default" means.
  const defaultWorkerUrl = 'https://abc-tunnel.us'

  // URL visibility. An endpoint URL is a credential in practice: anyone who can
  // read it can call the gateway, so the rows default to masked and reveal on
  // demand rather than the other way round.
  let showTunnelUrl = $state(false)
  let showTailscaleUrl = $state(false)
  // Local gets the same toggle. Its URL is the least sensitive of the three —
  // it only resolves on the machine itself — but the row used to be the one
  // that could not be masked at all, which made the other two look optional.
  let showLocalUrl = $state(true)

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
  // Rotation record. The gateway keeps the address the tunnel served before the
  // current one, so this survives a page refresh — an operator who reloads
  // still learns that the URL they handed out has changed.
  let tunnelPreviousUrl = $state('')
  let tunnelRotatedAt = $state('')
  let tunnelConnectionState = $state('')
  let isTunnelLoading = $state(false)
  let tunnelStatusText = $state('')
  let tunnelError = $state<string | null>(null)
  // One modal carries both expose paths, so there is a single visibility flag
  // and a tab selector. The six modals this replaced — enable, settings,
  // restart-confirm and disable for Cloudflare, plus connect and disable for
  // Tailscale — each needed their own flag, and the operator had to remember
  // which one held what.
  let showTunnelSettingsModal = $state(false)
  let tunnelSettingsTab = $state<'global' | 'cloudflare' | 'tailscale'>('cloudflare')
  // Confirmation dialogs stay separate: they interrupt a decision the operator
  // has already made, which is a different job from configuring the tunnel.
  let showDisableTunnelModal = $state(false)
  let showDisableTailscaleModal = $state(false)

  // Tunnel customization. Quick tunnel is the default and needs nothing; the
  // named mode is opt-in because it requires a Cloudflare token the operator
  // has to create first.
  let tunnelMode = $state<'quick' | 'named' | 'auto'>('quick')
  // The method the gateway reports as active. Kept apart from tunnelMode,
  // which is the modal's selection: the two differ the moment the operator
  // opens the switch dialog, and the confirmation must name the running one.
  let currentTunnelMode = $state('quick')
  // The configuration the running tunnel was started with. Start compares the
  // form against this snapshot: a tunnel that is already up and unchanged is
  // left alone, and one whose settings differ is rebuilt. Without the snapshot
  // every Start press would either silently no-op or tear down a healthy
  // tunnel for no reason.
  let activeTunnelConfig = $state<{
    mode: string
    shortId: string
    customDomain: string
    token: string
  } | null>(null)
  let tunnelShortId = $state('')
  let tunnelToken = $state('')
  let tunnelCustomDomain = $state('')
  // Token fields are plain text so the operator can see what they typed — a
  // password field hides typos, and a mistyped token fails at connect time with
  // nothing pointing at the field. Visibility is opt-out per field instead.
  let showTunnelToken = $state(false)
  let showCfApiToken = $state(false)
  let showTailscaleKey = $state(false)
  // Auto mode lets the gateway create the tunnel and its DNS record from a
  // Cloudflare API token, so there is no manual dashboard step to remember.
  let tunnelCfApiToken = $state('')
  let tunnelCfAccountId = $state('')
  let tunnelCfZone = $state('')

  // Watchdog + mapping service. The watchdog is on by default because a
  // tunnel that dies silently is worse than one that restarts itself; the
  // worker URL is blank by default, which means "use the built-in service".
  let tunnelWatchdogEnabled = $state(true)
  let tailscaleWatchdogEnabled = $state(true)
  // Hiding an unused row is the opposite default: an operator who never touched
  // the setting must still see both rows, because a row that is not there
  // cannot be turned on. Absent therefore means off, not on.
  let hideUnusedTunnel = $state(false)
  let tunnelWorkerUrl = $state('')
  let isSavingWorkerUrl = $state(false)
  let workerUrlSaved = $state(false)
  // Origin repair. The message is shown inline rather than as an alert because
  // it is a confirmation of a change the operator can verify by reloading the
  // endpoint — an alert would interrupt that check.
  let isRepairingOrigin = $state(false)
  let originRepairMessage = $state('')
  let originRepairError = $state(false)

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
  // Auth key for a headless node. Left empty, the enable falls back to the
  // interactive browser login, which is the right default on a desktop and
  // impossible on a server.
  let tailscaleAuthKey = $state('')
  // Which way in the operator picked. Defaults to interactive because that is
  // what a desktop user expects, and the key path is the one that needs
  // explaining.
  let tailscaleAuthMode = $state<'interactive' | 'key'>('interactive')
  let tailscaleInstalled = $state<boolean | null>(null)

  // A row counts as in use while it is actually running, not merely while it is
  // switched on in settings. `enabled` only records that the operator asked for
  // it once — a Cloudflare tunnel that is enabled but has no connector is not
  // serving anything, and hiding it is the whole point of the switch. Tailscale
  // is the exception in practice: its funnel is what reconnects, so a row that
  // is enabled but momentarily down still belongs on screen.
  let showTunnelRow = $derived(!hideUnusedTunnel || tunnelRunning)
  let showTailscaleRow = $derived(!hideUnusedTunnel || tailscaleEnabled)

  // Create-key modal state. The table drives every other key action itself;
  // only issuing a new key still belongs to the endpoint page, because it is
  // what the integration snippets above point an operator at.
  let isCreateKeyOpen = $state(false)
  let newKeyName = $state('')
  let isSubmittingKey = $state(false)
  let newlyCreatedKey = $state<string | null>(null)

  $effect(() => {
    if (settings) {
      requireApiKey = !!settings.requireApiKey
      tunnelDashboardAccess = !!settings.tunnelDashboardAccess
      if (typeof settings.requireLogin === 'boolean') requireLogin = settings.requireLogin
      if (typeof settings.hasPassword === 'boolean') hasPassword = settings.hasPassword
      // The watchdog switches and the row-visibility switch are read here too,
      // not only in onMount. onRefresh re-fetches settings after a write, and a
      // switch that only onMount populates keeps showing the value the operator
      // last pressed rather than the one the server stored — which is exactly
      // what a rollback needs to undo.
      if (typeof settings.tunnelWatchdogEnabled === 'boolean') {
        tunnelWatchdogEnabled = settings.tunnelWatchdogEnabled
      }
      if (typeof settings.tailscaleWatchdogEnabled === 'boolean') {
        tailscaleWatchdogEnabled = settings.tailscaleWatchdogEnabled
      }
      hideUnusedTunnel = settings.hideUnusedTunnel === true
    }
  })

  // Browser-side health probe: must reach origin (not just CF/TS edge).
  // The gateway serves /health; /api/health is the upstream Next.js path and
  // is tried second so a tunnel pointing at either origin resolves. Probing
  // only the upstream path is what left the card stuck on "Tunnel checking...":
  // the probe got a 404 from a gateway that was up and answering.
  async function clientPingUrl(url: string): Promise<boolean> {
    if (!url) return false
    for (const path of ['/health', '/api/health']) {
      try {
        const controller = new AbortController()
        const timeoutId = setTimeout(() => controller.abort(), 5000)
        const res = await fetch(`${url}${path}`, {
          mode: 'cors',
          cache: 'no-store',
          signal: controller.signal,
        })
        clearTimeout(timeoutId)
        if (res.ok) return true
      } catch {
        // try the next path
      }
    }
    return false
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
      const [settingsRes, tunnelRes] = await Promise.all([
        api.getSettings().catch(() => null),
        api.getTunnelStatus().catch(() => null)
      ])

      if (settingsRes) {
        requireApiKey = !!settingsRes.requireApiKey
        tunnelDashboardAccess = !!settingsRes.tunnelDashboardAccess
        // Absent means on: the watchdog is the default behaviour, and an
        // operator who never touched the setting must not have it silently
        // off because a missing key read as false.
        tunnelWatchdogEnabled = settingsRes.tunnelWatchdogEnabled !== false
        tailscaleWatchdogEnabled = settingsRes.tailscaleWatchdogEnabled !== false
        hideUnusedTunnel = settingsRes.hideUnusedTunnel === true
        tunnelWorkerUrl = settingsRes.tunnelWorkerUrl || ''
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
      tunnelPreviousUrl = status.tunnel.previousUrl || ''
      tunnelRotatedAt = status.tunnel.rotatedAt || ''
      tunnelConnectionState = status.tunnel.state || ''
      currentTunnelMode = status.tunnel.mode || 'quick'
      // Seed the mapping-service field with what the gateway is actually using.
      // An empty field reads as "nothing configured", which sends the operator
      // looking for a default they cannot see — and the placeholder alone does
      // not tell them whether the running tunnel is on the default service or
      // one they set earlier.
      if (status.tunnel.workerUrl) {
        tunnelWorkerUrl = status.tunnel.workerUrl
      }
      // Seed the comparison snapshot from what the gateway reports as running.
      // Without this a page refresh would lose the record and the next Start
      // press could not tell an unchanged form from a real change.
      if (tunnelRunning) {
        // Populate the form itself, not only the comparison snapshot. The
        // gateway knows the shortId and hostname it is running with; leaving
        // the inputs empty made an operator reopening the modal see blank
        // fields next to a connected tunnel, with no way to tell what it was
        // started with.
        if (status.tunnel.shortId) {
          tunnelShortId = status.tunnel.shortId
        }
        if (status.tunnel.customDomain) {
          tunnelCustomDomain = status.tunnel.customDomain
        }
        // The stored credentials are repopulated too. They are persisted so a
        // restored configuration works without being set up again, and the
        // operator needs to see what is actually stored before restarting a
        // tunnel — blank fields next to a connected tunnel read as "nothing
        // saved", which is the opposite of the truth.
        if (status.tunnel.token) {
          tunnelToken = status.tunnel.token
        }
        if (status.tunnel.cfApiToken) {
          tunnelCfApiToken = status.tunnel.cfApiToken
        }
        activeTunnelConfig = {
          mode: currentTunnelMode,
          shortId: status.tunnel.shortId || '',
          customDomain: status.tunnel.customDomain || '',
          token: '',
        }
        // The token is never echoed back by the API, so a named tunnel's token
        // cannot be compared. Treat it as matching: the operator re-typing the
        // same token is the common case, and a rotated one is caught by the
        // backend rejecting it.
        if (currentTunnelMode === 'named' && tunnelToken.trim()) {
          activeTunnelConfig.token = tunnelToken.trim()
        }
      } else {
        activeTunnelConfig = null
      }
    }
    if (status.tailscale) {
      tailscaleEnabled = !!(status.tailscale.settingsEnabled ?? status.tailscale.enabled)
      tailscaleRunning = !!status.tailscale.running
      tailscaleUrl = status.tailscale.tunnelUrl || ''
      // A stored key means the gateway can re-authenticate on its own, so the
      // dropdown should reflect that rather than defaulting to the interactive
      // path the operator already moved past.
      if (status.tailscale.authKey) {
        tailscaleAuthKey = status.tailscale.authKey
        tailscaleAuthMode = 'key'
      }
    }
  }

  onMount(() => {
    loadStatus()
    // Escape closes whichever overlay is open, the way a dialog is expected to
    // behave. The backdrop click covers the mouse; this covers the keyboard,
    // and it is the only way out for someone who opened the modal and then
    // decided against it without reaching for the pointer.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (showTunnelSettingsModal) showTunnelSettingsModal = false
      else if (showDisableTunnelModal) showDisableTunnelModal = false
      else if (showDisableTailscaleModal) showDisableTailscaleModal = false
    }
    window.addEventListener('keydown', onKey)
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
      window.removeEventListener('keydown', onKey)
    }
  })

  function copy(text: string, id: string) {
    copyToClipboard(text)
    copiedId = id
    setTimeout(() => {
      if (copiedId === id) copiedId = null
    }, 2000)
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

  // Human-readable name of the method currently in use, for the switch
  // confirmation. Derived from the last status response rather than from the
  // modal's own selection, so the warning describes what is actually
  // connected rather than what the operator is about to pick.
  // The stable URL the current shortId and mapping service will produce.
  //
  // The shape mirrors what the gateway actually serves: the worker maps
  // `r<shortId>` as a subdomain of its own host, so `https://abc-tunnel.us` with
  // shortId `ajaja` becomes `https://rajaja.abc-tunnel.us`. Deriving it from the
  // field the operator is editing means the preview follows what they type,
  // including a self-hosted mapping service, instead of describing a URL they
  // have to assemble in their head.
  let previewStableUrl = $derived.by(() => {
    const id = tunnelShortId.trim()
    if (!id) return ''
    const raw = tunnelWorkerUrl.trim() || 'https://abc-tunnel.us'
    const withScheme = raw.includes('://') ? raw : 'https://' + raw
    let host = withScheme
      .replace(/^https?:\/\//, '')
      .replace(/\/+$/, '')
    if (host === '') return ''
    return `https://r${id}.${host}`
  })

  // The rotation notice, or null when the URL has not changed. A quick tunnel
  // rotates its hostname on every reconnect, and the stable alias absorbs it —
  // so the only thing that breaks is an address the operator copied by hand
  // earlier. This surfaces that case instead of leaving the old URL to fail
  // silently in whatever client it was pasted into.
  let tunnelRotation = $derived.by(() => {
    if (!tunnelPreviousUrl || !tunnelUrl) return null
    if (tunnelPreviousUrl === tunnelUrl) return null
    let when = ''
    if (tunnelRotatedAt) {
      const then = new Date(tunnelRotatedAt)
      if (!Number.isNaN(then.getTime())) {
        const mins = Math.round((Date.now() - then.getTime()) / 60000)
        when = mins < 1 ? 'just now' : mins < 60 ? `${mins} min ago` : `${Math.round(mins / 60)}h ago`
      }
    }
    return { previous: tunnelPreviousUrl, when }
  })

  // What the amber row says while the endpoint is unreachable.
  //
  // The three cases read the same from the dashboard but mean different things:
  // a tunnel that has never answered is still starting, one that answered and
  // stopped is reconnecting, and one whose connector is alive but endpoint is
  // dead is stuck — that last one is what the origin repair fixes, and telling
  // the operator to "wait" would waste their time.
  let tunnelConnectingLabel = $derived.by(() => {
    if (tunnelEverReachable) return 'Tunnel reconnecting...'
    if (tunnelConnectionState === 'connecting' && tunnelRunning) return 'Tunnel connected but not answering — check the origin port'
    return 'Tunnel checking...'
  })

  // Toggle the Cloudflare watchdog. Persisted immediately: the running
  // watchdog reads the setting on its next tick, so the change takes effect
  // without a gateway restart.
  async function toggleTunnelWatchdog(value: boolean) {
    tunnelWatchdogEnabled = value
    try {
      await api.updateSettings({ tunnelWatchdogEnabled: value })
    } catch (err) {
      tunnelWatchdogEnabled = !value
      alert(`Failed to update Cloudflare auto-reconnect: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Whether the form differs from the running tunnel. A change means Start has
  // to tear the current tunnel down and build a new one, which is disruptive
  // enough to confirm first. The token is compared too, because a rotated
  // connector token is a real change even when nothing else moved.
  let tunnelConfigChanged = $derived.by(() => {
    if (!activeTunnelConfig || !tunnelEnabled) return false
    const a = activeTunnelConfig
    return (
      a.mode !== tunnelMode ||
      a.shortId !== (tunnelMode === 'quick' ? tunnelShortId.trim() : '') ||
      a.customDomain !== (tunnelMode === 'quick' ? '' : tunnelCustomDomain.trim()) ||
      (tunnelMode === 'named' && a.token !== tunnelToken.trim())
    )
  })

  // Open the settings modal on a given tab. Both entry points — the gear on the
  // endpoint card and the Enable button on the tunnel row — land on the same
  // modal, so there is no origin to remember and no way to get lost.
  function openTunnelSettings(tab: 'global' | 'cloudflare' | 'tailscale' = 'cloudflare') {
    tunnelSettingsTab = tab
    showTunnelSettingsModal = true
  }

  // Pick a method. Only the fields that belong to the previous method are
  // cleared — a token left over from a quick-tunnel attempt would otherwise be
  // sent with the new method, and the backend would read it as a request for a
  // named tunnel.
  //
  // The stored tokens are NOT cleared. They are persisted so a restore works
  // without being set up again, and wiping them here would silently undo that
  // the moment the operator changed method to look at another option.
  function selectTunnelMode(mode: 'quick' | 'named' | 'auto') {
    tunnelMode = mode
    tunnelCfAccountId = ''
    tunnelCfZone = ''
  }

  // Toggle the Tailscale watchdog. A separate setting from the Cloudflare one:
  // an operator may supervise one expose path themselves and want the other
  // looked after, and a single switch would force both on or both off.
  async function toggleTailscaleWatchdog(value: boolean) {
    tailscaleWatchdogEnabled = value
    try {
      await api.updateSettings({ tailscaleWatchdogEnabled: value })
    } catch (err) {
      tailscaleWatchdogEnabled = !value
      alert(`Failed to update Tailscale auto-reconnect: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Toggle whether an idle expose row disappears. The optimistic flip and the
  // rollback match the watchdog toggles: the switch moves the moment it is
  // pressed, and a failed write puts it back rather than leaving the UI
  // claiming a state the server never stored.
  async function toggleHideUnusedTunnel(value: boolean) {
    hideUnusedTunnel = value
    try {
      await api.updateSettings({ hideUnusedTunnel: value })
      onRefresh?.()
    } catch (err) {
      hideUnusedTunnel = !value
      alert(`Failed to update hide unused tunnel: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
  // Reset the mapping service to the default. Clearing the setting on the server
  // is what restores it — the backend treats an empty value as "use the
  // default" rather than as "no mapping service", which would silently break the
  // stable URL.
  async function resetTunnelWorkerUrl() {
    tunnelWorkerUrl = defaultWorkerUrl
    workerUrlSaved = false
    isSavingWorkerUrl = true
    try {
      await api.updateSettings({ tunnelWorkerUrl: '' })
      workerUrlSaved = true
    } catch (err) {
      alert(`Failed to reset mapping service: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSavingWorkerUrl = false
    }
  }

  // Repair the tunnel's ingress rule. The server rewrites it against the port
  // this gateway is actually listening on, which is the value the operator
  // cannot see from the Zero Trust dashboard.
  async function repairTunnelOrigin() {
    isRepairingOrigin = true
    originRepairMessage = ''
    originRepairError = false
    try {
      const res = await api.repairTunnelOrigin()
      if (res.error) throw new Error(res.error)
      originRepairMessage = `Ingress rule now points at ${res.hostname ?? ''} → port ${res.port ?? ''}.`
    } catch (err) {
      originRepairError = true
      originRepairMessage = err instanceof Error ? err.message : String(err)
    } finally {
      isRepairingOrigin = false
    }
  }

  // Save the mapping service. Blank restores the built-in default, which is
  // what an operator clearing the field means — not "stop registering". The
  // confirmation is explicit because the field used to save on blur, where a
  // mis-click outside the modal closed it and the operator could not tell
  // whether the value had been stored.
  async function saveTunnelWorkerUrl() {
    const next = tunnelWorkerUrl.trim()
    isSavingWorkerUrl = true
    workerUrlSaved = false
    try {
      await api.updateSettings({ tunnelWorkerUrl: next })
      workerUrlSaved = true
    } catch (err) {
      alert(`Failed to update mapping service: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSavingWorkerUrl = false
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

  // Pick how this machine authenticates to the tailnet. The key is kept when
  // leaving the key mode: it is stored so the gateway can re-authenticate on
  // its own, and clearing it here would discard that for no reason. The enable
  // call decides which path to take from the mode, not from whether the field
  // happens to be empty.
  function selectTailscaleAuthMode(mode: 'interactive' | 'key') {
    tailscaleAuthMode = mode
  }

  // Start Cloudflare Tunnel. The confirmation for a restart lives inline in the
  // settings modal rather than in a dialog of its own: the operator is already
  // looking at the change they made, so a second modal only adds a layer
  // between them and the button they need.
  async function doStartTunnel() {
    isTunnelLoading = true
    tunnelStatusText = 'Creating tunnel...'
    tunnelError = null

    try {
      const res = await api.enableTunnel({
        // shortId is quick-tunnel only: it feeds the mapping service's stable
        // alias. A named or auto-provisioned tunnel has a fixed hostname of the
        // operator's own, so sending a shortId would be meaningless — and the
        // backend would fall back to it if the field were left populated from
        // an earlier quick-tunnel attempt.
        shortId: tunnelMode === 'quick' ? tunnelShortId.trim() || undefined : undefined,
        token: tunnelMode === 'named' ? tunnelToken.trim() || undefined : undefined,
        customDomain: tunnelMode !== 'quick' ? tunnelCustomDomain.trim() || undefined : undefined,
        cfApiToken: tunnelMode === 'auto' ? tunnelCfApiToken.trim() || undefined : undefined,
        cfAccountId: tunnelMode === 'auto' ? tunnelCfAccountId.trim() || undefined : undefined,
        cfZone: tunnelMode === 'auto' ? tunnelCfZone.trim() || undefined : undefined,
      })
      if (res.error) {
        tunnelError = res.error
        return
      }
      if (res.tunnelUrl) {
        tunnelUrl = res.tunnelUrl
        publicUrl = res.publicUrl || ''
        tunnelEnabled = true
        tunnelRunning = true
        // Snapshot what the tunnel is now running with, so the next Start
        // press can tell an unchanged form from a real change.
        activeTunnelConfig = {
          mode: res.mode || tunnelMode,
          shortId: res.shortId || (tunnelMode === 'quick' ? tunnelShortId.trim() : ''),
          customDomain: tunnelMode === 'quick' ? '' : tunnelCustomDomain.trim(),
          token: tunnelMode === 'named' ? tunnelToken.trim() : '',
        }
        currentTunnelMode = res.mode || tunnelMode
        // Probe both URLs immediately rather than waiting for the 10-second
        // browser ping. The tunnel is up and the operator is looking at the row;
        // making them wait ten seconds to find out whether it answers is a gap
        // the interval alone cannot close.
        const cfUrl = res.publicUrl || res.tunnelUrl
        if (cfUrl) {
          markReachable(await clientPingUrl(cfUrl), 'tunnel')
        }
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
      // Nothing is running, so there is no configuration to compare against.
      activeTunnelConfig = null
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
    } catch {
      // A failed check is not fatal: startTailscale reports the real reason
      // from the enable call, which is more specific than "could not check".
      tailscaleInstalled = null
    }
    startTailscale()
  }

  async function startTailscale() {
    // The settings modal stays open. It is where the operator pressed Connect,
    // and the interactive login needs a button they can reach — closing it
    // would leave the login URL only on the endpoint card, behind the overlay
    // they just dismissed.
    isTailscaleLoading = true
    tailscaleStatusText = 'Connecting to Tailscale...'
    tailscaleError = null

    try {
      const res = await api.enableTailscale({
        authKey: tailscaleAuthMode === 'key' ? tailscaleAuthKey.trim() || undefined : undefined,
      })
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
        tailscaleAuthUrl = ''
        // Probe the new URL immediately. The browser ping runs on a 10-second
        // interval, so without this the row sits on "checking..." for up to ten
        // seconds after a connection that already succeeded — the operator has
        // a URL in hand and no confirmation it works.
        markReachable(await clientPingUrl(res.tunnelUrl), 'tailscale')
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



  // maskUrl hides the whole URL, not part of it.
  //
  // The previous mask kept the scheme and the host's first label visible so an
  // operator could tell which endpoint a row was. That is a real need, but it
  // was being served by the wrong control: the label above the row already
  // names the endpoint, and the reveal toggle is one click away. What the
  // partial mask actually did was leak the hostname — the one part of a tunnel
  // URL that is not already on screen — to anyone reading over a shoulder.
  //
  // So it is all or nothing: a fixed-width run of bullets that does not vary
  // with the URL's length, because a mask whose length tracks the thing it hides
  // tells the reader how long the hidden value is.
  function maskUrl(_url: string): string {
    return '•'.repeat(24)
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

</script>

{#snippet endpointBox(o: {
  label: string
  labelClass?: string
  active: boolean
  menuLabel: string
  urlValue: string
  revealed: boolean
  children: Snippet
})}
  <!-- One endpoint row: label bar with its action, then the URL beneath it,
       inside a single rounded box.

       The three rows on this card (Local, Tunnel, Tailscale) are the same
       control with different contents, so they share this snippet. Before it
       existed, Local was one box while Tunnel and Tailscale were a coloured bar
       plus a separate input with its own border — three rows that read as three
       different components.

       The URL line is the address and nothing else. Show/hide and copy moved to
       the overflow menu, so the input keeps the full width of the box — on a
       phone, two icon buttons beside it cost a third of the row and truncated
       the address it exists to show.

       A row with no URL yet shows its status text in the same slot, so the box
       keeps its height instead of collapsing and reflowing the card when a
       tunnel comes up. -->
  <div class="rounded-lg bg-surface-2 border border-border overflow-hidden">
    <div class="relative flex items-center gap-2 h-9 px-2">
      <span class="w-1.5 h-1.5 rounded-full shrink-0 {o.active ? 'bg-success' : 'bg-text-subtle'}"></span>
      <span
        class="min-w-0 flex-1 text-xs font-mono text-left font-semibold truncate {o.active
          ? o.labelClass || 'text-text-main'
          : 'text-text-muted'}"
      >
        {o.label}
      </span>
      <div class="relative z-10 w-9 shrink-0">
        {@render o.children()}
      </div>
    </div>
    {#if o.urlValue}
      <!-- The URL, and nothing beside it. Show/hide and copy live in the row's
           overflow menu, beside the other verbs that act on this endpoint. Two
           icon buttons on the URL line also cost the input a third of its width
           on a phone, which truncated the very address the row exists to show.

           The mask is all-or-nothing: a partial mask kept the hostname visible,
           and the hostname is the one part of a tunnel URL that is not already
           on screen. The label above names the endpoint, so nothing needs
           guessing from a half-shown string. -->
      <div class="border-t border-border-subtle px-3">
        <input
          type="text"
          value={o.revealed ? `${o.urlValue}/v1` : maskUrl(`${o.urlValue}/v1`)}
          readonly
          class="w-full min-w-0 font-mono text-sm bg-transparent border-0 rounded-none py-1.5 text-text-main selection:bg-brand-500/30 focus:outline-none"
        />
      </div>
    {:else}
      <div class="flex items-center gap-2 border-t border-border-subtle px-3 py-1.5 text-[11px] text-text-muted">
        {#if isTunnelLoading && o.label === 'Tunnel'}
          <Loader2 class="w-3.5 h-3.5 animate-spin text-brand-500" />
          <span class="truncate">{tunnelStatusText || 'Creating tunnel...'}</span>
        {:else if isTailscaleLoading && o.label === 'Tailscale'}
          <Loader2 class="w-3.5 h-3.5 animate-spin text-indigo-400" />
          <span class="truncate">{tailscaleStatusText || 'Connecting...'}</span>
        {:else if o.label === 'Tunnel' && tunnelError}
          <AlertCircle class="w-3.5 h-3.5 shrink-0 text-danger" />
          <span class="truncate">{tunnelError}</span>
        {:else if o.label === 'Tailscale' && tailscaleError}
          <AlertCircle class="w-3.5 h-3.5 shrink-0 text-danger" />
          <span class="truncate">{tailscaleError}</span>
        {:else if o.label === 'Tunnel' && tunnelEnabled}
          <Loader2 class="w-3.5 h-3.5 animate-spin text-amber-500" />
          <span class="truncate">{tunnelConnectingLabel}</span>
        {:else if o.label === 'Tailscale' && tailscaleEnabled}
          <Loader2 class="w-3.5 h-3.5 animate-spin text-amber-500" />
          <span class="truncate">{tailscaleEverReachable ? 'Tailscale reconnecting...' : 'Tailscale checking...'}</span>
        {:else}
          <span>Not connected</span>
        {/if}
      </div>
    {/if}
  </div>
{/snippet}

{#snippet localMenu()}
  <!-- Local now renders through the same Menu as Tunnel and Tailscale.
       It used to be a bare copy button, on the reasoning that a one-item menu
       costs a click to reach the same action. But Local is the only row whose
       URL is shown in full with no way to hide it, so the button left the
       address on screen permanently — and three rows carrying two different
       action controls read as three different components rather than one list.
       The menu also gives Local somewhere to put the mask toggle the other two
       already had. -->
  <Menu label="Local actions" hideLabel iconOnly align="right" minWidth="12rem">
    <MenuItem
      label={showLocalUrl ? 'Hide URL' : 'Show URL'}
      icon={showLocalUrl ? 'visibility_off' : 'visibility'}
      checkbox
      pressed={showLocalUrl}
      onSelect={() => (showLocalUrl = !showLocalUrl)}
    />
    <MenuItem label="Copy URL" icon="content_copy" onSelect={() => copy(localEndpoint, 'local_url')} />
    <!-- Local is the only row that is not a tunnel, so it has no tunnel of its
         own to configure — but it is the row an operator lands on first, and
         the Global tab is where both tunnels' behaviour and dashboard access
         live. Without this entry the only way in was the gear on a row that
         might not be connected at all. -->
    <MenuItem label="Settings" icon="tune" onSelect={() => openTunnelSettings('global')} />
  </Menu>
{/snippet}

{#snippet tunnelMenu()}
  <Menu label="Tunnel actions" hideLabel iconOnly align="right" minWidth="12rem">
    {#if tunnelEnabled && tunnelUrl}
      <MenuItem
        label={showTunnelUrl ? 'Hide URL' : 'Show URL'}
        icon={showTunnelUrl ? 'visibility_off' : 'visibility'}
        checkbox
        pressed={showTunnelUrl}
        onSelect={() => (showTunnelUrl = !showTunnelUrl)}
      />
      <MenuItem
        label="Copy URL"
        icon="content_copy"
        onSelect={() => copy(`${publicUrl || tunnelUrl}/v1`, 'tunnel_url')}
      />
    {/if}
    {#if tunnelEnabled}
      <!-- Always enabled. It used to be gated on tunnelConfigChanged, on the
           theory that a restart with nothing to apply would tear down a healthy
           tunnel for no reason. But restarting is itself a reason: an operator
           whose tunnel is answering slowly, or who just wants a clean connector,
           has no change to make and no way to say so. The gate turned a
           legitimate action into a dead menu item labelled "no changes". -->
      <MenuItem label="Restart" icon="restart_alt" onSelect={doStartTunnel} />
      <MenuItem label="Settings" icon="tune" onSelect={() => openTunnelSettings('cloudflare')} />
      <div class="my-1 border-t border-border-subtle" role="separator"></div>
      <MenuItem
        label="Disable"
        icon="power_settings_new"
        danger
        onSelect={() => (showDisableTunnelModal = true)}
      />
    {:else}
      <MenuItem label="Enable" icon="shield" onSelect={doStartTunnel} />
    {/if}
  </Menu>
{/snippet}

{#snippet tailscaleMenu()}
  <Menu label="Tailscale actions" hideLabel iconOnly align="right" minWidth="12rem">
    {#if tailscaleEnabled && tailscaleUrl}
      <MenuItem
        label={showTailscaleUrl ? 'Hide URL' : 'Show URL'}
        icon={showTailscaleUrl ? 'visibility_off' : 'visibility'}
        checkbox
        pressed={showTailscaleUrl}
        onSelect={() => (showTailscaleUrl = !showTailscaleUrl)}
      />
      <MenuItem
        label="Copy URL"
        icon="content_copy"
        onSelect={() => copy(`${tailscaleUrl}/v1`, 'ts_url')}
      />
    {/if}
    {#if tailscaleEnabled}
      <MenuItem label="Restart" icon="restart_alt" onSelect={startTailscale} />
      <MenuItem label="Settings" icon="tune" onSelect={() => openTunnelSettings('tailscale')} />
      <div class="my-1 border-t border-border-subtle" role="separator"></div>
      <MenuItem
        label="Disable"
        icon="power_settings_new"
        danger
        onSelect={() => (showDisableTailscaleModal = true)}
      />
    {:else}
      <MenuItem label="Enable" icon="shield" onSelect={handleTailscaleClick} />
    {/if}
  </Menu>
{/snippet}

<div class="flex flex-col gap-6">
  <!-- TOP SECTION: API Endpoint Card -->
  <Card padding="md" class="space-y-4">
    <div class="flex items-center gap-2 mb-2">
      <div class="p-2 rounded-lg bg-brand-500/10 text-brand-500">
        <Radio class="w-5 h-5" />
      </div>
      <div class="min-w-0 flex-1">
        <h2 class="text-lg font-semibold text-text-main flex items-center gap-2">
          API Endpoint
        </h2>
        <p class="text-xs text-text-muted">Direct, secure connection points to your local AI router</p>
      </div>
    </div>

    <!-- min-w-0 is load-bearing: inside the card's block flow this column would
         otherwise size to its widest child, and the endpoint row below measures
         itself against that inflated width instead of the card's. Without it the
         input collapses to ~190px on a narrow viewport while the card still has
         room to spare. -->
    <div class="flex flex-col gap-3 min-w-0">
    <!-- Local Endpoint Field. Same box as Tunnel and Tailscale below: a label
         bar with its action, then the URL on the line beneath it, inside one
         rounded rectangle.

         Local was the shape the other two were brought to. Its URL is stable,
         so there is no state dot — but it now carries the same menu as the
         other two, including the mask toggle. -->
    {@render endpointBox({
      label: 'Local',
      active: true,
      menuLabel: 'Local actions',
      urlValue: localOrigin,
      revealed: showLocalUrl,
      children: localMenu,
    })}

      <!-- Tunnel (Cloudflare) and Tailscale (Serve / Funnel).
           Both render through endpointBox, the same snippet Local uses, so all
           three rows are one visual control: a label bar with its action
           button, and the URL on the line beneath it inside the same rounded
           box.

           They used to be laid out differently from Local — a coloured bar for
           the label, then a separate input with its own border and background,
           and four icon buttons squeezed beside the URL on desktop. The result
           was three rows that looked like three different components. Sharing
           the snippet makes the difference between them what it actually is:
           which endpoint it is, and what state it is in.

           The state colour moves to the label text and a dot rather than
           filling the whole bar. A filled bar made a running tunnel look like a
           selected row, and it was the only row on the card carrying a
           background — so the card read as three zones instead of one list. -->
      {#if showTunnelRow}
        {@render endpointBox({
          label: 'Tunnel',
          active: tunnelRunning && !!tunnelUrl,
          menuLabel: 'Tunnel actions',
          urlValue: publicUrl || tunnelUrl,
          revealed: showTunnelUrl,
          children: tunnelMenu,
        })}

        {#if tunnelRotation}
          <!-- A quick tunnel's hostname changes on every reconnect. The stable
               alias absorbs it, so the only thing that breaks is an address the
               operator copied by hand earlier — and nothing else on this card
               would say so. This is that notice, shown only when the URL actually
               changed rather than on every poll. -->
          <div class="rounded-lg border border-warning/30 bg-warning/10 p-2.5 space-y-1.5">
            <p class="text-xs font-semibold text-text-main">Tunnel URL changed</p>
            <p class="text-[11px] text-text-muted leading-relaxed">
              The tunnel reconnected and now serves a new address
              {tunnelRotation.when ? ` (${tunnelRotation.when})` : ''}. Anything still pointed at
              <span class="font-mono break-all">{tunnelRotation.previous}</span> will fail until it is updated. The stable URL
              above keeps working.
            </p>
          </div>
        {/if}
      {/if}

      {#if showTailscaleRow}
        {@render endpointBox({
          label: 'Tailscale',
          labelClass: 'text-indigo-400',
          active: tailscaleRunning && !!tailscaleUrl,
          menuLabel: 'Tailscale actions',
          urlValue: tailscaleUrl,
          revealed: showTailscaleUrl,
          children: tailscaleMenu,
        })}
      {/if}
    <!-- Pre-enable security gate banner (upstream isLoginUnsafe) -->
    {#if isLoginUnsafe && !tunnelEnabled && !tailscaleEnabled}
      <div class="mt-4 flex items-center gap-2 px-3 py-2 rounded-lg bg-amber-500/10 border border-amber-500/20 text-amber-700 dark:text-amber-400 text-xs">
        <AlertTriangle class="w-4 h-4 shrink-0 text-amber-500" />
        <p class="flex-1 leading-relaxed">{unsafeReason}</p>
        <a
          href="/dashboard/profile"
          onclick={(e) => {
            if (navigate) {
              e.preventDefault()
              navigate('settings')
            }
          }}
          class="font-semibold underline hover:opacity-80 shrink-0"
        >
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
            <a
              href="/dashboard/profile"
              onclick={(e) => {
                if (navigate) {
                  e.preventDefault()
                  navigate('settings')
                }
              }}
              class="font-semibold underline hover:opacity-80 shrink-0"
            >
              {!requireLogin ? 'Enable' : 'Change password'}
            </a>
          </div>
        {/if}
      </div>
    {/if}
    <!-- Dashboard access and both tunnels' behaviour moved to the Tunnel
         Settings modal's Global tab. They are questions about the tunnels as a
         pair, so they are asked once, in one place, rather than twice on a card
         that only one of the two tunnels owns. -->
  </Card>

  <!-- BOTTOM SECTION: API Keys. The key list lives here (issue #199) so the
       endpoint a client calls and the token that authorises it are managed in
       one place; the standalone "API Keys" tab was a duplicate of this card. -->
  <ApiKeysTable {apiKeys} onRefresh={onRefresh} onCreate={() => (isCreateKeyOpen = true)} />
</div>

<!-- MODAL: Create API Key -->
{#if isCreateKeyOpen}
  <div class="fixed inset-0 z-\[100\] flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
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
  <div class="fixed inset-0 z-\[100\] flex items-center justify-center bg-black/75 backdrop-blur-sm p-4">
    <div class="w-full max-w-md p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-3 border-b border-border">
        <h3 class="text-base font-bold text-text-main">API Key Created</h3>
      </div>

      <div class="bg-amber-500/10 border border-amber-500/20 rounded-xl p-4 space-y-1 text-amber-700 dark:text-amber-300">
        <p class="text-sm font-bold">Save this key now!</p>
        <p class="text-xs leading-relaxed">
          This key can be revealed and copied again from the table below, but treat it as a
          credential: anyone who reaches this dashboard can read it.
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
<!-- MODAL: Tunnel Settings — one modal, two tabs.
     Cloudflare and Tailscale are the two ways this gateway can be reached
     remotely, and their settings used to live in six separate modals: enable,
     settings, restart-confirm and disable for Cloudflare, plus connect and
     disable for Tailscale. Changing method meant walking through three of them
     in a row, and nothing on screen answered "what is my tunnel running right
     now". One modal with a tab per path puts status, configuration, action and
     recovery side by side, and the restart confirmation is inline rather than a
     dialog of its own. -->
{#if showTunnelSettingsModal}
  <!-- Clicking the backdrop closes, which is what every other overlay on this
       dashboard already does. The handler sits on the backdrop rather than the
       panel and compares the event target against itself, so a click that
       starts inside the panel — a Toggle, a select, a drag that ends on the
       backdrop — does not close the modal on its way out. -->
  <div
    class="fixed inset-0 z-[100] flex items-center justify-center bg-black/75 backdrop-blur-sm p-4"
    onclick={(e) => {
      if (e.target === e.currentTarget) showTunnelSettingsModal = false
    }}
  >
    <div class="w-full max-w-lg rounded-2xl bg-surface border border-border shadow-2xl flex flex-col max-h-[90vh]">
      <!-- Header: title, tabs, close. Fixed so the way out is always reachable. -->
      <div class="p-6 pb-0 border-b border-border shrink-0 space-y-4">
        <div class="flex items-center justify-between">
          <h3 class="text-base font-bold text-text-main">Tunnel Settings</h3>
          <button
            type="button"
            onclick={() => (showTunnelSettingsModal = false)}
            class="text-text-muted hover:text-text-main cursor-pointer"
            aria-label="Close tunnel settings"
          >
            <X class="w-4 h-4" />
          </button>
        </div>
        <div class="flex gap-1" role="tablist" aria-label="Expose path">
          <button
            type="button"
            role="tab"
            aria-selected={tunnelSettingsTab === 'global'}
            onclick={() => (tunnelSettingsTab = 'global')}
            class="flex-1 py-2 px-3 rounded-t-lg text-xs font-semibold transition cursor-pointer border-b-2 {tunnelSettingsTab ===
            'global'
              ? 'border-brand-500 text-brand-500'
              : 'border-transparent text-text-muted hover:text-text-main'}"
          >
            <span class="flex items-center justify-center gap-1.5">
              <SettingsIcon class="w-3.5 h-3.5" />
              Global
            </span>
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={tunnelSettingsTab === 'cloudflare'}
            onclick={() => (tunnelSettingsTab = 'cloudflare')}
            class="flex-1 py-2 px-3 rounded-t-lg text-xs font-semibold transition cursor-pointer border-b-2 {tunnelSettingsTab ===
            'cloudflare'
              ? 'border-brand-500 text-brand-500'
              : 'border-transparent text-text-muted hover:text-text-main'}"
          >
            <span class="flex items-center justify-center gap-1.5">
              <CloudUpload class="w-3.5 h-3.5" />
              Cloudflare
            </span>
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={tunnelSettingsTab === 'tailscale'}
            onclick={() => (tunnelSettingsTab = 'tailscale')}
            class="flex-1 py-2 px-3 rounded-t-lg text-xs font-semibold transition cursor-pointer border-b-2 {tunnelSettingsTab ===
            'tailscale'
              ? 'border-indigo-500 text-indigo-400'
              : 'border-transparent text-text-muted hover:text-text-main'}"
          >
            <span class="flex items-center justify-center gap-1.5">
              <Shield class="w-3.5 h-3.5" />
              Tailscale
            </span>
          </button>
        </div>
      </div>

      <!-- Body: scrolls, so a long configuration never pushes the footer away. -->
      <div class="p-6 space-y-5 overflow-y-auto flex-1 min-h-0">
        {#if tunnelSettingsTab === 'cloudflare'}
          <!-- ── STATUS ─────────────────────────────────────────────── -->
          <section class="space-y-2">
            <h4 class="text-xs font-semibold text-text-main">Status</h4>
            {#if tunnelEnabled && tunnelRunning}
              <div class="rounded-xl border border-brand-500/25 bg-brand-500/5 p-3 space-y-2">
                <!-- The dot and the word are all this line carries. The URL used
                     to sit beside them, which repeated the field directly below
                     it — the same address, twice, one row apart. The status line
                     answers "is it up"; the field below answers "what is the
                     address". Neither needs the other's job. -->
                <div class="flex items-center gap-2">
                  <span class="w-1.5 h-1.5 rounded-full bg-success shrink-0"></span>
                  <p class="text-xs font-semibold text-text-main">Connected</p>
                </div>
                <div class="flex items-center gap-2">
                  <code class="flex-1 min-w-0 font-mono text-xs bg-bg border border-border rounded-lg px-3 py-2 text-text-main truncate">
                    {publicUrl || tunnelUrl}
                  </code>
                  <button
                    type="button"
                    onclick={() => copy(publicUrl || tunnelUrl, 'tunnel_settings_url')}
                    class="p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-brand-500 transition-colors shrink-0 cursor-pointer border border-border"
                    title="Copy tunnel URL"
                  >
                    {#if copiedId === 'tunnel_settings_url'}
                      <Check class="w-4 h-4 text-success" />
                    {:else}
                      <Copy class="w-4 h-4" />
                    {/if}
                  </button>
                </div>
              </div>
            {:else if tunnelEnabled}
              <div class="rounded-xl border border-amber-500/25 bg-amber-500/5 p-3 flex items-center gap-2">
                <Loader2 class="w-4 h-4 animate-spin text-amber-500 shrink-0" />
                <p class="text-xs text-amber-600 dark:text-amber-400">Connecting…</p>
              </div>
            {:else}
              <div class="rounded-xl border border-border bg-surface-2/50 p-3">
                <p class="text-xs text-text-muted">Not connected.</p>
              </div>
            {/if}
          </section>

          <!-- ── METHOD ───────────────────────────────────────────────
               A radio group, not a row of buttons and not a tab set. Only one
               method can be active — there is one origin port and one
               cloudflared — so this is an exclusive choice with a consequence,
               which is what a radio means. Buttons announced nothing about
               exclusivity, and tabs would have implied the other methods were
               merely out of view rather than switched off. -->
          <section class="space-y-3 pt-4 border-t border-border">
            <!-- A dropdown rather than a radio group. The three methods are
                 mutually exclusive, which a select states as plainly as a radio
                 group — and it costs one line instead of three, leaving the
                 modal short enough that the configuration below is reachable
                 without scrolling. The description of the selected method sits
                 directly under it, so nothing is lost by not listing all three
                 at once.

                 The label is the only heading: a section <h4> above it repeated
                 the same word, so the field read as two stacked titles. The
                 label is also what announces the dropdown to a screen reader
                 and what focuses it when clicked — an <h4> does neither.

                 The three names describe who does the work, which is the only
                 distinction an operator choosing between them can act on.
                 "Quick Free" names what the operator gets — a working public
                 URL at no cost and no setup — rather than the mechanism, which
                 is Cloudflare's business and not theirs. "Your Own" says the
                 tunnel already belongs to them; "Existing Tunnel" compared it
                 to another one they do not have. "Automatic Setup" replaces
                 "Managed Tunnel", which borrowed Cloudflare's
                 "remotely-managed" — a distinction about where the config file
                 lives, not about anything the operator chooses. All three modes
                 are managed by this gateway, so the word carried no information.

                 "Zero Trust" is deliberately not used: it is the name of the
                 dashboard, not of the tunnel. -->
            <div class="space-y-1.5">
              <label for="tunnel-method" class="text-xs font-semibold text-text-main">Connection Method</label>
              <select
                id="tunnel-method"
                value={tunnelMode}
                onchange={(e) => selectTunnelMode(e.currentTarget.value as 'quick' | 'named' | 'auto')}
                class="w-full text-sm bg-bg border border-border rounded-lg px-3 py-2 text-text-main cursor-pointer focus:outline-none focus:border-brand-500/50"
              >
                <option value="quick">Quick Free</option>
                <option value="named">Your Own</option>
                <option value="auto">Automatic Setup</option>
              </select>
              <p class="text-[11px] text-text-muted leading-relaxed">
                {#if tunnelMode === 'quick'}
                  <!-- Fastest of the three and the only one that needs nothing at
                       all — no account, no token, no domain. The cost is stated
                       in the same breath: the address is not yours to choose,
                       which is the whole reason the other two modes exist. -->
                  Fastest of the three and needs nothing at all — no account, no token, no domain. You get a
                  <span class="font-mono">*.trycloudflare.com</span> address plus a stable alias from the mapping service,
                  though the address itself is not yours to choose.{#if currentTunnelMode === 'quick' && tunnelRunning}<span
                    class="text-brand-500 font-semibold">· active</span
                  >{/if}
                {:else if tunnelMode === 'named'}
                  <!-- The step count is stated here too, so the comparison is
                       visible from both sides. An operator reading only this
                       mode's description has no reason to look at the others;
                       one reading both sees the trade rather than a list of
                       requirements. -->
                  Most steps of the three, but no API token needed. Create the tunnel yourself in the Cloudflare dashboard,
                  then paste its token here along with a hostname whose DNS you have pointed at it yourself.{#if currentTunnelMode ===
                    'named' &&
                    tunnelRunning}<span class="text-brand-500 font-semibold">· active</span>{/if}
                {:else}
                  <!-- The step count leads, because it is the reason to pick this
                       mode and the thing an operator cannot see: the token
                       requirement below reads as a cost, and on its own it is
                       enough to send someone back to the mode with six manual
                       steps. Stating the trade in one line lets them decide
                       instead of guessing. -->
                  Fewest steps of the three, but you need a Cloudflare API token first. The gateway then creates the tunnel,
                  its route and its DNS record for you — no Zero Trust dashboard, no ingress rule to type by hand. Needs a
                  token with <span class="font-mono">Tunnel:Edit</span> and <span class="font-mono">Zone:DNS:Edit</span>.{#if currentTunnelMode ===
                    'auto' &&
                    tunnelRunning}<span class="text-brand-500 font-semibold">· active</span>{/if}
                {/if}
              </p>
            </div>
            <!-- The consequence of switching, stated where the choice is made
                 rather than only in a confirmation afterwards. An operator
                 comparing methods needs to know the cost before picking, not
                 after. -->
            <p class="text-[11px] text-text-subtle leading-relaxed">
              {#if tunnelRunning && (publicUrl || tunnelUrl)}
                <!-- The URL is named rather than described as "the old URL". An
                     operator who has already handed the address to a client
                     needs to know which one to update, and "the old URL" makes
                     them go and look. -->
                Switching method disconnects <span class="font-mono text-text-muted break-all">{publicUrl || tunnelUrl}</span> and
                builds a new one — expect a short outage, and that address stops working.
              {:else}
                Switching method disconnects the current tunnel and builds a new one — expect a short outage, and the old URL
                stops working.
              {/if}
            </p>
          </section>

          <!-- ── CONFIGURATION ──────────────────────────────────────── -->
          <section class="space-y-3 pt-4 border-t border-border">
            <h4 class="text-xs font-semibold text-text-main">Configuration</h4>

            {#if tunnelMode === 'quick'}
              <div class="space-y-1.5">
                <label for="tunnel-shortid" class="text-xs font-semibold text-text-main">
                  Short ID <span class="font-normal text-text-subtle">(optional)</span>
                </label>
                <div class="relative">
                  <!-- The `r` is part of the URL the worker serves, so it is a
                       fixed prefix rather than something the operator types. It
                       sits inside the same field instead of beside it: two
                       boxes read as two values to fill in, and the operator is
                       left working out which one the `r` belongs to.

                       It is a span behind the input rather than a second input,
                       so the field stays one control with one caret. The span
                       takes no pointer events — a click on the `r` lands on the
                       input and puts the caret where the operator expected. -->
                  <span
                    class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 font-mono text-sm text-text-subtle select-none"
                    aria-hidden="true">r</span
                  >
                  <input
                    id="tunnel-shortid"
                    type="text"
                    bind:value={tunnelShortId}
                    placeholder="e.g. mygateway"
                    maxlength="20"
                    oninput={(e) => {
                      // The `r` is drawn inside the field, so an operator who
                      // types one anyway would produce `rr…` in the public URL
                      // — and the worker only routes the single-prefix form.
                      // Strip it as they type rather than rejecting the whole
                      // value, which would look like the field is broken.

                      // Mirrors ResolveShortID on the server: the typed prefix
                      // is trimmed, and trimming repeats while the result still
                      // looks prefixed, because an operator who pasted
                      // `rrajaja` typed it twice. The same rule runs
                      // server-side because this handler is bypassable by a
                      // direct API call.
                      const typed = e.currentTarget.value
                      if (typed.length > 3 && typed.startsWith('r')) {
                        e.currentTarget.value = typed.replace(/^r+/, '')
                      }
                    }}
                    class="w-full font-mono text-sm bg-bg border border-border rounded-lg pl-7 pr-3 py-2 text-text-main placeholder:text-text-subtle focus:outline-none focus:border-brand-500/50"
                  />
                </div>
                <p class="text-[11px] text-text-subtle leading-relaxed">
                  Lowercase letters, digits and hyphens (3-20 chars). The <span class="font-mono">r</span> is added for you —
                  your stable URL becomes
                  <!-- The address is the one thing in this paragraph the operator
                       has to read carefully, and it was the same colour as the
                       prose around it — so the sentence read as an explanation
                       rather than a result. Brand-500 is the accent the rest of
                       this modal already uses for the live value, which keeps
                       "this is the thing you get" consistent across screens. -->
                  <span class="font-mono text-brand-500">r{tunnelShortId.trim() || 'yourid'}.abc-tunnel.us</span>.
                  Leave empty for a random one. Not used by the other methods, which already have a fixed address.
                </p>
              </div>
            {/if}
            {#if tunnelMode === 'quick'}
              <div class="space-y-1.5">
                <label for="tunnel-worker-url" class="text-xs font-semibold text-text-main">
                  Mapping service <span class="font-normal text-text-subtle">(optional)</span>
                </label>
                <div class="flex items-center gap-2">
                  <input
                    id="tunnel-worker-url"
                    type="text"
                    bind:value={tunnelWorkerUrl}
                    placeholder="https://abc-tunnel.us"
                    class="flex-1 min-w-0 font-mono text-xs bg-bg border border-border rounded-lg px-3 py-2 text-text-main placeholder:text-text-subtle focus:outline-none focus:border-brand-500/50"
                  />
                  <button
                    type="button"
                    onclick={saveTunnelWorkerUrl}
                    disabled={isSavingWorkerUrl}
                    class="shrink-0 py-2 px-3 rounded-lg bg-surface-2 hover:bg-surface-3 border border-border text-xs font-semibold text-text-main transition cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {isSavingWorkerUrl ? 'Saving…' : 'Save'}
                  </button>
                  <button
                    type="button"
                    onclick={resetTunnelWorkerUrl}
                    disabled={isSavingWorkerUrl || tunnelWorkerUrl.trim() === defaultWorkerUrl}
                    title="Reset to the default mapping service"
                    aria-label="Reset mapping service to default"
                    class="shrink-0 p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-brand-500 transition-colors cursor-pointer border border-border disabled:opacity-40 disabled:cursor-not-allowed"
                  >
                    <RotateCcw class="w-3.5 h-3.5" />
                  </button>
                </div>
                <p class="text-[11px] text-text-subtle leading-relaxed">
                  A quick tunnel's <span class="font-mono">*.trycloudflare.com</span> address changes on every reconnect. This
                  service records your short ID against the current address, so one stable URL keeps working across reconnects.
                  Leave empty for the default; only change it if you run your own.
                </p>
                <!-- What the URL will be, before the tunnel is running. Once it
                     is running the address is already on screen — in the status
                     card's own field, with its copy button — so repeating it
                     here with a second copy button and a third paragraph
                     explaining it added three rows that said nothing new. The
                     preview below covers the only case that is not already
                     answered elsewhere: what the operator is about to get. -->
                {#if !publicUrl && previewStableUrl}
                  <p class="text-[11px] text-text-subtle leading-relaxed">
                    Your stable URL will be
                    <span class="font-mono text-brand-500">{previewStableUrl}</span> once the tunnel is running.
                  </p>
                {/if}
                {#if workerUrlSaved}
                  <p class="text-[11px] text-success">Saved.</p>
                {/if}
              </div>
            {/if}
            {#if tunnelMode === 'named'}
              <!-- The origin the operator must configure on the Cloudflare side.
                   Shown from the running gateway rather than typed out, because a
                   stale port here is what sends the connector at localhost:20128
                   while the gateway listens on 20130.

                   It sits above the hostname field on purpose: the operator reads
                   what to point at on the Cloudflare side, then types the hostname
                   that routing belongs to. Below the field it read as an
                   afterthought to a value already entered. -->
              <div class="rounded-xl border border-brand-500/25 bg-brand-500/5 p-3 space-y-2">
                <p class="text-xs font-semibold text-text-main">Point your hostname at this gateway</p>
                <p class="text-[11px] text-text-muted leading-relaxed">
                  In Cloudflare Zero Trust → Networks → Tunnels → your tunnel → Public Hostname, set the service to:
                </p>
                <div class="flex items-center gap-2">
                  <code class="flex-1 min-w-0 font-mono text-xs bg-bg border border-border rounded-lg px-3 py-2 text-text-main truncate">
                    {localOrigin}
                  </code>
                  <button
                    type="button"
                    onclick={() => copy(localOrigin, 'tunnel_origin')}
                    class="p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-brand-500 transition-colors shrink-0 cursor-pointer border border-border"
                    title="Copy origin address"
                  >
                    {#if copiedId === 'tunnel_origin'}
                      <Check class="w-4 h-4 text-success" />
                    {:else}
                      <Copy class="w-4 h-4" />
                    {/if}
                  </button>
                </div>
                <p class="text-[11px] text-text-subtle leading-relaxed">
                  Service type <span class="font-mono">HTTP</span>, and use the address exactly as shown — the port is this
                  gateway's own. A different port is the usual cause of a tunnel that connects but never answers.
                </p>
                {#if tunnelCfApiToken.trim()}
                  <!-- The repair writes the ingress rule through the Cloudflare API,
                       which is only possible when the Auto Setup token is saved.
                       Without it the operator has to make the change by hand in the
                       Zero Trust dashboard, so the button is withheld rather than
                       offered and then refused. -->
                  <div class="pt-1">
                    <button
                      type="button"
                      onclick={repairTunnelOrigin}
                      disabled={isRepairingOrigin}
                      class="inline-flex items-center gap-1.5 py-2 px-3 rounded-lg bg-surface-2 hover:bg-surface-3 border border-border text-xs font-semibold text-text-main transition cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      <Wrench class="w-3.5 h-3.5" />
                      {isRepairingOrigin ? 'Repairing…' : 'Repair origin'}
                    </button>
                    <p class="text-[11px] text-text-subtle leading-relaxed pt-1.5">
                      Rewrites the tunnel's ingress rule to point at <span class="font-mono">{localOrigin}</span>. Use it when
                      the tunnel connects but never answers.
                    </p>
                    {#if originRepairMessage}
                      <p class="text-[11px] pt-1 {originRepairError ? 'text-error' : 'text-success'}">
                        {originRepairMessage}
                      </p>
                    {/if}
                  </div>
                {/if}
              </div>
            {/if}
            {#if tunnelMode !== 'quick'}
              <div class="space-y-1.5">
                <label for="tunnel-domain" class="text-xs font-semibold text-text-main">Public Hostname</label>
                <input
                  id="tunnel-domain"
                  type="text"
                  bind:value={tunnelCustomDomain}
                  placeholder="router.example.com"
                  class="w-full font-mono text-sm bg-bg border border-border rounded-lg px-3 py-2 text-text-main placeholder:text-text-subtle focus:outline-none focus:border-brand-500/50"
                />
                <p class="text-[11px] text-text-subtle leading-relaxed">
                  {tunnelMode === 'auto'
                    ? 'The subdomain that will point at this gateway. Its DNS record is created for you on the zone below.'
                    : 'The hostname you routed to this tunnel in Cloudflare Zero Trust. It must be listed as a Public Hostname on the tunnel, and its DNS must resolve through Cloudflare.'}
                </p>
              </div>
            {#if tunnelMode === 'named'}

            {/if}

              <div class="space-y-1.5">
                <label for="tunnel-token" class="text-xs font-semibold text-text-main">Tunnel Token</label>
                <div class="flex items-center gap-2">
                  <input
                    id="tunnel-token"
                    type={showTunnelToken ? 'text' : 'password'}
                    bind:value={tunnelToken}
                    placeholder="eyJhIjoi..."
                    autocomplete="off"
                    class="flex-1 min-w-0 font-mono text-xs bg-bg border border-border rounded-lg px-3 py-2 text-text-main placeholder:text-text-subtle focus:outline-none focus:border-brand-500/50"
                  />
                  <button
                    type="button"
                    onclick={() => (showTunnelToken = !showTunnelToken)}
                    class="p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-brand-500 transition-colors shrink-0 cursor-pointer border border-border"
                    title={showTunnelToken ? 'Hide token' : 'Show token'}
                    aria-label={showTunnelToken ? 'Hide tunnel token' : 'Show tunnel token'}
                  >
                    {#if showTunnelToken}
                      <EyeOff class="w-4 h-4" />
                    {:else}
                      <Eye class="w-4 h-4" />
                    {/if}
                  </button>
                </div>
                <p class="text-[11px] text-text-subtle leading-relaxed">
                  From Cloudflare Zero Trust → Networks → Tunnels → your tunnel → Install connector. You can paste the whole
                  command — the token is picked out of it.
                </p>
              </div>
            {/if}
            {#if tunnelMode === 'auto'}
              <div class="space-y-3 rounded-xl border border-border bg-surface-2/50 p-3">
                <div class="space-y-1.5">
                  <label for="tunnel-cf-token" class="text-xs font-semibold text-text-main">Cloudflare API Token</label>
                  <div class="flex items-center gap-2">
                    <input
                      id="tunnel-cf-token"
                    type={showCfApiToken ? 'text' : 'password'}
                    bind:value={tunnelCfApiToken}
                    placeholder="eyJhIjoi..."
                    autocomplete="off"
                    class="flex-1 min-w-0 font-mono text-xs bg-bg border border-border rounded-lg px-3 py-2 text-text-main placeholder:text-text-subtle focus:outline-none focus:border-brand-500/50"
                  />
                  <button
                    type="button"
                    onclick={() => (showCfApiToken = !showCfApiToken)}
                    class="p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-brand-500 transition-colors shrink-0 cursor-pointer border border-border"
                    title={showCfApiToken ? 'Hide token' : 'Show token'}
                    aria-label={showCfApiToken ? 'Hide Cloudflare API token' : 'Show Cloudflare API token'}
                  >
                    {#if showCfApiToken}
                      <EyeOff class="w-4 h-4" />
                    {:else}
                      <Eye class="w-4 h-4" />
                    {/if}
                  </button>
                  </div>
                  <p class="text-[11px] text-text-subtle leading-relaxed">
                    Create one at My Profile → API Tokens with <span class="font-mono">Cloudflare Tunnel:Edit</span> on the
                    account and <span class="font-mono">Zone:DNS:Edit</span> on your zone. Used once to create the tunnel and
                    its DNS record; it is not stored.
                  </p>
                </div>
                <div class="space-y-1.5">
                  <label for="tunnel-cf-zone" class="text-xs font-semibold text-text-main">Zone</label>
                  <input
                    id="tunnel-cf-zone"
                    type="text"
                    bind:value={tunnelCfZone}
                    placeholder="example.com"
                    class="w-full font-mono text-sm bg-bg border border-border rounded-lg px-3 py-2 text-text-main placeholder:text-text-subtle focus:outline-none focus:border-brand-500/50"
                  />
                  <p class="text-[11px] text-text-subtle leading-relaxed">
                    The domain on your Cloudflare account that will host the hostname above.
                  </p>
                </div>
                <div class="space-y-1.5">
                  <label for="tunnel-cf-account" class="text-xs font-semibold text-text-main">
                    Account ID <span class="font-normal text-text-subtle">(optional)</span>
                  </label>
                  <input
                    id="tunnel-cf-account"
                    type="text"
                    bind:value={tunnelCfAccountId}
                    placeholder="auto-detected"
                    class="w-full font-mono text-xs bg-bg border border-border rounded-lg px-3 py-2 text-text-main placeholder:text-text-subtle focus:outline-none focus:border-brand-500/50"
                  />
                  <p class="text-[11px] text-text-subtle leading-relaxed">
                    Leave empty unless your token reaches more than one account.
                  </p>
                </div>
              </div>
            {/if}
          </section>


          <!-- ── PENDING CHANGES ──────────────────────────────────────
               Information only. The action that applies it lives in the footer,
               beside Disable, so both actions on a running tunnel sit together
               and neither scrolls out of reach. A button here would vanish the
               moment the operator scrolled down to read the configuration. -->
          {#if tunnelEnabled && tunnelConfigChanged}
            <div class="rounded-xl border border-amber-500/25 bg-amber-500/5 p-3 space-y-1">
              <p class="text-xs font-semibold text-amber-700 dark:text-amber-300">Unsaved changes</p>
              <p class="text-[11px] text-amber-700 dark:text-amber-300 leading-relaxed">
                Applying them disconnects the current tunnel and builds a new one — expect a short outage, and the current URL
                stops working until the new tunnel is up. Press <span class="font-semibold">Restart</span> below to apply them, or
                to rebuild the tunnel as it stands.
              </p>
            </div>
          {/if}
        {/if}
        {#if tunnelSettingsTab === 'tailscale'}
          <!-- ── TAILSCALE TAB ──────────────────────────────────────── -->
          <section class="space-y-2">
            <h4 class="text-xs font-semibold text-text-main">Status</h4>
            {#if tailscaleEnabled && tailscaleRunning}
              <div class="rounded-xl border border-indigo-500/25 bg-indigo-500/5 p-3 space-y-2">
                <div class="flex items-center gap-2">
                  <span class="w-1.5 h-1.5 rounded-full bg-success shrink-0"></span>
                  <p class="text-xs font-semibold text-text-main">Funnel connected</p>
                </div>
                <div class="flex items-center gap-2">
                  <code class="flex-1 min-w-0 font-mono text-xs bg-bg border border-border rounded-lg px-3 py-2 text-text-main truncate">
                    {tailscaleUrl}
                  </code>
                  <button
                    type="button"
                    onclick={() => copy(tailscaleUrl, 'ts_settings_url')}
                    class="p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-indigo-400 transition-colors shrink-0 cursor-pointer border border-border"
                    title="Copy Tailscale URL"
                  >
                    {#if copiedId === 'ts_settings_url'}
                      <Check class="w-4 h-4 text-success" />
                    {:else}
                      <Copy class="w-4 h-4" />
                    {/if}
                  </button>
                </div>
              </div>
            {:else if tailscaleInstalled === false}
              <div class="rounded-xl border border-border bg-surface-2/50 p-3 space-y-2">
                <p class="text-xs text-text-muted leading-relaxed">
                  Tailscale CLI is not installed or detected on your system path. Install it to use Funnel.
                </p>
                <div class="p-2 rounded-lg bg-bg border border-border font-mono text-[11px] text-text-main select-all">
                  curl -fsSL https://tailscale.com/install.sh | sh
                </div>
              </div>
            {:else}
              <div class="rounded-xl border border-border bg-surface-2/50 p-3">
                <p class="text-xs text-text-muted">Funnel not connected.</p>
              </div>
            {/if}
            <!-- A failed Connect is reported here, not only on the endpoint row.
                 The operator pressed the button in this modal, so the reason it
                 did not work has to appear where they are looking. -->
            {#if tailscaleError}
              <div class="rounded-xl border border-danger/30 bg-danger/10 p-3 flex items-start gap-2">
                <AlertCircle class="w-4 h-4 shrink-0 mt-0.5 text-danger" />
                <p class="text-[11px] text-red-600 dark:text-red-400 leading-relaxed">{tailscaleError}</p>
              </div>
            {/if}
          </section>
            <!-- A dropdown, matching the Cloudflare tab's method picker. Two
                 ways in, only one active, and the description of the chosen one
                 sits directly under it.

                 The label is the only heading here, for the same reason as the
                 Cloudflare tab: a section <h4> repeating the same word stacked
                 two titles over one field. -->
          <section class="space-y-3 pt-4 border-t border-border">
            <div class="space-y-1.5">
              <label for="tailscale-auth-mode" class="text-xs font-semibold text-text-main">Authentication</label>
              <select
                id="tailscale-auth-mode"
                value={tailscaleAuthMode}
                onchange={(e) => selectTailscaleAuthMode(e.currentTarget.value as 'interactive' | 'key')}
                class="w-full text-sm bg-bg border border-border rounded-lg px-3 py-2 text-text-main cursor-pointer focus:outline-none focus:border-indigo-500/50"
              >
                <option value="interactive">Interactive login</option>
                <option value="key">Auth key</option>
              </select>
              <p class="text-[11px] text-text-muted leading-relaxed">
                {#if tailscaleAuthMode === 'interactive'}
                  Opens a Tailscale login page in your browser to authorise this machine. Best for a desktop or a laptop you
                  can reach.
                {:else}
                  Authenticates without a browser. The only option on a headless gateway — a server, a container, or a Pi has
                  nowhere to open a login page.
                {/if}
              </p>
            </div>

            {#if tailscaleAuthMode === 'key'}
              <div class="space-y-1.5">
                <label for="tailscale-auth-key" class="text-xs font-semibold text-text-main">Auth key</label>
                <div class="flex items-center gap-2">
                  <input
                    id="tailscale-auth-key"
                  type={showTailscaleKey ? 'text' : 'password'}
                  bind:value={tailscaleAuthKey}
                  placeholder="tskey-auth-..."
                  autocomplete="off"
                  class="flex-1 min-w-0 font-mono text-xs bg-bg border border-border rounded-lg px-3 py-2 text-text-main placeholder:text-text-subtle focus:outline-none focus:border-indigo-500/50"
                />
                <button
                  type="button"
                  onclick={() => (showTailscaleKey = !showTailscaleKey)}
                  class="p-2 hover:bg-surface-2 rounded-lg text-text-muted hover:text-indigo-400 transition-colors shrink-0 cursor-pointer border border-border"
                  title={showTailscaleKey ? 'Hide key' : 'Show key'}
                  aria-label={showTailscaleKey ? 'Hide Tailscale auth key' : 'Show Tailscale auth key'}
                >
                  {#if showTailscaleKey}
                    <EyeOff class="w-4 h-4" />
                  {:else}
                    <Eye class="w-4 h-4" />
                  {/if}
                </button>
                </div>
                <p class="text-[11px] text-text-subtle leading-relaxed">
                  From the Tailscale admin console → Settings → Keys. Stored once the connection succeeds, so the gateway can
                  re-authenticate on its own if the key expires.
                </p>
              </div>
            {/if}
            <!-- Login in progress. The endpoint card also carries a Login
                 button, but it is behind the modal — an operator who pressed
                 Connect here would see the modal close and have no way to
                 reach the URL the gateway is waiting on. -->
            {#if isTailscaleLoading && tailscaleAuthUrl}
              <div class="rounded-xl border border-indigo-500/25 bg-indigo-500/5 p-3 space-y-2">
                <p class="text-xs font-semibold text-text-main">Login required</p>
                <p class="text-[11px] text-text-muted leading-relaxed">
                  Open the Tailscale login page to authorise this machine, then come back — the connection continues on its
                  own.
                </p>
                <button
                  type="button"
                  onclick={() => window.open(tailscaleAuthUrl, '_blank')}
                  class="w-full py-2 px-4 rounded-lg bg-indigo-600 hover:bg-indigo-700 text-white font-semibold text-xs transition cursor-pointer flex items-center justify-center gap-1.5"
                >
                  <ExternalLink class="w-3.5 h-3.5" />
                  Open login page
                </button>
              </div>
            {/if}
          </section>
        {/if}
        {#if tunnelSettingsTab === 'global'}
          <section class="space-y-3">
            <div class="space-y-0.5">
              <h4 class="text-xs font-semibold text-text-main">Cloudflare behaviour</h4>
              <p class="text-[11px] text-text-muted leading-relaxed">
                Rebuild the Cloudflare tunnel after a network change, a wake from sleep, or an unexpected exit.
              </p>
            </div>
            <div class="flex items-center justify-between gap-3">
              <p class="text-xs text-text-main">Restart tunnel if it drops</p>
              <Toggle
                checked={tunnelWatchdogEnabled}
                label="Restart Cloudflare tunnel if it drops"
                onChange={toggleTunnelWatchdog}
              />
            </div>
          </section>

          <section class="space-y-3 pt-4 border-t border-border">
            <div class="space-y-0.5">
              <h4 class="text-xs font-semibold text-text-main">Tailscale behaviour</h4>
              <p class="text-[11px] text-text-muted leading-relaxed">
                Re-establish the funnel after a network change or a wake from sleep. The Tailscale daemon itself is left
                alone — it reconnects on its own and restarting it would discard your login.
              </p>
            </div>
            <div class="flex items-center justify-between gap-3">
              <p class="text-xs text-text-main">Restore funnel if it drops</p>
              <Toggle
                checked={tailscaleWatchdogEnabled}
                label="Restore Tailscale funnel if it drops"
                onChange={toggleTailscaleWatchdog}
              />
            </div>
          </section>

          <section class="space-y-3 pt-4 border-t border-border">
            <div class="space-y-0.5">
              <h4 class="text-xs font-semibold text-text-main">Dashboard access</h4>
              <p class="text-[11px] text-text-muted leading-relaxed">
                When enabled, the dashboard can be reached through whichever tunnel is running. A login is still
                required — the tunnel decides who can reach the door, not who gets in.
              </p>
            </div>
            <div class="flex items-center justify-between gap-3">
              <p class="text-xs text-text-main">Allow dashboard access via tunnel</p>
              <Toggle
                checked={tunnelDashboardAccess}
                label="Allow dashboard access via tunnel"
                onChange={toggleTunnelDashboardAccess}
              />
            </div>
          </section>
          <section class="space-y-3 pt-4 border-t border-border">
            <div class="space-y-0.5">
              <h4 class="text-xs font-semibold text-text-main">Visibility</h4>
              <p class="text-[11px] text-text-muted leading-relaxed">
                Hide the Tunnel and Tailscale rows while neither is running. They come back the moment one is
                enabled — this tidies the card, it does not remove the controls.
              </p>
            </div>
            <div class="flex items-center justify-between gap-3">
              <p class="text-xs text-text-main">Hide unused tunnel rows</p>
              <Toggle
                checked={hideUnusedTunnel}
                label="Hide unused tunnel rows"
                onChange={toggleHideUnusedTunnel}
              />
            </div>
          </section>
        {/if}
      </div>


      <!-- Footer: the primary action for the active tab, plus close. Fixed so
           the action is reachable without scrolling back up. -->
      <div class="p-6 pt-4 border-t border-border shrink-0">
        {#if tunnelSettingsTab === 'global'}
          <!-- Global has no primary action of its own: every switch on it saves
               the moment it is flipped, so a footer button would only close the
               modal. Close is already in the header, one row up. -->
          <p class="text-[11px] text-text-muted text-center leading-relaxed">
            Changes save as you make them.
          </p>
        {:else if tunnelSettingsTab === 'cloudflare'}
          {#if tunnelEnabled}
            <!-- Both actions on a running tunnel sit side by side. Restart is
                 always enabled: restarting is itself a reason to press it, and
                 an operator with a slow or flaky tunnel has no change to make
                 and no other way to ask for a clean connector. The button only
                 disables while a restart is already in flight, which is a
                 different thing — that one prevents a double teardown. -->
            <div class="flex gap-2">
              <button
                type="button"
                onclick={doStartTunnel}
                disabled={isTunnelLoading}
                class="flex-1 py-2 px-4 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs transition cursor-pointer shadow-sm disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {isTunnelLoading ? 'Restarting…' : 'Restart'}
              </button>
              <button
                type="button"
                onclick={() => {
                  showTunnelSettingsModal = false
                  showDisableTunnelModal = true
                }}
                class="flex-1 py-2 px-4 rounded-lg bg-danger hover:bg-danger/90 text-white font-semibold text-xs transition cursor-pointer"
              >
                Disable
              </button>
            </div>
          {:else}
            <button
              type="button"
              onclick={doStartTunnel}
              disabled={isTunnelLoading}
              class="w-full py-2 px-4 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs transition cursor-pointer shadow-sm disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {isTunnelLoading ? 'Starting…' : 'Start Tunnel'}
            </button>
          {/if}
        {:else if tailscaleEnabled}
          <button
            type="button"
            onclick={() => {
              showTunnelSettingsModal = false
              showDisableTailscaleModal = true
            }}
            class="w-full py-2 px-4 rounded-lg bg-danger hover:bg-danger/90 text-white font-semibold text-xs transition cursor-pointer"
          >
            Disable Tailscale
          </button>
        {:else}
          <!-- Calls startTailscale directly rather than opening the old
               connect modal. That modal's Connect button was gated on
               tailscaleInstalled, which is null until a check has run — so the
               modal opened with its explanatory text and no way to proceed.
               startTailscale is what the endpoint row already uses, and it
               handles the not-installed and needs-login cases itself. -->
          <button
            type="button"
            onclick={startTailscale}
            disabled={isTailscaleLoading}
            class="w-full py-2 px-4 rounded-lg bg-indigo-600 hover:bg-indigo-700 text-white font-semibold text-xs transition cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {isTailscaleLoading ? 'Connecting…' : 'Connect Tailscale'}
          </button>
        {/if}
      </div>
    </div>
  </div>
{/if}
<!-- MODAL: Disable Cloudflare Tunnel -->
{#if showDisableTunnelModal}
  <div
    class="fixed inset-0 z-[100] flex items-center justify-center bg-black/75 backdrop-blur-sm p-4"
    onclick={(e) => {
      if (e.target === e.currentTarget) showDisableTunnelModal = false
    }}
  >
    <div class="w-full max-w-md p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <h3 class="text-base font-bold text-text-main">Disable Tunnel</h3>
        <button
          type="button"
          onclick={() => (showDisableTunnelModal = false)}
          class="text-text-muted hover:text-text-main cursor-pointer"
          aria-label="Close"
        >
          <X class="w-4 h-4" />
        </button>
      </div>
      <p class="text-sm text-text-muted leading-relaxed">
        The Cloudflare tunnel will be disconnected. Remote access via the tunnel URL will stop working.
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
<!-- MODAL: Disable Tailscale -->
{#if showDisableTailscaleModal}
  <div
    class="fixed inset-0 z-[100] flex items-center justify-center bg-black/75 backdrop-blur-sm p-4"
    onclick={(e) => {
      if (e.target === e.currentTarget) showDisableTailscaleModal = false
    }}
  >
    <div class="w-full max-w-md p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <h3 class="text-base font-bold text-text-main">Disable Tailscale</h3>
        <button
          type="button"
          onclick={() => (showDisableTailscaleModal = false)}
          class="text-text-muted hover:text-text-main cursor-pointer"
          aria-label="Close"
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