<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import {
    Check,
    Download,
    Key,
    Laptop,
    Loader2,
    PanelLeft,
    RefreshCw,
    Save,
    Shield,
    Sliders,
    Upload,
    User,
    Zap
  } from 'lucide-svelte'
  import Card from '../lib/ui/Card.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Input from '../lib/ui/Input.svelte'
  import { api, getAuthHeaders, responseErrorMessage, type DatabaseStats, type Settings } from '../api/client'
  import { isCompactUnitsEnabled, isProviderTogglePersistent, setCompactUnits, setProviderTogglePersistent } from '../lib/compactNumber'
  import {
    isAdvancedSettingsVisible,
    isDetailsExpandedByDefault,
    setAdvancedSettingsVisible,
    setDetailsExpandedByDefault,
    ANIMATION_LEVELS,
    getAnimationLevel,
    setAnimationLevel,
  } from '../lib/displayPrefs'
  import {
    USAGE_PANEL_FIXED_POSITION,
    USAGE_PANEL_GROUPS,
    USAGE_PANEL_ORDERABLE_GROUPS,
    USAGE_PANEL_PRESETS,
    type UsagePanelGroupId,
    type UsagePanelId,
    type UsagePanelPreset,
    activeUsagePreset,
    applyUsagePreset,
    isPanelVisible,
    movePanel,
    panelOrder,
    resetPanelOrder,
    saveUsagePanels,
    sendPanelTo,
    setPanelVisible,
    usagePanelsDirty,
  } from '../lib/usagePanel.svelte'
  import {
    ALWAYS_VISIBLE_NAV_IDS,
    NAV_PRESETS,
    activeNavPreset,
    arrangedNavGroups,
    dropNavEntry,
    moveNavEntry,
    navPresetHidden,
    normalizeNavOrder,
    orderHiddenNav,
    setNavVisible,
    type NavEntry,
    type NavPreset,
  } from '../lib/nav'
  import {
    CURRENCY_CATALOG,
    DEFAULT_RATES,
    describeRates,
    describeRefreshRates,
    formatCost,
    normalizeDisplayCurrency,
    type DisplayCurrency
  } from '../lib/currency'
  import { setDisplayCurrency } from '../lib/currency.svelte'

  interface Props {
    settings?: Settings
    onRefresh?: () => void
  }

  let {
    settings = {},
    onRefresh
  }: Props = $props()

  // General Settings State
  let requireLogin = $state(false)
  let sessionTimeout = $state('24h')
  let enableObservability = $state(false)
  let observabilityMode = $state<'full' | 'errors_only' | 'off'>('full')
  let retentionDays = $state<number>(7)
  let maxRequestRecords = $state<number>(5000)
  let usageHistoryRetentionDays = $state<number>(0)
  let dbStats = $state<DatabaseStats | null>(null)
  let dbStatsError = $state<string | null>(null)
  let isPruning = $state(false)
  let isPruningUsage = $state(false)
  let isVacuuming = $state(false)
  // Global System Prompt State
  let enableSystemPrompt = $state(false)
  let systemPromptPrefix = $state('')
  let systemPromptSuffix = $state('')
  let appName = $state('')
  let appLogoUrl = $state('')
  let isUploadingLogo = $state(false)
  let logoUploadError = $state('')
  let logoFileInput = $state<HTMLInputElement | null>(null)

  const ANTI_POISONING_PRESET =
    '[CRITICAL CODE INTEGRITY & ANTI-POISONING RULE]\n' +
    'Never substitute, rename, or alias technical provider names (e.g. opencode, opencode-zen, opencode-go, antigravity, codex, cline). Always preserve their exact original identifiers in all code, tests, and documentation.'

  // Routing Strategy State
  let fallbackStrategy = $state('failover')
  // Unit abbreviation is a localStorage-only preference (compactUnits key);
  // there is no server-side setting for it.
  // Provider card on/off switch visibility (providerTogglePersistent,
  // localStorage). On = always visible; off = hover reveal on desktop.
  let providerTogglePersistent = $state(isProviderTogglePersistent())
  // Animation level (displayAnimationLevel, localStorage). Applied to <html> so
  // the global rules in index.css can read it without per-component classes.
  let animationLevel = $state(getAnimationLevel())
  let compactUnits = $state(isCompactUnitsEnabled())
  // Usage detail panels (Activity Heatmap, Node Health) start collapsed; the
  // switch below flips that default (detailsExpandedByDefault, localStorage).
  let detailsExpanded = $state(isDetailsExpandedByDefault())

  // The heavy blocks below — the currency rate grid, the per-panel Usage list
  // with its arrangement controls, and the sidebar feature-visibility row — stay
  // folded until this is on. A disclosure preference like the two above, not a
  // feature flag: the currency still formats every cost and the arrangement
  // still drives the Usage page while folded.
  let advancedSettings = $state(isAdvancedSettingsVisible())
  // Usage-page panel visibility and arrangement. The card below edits the draft
  // in lib/usagePanel.svelte.ts; the global Save writes it with the rest of the
  // form, so an edit there raises the same bottom-right reminder a switch does.
  let panelPreset = $derived(activeUsagePreset())
  let panelHint = $derived(
    USAGE_PANEL_PRESETS.find((p) => p.id === panelPreset)?.hint ?? 'Custom selection'
  )

  // Drag-and-drop arrangement, shared by the Usage panel list and the sidebar
  // list. A row's handle is the drag source and the row is the drop target, so a
  // drag never starts from the toggle; the arrows stay as the keyboard path,
  // since a drag has no keyboard equivalent. Both write the same draft, so one
  // Save covers either — and a drag that started in one list cannot land in the
  // other, which is what `zone` is for.
  type DragZone = 'panel' | 'nav'
  let dragZone = $state<DragZone | null>(null)
  let dragGroup = $state<UsagePanelGroupId | null>(null)
  let dragId = $state<string | null>(null)
  let dragTarget = $state<{ id: string; before: boolean } | null>(null)

  function startRowDrag(
    event: DragEvent,
    zone: DragZone,
    id: string,
    group: UsagePanelGroupId | null = null
  ) {
    dragZone = zone
    dragGroup = group
    dragId = id
    dragTarget = null
    if (event.dataTransfer) {
      event.dataTransfer.effectAllowed = 'move'
      event.dataTransfer.setData('text/plain', id)
    }
  }

  // Which half of the row the pointer is over decides whether the row lands above
  // or below it, which is the only drop feedback the list needs.
  function trackRowDrop(
    event: DragEvent,
    zone: DragZone,
    id: string,
    group: UsagePanelGroupId | null = null
  ) {
    if (dragZone !== zone || !dragId || dragId === id) return
    // The Usage list is drawn one section at a time, and a panel belongs to its
    // section: a drop across sections has no destination.
    if (zone === 'panel' && group !== dragGroup) return
    event.preventDefault()
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
    dragTarget = { id, before: event.clientY < rect.top + rect.height / 2 }
  }

  function leaveRowDrop(event: DragEvent, id: string) {
    const row = event.currentTarget as HTMLElement
    if (dragTarget?.id === id && !row.contains(event.relatedTarget as Node | null)) dragTarget = null
  }

  function dropOnRow(
    event: DragEvent,
    zone: DragZone,
    id: string,
    group: UsagePanelGroupId | null = null
  ) {
    if (dragZone !== zone || !dragId) return
    if (zone === 'panel' && group !== dragGroup) return
    event.preventDefault()
    const before = dragTarget?.id === id ? dragTarget.before : true
    if (zone === 'panel' && dragGroup) {
      sendPanelTo(dragGroup, dragId as UsagePanelId, id as UsagePanelId, before)
    }
    if (zone === 'nav') navOrder = dropNavEntry(navOrder, dragId, id, before)
    endRowDrag()
    markUnsaved()
  }

  function endRowDrag() {
    dragZone = null
    dragGroup = null
    dragId = null
    dragTarget = null
  }

  // The cards have no Save of their own. A preset, an arrow, a drop or a switch
  // inside them is not a form field, so each edit raises the settings reminder
  // itself — the same one a settings input raises through the form handler.
  function markLayoutEdit(apply: () => void) {
    apply()
    markUnsaved()
  }
  let stickyRoundRobinLimit = $state(3)
  let comboStrategy = $state('first-model')
  let forceFallback = $state(false)

  // Feature Visibility: ids of the sidebar entries this deployment hides. The
  // entry list itself lives in web/src/lib/nav.ts, shared with the sidebar.
  let hiddenNav = $state<string[]>([])
  // The order those entries are drawn in. Empty means "the order
  // web/src/lib/nav.ts declares", which is what Reset order writes back.
  let navOrder = $state<string[]>([])
  // The preset the hide list currently equals, and what it leaves out — the same
  // read-back the Usage panel list does, so "Custom" means the same thing here.
  let navPreset = $derived(activeNavPreset(hiddenNav))
  let navPresetHint = $derived(
    NAV_PRESETS.find((preset) => preset.id === navPreset)?.hint ?? 'A selection of your own'
  )
  // A visibility change is not saved until the operator presses Save, so the
  // button is offered on its own for a few seconds after a change.
  let showSaveHint = $state(false)
  let saveHintTimer: ReturnType<typeof setTimeout> | null = null
  // Currency & rates: the display currency every cost surface reads. The catalog
  // and the formatting rules live in web/src/lib/currency.ts.
  let currencyCode = $state('USD')
  let currencyRates = $state<Record<string, number>>({})
  let currencyShowBoth = $state(false)
  let currencyUpdatedAt = $state<string | undefined>(undefined)
  let isRefreshingRates = $state(false)
  let ratesMessage = $state<string | null>(null)

  // Password Management State
  let currentPassword = $state('')
  let newPassword = $state('')
  let confirmNewPassword = $state('')
  let isUpdatingPassword = $state(false)
  let passwordSuccessMessage = $state<string | null>(null)
  let passwordErrorMessage = $state<string | null>(null)
  let showPasswordFields = $state(false)

  // SSO State
  let authMode = $state<'password' | 'oidc' | 'saml'>('password')
  let oidcIssuerUrl = $state('')
  let oidcClientId = $state('')
  let oidcScopes = $state('openid profile email')
  let oidcLoginLabel = $state('Sign in with OIDC')

  let samlEntryPoint = $state('')
  let samlIssuer = $state('')
  let samlCert = $state('')
  let samlLoginLabel = $state('Sign in with SAML SSO')

  // Save states
  let isSavingSettings = $state(false)
  let saveSuccess = $state(false)

  // Database Backup / Import
  let isDownloadingBackup = $state(false)
  let isImportingBackup = $state(false)
  let fileInput: HTMLInputElement | null = $state(null)
  let dbPassword = $state('')
  let dbAuthOpen = $state(false)
  let pendingImportFile: File | null = $state(null)

  $effect(() => {
    if (settings) {
      requireLogin = !!settings.requireLogin
      enableObservability = !!settings.enableObservability
      if (settings.observabilityMode === 'full' || settings.observabilityMode === 'errors_only' || settings.observabilityMode === 'off') {
        observabilityMode = settings.observabilityMode
      }
      if (typeof settings.retentionDays === 'number') retentionDays = settings.retentionDays
      if (typeof settings.maxRequestRecords === 'number') maxRequestRecords = settings.maxRequestRecords
      if (typeof settings.usageHistoryRetentionDays === 'number') {
        usageHistoryRetentionDays = settings.usageHistoryRetentionDays
      }
      if (typeof settings.sessionTimeout === 'string') sessionTimeout = settings.sessionTimeout
      if (typeof settings.fallbackStrategy === 'string') fallbackStrategy = settings.fallbackStrategy
      if (typeof settings.stickyRoundRobinLimit === 'number') stickyRoundRobinLimit = settings.stickyRoundRobinLimit
      if (typeof settings.comboStrategy === 'string') comboStrategy = settings.comboStrategy
      if (typeof settings.forceFallback === 'boolean') forceFallback = settings.forceFallback

      // Global system prompt
      enableSystemPrompt = !!settings.systemPromptEnabled
      if (typeof settings.systemPromptPrefix === 'string') systemPromptPrefix = settings.systemPromptPrefix
      if (typeof settings.systemPromptSuffix === 'string') systemPromptSuffix = settings.systemPromptSuffix

      // SSO
      if (settings.authMode === 'oidc' || settings.authMode === 'saml') authMode = settings.authMode
      if (typeof settings.oidcIssuerUrl === 'string') oidcIssuerUrl = settings.oidcIssuerUrl
      if (typeof settings.oidcClientId === 'string') oidcClientId = settings.oidcClientId
      if (typeof settings.oidcScopes === 'string') oidcScopes = settings.oidcScopes
      if (typeof settings.oidcLoginLabel === 'string') oidcLoginLabel = settings.oidcLoginLabel

      if (typeof settings.samlEntryPoint === 'string') samlEntryPoint = settings.samlEntryPoint
      if (typeof settings.samlIssuer === 'string') samlIssuer = settings.samlIssuer
      if (typeof settings.samlCert === 'string') samlCert = settings.samlCert
      if (typeof settings.samlLoginLabel === 'string') samlLoginLabel = settings.samlLoginLabel

      // Feature Visibility
      hiddenNav = orderHiddenNav(settings.navVisibility)
      navOrder = normalizeNavOrder(settings.navVisibility)
      // Currency
      applyCurrency(settings.displayCurrency)
    }
  })

  async function loadSettings() {
    try {
      const s = await api.getSettings()
      if (s) {
        requireLogin = !!s.requireLogin
        enableObservability = !!s.enableObservability
        if (s.sessionTimeout) sessionTimeout = String(s.sessionTimeout)
        if (s.fallbackStrategy) fallbackStrategy = String(s.fallbackStrategy)
        if (typeof s.stickyRoundRobinLimit === 'number') stickyRoundRobinLimit = s.stickyRoundRobinLimit
        // Feature Visibility
        hiddenNav = orderHiddenNav(s.navVisibility)
        navOrder = normalizeNavOrder(s.navVisibility)
        if (s.comboStrategy) comboStrategy = String(s.comboStrategy)
        if (typeof s.forceFallback === 'boolean') forceFallback = s.forceFallback
        enableSystemPrompt = !!s.systemPromptEnabled
        if (typeof s.systemPromptPrefix === 'string') systemPromptPrefix = s.systemPromptPrefix
        if (typeof s.systemPromptSuffix === 'string') systemPromptSuffix = s.systemPromptSuffix
        if (typeof s.appName === 'string') appName = s.appName
        if (typeof s.appLogoUrl === 'string') appLogoUrl = s.appLogoUrl

        if (s.authMode === 'oidc' || s.authMode === 'saml') authMode = s.authMode
        if (s.oidcIssuerUrl) oidcIssuerUrl = String(s.oidcIssuerUrl)
        if (s.oidcClientId) oidcClientId = String(s.oidcClientId)
        if (s.oidcScopes) oidcScopes = String(s.oidcScopes)
        if (s.oidcLoginLabel) oidcLoginLabel = String(s.oidcLoginLabel)

        if (s.samlEntryPoint) samlEntryPoint = String(s.samlEntryPoint)
        if (s.samlIssuer) samlIssuer = String(s.samlIssuer)
        if (s.samlCert) samlCert = String(s.samlCert)
        if (s.samlLoginLabel) samlLoginLabel = String(s.samlLoginLabel)
        applyCurrency(s.displayCurrency)
      }
    } catch {
      // silent
    }
  }

  onMount(() => {
    loadSettings()
    // The storage card below renders its numbers and its actions from dbStats, so
    // it stays empty (and the ledger row count and prune button stay hidden)
    // until something asks for them. Without this the operator has to press a
    // button that is not on the page yet in order to see the page.
    loadDbStats()
  })

  /** Fills the suffix with the code-integrity rule. The prefix keeps whatever the operator wrote:
      the preset is a constraint, not an identity, and overwriting both slots would lose it. */
  function loadAntiPoisoningPreset() {
    systemPromptSuffix = ANTI_POISONING_PRESET
    enableSystemPrompt = true
  }

  /** Offers the save button for a few seconds after a visibility change. */
  function markUnsaved() {
    showSaveHint = true
    if (saveHintTimer) clearTimeout(saveHintTimer)
    saveHintTimer = setTimeout(() => (showSaveHint = false), 4000)
  }

  onDestroy(() => {
    if (saveHintTimer) clearTimeout(saveHintTimer)
  })

  /**
   * Any edit in the settings form raises the save reminder. Controls that save
   * themselves opt out with data-save-hint="off": the Display card, whose
   * preferences live in localStorage, the password form, the backup file
   * picker, and the backup password dialog.
   *
   * The switches are <button role="switch">, so they emit click rather than
   * input/change; every other button in the form either saves on its own or
   * opens a dialog, so only a switch click counts.
   */
  function handleSettingsInput(event: Event) {
    const target = event.target as HTMLElement | null
    if (target?.closest('[data-save-hint="off"]')) return
    if (event.type === 'click' && !target?.closest('button[role="switch"]')) return
    markUnsaved()
  }

  /** Pushes the stored currency setting into the panel's fields and the store. */
  function applyCurrency(value: unknown) {
    const normalized = normalizeDisplayCurrency(value)
    currencyCode = normalized.code
    currencyRates = { ...normalized.rates }
    currencyShowBoth = normalized.showBoth
    currencyUpdatedAt = normalized.updatedAt
    setDisplayCurrency(normalized)
  }

  /** The object this panel saves, and the one the cost surfaces read. */
  function currencySetting(): DisplayCurrency {
    return {
      code: currencyCode,
      rates: currencyRates,
      showBoth: currencyShowBoth,
      ...(currencyUpdatedAt ? { updatedAt: currencyUpdatedAt } : {}),
    }
  }

  /** An emptied field drops the rate, so the seed shows through again. */
  function setRate(code: string, raw: string) {
    if (raw === '') {
      const next = { ...currencyRates }
      delete next[code]
      currencyRates = next
      return
    }
    const value = Number(raw)
    if (!Number.isFinite(value) || value <= 0) return
    currencyRates = { ...currencyRates, [code]: value }
  }

  /**
   * Asks the server for the live rates the catalog cares about. A failure leaves
   * the manual table untouched — it is a refresh, never a clear.
   */
  async function refreshRates() {
    isRefreshingRates = true
    ratesMessage = null
    try {
      const result = await api.refreshCurrencyRates({
        code: currencyCode,
        showBoth: currencyShowBoth,
        codes: CURRENCY_CATALOG.map((option) => option.code),
      })
      applyCurrency(result.displayCurrency)
      // The server overlays the provider's rates on the manual table, so a code
      // it did not publish kept its rate; say so instead of letting the count
      // read as if those rates had been dropped.
      ratesMessage = describeRefreshRates(
        result.ratesStored,
        result.ratesMissing,
        new URL(result.source).host
      )
      onRefresh?.()
    } catch (err) {
      ratesMessage = `Refresh failed, the rates below are unchanged: ${
        err instanceof Error ? err.message : String(err)
      }`
    } finally {
      isRefreshingRates = false
    }
  }

  /** Uploads the white-label logo and adopts the URL the gateway stored it
      under. The upload writes appLogoUrl on the server, so the field is
      refreshed from the response rather than guessed here. */
  async function uploadLogoFile(file: File) {
    isUploadingLogo = true
    logoUploadError = ''
    try {
      const res = await api.uploadLogo(file)
      appLogoUrl = res.appLogoUrl
      onRefresh?.()
    } catch (err) {
      logoUploadError = err instanceof Error ? err.message : String(err)
    } finally {
      isUploadingLogo = false
    }
  }
  /** Reads the picked file off the input and clears it afterwards, so choosing
      the same file twice in a row fires change again. The File is captured
      before the reset: clearing the input drops its files list. */
  function handleLogoFileSelected(e: Event) {
    const input = e.target as HTMLInputElement
    const file = input.files?.[0]
    input.value = ''
    if (file) uploadLogoFile(file)
  }
  async function handleSaveAll() {
    isSavingSettings = true
    saveSuccess = false
    try {
      await api.updateSettings({
        requireLogin,
        sessionTimeout,
        enableObservability,
        observabilityMode,
        retentionDays,
        maxRequestRecords,
        fallbackStrategy,
        usageHistoryRetentionDays,
        stickyRoundRobinLimit,
        comboStrategy,
        forceFallback,
        systemPromptEnabled: enableSystemPrompt,
        systemPromptPrefix,
        systemPromptSuffix,
        appName: appName.trim(),
        appLogoUrl: appLogoUrl.trim(),
        authMode,
        oidcIssuerUrl,
        oidcClientId,
        oidcScopes,
        oidcLoginLabel,
        navVisibility: { hidden: hiddenNav, order: navOrder },
        samlEntryPoint,
        samlIssuer,
        samlCert,
        samlLoginLabel,
        displayCurrency: currencySetting(),
      })
      // The arrangement is a localStorage preference, so it is written only once
      // the settings it was edited alongside have been kept.
      if (usagePanelsDirty()) saveUsagePanels()
      saveSuccess = true
      showSaveHint = false
      if (saveHintTimer) clearTimeout(saveHintTimer)
      saveHintTimer = null
      onRefresh?.()
      setTimeout(() => (saveSuccess = false), 3000)
    } catch (err) {
      alert(`Failed to save settings: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSavingSettings = false
    }
  }

  async function handleUpdatePassword(e: SubmitEvent) {
    e.preventDefault()
    passwordErrorMessage = null
    passwordSuccessMessage = null

    if (newPassword !== confirmNewPassword) {
      passwordErrorMessage = 'Passwords do not match'
      return
    }
    if (newPassword.length < 6) {
      passwordErrorMessage = 'Password must be at least 6 characters'
      return
    }

    isUpdatingPassword = true
    try {
      await api.updateSettings({
        currentPassword,
        newPassword,
      })
      passwordSuccessMessage = 'Password updated successfully!'
      currentPassword = ''
      newPassword = ''
      confirmNewPassword = ''
      showPasswordFields = false
      onRefresh?.()
    } catch (err) {
      passwordErrorMessage = err instanceof Error ? err.message : 'Failed to update password'
    } finally {
      isUpdatingPassword = false
    }
  }

  // closeDbAuth is the single dismissal path for the password modal. Modal
  // wires Escape, the overlay and both ✕ buttons to onClose, and an import
  // already in flight is a destructive operation the user must not be able to
  // abandon by pressing a key — dismissing the dialog would leave the POST
  // running and then wipe the database on completion.
  function closeDbAuth() {
    if (isImportingBackup || isDownloadingBackup) return
    dbAuthOpen = false
    dbPassword = ''
    pendingImportFile = null
  }

  function handleDownloadBackup() {
    if (isImportingBackup || isDownloadingBackup) return
    dbPassword = ''
    pendingImportFile = null
    dbAuthOpen = true
  }

  async function runDownloadBackup() {
    isDownloadingBackup = true
    const password = dbPassword
    try {
      const res = await fetch('/api/settings/database', {
        headers: { ...getAuthHeaders(), 'x-9r-password': password },
      })
      if (!res.ok) {
        throw new Error(await responseErrorMessage(res, 'Failed to export database'))
      }
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      const stamp = new Date().toISOString().replace(/[.:]/g, '-')
      a.href = url
      a.download = `9router-backup-${stamp}.json`
      document.body.appendChild(a)
      a.click()
      setTimeout(() => {
        document.body.removeChild(a)
        URL.revokeObjectURL(url)
      }, 1000)
    } catch (err) {
      alert(`Failed to download backup: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isDownloadingBackup = false
      dbAuthOpen = false
      dbPassword = ''
    }
  }

  function handleFileSelected(e: Event) {
    if (isImportingBackup || isDownloadingBackup) return
    const target = e.target as HTMLInputElement
    const file = target.files?.[0]
    if (!file) return

    target.value = ''
    pendingImportFile = file
    dbPassword = ''
    dbAuthOpen = true
  }

  async function runImportBackup() {
    const file = pendingImportFile
    if (!file) return
    // Snapshot the password before the first await: file.text() and
    // JSON.parse() can both yield, and reading live state afterwards would let a
    // concurrent dismissal blank the credential the request is authorized with.
    const password = dbPassword
    isImportingBackup = true
    try {
      let res: Response
      const isZip = file.name.endsWith('.zip') || file.type === 'application/zip'
      if (isZip) {
        const buffer = await file.arrayBuffer()
        res = await fetch('/api/settings/database', {
          method: 'POST',
          headers: { ...getAuthHeaders(), 'x-9r-password': password, 'Content-Type': 'application/zip' },
          body: buffer,
        })
      } else {
        const raw = await file.text()
        const payload = JSON.parse(raw)
        res = await fetch('/api/settings/database', {
          method: 'POST',
          headers: { ...getAuthHeaders(), 'x-9r-password': password, 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        })
      }
      if (!res.ok) {
        throw new Error(await responseErrorMessage(res, 'Failed to import database'))
      }
      alert('Database backup imported successfully! Reloading page...')
      window.location.reload()
    } catch (err) {
      alert(`Failed to import database: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isImportingBackup = false
      pendingImportFile = null
      dbAuthOpen = false
      dbPassword = ''
    }
  }

  async function loadDbStats() {
    try {
      dbStats = await api.getDatabaseStats()
      dbStatsError = null
    } catch (err) {
      // The card renders without the numbers rather than not at all: the rows
      // fall back to "—" and the prune/vacuum actions stay reachable, because a
      // failed probe must not take the buttons with it.
      dbStatsError = err instanceof Error ? err.message : String(err)
    }
  }

  async function handlePrune() {
    if (!confirm('Prune expired request details according to retention settings?')) return
    try {
      isPruning = true
      const res = await api.pruneDatabase()
      alert(`Pruned ${res.pruned} record(s).`)
      await loadDbStats()
    } catch (err) {
      alert(`Failed to prune: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isPruning = false
    }
  }


  async function handlePruneUsage() {
    const daysLabel = usageHistoryRetentionDays > 0 ? `${usageHistoryRetentionDays} days` : '30 days'
    const effectiveDays = usageHistoryRetentionDays > 0 ? usageHistoryRetentionDays : 30
    try {
      isPruningUsage = true
      const preview = await api.pruneUsageHistory({ days: effectiveDays, dryRun: true })
      const oldest = dbStats?.usageHistoryOldestTimestamp ? ` (oldest row: ${dbStats.usageHistoryOldestTimestamp.slice(0, 10)})` : ''
      const prompt = `Prune usage history older than ${daysLabel}${oldest}?\n\nThis will permanently delete ${preview.wouldDelete?.toLocaleString() ?? 0} request records before ${preview.cutoff.slice(0, 10)}. Per-key token budgets will be carried over.\n\nType "PRUNE" to confirm:`
      const answer = window.prompt(prompt)
      if (answer !== 'PRUNE') return

      const res = await api.pruneUsageHistory({ days: effectiveDays })
      alert(`Pruned ${res.pruned?.toLocaleString() ?? 0} ledger record(s). ${res.carriedTokens?.toLocaleString() ?? 0} tokens carried into per-key budgets.`)
      await loadDbStats()
    } catch (err) {
      alert(`Failed to prune usage history: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isPruningUsage = false
    }
  }
  async function handleVacuum() {
    if (!confirm('Compact SQLite database (VACUUM)? This will return unallocated pages back to disk.')) return
    try {
      isVacuuming = true
      await api.vacuumDatabase()
      alert('Database vacuumed successfully!')
      await loadDbStats()
    } catch (err) {
      alert(`Failed to vacuum: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isVacuuming = false
    }
  }

  function formatBytes(bytes?: number): string {
    if (!bytes || bytes <= 0) return '0 B'
    const units = ['B', 'KB', 'MB', 'GB']
    const i = Math.floor(Math.log(bytes) / Math.log(1024))
    return `${(bytes / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0)} ${units[i]}`
  }
</script>

<div class="flex flex-col gap-6">
  <!-- PAGE HEADER & SAVE BUTTON -->
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
    <div class="space-y-1">
      <div class="flex items-center gap-2">
        <div class="p-2 rounded-lg bg-brand-500/10 text-brand-500">
          <User class="w-5 h-5" />
        </div>
        <div>
          <h1 class="font-headline text-2xl sm:text-3xl font-bold text-text-main tracking-tight flex items-center gap-2">
            Settings & Profile
          </h1>
          <p class="font-body text-xs sm:text-sm text-text-muted">
            System configuration, authentication credentials, and local data storage
          </p>
        </div>
      </div>
    </div>

    <button
      type="button"
      onclick={handleSaveAll}
      disabled={isSavingSettings}
      class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 disabled:opacity-50 text-white font-semibold text-xs transition cursor-pointer shadow-md shadow-brand-500/20"
    >
      {#if isSavingSettings}
        <Loader2 class="w-3.5 h-3.5 animate-spin" />
        <span>Saving...</span>
      {:else if saveSuccess}
        <Check class="w-3.5 h-3.5" />
        <span>Settings Saved!</span>
      {:else}
        <Save class="w-3.5 h-3.5" />
        <span>Save Changes</span>
      {/if}
    </button>
  </div>

  <div
    class="grid grid-cols-1 lg:grid-cols-2 gap-5"
    oninput={handleSettingsInput}
    onchange={handleSettingsInput}
    onclick={handleSettingsInput}
  >

    <!-- SECTION 1: Runtime Storage -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Laptop class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Runtime Storage</h2>
        </div>
        <span class="text-[10px] font-mono font-bold px-2 py-0.5 rounded bg-success/10 text-success border border-success/20">
          Server managed
        </span>
      </div>

      <div class="space-y-2 text-xs">
        <div class="p-3 rounded-xl bg-bg border border-border space-y-1">
          <div class="text-[10px] text-text-subtle uppercase font-mono tracking-wider font-semibold">
            Default Database Location
          </div>
          <div class="font-mono text-xs text-text-main font-semibold">
            ~/.9router/db/data.sqlite
          </div>
          <div class="text-[11px] text-text-muted pt-1">
            The Go server opens the configured SQLite database in WAL mode. DB_PATH or DATA_DIR may select a different location.
          </div>
          <div class="text-[11px] text-text-subtle pt-1">
            The native server does not run upstream's versioned schema migration. A new empty database must be initialized with a compatible upstream schema before use.
          </div>
          <div class="text-[11px] text-text-subtle pt-1">
            Backups are dashboard-data payloads, not a byte-for-byte copy of the live SQLite file.
          </div>
        </div>

        <div class="flex items-center gap-2 pt-1">
          <button
            type="button"
            onclick={handleDownloadBackup}
            disabled={isDownloadingBackup || isImportingBackup}
            class="flex-1 py-2 px-3 rounded-lg bg-surface-2 hover:bg-surface-3 border border-border text-xs font-semibold text-text-main transition cursor-pointer flex items-center justify-center gap-1.5"
          >
            <Download class="w-3.5 h-3.5 text-brand-500" />
            <span>Download Backup</span>
          </button>

          <input
            type="file"
            data-save-hint="off"
            accept=".zip,.json,application/zip,application/json"
            bind:this={fileInput}
            onchange={handleFileSelected}
            class="hidden"
          />

          <button
            type="button"
            onclick={() => fileInput?.click()}
            disabled={isImportingBackup || isDownloadingBackup}
            class="flex-1 py-2 px-3 rounded-lg bg-surface-2 hover:bg-surface-3 border border-border text-xs font-semibold text-text-main transition cursor-pointer flex items-center justify-center gap-1.5"
          >
            <Upload class="w-3.5 h-3.5 text-text-muted" />
            <span>Import Backup</span>
          </button>
        </div>
      </div>
    </Card>

    <!-- SECTION 2: Security & Master Password -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Shield class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Security & Password</h2>
        </div>
        <Toggle
          checked={requireLogin}
          size="sm"
          label="Require login"
          onChange={(val) => (requireLogin = val)}
        />
      </div>

      <div class="space-y-3 text-xs">
        <div class="flex items-center justify-between">
          <div>
            <p class="font-semibold text-text-main">Require Password on Localhost</p>
            <p class="text-[11px] text-text-subtle">Default password is <code class="font-mono text-brand-500">123456</code></p>
          </div>
        </div>

        <div class="space-y-1 pt-1">
          <label for="session-timeout" class="block font-semibold text-text-main">Session Timeout</label>
          <select
            id="session-timeout"
            bind:value={sessionTimeout}
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="15m">15 Minutes</option>
            <option value="1h">1 Hour</option>
            <option value="24h">24 Hours</option>
            <option value="7d">7 Days</option>
            <option value="never">Never (Stay signed in)</option>
          </select>
        </div>

        <!-- Update Password Toggle & Form -->
        <div class="pt-2 border-t border-border/60">
          <button
            type="button"
            onclick={() => (showPasswordFields = !showPasswordFields)}
            class="text-xs font-semibold text-brand-500 hover:opacity-80 cursor-pointer"
          >
            {showPasswordFields ? 'Hide Change Password' : 'Change Master Password'}
          </button>

          {#if showPasswordFields}
            <form onsubmit={handleUpdatePassword} class="space-y-2.5 pt-3" data-save-hint="off">
              {#if passwordErrorMessage}
                <div class="p-2.5 rounded-lg bg-danger/10 border border-danger/20 text-red-600 dark:text-red-400 text-[11px]">
                  {passwordErrorMessage}
                </div>
              {/if}
              {#if passwordSuccessMessage}
                <div class="p-2.5 rounded-lg bg-success/10 border border-success/20 text-success text-[11px]">
                  {passwordSuccessMessage}
                </div>
              {/if}

              <div class="space-y-1">
                <label for="curr-pwd" class="block text-[11px] font-semibold text-text-muted">Current Password</label>
                <input
                  id="curr-pwd"
                  type="password"
                  bind:value={currentPassword}
                  placeholder="123456"
                  class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
                  required
                />
              </div>

              <div class="space-y-1">
                <label for="new-pwd" class="block text-[11px] font-semibold text-text-muted">New Password</label>
                <input
                  id="new-pwd"
                  type="password"
                  bind:value={newPassword}
                  placeholder="At least 6 characters"
                  class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
                  required
                />
              </div>

              <div class="space-y-1">
                <label for="confirm-pwd" class="block text-[11px] font-semibold text-text-muted">Confirm New Password</label>
                <input
                  id="confirm-pwd"
                  type="password"
                  bind:value={confirmNewPassword}
                  placeholder="Re-enter new password"
                  class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
                  required
                />
              </div>

              <button
                type="submit"
                disabled={isUpdatingPassword}
                class="w-full py-2 px-3 rounded-lg bg-brand-500 hover:bg-brand-600 text-white font-semibold text-xs transition cursor-pointer"
              >
                {isUpdatingPassword ? 'Updating...' : 'Update Password'}
              </button>
            </form>
          {/if}
        </div>
      </div>
    </Card>

    <!-- SECTION 3: Single Sign-On (SSO) -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Key class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Single Sign-On (SSO)</h2>
        </div>
      </div>

      <div class="space-y-3 text-xs">
        <div class="space-y-1">
          <label for="auth-mode-select" class="block font-semibold text-text-main">Authentication Mode</label>
          <select
            id="auth-mode-select"
            bind:value={authMode}
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="password">Password Only</option>
            <option value="oidc">OpenID Connect (OIDC)</option>
            <option value="saml">SAML 2.0 SSO</option>
          </select>
        </div>

        {#if authMode === 'oidc'}
          <div class="space-y-2 pt-2 border-t border-border/60">
            <div class="space-y-1">
              <label for="oidc-issuer" class="block font-semibold text-text-muted">Issuer URL</label>
              <input
                id="oidc-issuer"
                type="text"
                bind:value={oidcIssuerUrl}
                placeholder="https://accounts.google.com or Okta URL"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
            <div class="space-y-1">
              <label for="oidc-client-id" class="block font-semibold text-text-muted">Client ID</label>
              <input
                id="oidc-client-id"
                type="text"
                bind:value={oidcClientId}
                placeholder="client-id"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
            <div class="space-y-1">
              <label for="oidc-scopes" class="block font-semibold text-text-muted">Scopes</label>
              <input
                id="oidc-scopes"
                type="text"
                bind:value={oidcScopes}
                placeholder="openid profile email"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
          </div>
        {:else if authMode === 'saml'}
          <div class="space-y-2 pt-2 border-t border-border/60">
            <div class="space-y-1">
              <label for="saml-entrypoint" class="block font-semibold text-text-muted">SAML EntryPoint (SSO URL)</label>
              <input
                id="saml-entrypoint"
                type="text"
                bind:value={samlEntryPoint}
                placeholder="https://idp.example.com/sso"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
            <div class="space-y-1">
              <label for="saml-issuer" class="block font-semibold text-text-muted">SP Entity ID / Issuer</label>
              <input
                id="saml-issuer"
                type="text"
                bind:value={samlIssuer}
                placeholder="https://9router.local"
                class="w-full px-3 py-1.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              />
            </div>
            <div class="space-y-1">
              <label for="saml-cert" class="block font-semibold text-text-muted">X.509 Certificate (PEM)</label>
              <textarea
                id="saml-cert"
                bind:value={samlCert}
                rows={3}
                placeholder="-----BEGIN CERTIFICATE-----&#10;...&#10;-----END CERTIFICATE-----"
                class="w-full p-2.5 rounded-lg bg-bg border border-border text-xs font-mono text-text-main"
              ></textarea>
            </div>
          </div>
        {/if}
      </div>
    </Card>

    <!-- SECTION 4: Routing Strategy & Limits -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center gap-2 pb-2 border-b border-border">
        <Sliders class="w-4 h-4 text-brand-500" />
        <h2 class="text-sm font-bold text-text-main">Default Routing Strategy</h2>
      </div>

      <div class="space-y-3 text-xs">
        <div class="space-y-1">
          <label for="fallback-strat" class="block font-semibold text-text-main">Connection Fallback Strategy</label>
          <select
            id="fallback-strat"
            bind:value={fallbackStrategy}
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="failover">Failover (Try next on error/rate-limit)</option>
            <option value="round-robin">Round Robin (Distribute requests across all connections)</option>
            <option value="sticky-round-robin">Sticky Round-Robin (Keep active connection up to limit)</option>
          </select>
        </div>

        {#if fallbackStrategy === 'sticky-round-robin'}
          <div class="space-y-1">
            <label for="sticky-limit" class="block font-semibold text-text-main">Sticky Request Limit</label>
            <input
              id="sticky-limit"
              type="number"
              bind:value={stickyRoundRobinLimit}
              min="1"
              max="100"
              class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
            />
            <p class="text-[11px] text-text-subtle">
              Number of consecutive requests routed to the same credential before rotating.
            </p>
          </div>
        {/if}

        <div class="space-y-1">
          <label for="combo-strat" class="block font-semibold text-text-main">Combo Routing Mode</label>
          <select
            id="combo-strat"
            bind:value={comboStrategy}
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
          >
            <option value="first-model">First Model (Always try primary model first)</option>
            <option value="round-robin">Round Robin (Rotate starting model)</option>
          </select>
        </div>

        <Toggle
          checked={forceFallback}
          size="sm"
          label="Force Fallback (all accounts cooling down)"
          onChange={(v: boolean) => (forceFallback = v)}
        />
        <p class="text-[11px] text-text-subtle">
          When every account is in cooldown, send the request through the account that
          frees up first instead of failing. Requests can still be rejected upstream.
        </p>

      </div>
    </Card>

    <!-- SECTION 5: Observability & Storage Retention -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Zap class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Observability & Storage Retention</h2>
        </div>
        <Toggle
          checked={enableObservability}
          size="sm"
          label="Enable Observability"
          onChange={(val) => (enableObservability = val)}
        />
      </div>

      <div class="space-y-4 text-xs">
        <p class="text-text-muted leading-relaxed">
          Manage real-time request logging, retention limits, and storage compaction for diagnostic tables.
        </p>

        <!-- Observability Mode & Retention Controls -->
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
          <div class="space-y-1">
            <label class="text-[11px] font-medium text-text-muted">Observability Mode</label>
            <select
              bind:value={observabilityMode}
              class="w-full rounded-lg border border-border bg-surface-2 px-2.5 py-1.5 text-xs text-text-main focus:border-brand-500 focus:outline-none"
            >
              <option value="full">Full (Record all requests)</option>
              <option value="errors_only">Errors Only (Diagnostics only)</option>
              <option value="off">Off (Zero-Trace / Headless)</option>
            </select>
          </div>

          <div class="space-y-1">
            <label class="text-[11px] font-medium text-text-muted">Diagnostics Retention</label>
            <select
              bind:value={retentionDays}
              class="w-full rounded-lg border border-border bg-surface-2 px-2.5 py-1.5 text-xs text-text-main focus:border-brand-500 focus:outline-none"
            >
              <option value={1}>1 Day (STB / Low-Memory)</option>
              <option value={3}>3 Days</option>
              <option value={7}>7 Days (Recommended)</option>
              <option value={14}>14 Days</option>
              <option value={30}>30 Days</option>
              <option value={0}>Unlimited</option>
            </select>
          </div>

          <div class="space-y-1">
            <label class="text-[11px] font-medium text-text-muted">Max Request Records</label>
            <input
              type="number"
              bind:value={maxRequestRecords}
              min="500"
              step="500"
              class="w-full rounded-lg border border-border bg-surface-2 px-2.5 py-1.5 text-xs text-text-main focus:border-brand-500 focus:outline-none"
            />
          </div>

          <div class="space-y-1">
            <label class="text-[11px] font-medium text-text-muted">Usage Ledger Retention</label>
            <select
              bind:value={usageHistoryRetentionDays}
              class="w-full rounded-lg border border-border bg-surface-2 px-2.5 py-1.5 text-xs text-text-main focus:border-brand-500 focus:outline-none"
            >
              <option value={0}>Off (Keep Forever, Recommended)</option>
              <option value={30}>30 Days</option>
              <option value={90}>90 Days</option>
              <option value={180}>180 Days</option>
              <option value={365}>365 Days (1 Year)</option>
            </select>
          </div>
        </div>

        <!-- Database Storage Stats & Actions. The numbers come from the
             admin-session stats endpoint, so an install whose dashboard is open
             without a session (requireLogin off) fails it while every other card
             works: the rows then read "—" and the reason is printed, instead of
             the whole block — buttons included — vanishing. -->
        <div class="p-3 rounded-xl bg-bg border border-border space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-2 text-[11px]">
            <div>
              <span class="text-text-muted">Database File Size: </span>
              <span class="font-mono font-semibold text-text-main"
                >{dbStats ? formatBytes(dbStats.databaseSizeBytes) : '—'}</span
              >
            </div>
            <div>
              <span class="text-text-muted">Request Details: </span>
              <span class="font-mono font-semibold text-text-main"
                >{dbStats ? `${(dbStats.requestDetailsCount ?? 0).toLocaleString()} rows` : '—'}</span
              >
            </div>
            <div>
              <span class="text-text-muted">Usage Ledger: </span>
              <span class="font-mono font-semibold text-text-main"
                >{dbStats ? `${(dbStats.usageHistoryCount ?? 0).toLocaleString()} rows` : '—'}</span
              >
              {#if dbStats?.usageHistoryOldestTimestamp}
                <span class="text-[10px] text-text-subtle font-mono ml-1"
                  >(since {dbStats.usageHistoryOldestTimestamp.slice(0, 10)})</span
                >
              {/if}
            </div>
            {#if !dbStats || (dbStats.reclaimableBytes ?? 0) > 0}
              <div>
                <span class="text-text-muted">Reclaimable: </span>
                <span class="font-mono font-semibold text-amber-500"
                  >{dbStats ? formatBytes(dbStats.reclaimableBytes ?? 0) : '—'}</span
                >
              </div>
            {/if}
          </div>

          {#if dbStatsError}
            <div
              class="flex flex-wrap items-center gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-2.5 py-2 text-[11px] text-amber-500"
            >
              <span>Storage stats unavailable — {dbStatsError}</span>
              <button
                type="button"
                onclick={loadDbStats}
                class="rounded-md border border-amber-500/40 px-2 py-0.5 font-semibold transition-colors hover:bg-amber-500/20 cursor-pointer"
              >
                Retry
              </button>
            </div>
          {/if}

          <div class="flex flex-wrap gap-2 pt-1 border-t border-border-subtle/60">
            <button
              type="button"
              onclick={handlePrune}
              disabled={isPruning}
              class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-surface-2 hover:bg-surface-3 border border-border text-xs text-text-main font-medium transition cursor-pointer disabled:opacity-50"
            >
              <span>{isPruning ? 'Pruning...' : 'Prune Expired Records'}</span>
            </button>
            <button
              type="button"
              onclick={handlePruneUsage}
              disabled={isPruningUsage || (dbStats !== null && (dbStats.usageHistoryCount ?? 0) === 0)}
              class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-surface-2 hover:bg-surface-3 border border-border text-xs text-text-main font-medium transition cursor-pointer disabled:opacity-50"
            >
              <span>{isPruningUsage ? 'Pruning Ledger...' : 'Prune Usage History'}</span>
            </button>
            <button
              type="button"
              onclick={handleVacuum}
              disabled={isVacuuming}
              class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-brand-500/10 hover:bg-brand-500/20 border border-brand-500/30 text-xs text-brand-500 font-semibold transition cursor-pointer disabled:opacity-50"
            >
              <span>{isVacuuming ? 'Compacting...' : 'Compact Database (VACUUM)'}</span>
            </button>
          </div>
        </div>
      </div>
    </Card>

    <!-- SECTION 6: Global System Prompt & Anti-Poisoning -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Shield class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Global System Prompt &amp; Anti-Poisoning</h2>
        </div>
        <Toggle
          checked={enableSystemPrompt}
          size="sm"
          label="Enable Global System Prompt"
          onChange={(val: boolean) => (enableSystemPrompt = val)}
        />
      </div>

      <div class="space-y-4 text-xs">
        <p class="text-text-muted leading-relaxed">
          The prefix is placed at the very start of every request's system prompt and the suffix at its
          very end, for OpenAI Chat, Claude Messages and Responses bodies alike. Put identity rules in
          the prefix and non-negotiable constraints in the suffix: the suffix is the model's last word
          before the user's content.
        </p>

        <div class="flex justify-end">
          <button
            type="button"
            onclick={loadAntiPoisoningPreset}
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-brand-500/10 hover:bg-brand-500/20 border border-brand-500/30 text-xs text-brand-500 font-semibold transition cursor-pointer"
          >
            <span>Load Anti-Poisoning Preset</span>
          </button>
        </div>

        <div class="space-y-1">
          <label for="sys-prompt-prefix" class="block font-semibold text-text-main">Prefix</label>
          <textarea
            id="sys-prompt-prefix"
            bind:value={systemPromptPrefix}
            rows="4"
            spellcheck="false"
            placeholder="Identity and invariants read before anything else."
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
          ></textarea>
        </div>

        <div class="space-y-1">
          <label for="sys-prompt-suffix" class="block font-semibold text-text-main">Suffix</label>
          <textarea
            id="sys-prompt-suffix"
            bind:value={systemPromptSuffix}
            rows="4"
            spellcheck="false"
            placeholder="Constraints the model must hold after reading user content."
            class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
          ></textarea>
        </div>
      </div>
    </Card>
    <!-- SECTION: White-label branding. The sidebar renders these two values, so
         the card is the only place the product name and logo are set. An empty
         value falls back to the project defaults, which is what an operator who
         never opens this card keeps. -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center gap-2 pb-2 border-b border-border">
        <Sliders class="w-4 h-4 text-brand-500" />
        <h2 class="text-sm font-bold text-text-main">White-label branding</h2>
      </div>
      <p class="text-xs text-text-muted">
        Customize the name and logo shown in the dashboard sidebar. Leave blank to use the 9router-go defaults.
      </p>
      <label class="block space-y-1">
        <span class="text-xs font-medium text-text-main">Application name</span>
        <Input bind:value={appName} placeholder="9router-go" />
      </label>
      <label class="block space-y-1">
        <span class="text-xs font-medium text-text-main">Logo URL</span>
        <Input bind:value={appLogoUrl} placeholder="https://example.com/logo.png" />
        <span class="text-[11px] text-text-subtle">An HTTPS URL, or a local absolute path such as /logo.png</span>
      </label>
      <!-- Upload writes the file into the gateway's own data directory and
           rewrites the URL above, so an operator with no public host still
           gets a branded sidebar: the <img> points at this gateway. -->
      <div class="flex items-center gap-2">
        <input
          type="file"
          accept=".png,.svg,.webp,.jpg,.jpeg,image/png,image/svg+xml,image/webp,image/jpeg"
          bind:this={logoFileInput}
          onchange={handleLogoFileSelected}
          class="hidden"
        />
        <button
          type="button"
          class="inline-flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 text-xs font-medium text-text-main transition-colors hover:border-brand-500/40 disabled:opacity-50"
          disabled={isUploadingLogo}
          onclick={() => logoFileInput?.click()}
        >
          <Upload class="h-3.5 w-3.5" />
          {isUploadingLogo ? 'Uploading…' : 'Upload image'}
        </button>
        <span class="text-[11px] text-text-subtle">PNG, SVG, WebP or JPEG · max 2 MB</span>
      </div>
      {#if logoUploadError}
        <p class="text-[11px] font-medium text-error">{logoUploadError}</p>
      {/if}
      {#if appLogoUrl}
        <div class="flex items-center gap-3 rounded-lg bg-surface-2 p-3">
          <img src={appLogoUrl} alt="Logo preview" class="h-10 w-10 rounded object-contain" />
          <span class="text-xs text-text-muted">Preview</span>
        </div>
      {/if}
    </Card>
    <!-- SECTION: Display. Unit abbreviation, the usage-detail default, an
         advanced-settings disclosure that unfolds the currency rates, the
         per-panel Usage list and the sidebar features, and the display currency
         itself. The switches here are localStorage preferences that apply at
         once, like the two toggles at the top: every change still raises the
         save reminder, because the currency they sit beside is a saved setting. -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Sliders class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Display</h2>
        </div>
      </div>

      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <p class="text-xs font-semibold text-text-main">Unit abbreviation</p>
          <p class="text-[11px] text-text-subtle">Large numbers as 亿/万 (zh) or M/B (en); off → full numbers</p>
        </div>
        <Toggle
          checked={compactUnits}
          size="sm"
          label="Unit abbreviation"
          onChange={(val: boolean) => {
            setCompactUnits(val)
            compactUnits = isCompactUnitsEnabled()
          }}
        />
      </div>

      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <p class="text-xs font-semibold text-text-main">Advanced settings</p>
          <p class="text-[11px] text-text-subtle">
            Unfolds the currency rates, the Sidebar card and the per-panel Usage list below. Folding
            them back changes nothing about what they do.
          </p>
        </div>
        <Toggle
          checked={advancedSettings}
          size="sm"
          label="Advanced settings"
          onChange={(val: boolean) => {
            setAdvancedSettingsVisible(val)
            advancedSettings = isAdvancedSettingsVisible()
          }}
        />
      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <p class="text-xs font-semibold text-text-main">Provider card toggle</p>
          <p class="text-[11px] text-text-subtle">
            Show the on/off switch on each provider card permanently on desktop
            instead of revealing it on hover.
          </p>
        </div>
        <Toggle
          checked={providerTogglePersistent}
          size="sm"
          label="Provider card toggle persistence"
          onChange={(val) => {
            setProviderTogglePersistent(val)
            providerTogglePersistent = val
          }}
        />
      </div>
      <div class="flex items-center justify-between gap-3 border-t border-border pt-3">
        <div class="min-w-0">
          <p class="text-xs font-semibold text-text-main">Animation level</p>
          <p class="text-[11px] text-text-subtle">
            How much the dashboard animates. Minimal drops blur and transitions but
            keeps loading spinners; Off stops those too.
          </p>
        </div>
        <select
          bind:value={animationLevel}
          onchange={() => setAnimationLevel(animationLevel)}
          class="shrink-0 rounded-lg border border-border bg-bg px-2 py-1 text-xs text-text-main focus:outline-none focus:border-brand-500 cursor-pointer"
        >
          {#each ANIMATION_LEVELS as option (option.id)}
            <option value={option.id}>{option.label}</option>
          {/each}
        </select>
      </div>
      <div class="space-y-3 border-t border-border pt-3">
        <p class="text-xs font-semibold text-text-main">Currency</p>

        <p class="text-[11px] text-text-subtle leading-relaxed">
          Every cost is measured in USD and shown here in the currency you pick. The rates are yours
          to keep: edit them, or refresh them from a public provider.
        </p>

        <div class="space-y-3">
          <div class="space-y-1">
            <label for="currency-code" class="text-xs font-semibold text-text-main">Display currency</label>
            <select
              id="currency-code"
              bind:value={currencyCode}
              class="w-full px-3 py-2 rounded-lg bg-bg border border-border text-xs text-text-main focus:outline-none focus:border-brand-500"
            >
              {#each CURRENCY_CATALOG as option (option.code)}
                <option value={option.code}>{option.code} — {option.label}</option>
              {/each}
            </select>
          </div>
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs text-text-main">Show USD next to the converted amount</span>
            <Toggle
              checked={currencyShowBoth}
              size="sm"
              label="Show USD next to the converted amount"
              onChange={(val) => (currencyShowBoth = val)}
            />
          </div>
        </div>

        {#if advancedSettings}
        <div class="space-y-1">
          <p class="text-xs text-text-main">Rates — one USD in each currency</p>
          <div class="grid grid-cols-2 sm:grid-cols-3 gap-2">
            {#each CURRENCY_CATALOG as option (option.code)}
              <label class="flex items-center gap-2 rounded-[10px] border border-border-subtle px-2 py-1.5">
                <span class="w-9 text-[11px] font-semibold text-text-muted">{option.code}</span>
                <input
                  type="number"
                  min="0"
                  step="any"
                  inputmode="decimal"
                  value={currencyRates[option.code] ?? ''}
                  placeholder={String(DEFAULT_RATES[option.code] ?? '')}
                  oninput={(e) => setRate(option.code, (e.target as HTMLInputElement).value)}
                  class="w-full bg-transparent text-xs text-text-main focus:outline-none"
                />
              </label>
            {/each}
          </div>
        </div>
        {/if}

        <div class="flex flex-wrap items-center justify-between gap-2">
          <p class="text-[11px] text-text-subtle">
            1 USD = {formatCost(1, { code: currencyCode, rates: currencyRates, showBoth: false })} ·
            {describeRates(currencySetting())}
          </p>
          <Button variant="secondary" size="sm" onclick={refreshRates} loading={isRefreshingRates}>
            <RefreshCw class="w-3.5 h-3.5 text-text-muted" />
            <span>Update currency</span>
          </Button>
        </div>

        {#if ratesMessage}
          <p class="text-[11px] text-text-muted leading-relaxed">{ratesMessage}</p>
        {/if}
      </div>
    </Card>
    {#if advancedSettings}
    <!-- SECTION: Sidebar. Which entries this deployment draws, and in what order
         they sit, folded behind the Display switch with the Usage panel card: both
         are arrangement surfaces rather than day-to-day preferences. Both facts
         ride the one server-side navVisibility value, so a single Save publishes
         them; the preset fills the switches and leaves the arrangement where the
         operator put it. -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between gap-2 pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <PanelLeft class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Sidebar</h2>
        </div>
        <button
          type="button"
          onclick={() => markLayoutEdit(() => (navOrder = []))}
          class="text-[10px] font-medium text-text-muted transition-colors hover:text-text-main cursor-pointer"
        >
          Reset order
        </button>
      </div>

      <div class="space-y-2">
        <div class="space-y-1">
          <label class="text-[11px] font-medium text-text-muted" for="sidebar-preset">Preset</label>
          <select
            id="sidebar-preset"
            value={navPreset}
            title={navPresetHint}
            onchange={(event) =>
              markLayoutEdit(() =>
                (hiddenNav = navPresetHidden(
                  (event.currentTarget as HTMLSelectElement).value as NavPreset
                ))
              )}
            class="w-full rounded-lg border border-border bg-surface-2 px-2.5 py-1.5 text-xs text-text-main focus:border-brand-500 focus:outline-none"
          >
            <!-- Shown only while the switches match no preset: a selection of
                 one's own is a state to read, not one to pick. -->
            <option value="custom" disabled>Custom</option>
            {#each NAV_PRESETS as preset (preset.id)}
              <option value={preset.id} title={preset.hint}>{preset.label}</option>
            {/each}
          </select>
          <p class="text-[11px] text-text-subtle">{navPresetHint}</p>
        </div>

        {#snippet navRow(entry: NavEntry, siblings: NavEntry[])}
          {@const index = siblings.findIndex((candidate) => candidate.id === entry.id)}
          <div
            class="relative flex items-center justify-between gap-3 rounded-md px-1 py-1 {dragZone ===
            'nav' && dragId === entry.id
              ? 'opacity-40'
              : ''}"
            ondragover={(event) => trackRowDrop(event, 'nav', entry.id)}
            ondragleave={(event) => leaveRowDrop(event, entry.id)}
            ondrop={(event) => dropOnRow(event, 'nav', entry.id)}
          >
            {#if dragTarget?.id === entry.id && dragId !== entry.id}
              <div
                class="pointer-events-none absolute inset-x-0 h-0.5 bg-primary {dragTarget?.before
                  ? 'top-0'
                  : 'bottom-0'}"
              ></div>
            {/if}
            <div class="flex min-w-0 items-center gap-1.5">
              <span
                class="material-symbols-outlined cursor-grab text-[14px] text-text-subtle active:cursor-grabbing"
                title="Drag to reorder"
                aria-hidden="true"
                draggable="true"
                ondragstart={(event) => startRowDrag(event, 'nav', entry.id)}
                ondragend={endRowDrag}>drag_indicator</span
              >
              <p class="min-w-0 truncate text-xs text-text-main">{entry.label}</p>
            </div>
            <div class="flex items-center gap-2">
              <div class="flex items-center gap-0.5">
                <button
                  type="button"
                  title="Move up"
                  aria-label="Move {entry.label} up"
                  disabled={index === 0}
                  onclick={() =>
                    markLayoutEdit(() => (navOrder = moveNavEntry(navOrder, entry.id, -1)))}
                  class="rounded p-0.5 text-text-muted transition-colors hover:text-text-main disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"
                >
                  <span class="material-symbols-outlined text-[14px]">arrow_upward</span>
                </button>
                <button
                  type="button"
                  title="Move down"
                  aria-label="Move {entry.label} down"
                  disabled={index === siblings.length - 1}
                  onclick={() =>
                    markLayoutEdit(() => (navOrder = moveNavEntry(navOrder, entry.id, 1)))}
                  class="rounded p-0.5 text-text-muted transition-colors hover:text-text-main disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"
                >
                  <span class="material-symbols-outlined text-[14px]">arrow_downward</span>
                </button>
              </div>
              <Toggle
                checked={!hiddenNav.includes(entry.id)}
                disabled={true}
                title="Visibility is managed per deployment; use the arrows or drag to reorder"
                size="sm"
                label="{entry.label} in the sidebar"
                onChange={(val: boolean) => {
                  hiddenNav = setNavVisible(hiddenNav, entry.id, val)
                }}
              />
            </div>
          </div>
        {/snippet}

        {#each arrangedNavGroups(navOrder) as group (group.id)}
          <div class="space-y-1">
            {#if group.label}
              <p class="text-[10px] font-semibold uppercase tracking-wider text-text-subtle">
                {group.label}
              </p>
            {/if}
            {#each group.entries as entry (entry.id)}
              {@render navRow(entry, group.entries)}
              {#if entry.kind === 'accordion'}
                <div class="ml-4 space-y-1 border-l border-border-subtle pl-2">
                  {#each entry.children as child (child.id)}
                    {@render navRow(child, entry.children)}
                  {/each}
                </div>
              {/if}
            {/each}
          </div>
        {/each}
      </div>
    </Card>
    {/if}
    {#if advancedSettings}
    <!-- SECTION: Usage dashboard. Per-panel visibility and arrangement for every
         Usage section (Overview, Details, Cache Analytics, Compression
         Analytics), folded away with the rest of the advanced settings: the
         arrangement keeps driving the Usage page while the card is hidden. It has
         no Save of its own on purpose — the presets, the toggles, the arrows and a
         drag all raise the global reminder, and the global Save writes the
         arrangement with the rest of the form. -->
    <Card padding="md" class="space-y-4">
      <div class="flex items-center justify-between gap-2 pb-2 border-b border-border">
        <div class="flex items-center gap-2">
          <Sliders class="w-4 h-4 text-brand-500" />
          <h2 class="text-sm font-bold text-text-main">Usage dashboard</h2>
        </div>
      </div>
      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <p class="text-xs font-semibold text-text-main">Expand usage details</p>
          <p class="text-[11px] text-text-subtle">
            Activity Heatmap and Node Health open instead of starting collapsed
          </p>
        </div>
        <Toggle
          checked={detailsExpanded}
          size="sm"
          label="Expand usage details"
          onChange={(val: boolean) => {
            setDetailsExpandedByDefault(val)
            detailsExpanded = isDetailsExpandedByDefault()
          }}
        />
      </div>
      <div class="space-y-4">
        <div class="space-y-2">
        <div class="space-y-1">
          <label class="text-[11px] font-medium text-text-muted" for="usage-panel-preset">Preset</label>
          <select
            id="usage-panel-preset"
            value={panelPreset}
            title={panelHint}
            onchange={(event) =>
              markLayoutEdit(() =>
                applyUsagePreset((event.currentTarget as HTMLSelectElement).value as UsagePanelPreset)
              )}
            class="w-full rounded-lg border border-border bg-surface-2 px-2.5 py-1.5 text-xs text-text-main focus:border-brand-500 focus:outline-none"
          >
            <!-- Shown only while the toggles match no preset: an arrangement of
                 one's own is a state to read, not one to pick. -->
            <option value="custom" disabled>Custom</option>
            {#each USAGE_PANEL_PRESETS as preset (preset.id)}
              <option value={preset.id} title={preset.hint}>{preset.label}</option>
            {/each}
          </select>
        </div>
          <p class="text-[11px] text-text-subtle">{panelHint}</p>
        </div>
        {#each USAGE_PANEL_GROUPS as group (group.id)}
          {@const orderable = USAGE_PANEL_ORDERABLE_GROUPS.includes(group.id)}
          <div class="space-y-2">
            <div class="flex items-center justify-between gap-2">
              <p class="text-[10px] font-semibold uppercase tracking-wider text-text-subtle">
                {group.label}
              </p>
              {#if orderable}
                <button
                  type="button"
                  onclick={() => markLayoutEdit(() => resetPanelOrder(group.id))}
                  class="text-[10px] font-medium text-text-muted transition-colors hover:text-text-main cursor-pointer"
                >
                  Reset order
                </button>
              {:else}
                <span class="text-[10px] text-text-subtle">Order fixed by the layout</span>
              {/if}
            </div>
            {#each panelOrder(group.id) as itemId, index (itemId)}
              {@const item = group.items.find((candidate) => candidate.id === itemId)}
              {@const fixed = USAGE_PANEL_FIXED_POSITION[itemId]}
              {@const grabbable = orderable && !fixed}
              {@const landing = dragTarget?.id === itemId && dragId !== itemId}
              <div
                class="relative flex items-center justify-between gap-3 rounded-md px-1 py-1 {dragId ===
                itemId
                  ? 'opacity-40'
                  : ''}"
                ondragover={(event) => trackRowDrop(event, 'panel', itemId, group.id)}
                ondragleave={(event) => leaveRowDrop(event, itemId)}
                ondrop={(event) => dropOnRow(event, 'panel', itemId, group.id)}
              >
                {#if landing}
                  <div
                    class="pointer-events-none absolute inset-x-0 h-0.5 bg-primary {dragTarget?.before
                      ? 'top-0'
                      : 'bottom-0'}"
                  ></div>
                {/if}
                <div class="flex min-w-0 items-center gap-1.5">
                  {#if grabbable}
                    <span
                      class="material-symbols-outlined cursor-grab text-[14px] text-text-subtle active:cursor-grabbing"
                      title="Drag to reorder"
                      aria-hidden="true"
                      draggable="true"
                      ondragstart={(event) => startRowDrag(event, 'panel', itemId, group.id)}
                      ondragend={endRowDrag}>drag_indicator</span
                    >
                  {/if}
                  <p class="min-w-0 text-xs text-text-main">{item?.label}</p>
                </div>
                <div class="flex items-center gap-2">
                  {#if orderable}
                    <div class="flex items-center gap-0.5">
                      <button
                        type="button"
                        title={fixed ?? 'Move up'}
                        aria-label="Move {item?.label} up"
                        disabled={Boolean(fixed) || index === 0}
                        onclick={() => markLayoutEdit(() => movePanel(group.id, itemId, -1))}
                        class="rounded p-0.5 text-text-muted transition-colors hover:text-text-main disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"
                      >
                        <span class="material-symbols-outlined text-[14px]">arrow_upward</span>
                      </button>
                      <button
                        type="button"
                        title={fixed ?? 'Move down'}
                        aria-label="Move {item?.label} down"
                        disabled={Boolean(fixed) || index === panelOrder(group.id).length - 1}
                        onclick={() => markLayoutEdit(() => movePanel(group.id, itemId, 1))}
                        class="rounded p-0.5 text-text-muted transition-colors hover:text-text-main disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"
                      >
                        <span class="material-symbols-outlined text-[14px]">arrow_downward</span>
                      </button>
                    </div>
                  {/if}
                  <Toggle
                    checked={isPanelVisible(itemId)}
                    size="sm"
                    label={item?.label ?? itemId}
                    onChange={(val: boolean) => setPanelVisible(itemId, val)}
                  />
                </div>
              </div>
            {/each}
          </div>
        {/each}
      </div>
    </Card>
    {/if}

    <!-- Save reminder: an operator who flipped a switch must not walk away
         without saving. It appears on a change and hides itself again after a
         few seconds (markUnsaved). -->
    {#if showSaveHint}
      <div
        role="status"
        class="fixed bottom-5 right-5 z-40 flex items-center gap-3 rounded-[10px] border border-border bg-surface-2 px-4 py-3 shadow-[var(--shadow-elev)]"
      >
        <span class="text-xs text-text-muted">Unsaved changes</span>
        <Button variant="primary" onclick={handleSaveAll} loading={isSavingSettings}>
          <Save class="w-3.5 h-3.5" />
          <span>Save</span>
        </Button>
      </div>
    {/if}

    <Modal
      isOpen={dbAuthOpen}
      onClose={closeDbAuth}
      title={pendingImportFile ? 'Confirm Import' : 'Confirm Download'}
      size="sm"
    >
      <div class="space-y-3" data-save-hint="off">
        <p class="text-text-muted">
          {pendingImportFile
            ? `Import "${pendingImportFile.name}"? This will overwrite existing server data.`
            : 'Download a full backup of the dashboard database (.json file)?'}
        </p>
        <Input
          type="password"
          label="Dashboard password"
          placeholder="Enter password to authorize"
          bind:value={dbPassword}
        />
      </div>
      {#snippet footer()}
        <Button variant="ghost" onclick={closeDbAuth} disabled={isImportingBackup || isDownloadingBackup}>
          Cancel
        </Button>
        <Button
          variant="primary"
          onclick={() => (pendingImportFile ? runImportBackup() : runDownloadBackup())}
          loading={isImportingBackup || isDownloadingBackup}
        >
          {pendingImportFile ? 'Import' : 'Download'}
        </Button>
      {/snippet}
    </Modal>
  </div>
</div>
