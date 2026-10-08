<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Input from '../lib/ui/Input.svelte'
  import { copyToClipboard } from '../lib/clipboard'
  import { api, type Settings } from '../api/client'

  interface Props {
    settings?: Settings
    onRefresh?: () => void
  }

  let { settings = {}, onRefresh }: Props = $props()

  // State
  let rtkEnabled = $state(true)
  let rtkMode = $state<'simple' | 'advance'>('simple')
  let rtkIntensity = $state<'minimal' | 'standard' | 'aggressive'>('standard')
  let rtkMaxLines = $state(100)
  let rtkMaxChars = $state(8000)
  let rtkDeduplicate = $state(true)
  let rtkCategories = $state<Record<string, boolean>>({
    git: true,
    build: true,
    test: true,
    package: true,
    docker: true,
    system: true,
  })
  let rtkFilters = $state<Record<string, boolean>>({})
  let rtkFiltersList = $state<
    Array<{
      id: string
      label: string
      category: string
      description: string
      priority: number
      enabled: boolean
    }>
  >([])
  let rtkFiltersLoading = $state(false)
  let rtkRawRetention = $state<'never' | 'failures' | 'always'>('never')

  // RTK Test Bench State
  let rtkTestInput = $state('')
  let rtkTestLoading = $state(false)
  let rtkTestResult = $state<{
    originalTokens: number
    compressedTokens: number
    savedTokens: number
    savedPct: number
    text: string
    detectedCategory: string
    techniquesUsed: string[]
  } | null>(null)

  let headroomEnabled = $state(false)
  let headroomUrl = $state('http://localhost:8787')
  let headroomTimeoutMs = $state(3000)
  let headroomStatus = $state<{
    installed: boolean
    running: boolean
    python: string | null
    loading: boolean
    localUrl?: boolean
    canStart?: boolean
    managedPid?: number | null
    path?: string | null
    version?: string | null
  }>({
    installed: false,
    running: false,
    python: null,
    loading: true,
  })

  let isHeadroomModalOpen = $state(false)
  let headroomActionLoading = $state(false)
  let headroomActionError = $state('')

  let headroomExtras = $state<{
    version: string | null
    extras: { code: boolean; ml: boolean }
    available: string[]
    loading: boolean
  }>({
    version: null,
    extras: { code: false, ml: false },
    available: ['code', 'ml'],
    loading: false,
  })

  let pendingExtras = $state<string[]>([])
  let extrasActionLoading = $state(false)
  let extrasActionError = $state('')
  let removingExtra = $state<string | null>(null)
  let installLog = $state('')
  let extrasConfirm = $state<{
    title: string
    message: string
    confirmText: string
    variant: 'primary' | 'danger'
    onConfirm: () => void
  } | null>(null)

  let codeAware = $state(false)
  let kompress = $state(true)
  let restartingProxy = $state(false)
  let logPollInterval: ReturnType<typeof setInterval> | null = null

  let cavemanEnabled = $state(false)
  let cavemanMode = $state<'simple' | 'advance'>('simple')
  let cavemanLevel = $state('full')
  let cavemanAutoClarity = $state(true)
  let cavemanLanguage = $state('en')
  let cavemanInputMode = $state(false)
  let cavemanPreserveKeywords = $state('')

  // Caveman Preview & Test State
  let cavemanPromptPreview = $state('')
  let cavemanPreviewLoading = $state(false)
  let cavemanTestInput = $state('')
  let cavemanTestLoading = $state(false)
  let cavemanTestResult = $state<{
    text: string
    originalTokens: number
    compressedTokens: number
    savedPct: number
  } | null>(null)

  let ponytailEnabled = $state(false)
  let ponytailLevel = $state('full')
  let adhdEnabled = $state(false)
  let adhdLevel = $state('full')
  let semanticCacheEnabled = $state(false)
  let semanticCacheTTL = $state('1440')
  let semanticCacheMaxEntries = $state('1000')
  let locale = $state('en')

  // Collapsible Card States (Compact by default)
  let rtkExpanded = $state(false)
  let cavemanExpanded = $state(false)
  let ponytailExpanded = $state(false)
  let headroomExpanded = $state(false)
  let adhdExpanded = $state(false)
  let showRecommendedGuide = $state(false)
  let recommendedApplied = $state(false)

  async function applyRecommendedPreset() {
    rtkEnabled = true
    cavemanEnabled = true
    cavemanLevel = 'full'
    adhdEnabled = true
    adhdLevel = 'full'
    ponytailEnabled = false
    headroomEnabled = false

    await patchSetting({
      rtkEnabled: true,
      cavemanEnabled: true,
      cavemanLevel: 'full',
      adhdEnabled: true,
      adhdLevel: 'full',
      ponytailEnabled: false,
      headroomEnabled: false,
    })

    recommendedApplied = true
    setTimeout(() => {
      recommendedApplied = false
    }, 3000)
  }

  // Collapsible Filter Categories in RTK Advance mode
  let expandedFilterCategories = $state<Record<string, boolean>>({
    git: true,
    build: false,
    test: false,
    package: false,
    docker: false,
    system: false,
  })

  let copiedInstallCmd = $state(false)

  const WENYAN_LOCALES = ['zh', 'zh-CN', 'zh-TW']
  const CAVEMAN_LEVELS = [
    { id: 'lite', label: 'Lite', desc: 'Drop filler, keep grammar' },
    { id: 'full', label: 'Full', desc: 'Drop articles, fragments OK' },
    { id: 'ultra', label: 'Ultra', desc: 'Telegraphic, max compression' },
    { id: 'wenyan-lite', label: '文 Lite', desc: 'Classical Chinese, light compression', wenyan: true },
    { id: 'wenyan', label: '文 Full', desc: 'Maximum 文言文, 80-90% reduction', wenyan: true },
    { id: 'wenyan-ultra', label: '文 Ultra', desc: 'Extreme classical compression', wenyan: true },
  ]

  const PONYTAIL_LEVELS = [
    { id: 'lite', label: 'Lite', desc: 'Build asked, name lazier option' },
    { id: 'full', label: 'Full', desc: 'Ladder enforced: stdlib/native first' },
    { id: 'ultra', label: 'Ultra', desc: 'YAGNI extremist, deletion first' },
  ]

  const ADHD_LEVELS = [
    { id: 'full', label: 'Full', desc: '10 Rules' },
    { id: 'lite', label: 'Lite', desc: 'Compact' },
  ]

  let isWenyanLocale = $derived(WENYAN_LOCALES.includes(locale))
  let visibleCavemanLevels = $derived(
    isWenyanLocale ? CAVEMAN_LEVELS : CAVEMAN_LEVELS.filter((lvl) => !lvl.wenyan)
  )

  let headroomRunning = $derived(!!headroomStatus.running)
  let headroomStatusLabel = $derived(
    headroomStatus.loading
      ? 'Checking…'
      : headroomRunning
        ? 'Running'
        : headroomStatus.localUrl !== false && !headroomStatus.installed
          ? 'Not installed'
          : headroomStatus.localUrl !== false
            ? 'Stopped'
            : 'External'
  )
  let headroomLocalUrl = $derived(headroomStatus.localUrl !== false)
  let headroomCanStart = $derived(!!headroomStatus.canStart)
  let headroomManaged = $derived(headroomLocalUrl && !!headroomStatus.managedPid)

  // Sync props from settings
  $effect(() => {
    if (settings) {
      if (typeof settings.rtkEnabled === 'boolean') rtkEnabled = settings.rtkEnabled
      if (settings.rtkMode === 'simple' || settings.rtkMode === 'advance') rtkMode = settings.rtkMode
      if (settings.rtkIntensity === 'minimal' || settings.rtkIntensity === 'standard' || settings.rtkIntensity === 'aggressive') rtkIntensity = settings.rtkIntensity
      if (typeof settings.rtkMaxLines === 'number' && settings.rtkMaxLines > 0) rtkMaxLines = settings.rtkMaxLines
      if (typeof settings.rtkMaxChars === 'number' && settings.rtkMaxChars > 0) rtkMaxChars = settings.rtkMaxChars
      if (typeof settings.rtkDeduplicate === 'boolean') rtkDeduplicate = settings.rtkDeduplicate
      if (settings.rtkCategories && typeof settings.rtkCategories === 'object') rtkCategories = { ...rtkCategories, ...settings.rtkCategories }
      if (settings.rtkFilters && typeof settings.rtkFilters === 'object') rtkFilters = { ...settings.rtkFilters }
      if (settings.rtkRawRetention === 'never' || settings.rtkRawRetention === 'failures' || settings.rtkRawRetention === 'always') rtkRawRetention = settings.rtkRawRetention

      if (typeof settings.headroomEnabled === 'boolean') headroomEnabled = settings.headroomEnabled
      if (typeof settings.headroomUrl === 'string' && settings.headroomUrl) headroomUrl = settings.headroomUrl
      if (typeof settings.headroomTimeoutMs === 'number' && settings.headroomTimeoutMs > 0) {
        headroomTimeoutMs = settings.headroomTimeoutMs
      }
      if (typeof settings.headroomCodeAware === 'boolean') codeAware = settings.headroomCodeAware
      if (typeof settings.headroomKompress === 'boolean') kompress = settings.headroomKompress

      if (typeof settings.cavemanEnabled === 'boolean') cavemanEnabled = settings.cavemanEnabled
      if (settings.cavemanMode === 'simple' || settings.cavemanMode === 'advance') cavemanMode = settings.cavemanMode
      if (typeof settings.cavemanLevel === 'string' && settings.cavemanLevel) cavemanLevel = settings.cavemanLevel
      if (typeof settings.cavemanAutoClarity === 'boolean') cavemanAutoClarity = settings.cavemanAutoClarity
      if (typeof settings.cavemanLanguage === 'string' && settings.cavemanLanguage) cavemanLanguage = settings.cavemanLanguage
      if (typeof settings.cavemanInputMode === 'boolean') cavemanInputMode = settings.cavemanInputMode
      if (typeof settings.cavemanPreserveKeywords === 'string') cavemanPreserveKeywords = settings.cavemanPreserveKeywords

      if (typeof settings.ponytailEnabled === 'boolean') ponytailEnabled = settings.ponytailEnabled
      if (typeof settings.ponytailLevel === 'string' && settings.ponytailLevel) ponytailLevel = settings.ponytailLevel
      if (typeof settings.adhdEnabled === 'boolean') adhdEnabled = settings.adhdEnabled
      if (typeof settings.adhdLevel === 'string' && settings.adhdLevel) adhdLevel = settings.adhdLevel
      if (typeof settings.semanticCacheEnabled === 'boolean') semanticCacheEnabled = settings.semanticCacheEnabled
      if (typeof settings.semanticCacheTTL === 'number' && settings.semanticCacheTTL > 0) semanticCacheTTL = String(settings.semanticCacheTTL)
      if (typeof settings.semanticCacheMaxEntries === 'number' && settings.semanticCacheMaxEntries > 0) semanticCacheMaxEntries = String(settings.semanticCacheMaxEntries)
    }
  })

  // Watch wenyan locale change
  $effect(() => {
    const current = CAVEMAN_LEVELS.find((lvl) => lvl.id === cavemanLevel)
    if (current?.wenyan && !isWenyanLocale) {
      cavemanLevel = 'ultra'
      patchSetting({ cavemanLevel: 'ultra' })
    }
  })

  async function patchSetting(patch: Partial<Settings>) {
    try {
      await api.updateSettings(patch)
      onRefresh?.()
    } catch (error) {
      console.log('Error updating setting:', error)
    }
  }

  async function handleToggleRTK(value: boolean) {
    rtkEnabled = value
    await patchSetting({ rtkEnabled: value })
  }

  function handleSetRtkMode(mode: 'simple' | 'advance') {
    rtkMode = mode
    patchSetting({ rtkMode: mode })
  }

  function handleSelectRtkIntensity(intensity: 'minimal' | 'standard' | 'aggressive') {
    rtkIntensity = intensity
    if (intensity === 'minimal') {
      rtkMaxLines = 200
      rtkMaxChars = 16000
    } else if (intensity === 'aggressive') {
      rtkMaxLines = 50
      rtkMaxChars = 4000
    } else {
      rtkMaxLines = 100
      rtkMaxChars = 8000
    }
    patchSetting({ rtkIntensity: intensity, rtkMaxLines, rtkMaxChars })
  }

  function handleRtkLimitsChange() {
    patchSetting({ rtkMaxLines, rtkMaxChars })
  }

  function handleToggleRtkDeduplicate(value: boolean) {
    rtkDeduplicate = value
    patchSetting({ rtkDeduplicate: value })
  }

  function handleToggleRtkCategory(cat: string) {
    const next = { ...rtkCategories, [cat]: !(rtkCategories[cat] !== false) }
    rtkCategories = next
    patchSetting({ rtkCategories: next })
  }

  function handleToggleIndividualFilter(filterId: string, currentCategory: string) {
    // If filterId is explicitly set, toggle it; otherwise it inherited from category
    const currentEnabled = rtkFilters[filterId] !== undefined
      ? rtkFilters[filterId]
      : (rtkCategories[currentCategory] !== false)
    const nextVal = !currentEnabled
    const nextFilters = { ...rtkFilters, [filterId]: nextVal }
    rtkFilters = nextFilters
    patchSetting({ rtkFilters: nextFilters })
  }

  function toggleFilterCategoryExpand(catKey: string) {
    expandedFilterCategories = {
      ...expandedFilterCategories,
      [catKey]: !expandedFilterCategories[catKey]
    }
  }

  function handleSelectRtkRetention(retention: 'never' | 'failures' | 'always') {
    rtkRawRetention = retention
    patchSetting({ rtkRawRetention: retention })
  }

  function handleSetRtkPreset(type: 'git' | 'test' | 'docker') {
    if (type === 'git') {
      rtkTestInput = `diff --git a/internal/tokensaver/rtk.go b/internal/tokensaver/rtk.go
index 89abcde..1234567 100644
--- a/internal/tokensaver/rtk.go
+++ b/internal/tokensaver/rtk.go
@@ -10,6 +10,12 @@
 func CompressText(s string) string {
-    return s
+    return CompressTextWithConfig(s, DefaultRTKConfig())
 }
@@ -40,6 +46,15 @@
+func DeduplicateLines(s string) (string, bool) {
+    // line collapse logic
+}`
    } else if (type === 'test') {
      rtkTestInput = `PASS src/components/TokenSaverView.test.ts
  ● Token Saver Settings
    ✓ should render simple and advance tabs (12 ms)
    ✓ should update intensity (8 ms)
FAIL src/api/client.test.ts
  ● API Client testRtk
    Expected status 200, received 500
    at Object.<anonymous> (src/api/client.test.ts:42:18)
    at runMicrotasks (<anonymous>)
    at processTicksAndRejections (node:internal/process/task_queues:95:5)
    at runNextTicks (node:internal/process/task_queues:64:3)
Test Suites: 1 failed, 1 passed, 2 total
Tests: 1 failed, 2 passed, 3 total
Snapshots: 0 total
Time: 2.456 s`
    } else if (type === 'docker') {
      rtkTestInput = `CONTAINER ID   IMAGE          COMMAND                  CREATED         STATUS         PORTS                    NAMES
a1b2c3d4e5f6   postgres:16    "docker-entrypoint.s…"   2 hours ago     Up 2 hours     0.0.0.0:5432->5432/tcp   db-primary
f6e5d4c3b2a1   redis:7-alpine "docker-entrypoint.s…"   2 hours ago     Up 2 hours     0.0.0.0:6379->6379/tcp   cache-redis
1234567890ab   nginx:alpine   "/docker-entrypoint.…"   2 hours ago     Up 2 hours     0.0.0.0:80->80/tcp       web-proxy`
    }
  }

  async function handleRunRtkTest() {
    if (!rtkTestInput.trim()) return
    rtkTestLoading = true
    try {
      const res = await api.testRtk({
        text: rtkTestInput,
        config: {
          mode: rtkMode,
          intensity: rtkIntensity,
          maxLines: rtkMaxLines,
          maxChars: rtkMaxChars,
          deduplicate: rtkDeduplicate,
          categories: rtkCategories,
          rawRetention: rtkRawRetention,
        },
      })
      rtkTestResult = res
    } catch (e: any) {
      console.error('Failed to run RTK test', e)
    } finally {
      rtkTestLoading = false
    }
  }

  function handleSetCavemanMode(mode: 'simple' | 'advance') {
    cavemanMode = mode
    patchSetting({ cavemanMode: mode })
  }

  function handleToggleCavemanAutoClarity(value: boolean) {
    cavemanAutoClarity = value
    patchSetting({ cavemanAutoClarity: value })
  }

  function handleSelectCavemanLanguage(lang: string) {
    cavemanLanguage = lang
    patchSetting({ cavemanLanguage: lang })
    loadCavemanPreview()
  }

  function handleToggleCavemanInputMode(value: boolean) {
    cavemanInputMode = value
    patchSetting({ cavemanInputMode: value })
  }

  function handleCavemanPreserveKeywordsBlur() {
    patchSetting({ cavemanPreserveKeywords })
  }

  async function loadCavemanPreview() {
    cavemanPreviewLoading = true
    try {
      const res = await api.testCaveman({
        level: cavemanLevel,
        language: cavemanLanguage,
        mode: 'preview',
        text: '',
      })
      cavemanPromptPreview = res.text
    } catch (e) {
      console.error('Failed to load caveman preview', e)
    } finally {
      cavemanPreviewLoading = false
    }
  }

  function handleSetCavemanPreset(type: 'polite' | 'troubleshoot' | 'indonesia') {
    if (type === 'polite') {
      cavemanTestInput = 'Hello, can you please help me to write a quicksort algorithm in Go? Thank you very much!'
    } else if (type === 'troubleshoot') {
      cavemanTestInput = 'Hi there, could you please help me debug this database connection timeout issue? Thanks!'
    } else if (type === 'indonesia') {
      cavemanTestInput = 'Halo, tolong bantu saya untuk membuat function validasi email di Svelte. Terima kasih!'
    }
  }

  async function handleRunCavemanTest() {
    if (!cavemanTestInput.trim()) return
    cavemanTestLoading = true
    try {
      const res = await api.testCaveman({
        text: cavemanTestInput,
        level: cavemanLevel,
        language: cavemanLanguage,
        preserveKeywords: cavemanPreserveKeywords,
      })
      cavemanTestResult = res
    } catch (e: any) {
      console.error('Failed to run Caveman test', e)
    } finally {
      cavemanTestLoading = false
    }
  }

  async function handleToggleHeadroom(value: boolean) {
    const nextUrl = headroomUrl.trim() || 'http://localhost:8787'
    headroomUrl = nextUrl
    headroomEnabled = value
    await patchSetting({ headroomEnabled: value, headroomUrl: nextUrl })
  }

  async function handleHeadroomUrlBlur() {
    const next = headroomUrl.trim() || 'http://localhost:8787'
    headroomUrl = next
    await patchSetting({ headroomUrl: next })
    refreshHeadroomStatus()
  }

  async function handleHeadroomTimeoutBlur() {
    const raw = Math.round(Number(headroomTimeoutMs))
    const next = Number.isFinite(raw) && raw > 0 ? raw : 3000
    headroomTimeoutMs = next
    await patchSetting({ headroomTimeoutMs: next })
  }

  async function refreshHeadroomStatus() {
    headroomStatus = { ...headroomStatus, loading: true }
    try {
      const data = await api.getHeadroomStatus()
      headroomStatus = {
        installed: !!data.installed,
        running: !!data.running,
        python: data.python ?? null,
        loading: false,
        localUrl: data.localUrl,
        canStart: data.canStart,
        managedPid: data.managedPid,
        path: data.path,
        version: data.version,
      }
      if (!data.installed) {
        headroomExtras = {
          version: null,
          extras: { code: false, ml: false },
          available: ['code', 'ml'],
          loading: false,
        }
        pendingExtras = []
        return
      }
      try {
        const ed = await api.getHeadroomExtras()
        headroomExtras = {
          version: ed.version ?? null,
          extras: (ed.extras as { code: boolean; ml: boolean }) || { code: false, ml: false },
          available: ed.available || ['code', 'ml'],
          loading: false,
        }
        pendingExtras = []
      } catch {
        headroomExtras = {
          version: null,
          extras: { code: false, ml: false },
          available: ['code', 'ml'],
          loading: false,
        }
        pendingExtras = []
      }
    } catch {
      headroomStatus = {
        installed: false,
        running: false,
        python: null,
        loading: false,
      }
      headroomExtras = {
        version: null,
        extras: { code: false, ml: false },
        available: ['code', 'ml'],
        loading: false,
      }
      pendingExtras = []
    }
  }

  async function handleHeadroomStart() {
    headroomActionError = ''
    headroomActionLoading = true
    try {
      const res = await api.startHeadroom()
      if (res.error) throw new Error(res.error)
      await refreshHeadroomStatus()
    } catch (e: any) {
      headroomActionError = e.message || 'Failed to start proxy'
    } finally {
      headroomActionLoading = false
    }
  }

  async function handleHeadroomStop() {
    headroomActionLoading = true
    try {
      await api.stopHeadroom()
      await refreshHeadroomStatus()
    } catch (e: any) {
      headroomActionError = e.message || 'Failed to stop proxy'
    } finally {
      headroomActionLoading = false
    }
  }

  function togglePendingExtra(extra: string) {
    if (pendingExtras.includes(extra)) {
      pendingExtras = pendingExtras.filter((e) => e !== extra)
    } else {
      pendingExtras = [...pendingExtras, extra]
    }
  }

  function startLogPolling() {
    installLog = ''
    if (logPollInterval) clearInterval(logPollInterval)
    const tick = async () => {
      try {
        const d = await api.getHeadroomExtras(true)
        if (typeof d.log === 'string') installLog = d.log
      } catch {}
    }
    tick()
    logPollInterval = setInterval(tick, 1500)
  }

  function stopLogPolling() {
    if (logPollInterval) {
      clearInterval(logPollInterval)
      logPollInterval = null
    }
  }

  onDestroy(() => {
    stopLogPolling()
  })

  async function installExtrasConfirmed() {
    if (pendingExtras.length === 0) return
    extrasActionLoading = true
    extrasActionError = ''
    startLogPolling()
    try {
      const data = await api.installHeadroomExtras(pendingExtras)
      headroomExtras = {
        ...headroomExtras,
        version: data.version ?? headroomExtras.version,
        extras: (data.extras as { code: boolean; ml: boolean }) || headroomExtras.extras,
      }
      pendingExtras = []
    } catch (e: any) {
      extrasActionError = e.message || 'Install failed'
    } finally {
      stopLogPolling()
      extrasActionLoading = false
    }
  }

  async function removeExtraConfirmed(extra: string) {
    removingExtra = extra
    extrasActionError = ''
    startLogPolling()
    try {
      const data = await api.uninstallHeadroomExtras([extra])
      headroomExtras = {
        ...headroomExtras,
        version: data.version ?? headroomExtras.version,
        extras: (data.extras as { code: boolean; ml: boolean }) || headroomExtras.extras,
      }
    } catch (e: any) {
      extrasActionError = e.message || 'Remove failed'
    } finally {
      stopLogPolling()
      removingExtra = null
    }
  }

  function handleInstallExtras() {
    if (pendingExtras.length === 0) return
    if (pendingExtras.includes('ml')) {
      extrasConfirm = {
        title: 'Install [ml]',
        message: '[ml] downloads ~1 GB (torch + huggingface-hub). Continue?',
        confirmText: 'Install',
        variant: 'primary',
        onConfirm: installExtrasConfirmed,
      }
      return
    }
    installExtrasConfirmed()
  }

  function handleRemoveExtra(extra: string) {
    extrasConfirm = {
      title: `Remove [${extra}]`,
      message: `Remove [${extra}] and its packages?`,
      confirmText: 'Remove',
      variant: 'danger',
      onConfirm: () => removeExtraConfirmed(extra),
    }
  }

  async function toggleExtraActive(extra: string, value: boolean) {
    extrasActionError = ''
    if (extra === 'code') codeAware = value
    if (extra === 'ml') kompress = value
    const key = extra === 'code' ? 'headroomCodeAware' : 'headroomKompress'
    await patchSetting({ [key]: value })
    if (!headroomStatus.running) return
    restartingProxy = true
    try {
      const res = await api.restartHeadroom()
      if (res.error) throw new Error(res.error)
      await refreshHeadroomStatus()
    } catch (e: any) {
      extrasActionError = e.message || 'Restart failed'
    } finally {
      restartingProxy = false
    }
  }

  function handleToggleCaveman(value: boolean) {
    cavemanEnabled = value
    patchSetting({ cavemanEnabled: value })
  }

  function handleSelectCavemanLevel(levelId: string) {
    cavemanLevel = levelId
    patchSetting({ cavemanLevel: levelId })
    loadCavemanPreview()
  }

  function handleTogglePonytail(value: boolean) {
    ponytailEnabled = value
    patchSetting({ ponytailEnabled: value })
  }

  function handleSelectPonytailLevel(levelId: string) {
    ponytailLevel = levelId
    patchSetting({ ponytailLevel: levelId })
  }

  function handleToggleADHD(value: boolean) {
    adhdEnabled = value
    patchSetting({ adhdEnabled: value })
  }

  function handleSelectADHDLevel(levelId: string) {
    adhdLevel = levelId
    patchSetting({ adhdLevel: levelId })
  }

  function copyInstallCommand() {
    copyToClipboard('pip install "headroom-ai[proxy]"')
    copiedInstallCmd = true
    setTimeout(() => (copiedInstallCmd = false), 2000)
  }

  onMount(async () => {
    try {
      const savedLocale = localStorage.getItem('9router-locale') || localStorage.getItem('locale')
      if (savedLocale) locale = savedLocale
    } catch {}

    try {
      const data = await api.getSettings()
      if (data) {
        rtkEnabled = data.rtkEnabled !== false
        if (data.rtkMode === 'simple' || data.rtkMode === 'advance') rtkMode = data.rtkMode
        if (data.rtkIntensity === 'minimal' || data.rtkIntensity === 'standard' || data.rtkIntensity === 'aggressive') rtkIntensity = data.rtkIntensity
        if (typeof data.rtkMaxLines === 'number' && data.rtkMaxLines > 0) rtkMaxLines = data.rtkMaxLines
        if (typeof data.rtkMaxChars === 'number' && data.rtkMaxChars > 0) rtkMaxChars = data.rtkMaxChars
        if (typeof data.rtkDeduplicate === 'boolean') rtkDeduplicate = data.rtkDeduplicate
        if (data.rtkCategories && typeof data.rtkCategories === 'object') rtkCategories = { ...rtkCategories, ...data.rtkCategories }
        if (data.rtkFilters && typeof data.rtkFilters === 'object') rtkFilters = { ...data.rtkFilters }
        if (data.rtkRawRetention === 'never' || data.rtkRawRetention === 'failures' || data.rtkRawRetention === 'always') rtkRawRetention = data.rtkRawRetention

        headroomEnabled = !!data.headroomEnabled
        headroomUrl = data.headroomUrl || 'http://localhost:8787'
        if (typeof data.headroomTimeoutMs === 'number') headroomTimeoutMs = data.headroomTimeoutMs
        codeAware = data.headroomCodeAware === true
        kompress = data.headroomKompress !== false

        cavemanEnabled = !!data.cavemanEnabled
        if (data.cavemanMode === 'simple' || data.cavemanMode === 'advance') cavemanMode = data.cavemanMode
        cavemanLevel = data.cavemanLevel || 'full'
        if (typeof data.cavemanAutoClarity === 'boolean') cavemanAutoClarity = data.cavemanAutoClarity
        if (typeof data.cavemanLanguage === 'string' && data.cavemanLanguage) cavemanLanguage = data.cavemanLanguage
        if (typeof data.cavemanInputMode === 'boolean') cavemanInputMode = data.cavemanInputMode
        if (typeof data.cavemanPreserveKeywords === 'string') cavemanPreserveKeywords = data.cavemanPreserveKeywords

        ponytailEnabled = !!data.ponytailEnabled
        ponytailLevel = data.ponytailLevel || 'full'
        adhdEnabled = !!data.adhdEnabled
        adhdLevel = data.adhdLevel || 'full'
      }
    } catch {}

    try {
      rtkFiltersLoading = true
      const res = await api.getRTKFilters()
      if (res && res.filters) {
        rtkFiltersList = res.filters
      }
    } catch (e) {
      console.log('Error fetching RTK filters:', e)
    } finally {
      rtkFiltersLoading = false
    }

    loadCavemanPreview()
    refreshHeadroomStatus()
  })
</script>

<div class="space-y-6 p-6">
  <!-- Optimal Configuration & Synergy Guide -->
  <div class="rounded-xl border border-primary/25 bg-gradient-to-r from-primary/5 via-surface-2 to-surface-2 p-4 text-text-main shadow-sm">
    <div class="flex items-center justify-between gap-4">
      <div class="flex items-center gap-3">
        <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/15 text-primary">
          <span class="material-symbols-outlined text-xl">recommend</span>
        </span>
        <div>
          <h3 class="text-sm font-semibold flex items-center gap-2">
            Optimal Setup: The Developer Stack
            <span class="text-[10px] font-mono px-2 py-0.5 rounded-full bg-primary/10 text-primary border border-primary/20">
              Recommended Synergy
            </span>
          </h3>
          <p class="text-xs text-text-muted">
            Combine RTK + Caveman + ADHD for maximum token efficiency and crisp coding responses.
          </p>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-primary text-white hover:bg-primary/90 transition-colors cursor-pointer shadow-sm disabled:opacity-50"
          onclick={applyRecommendedPreset}
        >
          <span class="material-symbols-outlined text-sm">
            {recommendedApplied ? 'check' : 'auto_fix_high'}
          </span>
          {recommendedApplied ? 'Applied!' : 'Apply Recommended'}
        </button>
        <button
          type="button"
          class="inline-flex items-center justify-center h-8 w-8 rounded-lg text-text-muted hover:text-text-main hover:bg-surface-3 transition-colors cursor-pointer"
          onclick={() => showRecommendedGuide = !showRecommendedGuide}
          aria-label="Toggle details"
        >
          <span class="material-symbols-outlined text-lg transition-transform {showRecommendedGuide ? 'rotate-180' : ''}">
            expand_more
          </span>
        </button>
      </div>
    </div>

    {#if showRecommendedGuide}
      <div class="mt-4 pt-4 border-t border-border/60 text-xs text-text-muted space-y-3">
        <p class="text-text-secondary leading-relaxed">
          While you are free to customize and experiment with any combination, these modules are designed to work together at different stages of the request lifecycle:
        </p>

        <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
          <div class="p-3 rounded-lg bg-surface-1 border border-border/50">
            <div class="flex items-center gap-1.5 font-semibold text-text-main mb-1">
              <span class="material-symbols-outlined text-sm text-primary">terminal</span>
              1. RTK (Terminal Output)
            </div>
            <p class="text-[11px] leading-relaxed">
              Compresses raw bash logs, git diffs, package manager output, and compiler errors before they enter the prompt (saves 60–90% input tokens).
            </p>
          </div>

          <div class="p-3 rounded-lg bg-surface-1 border border-border/50">
            <div class="flex items-center gap-1.5 font-semibold text-text-main mb-1">
              <span class="material-symbols-outlined text-sm text-secondary">compress</span>
              2. Caveman (Linguistic Economy)
            </div>
            <p class="text-[11px] leading-relaxed">
              Instructs the model to discard polite filler, pleasantries, and redundant framing, reducing output token bloat. Includes Auto-Clarity bypass for safety.
            </p>
          </div>

          <div class="p-3 rounded-lg bg-surface-1 border border-border/50">
            <div class="flex items-center gap-1.5 font-semibold text-text-main mb-1">
              <span class="material-symbols-outlined text-sm text-success">format_list_numbered</span>
              3. ADHD (Action-First Format)
            </div>
            <p class="text-[11px] leading-relaxed">
              Enforces structured action steps, numbered workflows under 2 minutes, and zero conversational fluff. Perfect pair with Caveman for coding.
            </p>
          </div>
        </div>

        <div class="p-2.5 rounded-lg bg-surface-3/50 text-[11px] flex items-start gap-2">
          <span class="material-symbols-outlined text-sm text-warning shrink-0 mt-0.5">info</span>
          <div>
            <strong class="text-text-main">Cache-Friendly Tip:</strong> Keep <code class="px-1 py-0.5 rounded bg-surface-2 font-mono">Headroom</code> and dynamic input rewriting disabled when using modern LLMs (Claude 3.5+, Gemini 1.5/2.0) to preserve upstream 90% prompt caching discounts. Keep <code class="px-1 py-0.5 rounded bg-surface-2 font-mono">Ponytail</code> off for serious programming sessions.
          </div>
        </div>
      </div>
    {/if}
  </div>

  <Card id="rtk">
    <div class="flex items-center justify-between mb-2">
      <h2 class="text-lg font-semibold flex items-center gap-2">
        <span class="material-symbols-outlined text-primary">bolt</span>
        Token Saver
      </h2>
    </div>

    <!-- 1. Compress tool output (RTK) -->
    <div class="flex items-center justify-between pt-2 pb-4 border-b border-border gap-4 flex-wrap">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2 flex-wrap">
          <p class="font-medium">
            Compress tool output{' '}
            <a
              href="https://github.com/rtk-ai/rtk"
              target="_blank"
              rel="noreferrer"
              class="text-xs font-normal text-primary underline hover:opacity-80"
            >
              (RTK)
            </a>
          </p>
          <span class="text-xs px-2 py-0.5 rounded font-medium {rtkEnabled ? 'bg-success/15 text-success' : 'bg-surface-3 text-text-muted'}">
            {rtkEnabled ? 'Active' : 'Disabled'}
          </span>
        </div>
        <p class="text-sm text-text-muted">
          git/grep/ls/tree/logs → 60-90% fewer input tokens ({rtkFiltersList.length || 55} OmniRoute filters)
        </p>
      </div>
      <div class="flex items-center gap-2 shrink-0">
        {#if rtkEnabled}
          <button
            type="button"
            onclick={() => (rtkExpanded = !rtkExpanded)}
            class="px-2.5 py-1 rounded text-xs font-medium border border-border bg-surface-1 hover:bg-surface-2 text-text-main flex items-center gap-1 cursor-pointer transition-colors"
          >
            <span>{rtkExpanded ? 'Hide Settings' : 'Configure'}</span>
            <span class="material-symbols-outlined text-sm transition-transform {rtkExpanded ? 'rotate-180' : ''}">
              expand_more
            </span>
          </button>
        {/if}
        <Toggle
          checked={rtkEnabled}
          onChange={() => handleToggleRTK(!rtkEnabled)}
        />
      </div>
    </div>

    {#if rtkEnabled && rtkExpanded}
      <div class="py-4 border-b border-border space-y-4">
        <!-- Sub-tabs: Simple | Advance -->
        <div class="flex items-center justify-between flex-wrap gap-2">
          <div class="inline-flex p-1 rounded-lg bg-surface-2 border border-border">
            <button
              type="button"
              onclick={() => handleSetRtkMode('simple')}
              class="px-3 py-1 rounded text-xs font-medium transition-colors cursor-pointer {rtkMode === 'simple'
                ? 'bg-primary text-white shadow-sm'
                : 'text-text-muted hover:text-text-main'}"
            >
              Simple
            </button>
            <button
              type="button"
              onclick={() => handleSetRtkMode('advance')}
              class="px-3 py-1 rounded text-xs font-medium transition-colors cursor-pointer {rtkMode === 'advance'
                ? 'bg-primary text-white shadow-sm'
                : 'text-text-muted hover:text-text-main'}"
            >
              Advance
            </button>
          </div>
          <span class="text-xs text-text-muted font-mono">mode: {rtkMode}</span>
        </div>

        {#if rtkMode === 'simple'}
          <!-- Simple Tab -->
          <div class="space-y-4 rounded-lg bg-surface-2/40 p-4 border border-border">
            <!-- Intensity pills -->
            <div>
              <p class="text-xs font-medium text-text-muted mb-2">Compression Intensity</p>
              <div class="flex items-center gap-2 flex-wrap">
                {#each [
                  { id: 'minimal', label: 'Minimal', desc: 'Preserve maximal context (2x lines & chars)' },
                  { id: 'standard', label: 'Standard', desc: 'Balanced token savings & context' },
                  { id: 'aggressive', label: 'Aggressive', desc: 'Maximum compression (50% lines & chars)' }
                ] as item}
                  <button
                    type="button"
                    onclick={() => handleSelectRtkIntensity(item.id as any)}
                    class="px-3 py-1.5 rounded-lg text-xs font-medium border transition-colors cursor-pointer {rtkIntensity === item.id
                      ? 'bg-primary text-white border-primary shadow-sm'
                      : 'bg-surface-2 border-border text-text-muted hover:bg-surface-3 hover:text-text-main'}"
                    title={item.desc}
                  >
                    {item.label}
                  </button>
                {/each}
              </div>
            </div>

            <!-- Quick sliders/inputs -->
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div class="flex flex-col gap-1.5">
                <label for="rtk-max-lines" class="text-xs font-medium text-text-muted flex justify-between">
                  <span>Max Lines per Tool Result</span>
                  <span class="font-mono text-primary">{rtkMaxLines} lines</span>
                </label>
                <input
                  id="rtk-max-lines"
                  type="range"
                  min="20"
                  max="500"
                  step="10"
                  bind:value={rtkMaxLines}
                  onchange={handleRtkLimitsChange}
                  class="accent-primary cursor-pointer w-full"
                />
              </div>

              <div class="flex flex-col gap-1.5">
                <label for="rtk-max-chars" class="text-xs font-medium text-text-muted flex justify-between">
                  <span>Max Characters</span>
                  <span class="font-mono text-primary">{rtkMaxChars} chars</span>
                </label>
                <input
                  id="rtk-max-chars"
                  type="range"
                  min="1000"
                  max="30000"
                  step="1000"
                  bind:value={rtkMaxChars}
                  onchange={handleRtkLimitsChange}
                  class="accent-primary cursor-pointer w-full"
                />
              </div>
            </div>

            <!-- Deduplication toggle -->
            <div class="flex items-center justify-between pt-2 border-t border-border/60">
              <div>
                <p class="text-xs font-medium text-text-main">Consecutive Line Deduplication</p>
                <p class="text-[11px] text-text-muted">Collapse repetitive log entries into "... (repeated N times)"</p>
              </div>
              <Toggle
                checked={rtkDeduplicate}
                onChange={() => handleToggleRtkDeduplicate(!rtkDeduplicate)}
              />
            </div>

            <!-- Enabled category badges -->
            <div class="pt-2 border-t border-border/60">
              <p class="text-xs font-medium text-text-muted mb-2">Active Category Filters</p>
              <div class="flex items-center gap-2 flex-wrap">
                {#each [
                  { id: 'git', label: 'Git (diff/log/status)' },
                  { id: 'build', label: 'Build (tsc/eslint/vite/go)' },
                  { id: 'test', label: 'Test Runners (jest/vitest/pytest)' },
                  { id: 'package', label: 'Package Managers (npm/pnpm/pip)' },
                  { id: 'docker', label: 'Docker / System (containers/ls/ps)' }
                ] as cat}
                  <span class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[11px] font-medium bg-primary/10 text-primary border border-primary/20">
                    <span class="material-symbols-outlined text-[13px]">check_circle</span>
                    {cat.label}
                  </span>
                {/each}
              </div>
            </div>
          </div>
        {:else}
          <!-- Advance Tab -->
          <div class="space-y-4 rounded-lg bg-surface-2/40 p-4 border border-border">
            <!-- Granular filter toggles by category -->
            <div class="space-y-3">
              <div class="flex items-center justify-between">
                <div>
                  <p class="text-xs font-medium text-text-main">Granular Filter Controls (55 Filters)</p>
                  <p class="text-[11px] text-text-muted">Configure active OmniRoute rules per category or toggle individual tool filters</p>
                </div>
                {#if rtkFiltersLoading}
                  <span class="text-xs text-text-muted flex items-center gap-1">
                    <span class="material-symbols-outlined text-xs animate-spin">refresh</span>
                    Loading filters...
                  </span>
                {/if}
              </div>

              {#each [
                { key: 'git', label: 'Git Commands', desc: 'git diff, status, log, branch' },
                { key: 'build', label: 'Compilers & Build', desc: 'tsc, eslint, vite, webpack, go, gradle, make, nx, turbo, ruff, biome' },
                { key: 'test', label: 'Test Runners', desc: 'jest, vitest, pytest, go test, cargo test, playwright' },
                { key: 'package', label: 'Package Managers', desc: 'npm, pnpm, pip, poetry, uv, bundle, composer' },
                { key: 'docker', label: 'Cloud & Containers', desc: 'docker, kubectl, aws, gcloud, gh, terraform, tofu, rsync, curl, wget' },
                { key: 'system', label: 'System Tools', desc: 'ls, ps, df, du, find, grep, stacktraces, json, generic' }
              ] as group}
                {@const catFilters = rtkFiltersList.filter(f => f.category === group.key)}
                {@const isCatOn = rtkCategories[group.key] !== false}
                {@const isExpanded = !!expandedFilterCategories[group.key]}

                <div class="rounded-lg border border-border bg-surface-1 overflow-hidden">
                  <!-- Category Header row -->
                  <div class="flex items-center justify-between p-2.5 bg-surface-2/40 hover:bg-surface-2 transition-colors">
                    <div class="flex items-center gap-2.5 min-w-0 flex-1">
                      <button
                        type="button"
                        onclick={() => toggleFilterCategoryExpand(group.key)}
                        class="p-0.5 rounded text-text-muted hover:text-text-main cursor-pointer"
                        title={isExpanded ? 'Collapse' : 'Expand'}
                      >
                        <span class="material-symbols-outlined text-base transition-transform {isExpanded ? 'rotate-90' : ''}">
                          chevron_right
                        </span>
                      </button>
                      <button type="button" class="min-w-0 flex-1 text-left cursor-pointer bg-transparent border-0 p-0" onclick={() => toggleFilterCategoryExpand(group.key)}>
                        <div class="flex items-center gap-2">
                          <span class="text-xs font-semibold text-text-main">{group.label}</span>
                          <span class="text-[10px] px-1.5 py-0.2 rounded-full font-mono {isCatOn ? 'bg-primary/10 text-primary' : 'bg-surface-3 text-text-muted'}">
                            {catFilters.length} filters
                          </span>
                        </div>
                        <p class="text-[10px] text-text-muted truncate">{group.desc}</p>
                      </button>
                    </div>
                    <div class="flex items-center gap-2 shrink-0 ml-2">
                      <span class="text-[11px] font-medium {isCatOn ? 'text-primary' : 'text-text-muted'}">
                        {isCatOn ? 'Enabled' : 'Disabled'}
                      </span>
                      <Toggle
                        size="sm"
                        checked={isCatOn}
                        onChange={() => handleToggleRtkCategory(group.key)}
                      />
                    </div>
                  </div>

                  <!-- Expanded individual filters list -->
                  {#if isExpanded}
                    <div class="p-3 border-t border-border/60 bg-surface-1 grid grid-cols-1 md:grid-cols-2 gap-2">
                      {#each catFilters as f (f.id)}
                        {@const isFilterOn = rtkFilters[f.id] !== undefined ? rtkFilters[f.id] : isCatOn}
                        <div class="flex items-start justify-between p-2 rounded border border-border/50 bg-surface-2/30 hover:bg-surface-2/60 transition-colors gap-2">
                          <div class="min-w-0 flex-1">
                            <div class="flex items-center gap-1.5">
                              <span class="text-xs font-medium text-text-main">{f.label}</span>
                              <span class="text-[9px] font-mono text-text-muted">({f.id})</span>
                            </div>
                            <p class="text-[10px] text-text-muted leading-tight mt-0.5">{f.description}</p>
                          </div>
                          <Toggle
                            size="sm"
                            checked={isFilterOn}
                            onChange={() => handleToggleIndividualFilter(f.id, group.key)}
                          />
                        </div>
                      {/each}
                    </div>
                  {/if}
                </div>
              {/each}
            </div>

            <!-- Raw output retention selector -->
            <div class="flex items-center justify-between flex-wrap gap-2 pt-2 border-t border-border/60">
              <div>
                <p class="text-xs font-medium text-text-main">Raw Output Retention</p>
                <p class="text-[11px] text-text-muted">Retain uncompressed raw tool execution outputs for debugging</p>
              </div>
              <div class="flex items-center gap-1.5">
                {#each [
                  { id: 'never', label: 'Never' },
                  { id: 'failures', label: 'On Failures' },
                  { id: 'always', label: 'Always' }
                ] as ret}
                  <button
                    type="button"
                    onclick={() => handleSelectRtkRetention(ret.id as any)}
                    class="px-2.5 py-1 rounded text-xs font-medium border transition-colors cursor-pointer {rtkRawRetention === ret.id
                      ? 'bg-primary text-white border-primary'
                      : 'bg-surface-1 border-border text-text-muted hover:bg-surface-2'}"
                  >
                    {ret.label}
                  </button>
                {/each}
              </div>
            </div>

            <!-- Live Test Bench -->
            <div class="pt-3 border-t border-border space-y-2">
              <div class="flex items-center justify-between flex-wrap gap-2">
                <span class="text-xs font-semibold uppercase tracking-wider text-primary flex items-center gap-1.5">
                  <span class="material-symbols-outlined text-sm">science</span>
                  Live Test Bench
                </span>
                <div class="flex items-center gap-1.5">
                  <span class="text-[11px] text-text-muted mr-1">Sample Presets:</span>
                  <button
                    type="button"
                    onclick={() => handleSetRtkPreset('git')}
                    class="px-2 py-0.5 rounded text-[11px] bg-surface-1 border border-border hover:bg-surface-2 cursor-pointer"
                  >
                    git diff
                  </button>
                  <button
                    type="button"
                    onclick={() => handleSetRtkPreset('test')}
                    class="px-2 py-0.5 rounded text-[11px] bg-surface-1 border border-border hover:bg-surface-2 cursor-pointer"
                  >
                    npm test
                  </button>
                  <button
                    type="button"
                    onclick={() => handleSetRtkPreset('docker')}
                    class="px-2 py-0.5 rounded text-[11px] bg-surface-1 border border-border hover:bg-surface-2 cursor-pointer"
                  >
                    docker logs
                  </button>
                </div>
              </div>

              <textarea
                bind:value={rtkTestInput}
                rows={4}
                placeholder="Paste sample tool output here (e.g. git diff, npm test, docker logs)..."
                class="w-full px-3 py-2 text-xs font-mono rounded bg-surface-1 border border-border focus:outline-none focus:ring-1 focus:ring-primary text-text-main"
              ></textarea>

              <div class="flex justify-end gap-2">
                {#if rtkTestInput}
                  <button
                    type="button"
                    onclick={() => { rtkTestInput = ''; rtkTestResult = null; }}
                    class="px-3 py-1.5 rounded text-xs text-text-muted hover:text-text-main cursor-pointer"
                  >
                    Clear
                  </button>
                {/if}
                <button
                  type="button"
                  onclick={handleRunRtkTest}
                  disabled={rtkTestLoading || !rtkTestInput.trim()}
                  class="px-4 py-1.5 rounded-lg text-xs font-medium bg-primary text-white hover:opacity-90 disabled:opacity-50 transition-opacity cursor-pointer flex items-center gap-1.5 shadow-sm"
                >
                  {#if rtkTestLoading}
                    <span class="material-symbols-outlined text-sm animate-spin">refresh</span>
                    Compressing...
                  {:else}
                    <span class="material-symbols-outlined text-sm">compress</span>
                    Compress Output
                  {/if}
                </button>
              </div>

              {#if rtkTestResult}
                <div class="mt-3 p-3 rounded-lg bg-surface-1 border border-border space-y-3">
                  <!-- Metrics header -->
                  <div class="flex items-center justify-between flex-wrap gap-2 text-xs">
                    <div class="flex items-center gap-2">
                      <span class="px-2 py-0.5 rounded bg-primary/20 text-primary font-mono font-medium">
                        Category: {rtkTestResult.detectedCategory}
                      </span>
                      {#each rtkTestResult.techniquesUsed as tech}
                        <span class="px-2 py-0.5 rounded bg-black/10 dark:bg-white/10 text-[11px] font-mono text-text-muted">
                          {tech}
                        </span>
                      {/each}
                    </div>
                    <div class="flex items-center gap-2 font-mono text-xs">
                      <span class="text-text-muted">Orig: {rtkTestResult.originalTokens} tok</span>
                      <span>→</span>
                      <span class="text-success font-medium">Comp: {rtkTestResult.compressedTokens} tok</span>
                      <span class="px-2 py-0.5 rounded bg-success/20 text-success font-bold">
                        -{rtkTestResult.savedPct}% saved
                      </span>
                    </div>
                  </div>

                  <!-- Compressed Output Preview -->
                  <div class="relative">
                    <pre class="p-2.5 rounded bg-black/5 dark:bg-white/5 font-mono text-[11px] overflow-auto max-h-48 whitespace-pre-wrap">{rtkTestResult.text}</pre>
                    <button
                      type="button"
                      onclick={() => copyToClipboard(rtkTestResult?.text || '')}
                      class="absolute top-2 right-2 px-2 py-1 rounded bg-surface-2 border border-border text-[10px] text-text-muted hover:text-text-main cursor-pointer"
                    >
                      Copy
                    </button>
                  </div>
                </div>
              {/if}
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- 2. Compress context (Headroom) -->
    <div class="flex items-center justify-between py-4 border-t border-border gap-4 flex-wrap">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-3 flex-wrap">
          <p class="font-medium">
            Compress context{' '}
            <a
              href="https://github.com/chopratejas/headroom"
              target="_blank"
              rel="noreferrer"
              class="text-xs font-normal text-primary underline hover:opacity-80"
            >
              (Headroom)
            </a>
          </p>
          <span
            class="text-xs px-2 py-0.5 rounded {headroomRunning ? 'bg-success/15 text-success' : 'bg-warning/15 text-warning'}"
          >
            {headroomStatusLabel}
          </span>
          <button
            type="button"
            onclick={() => (isHeadroomModalOpen = true)}
            class="text-xs text-primary underline hover:opacity-80 cursor-pointer"
          >
            {headroomRunning ? 'Manage' : 'Setup'}
          </button>
        </div>
        <p class="text-sm text-text-muted mt-1">
          Compress prompts via /v1/compress before routing to the model
        </p>
      </div>
      <div class="flex items-center gap-2 shrink-0">
        {#if headroomStatus.installed}
          <button
            type="button"
            onclick={() => (headroomExpanded = !headroomExpanded)}
            class="px-2.5 py-1 rounded text-xs font-medium border border-border bg-surface-1 hover:bg-surface-2 text-text-main flex items-center gap-1 cursor-pointer transition-colors"
          >
            <span>{headroomExpanded ? 'Hide Settings' : 'Configure'}</span>
            <span class="material-symbols-outlined text-sm transition-transform {headroomExpanded ? 'rotate-180' : ''}">
              expand_more
            </span>
          </button>
        {/if}
        <Toggle
          checked={headroomEnabled}
          onChange={() => handleToggleHeadroom(!headroomEnabled)}
        />
      </div>
    </div>

    {#if headroomStatus.installed && headroomExpanded}
      <div class="mb-3 ml-1 pl-3 pb-4 border-l-2 border-border">
        <div class="flex items-center gap-2 flex-wrap">
          <span class="text-xs text-text-muted">
            Compression extras{headroomExtras.version ? ` · v${headroomExtras.version}` : ''}:
          </span>
          {#each headroomExtras.available as extra (extra)}
            {@const installed = !!headroomExtras.extras[extra as 'code' | 'ml']}
            {@const pending = pendingExtras.includes(extra)}
            {@const extraTitle =
              extra === 'code'
                ? 'tree-sitter AST compression for code responses'
                : 'Kompress-v2 HF model for prose/agentic traces (~+1GB)'}

            {#if installed}
              {@const active = extra === 'code' ? codeAware : kompress}
              <div
                class="flex items-center gap-1.5 text-xs px-2 py-1 rounded border border-success/40 bg-success/5 text-text"
                title={extraTitle}
              >
                <Toggle
                  size="sm"
                  checked={active}
                  disabled={restartingProxy}
                  onChange={() => toggleExtraActive(extra, !active)}
                />
                <span class="font-medium">[{extra}]</span>
                <button
                  type="button"
                  onclick={() => handleRemoveExtra(extra)}
                  disabled={removingExtra === extra}
                  class="ml-1 text-red-600 dark:text-red-400 underline hover:opacity-80 disabled:opacity-50 cursor-pointer"
                  title={`Uninstall [${extra}]`}
                >
                  {removingExtra === extra ? 'Uninstalling…' : 'Uninstall'}
                </button>
              </div>
            {:else}
              <label
                class="flex items-center gap-1.5 text-xs px-2 py-1 rounded border cursor-pointer transition-colors {pending
                  ? 'border-primary bg-primary/10 text-primary'
                  : 'border-border text-text-muted hover:bg-surface-2'}"
                title={extraTitle}
              >
                <input
                  type="checkbox"
                  class="w-3 h-3 cursor-pointer"
                  checked={pending}
                  onchange={() => togglePendingExtra(extra)}
                />
                <span class="font-medium">[{extra}]</span>
              </label>
            {/if}
          {/each}

          {#if pendingExtras.length > 0}
            <button
              type="button"
              onclick={handleInstallExtras}
              disabled={extrasActionLoading}
              class="px-2.5 py-1 rounded text-xs font-medium bg-primary text-white hover:bg-primary/90 disabled:opacity-50 transition-colors cursor-pointer"
            >
              {extrasActionLoading ? 'Installing…' : `Install (${pendingExtras.length})`}
            </button>
          {/if}
        </div>

        {#if extrasActionError}
          <p class="text-xs text-red-600 dark:text-red-400 mt-2">{extrasActionError}</p>
        {/if}

        {#if installLog}
          <pre
            class="mt-2 text-[10px] font-mono p-2 rounded bg-black/5 dark:bg-white/5 max-h-32 overflow-auto whitespace-pre-wrap"
          >{installLog}</pre>
        {/if}

        <p class="text-xs text-text-muted mt-1">
          Adding <code>[code]</code> enables tree-sitter AST compression (Python/JS/TS/Go/Rust/Java/C/C++/Perl). Adding{' '}
          <code>[ml]</code> adds ~1 GB (torch + huggingface-hub).
        </p>
      </div>
    {/if}

    <!-- 3. Compress LLM output (Caveman) -->
    <div class="flex items-center justify-between pt-4 border-t border-border gap-4 flex-wrap">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2 flex-wrap">
          <p class="font-medium">
            Compress LLM output{' '}
            <a
              href="https://github.com/JuliusBrussee/caveman"
              target="_blank"
              rel="noreferrer"
              class="text-xs font-normal text-primary underline hover:opacity-80"
            >
              (Caveman)
            </a>
          </p>
          <span class="text-xs px-2 py-0.5 rounded font-medium {cavemanEnabled ? 'bg-success/15 text-success' : 'bg-surface-3 text-text-muted'}">
            {cavemanEnabled ? `Active (${cavemanLevel})` : 'Disabled'}
          </span>
        </div>
        <p class="text-sm text-text-muted">
          Terse-style system prompt → ~65% fewer output tokens (up to 87%)
        </p>
      </div>
      <div class="flex items-center gap-2 shrink-0">
        {#if cavemanEnabled}
          <button
            type="button"
            onclick={() => (cavemanExpanded = !cavemanExpanded)}
            class="px-2.5 py-1 rounded text-xs font-medium border border-border bg-surface-1 hover:bg-surface-2 text-text-main flex items-center gap-1 cursor-pointer transition-colors"
          >
            <span>{cavemanExpanded ? 'Hide Settings' : 'Configure'}</span>
            <span class="material-symbols-outlined text-sm transition-transform {cavemanExpanded ? 'rotate-180' : ''}">
              expand_more
            </span>
          </button>
        {/if}
        <Toggle
          checked={cavemanEnabled}
          onChange={() => handleToggleCaveman(!cavemanEnabled)}
        />
      </div>
    </div>

    {#if cavemanEnabled && cavemanExpanded}
      <div class="pt-2 pb-4 border-b border-border space-y-4">
        <!-- Sub-tabs: Simple | Advance -->
        <div class="flex items-center justify-between flex-wrap gap-2">
          <div class="inline-flex p-1 rounded-lg bg-surface-2 border border-border">
            <button
              type="button"
              onclick={() => handleSetCavemanMode('simple')}
              class="px-3 py-1 rounded text-xs font-medium transition-colors cursor-pointer {cavemanMode === 'simple'
                ? 'bg-primary text-white shadow-sm'
                : 'text-text-muted hover:text-text-main'}"
            >
              Simple
            </button>
            <button
              type="button"
              onclick={() => handleSetCavemanMode('advance')}
              class="px-3 py-1 rounded text-xs font-medium transition-colors cursor-pointer {cavemanMode === 'advance'
                ? 'bg-primary text-white shadow-sm'
                : 'text-text-muted hover:text-text-main'}"
            >
              Advance
            </button>
          </div>
          <span class="text-xs text-text-muted font-mono">mode: {cavemanMode}</span>
        </div>

        {#if cavemanMode === 'simple'}
          <!-- Simple Tab -->
          <div class="space-y-4 rounded-lg bg-surface-2/40 p-4 border border-border">
            <!-- Intensity pills & Language -->
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div>
                <p class="text-xs font-medium text-text-muted mb-2">Terseness Intensity</p>
                <div class="flex items-center gap-1.5 flex-wrap">
                  {#each visibleCavemanLevels as lvl (lvl.id)}
                    <button
                      type="button"
                      onclick={() => handleSelectCavemanLevel(lvl.id)}
                      class="px-3 py-1.5 rounded-lg text-xs font-medium border transition-colors cursor-pointer {cavemanLevel === lvl.id
                        ? 'bg-primary text-white border-primary shadow-sm'
                        : 'bg-surface-2 border-border text-text-muted hover:bg-surface-3 hover:text-text-main'}"
                      title={lvl.desc}
                    >
                      {lvl.label}
                    </button>
                  {/each}
                </div>
                <p class="text-[11px] text-primary mt-1.5">
                  {CAVEMAN_LEVELS.find((lvl) => lvl.id === cavemanLevel)?.desc || ''}
                </p>
              </div>

              <div>
                <p class="text-xs font-medium text-text-muted mb-2">System Prompt Language</p>
                <div class="flex items-center gap-2">
                  {#each [
                    { id: 'en', label: 'English' },
                    { id: 'id', label: 'Bahasa Indonesia' }
                  ] as lang}
                    <button
                      type="button"
                      onclick={() => handleSelectCavemanLanguage(lang.id)}
                      class="px-3 py-1.5 rounded-lg text-xs font-medium border transition-colors cursor-pointer {cavemanLanguage === lang.id
                        ? 'bg-primary text-white border-primary shadow-sm'
                        : 'bg-surface-2 border-border text-text-muted hover:bg-surface-3 hover:text-text-main'}"
                    >
                      {lang.label}
                    </button>
                  {/each}
                </div>
              </div>
            </div>

            <!-- Auto-Clarity Bypass toggle -->
            <div class="flex items-center justify-between pt-2 border-t border-border/60 gap-4">
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <p class="text-xs font-medium text-text-main">Auto-Clarity Bypass</p>
                  <span class="px-1.5 py-0.5 rounded text-[10px] font-mono bg-warning/15 text-warning">
                    Security & Detail Guard
                  </span>
                </div>
                <p class="text-[11px] text-text-muted mt-0.5">
                  Bypasses terse compression on destructive commands (`rm -rf`, `DROP TABLE`), CVE/security alerts, or when explicit detailed explanations are asked.
                </p>
              </div>
              <Toggle
                checked={cavemanAutoClarity}
                onChange={() => handleToggleCavemanAutoClarity(!cavemanAutoClarity)}
              />
            </div>

            <!-- Live Prompt Preview -->
            <div class="pt-2 border-t border-border/60 space-y-1.5">
              <div class="flex items-center justify-between">
                <span class="text-xs font-semibold text-text-muted flex items-center gap-1.5">
                  <span class="material-symbols-outlined text-sm">visibility</span>
                  Injected System Prompt Preview
                </span>
                {#if cavemanPromptPreview}
                  <button
                    type="button"
                    onclick={() => copyToClipboard(cavemanPromptPreview)}
                    class="px-2 py-0.5 rounded bg-surface-1 border border-border text-[10px] text-text-muted hover:text-text-main cursor-pointer"
                  >
                    Copy Prompt
                  </button>
                {/if}
              </div>
              {#if cavemanPreviewLoading}
                <div class="p-3 text-xs text-text-muted font-mono bg-surface-1 rounded border border-border animate-pulse">
                  Loading system prompt...
                </div>
              {:else}
                <pre class="p-2.5 rounded bg-black/5 dark:bg-white/5 font-mono text-[11px] overflow-auto max-h-36 whitespace-pre-wrap text-text-muted">{cavemanPromptPreview || 'Prompt preview unavailable'}</pre>
              {/if}
            </div>
          </div>
        {:else}
          <!-- Advance Tab -->
          <div class="space-y-4 rounded-lg bg-surface-2/40 p-4 border border-border">
            <!-- Input Message Pre-compression toggle -->
            <div class="flex items-center justify-between gap-4">
              <div class="min-w-0 flex-1">
                <p class="text-xs font-medium text-text-main">Input Message Pre-compression (User Prompts)</p>
                <p class="text-[11px] text-text-muted">
                  Strips greetings, conversational filler, and polite hedges ("Hello", "Please help me to", "Thank you") from incoming user prompts.
                </p>
              </div>
              <Toggle
                checked={cavemanInputMode}
                onChange={() => handleToggleCavemanInputMode(!cavemanInputMode)}
              />
            </div>

            <!-- Custom Protected Keywords input -->
            <div class="flex flex-col gap-1.5 pt-2 border-t border-border/60">
              <label for="caveman-keywords" class="text-xs font-medium text-text-muted">
                Custom Protected Keywords (never stripped)
              </label>
              <input
                id="caveman-keywords"
                type="text"
                bind:value={cavemanPreserveKeywords}
                onblur={handleCavemanPreserveKeywordsBlur}
                placeholder="e.g. please, thank you, exact"
                class="w-full px-3 py-1.5 rounded-lg bg-surface-1 border border-border text-xs focus:outline-none focus:ring-1 focus:ring-primary text-text-main font-mono"
              />
              <p class="text-[10px] text-text-muted">
                Comma-separated words that will be preserved intact if found in user prompts.
              </p>
            </div>

            <!-- Live Prompt Compressor Bench -->
            <div class="pt-3 border-t border-border space-y-2">
              <div class="flex items-center justify-between flex-wrap gap-2">
                <span class="text-xs font-semibold uppercase tracking-wider text-primary flex items-center gap-1.5">
                  <span class="material-symbols-outlined text-sm">compress</span>
                  Live Prompt Compressor Bench
                </span>
                <div class="flex items-center gap-1.5">
                  <span class="text-[11px] text-text-muted mr-1">Sample Presets:</span>
                  <button
                    type="button"
                    onclick={() => handleSetCavemanPreset('polite')}
                    class="px-2 py-0.5 rounded text-[11px] bg-surface-1 border border-border hover:bg-surface-2 cursor-pointer"
                  >
                    Polite Query
                  </button>
                  <button
                    type="button"
                    onclick={() => handleSetCavemanPreset('troubleshoot')}
                    class="px-2 py-0.5 rounded text-[11px] bg-surface-1 border border-border hover:bg-surface-2 cursor-pointer"
                  >
                    Troubleshooting
                  </button>
                  <button
                    type="button"
                    onclick={() => handleSetCavemanPreset('indonesia')}
                    class="px-2 py-0.5 rounded text-[11px] bg-surface-1 border border-border hover:bg-surface-2 cursor-pointer"
                  >
                    Bahasa Indonesia
                  </button>
                </div>
              </div>

              <textarea
                bind:value={cavemanTestInput}
                rows={3}
                placeholder="Type user message here to test input pre-compression (e.g. 'Hello, please help me to...')"
                class="w-full px-3 py-2 text-xs font-mono rounded bg-surface-1 border border-border focus:outline-none focus:ring-1 focus:ring-primary text-text-main"
              ></textarea>

              <div class="flex justify-end gap-2">
                {#if cavemanTestInput}
                  <button
                    type="button"
                    onclick={() => { cavemanTestInput = ''; cavemanTestResult = null; }}
                    class="px-3 py-1.5 rounded text-xs text-text-muted hover:text-text-main cursor-pointer"
                  >
                    Clear
                  </button>
                {/if}
                <button
                  type="button"
                  onclick={handleRunCavemanTest}
                  disabled={cavemanTestLoading || !cavemanTestInput.trim()}
                  class="px-4 py-1.5 rounded-lg text-xs font-medium bg-primary text-white hover:opacity-90 disabled:opacity-50 transition-opacity cursor-pointer flex items-center gap-1.5 shadow-sm"
                >
                  {#if cavemanTestLoading}
                    <span class="material-symbols-outlined text-sm animate-spin">refresh</span>
                    Compressing...
                  {:else}
                    <span class="material-symbols-outlined text-sm">bolt</span>
                    Compress Prompt
                  {/if}
                </button>
              </div>

              {#if cavemanTestResult}
                <div class="mt-3 p-3 rounded-lg bg-surface-1 border border-border space-y-3">
                  <!-- Metrics header -->
                  <div class="flex items-center justify-between flex-wrap gap-2 text-xs">
                    <span class="text-xs font-medium text-text-muted">Compressed Result:</span>
                    <div class="flex items-center gap-2 font-mono text-xs">
                      <span class="text-text-muted">Orig: {cavemanTestResult.originalTokens} tok</span>
                      <span>→</span>
                      <span class="text-success font-medium">Comp: {cavemanTestResult.compressedTokens} tok</span>
                      {#if cavemanTestResult.savedPct > 0}
                        <span class="px-2 py-0.5 rounded bg-success/20 text-success font-bold">
                          -{cavemanTestResult.savedPct}% saved
                        </span>
                      {/if}
                    </div>
                  </div>

                  <!-- Compressed Text -->
                  <div class="relative">
                    <pre class="p-2.5 rounded bg-black/5 dark:bg-white/5 font-mono text-[11px] overflow-auto max-h-36 whitespace-pre-wrap">{cavemanTestResult.text}</pre>
                    <button
                      type="button"
                      onclick={() => copyToClipboard(cavemanTestResult?.text || '')}
                      class="absolute top-2 right-2 px-2 py-1 rounded bg-surface-2 border border-border text-[10px] text-text-muted hover:text-text-main cursor-pointer"
                    >
                      Copy
                    </button>
                  </div>
                </div>
              {/if}
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- 4. Lazy senior dev (Ponytail) -->
    <div class="flex items-center justify-between pt-4 mt-4 border-t border-border gap-4 flex-wrap">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2 flex-wrap">
          <p class="font-medium">
            Lazy senior dev{' '}
            <a
              href="https://github.com/DietrichGebert/ponytail"
              target="_blank"
              rel="noreferrer"
              class="text-xs font-normal text-primary underline hover:opacity-80"
            >
              (Ponytail)
            </a>
          </p>
          <span class="text-xs px-2 py-0.5 rounded font-medium {ponytailEnabled ? `bg-success/15 text-success` : 'bg-surface-3 text-text-muted'}">
            {ponytailEnabled ? `Active (${ponytailLevel})` : 'Disabled'}
          </span>
        </div>
        <p class="text-sm text-text-muted">
          Bias the model toward minimal code: YAGNI, reuse stdlib, deletion over addition
        </p>
      </div>
      <div class="flex items-center gap-2 shrink-0">
        {#if ponytailEnabled}
          <button
            type="button"
            onclick={() => (ponytailExpanded = !ponytailExpanded)}
            class="px-2.5 py-1 rounded text-xs font-medium border border-border bg-surface-1 hover:bg-surface-2 text-text-main flex items-center gap-1 cursor-pointer transition-colors"
          >
            <span>{ponytailExpanded ? 'Hide Settings' : 'Configure'}</span>
            <span class="material-symbols-outlined text-sm transition-transform {ponytailExpanded ? 'rotate-180' : ''}">
              expand_more
            </span>
          </button>
        {/if}
        <Toggle
          checked={ponytailEnabled}
          onChange={() => handleTogglePonytail(!ponytailEnabled)}
        />
      </div>
    </div>

    {#if ponytailEnabled && ponytailExpanded}
      <div class="py-3 px-4 rounded-lg bg-surface-2/40 border border-border mt-3 space-y-2">
        <div class="flex items-center justify-between flex-wrap gap-2">
          <span class="text-xs font-medium text-text-muted">Ponytail Strategy:</span>
          <div class="flex items-center gap-1.5 flex-wrap">
            {#each PONYTAIL_LEVELS as lvl (lvl.id)}
              <button
                type="button"
                onclick={() => handleSelectPonytailLevel(lvl.id)}
                class="px-3 py-1.5 rounded text-xs font-medium border transition-colors cursor-pointer {ponytailLevel === lvl.id
                  ? 'bg-primary text-white border-primary shadow-sm'
                  : 'bg-surface-2 border-border text-text-muted hover:bg-surface-3'}"
                title={lvl.desc}
              >
                {lvl.label}
              </button>
            {/each}
          </div>
        </div>
        <p class="text-xs text-primary">
          {PONYTAIL_LEVELS.find((lvl) => lvl.id === ponytailLevel)?.desc || ''}
        </p>
      </div>
    {/if}

    <!-- 5. Action-first output (I have ADHD) -->
    <div class="flex items-center justify-between pt-4 mt-4 border-t border-border gap-4 flex-wrap">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2 flex-wrap">
          <p class="font-medium">
            I have ADHD{' '}
            <a
              href="https://github.com/ayghri/i-have-adhd"
              target="_blank"
              rel="noreferrer"
              class="text-xs font-normal text-primary underline hover:opacity-80"
            >
              (Action-First)
            </a>
          </p>
          <span class="text-xs px-2 py-0.5 rounded font-medium {adhdEnabled ? `bg-success/15 text-success` : 'bg-surface-3 text-text-muted'}">
            {adhdEnabled ? `Active (${adhdLevel})` : 'Disabled'}
          </span>
        </div>
        <p class="text-sm text-text-muted">
          Action-first output style: lead with command/code, numbered bounded steps, one concrete next step under 2 mins, no fluff/preamble.
        </p>
      </div>
      <div class="flex items-center gap-2 shrink-0">
        {#if adhdEnabled}
          <button
            type="button"
            onclick={() => (adhdExpanded = !adhdExpanded)}
            class="px-2.5 py-1 rounded text-xs font-medium border border-border bg-surface-1 hover:bg-surface-2 text-text-main flex items-center gap-1 cursor-pointer transition-colors"
          >
            <span>{adhdExpanded ? 'Hide Settings' : 'Configure'}</span>
            <span class="material-symbols-outlined text-sm transition-transform {adhdExpanded ? 'rotate-180' : ''}">
              expand_more
            </span>
          </button>
        {/if}
        <Toggle
          checked={adhdEnabled}
          onChange={() => handleToggleADHD(!adhdEnabled)}
        />
      </div>
    </div>

    {#if adhdEnabled && adhdExpanded}
      <div class="py-3 px-4 rounded-lg bg-surface-2/40 border border-border mt-3 space-y-2">
        <div class="flex items-center justify-between flex-wrap gap-2">
          <span class="text-xs font-medium text-text-muted">ADHD Style Intensity:</span>
          <div class="flex items-center gap-1.5 flex-wrap">
            {#each ADHD_LEVELS as lvl (lvl.id)}
              <button
                type="button"
                onclick={() => handleSelectADHDLevel(lvl.id)}
                class="px-3 py-1.5 rounded text-xs font-medium border transition-colors cursor-pointer {adhdLevel === lvl.id
                  ? 'bg-primary text-white border-primary shadow-sm'
                  : 'bg-surface-2 border-border text-text-muted hover:bg-surface-3'}"
                title={lvl.desc}
              >
                {lvl.label}
              </button>
            {/each}
          </div>
        </div>
        <p class="text-xs text-primary">
          {ADHD_LEVELS.find((lvl) => lvl.id === adhdLevel)?.desc || ''}
        </p>
      </div>
    {/if}

    <!-- 6. Prompt & Response Cache -->
    <div class="flex items-center justify-between pt-4 mt-4 border-t border-border gap-4 flex-wrap">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2 flex-wrap">
          <p class="font-medium">
            Prompt / Response Cache
          </p>
          <span class="text-xs px-2 py-0.5 rounded bg-primary/10 text-primary font-medium">
            100% token savings
          </span>
        </div>
        <p class="text-xs text-text-muted mt-1">
          Exact-match prompt & response cache. Skips upstream calls completely on cache hit, eliminating 100% latency and tokens.
        </p>
      </div>

      <div class="flex items-center gap-2 shrink-0">
        <Toggle
          checked={semanticCacheEnabled}
          onChange={() => {
            semanticCacheEnabled = !semanticCacheEnabled
            patchSetting({ semanticCacheEnabled })
          }}
        />
      </div>
    </div>

    {#if semanticCacheEnabled}
      <div class="mt-3 p-3 rounded-lg bg-surface-2 border border-border/50 space-y-3">
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
          <div>
            <label for="cache-ttl" class="text-text-muted block mb-1">Cache TTL (minutes)</label>
            <input
              id="cache-ttl"
              type="number"
              bind:value={semanticCacheTTL}
              onchange={() => patchSetting({ semanticCacheTTL: parseInt(semanticCacheTTL, 10) || 1440 })}
              placeholder="1440"
              class="w-full bg-surface-2 border border-border rounded px-3 py-1.5 text-xs text-text-main focus:outline-none focus:border-primary"
            />
          </div>
          <div>
            <label for="cache-max-entries" class="text-text-muted block mb-1">Max Entries (Capacity)</label>
            <input
              id="cache-max-entries"
              type="number"
              bind:value={semanticCacheMaxEntries}
              onchange={() => patchSetting({ semanticCacheMaxEntries: parseInt(semanticCacheMaxEntries, 10) || 1000 })}
              placeholder="1000"
              class="w-full bg-surface-2 border border-border rounded px-3 py-1.5 text-xs text-text-main focus:outline-none focus:border-primary"
            />
          </div>
        </div>
      </div>
    {/if}
  </Card>
</div>

<!-- MODAL: Headroom Setup & Configuration -->
<Modal
  isOpen={isHeadroomModalOpen}
  title={headroomRunning ? 'Headroom' : 'Setup Headroom'}
  onClose={() => (isHeadroomModalOpen = false)}
>
  <div class="flex flex-col gap-4">
    <div class="flex items-center justify-between text-sm">
      <span>Status</span>
      <span class={headroomRunning ? 'text-success font-medium' : 'text-warning font-medium'}>
        {headroomStatusLabel}
      </span>
    </div>

    {#if headroomRunning}
      <a
        href="/api/headroom/proxy/dashboard"
        target="_blank"
        rel="noreferrer"
        class="w-full rounded border border-border px-4 py-2 text-center text-sm hover:bg-surface-2 transition-colors text-text-main block font-medium"
      >
        Open Headroom Dashboard
      </a>
    {/if}

    <div class="flex flex-col gap-1">
      <p class="text-sm font-medium">Proxy URL</p>
      <input
        type="text"
        bind:value={headroomUrl}
        onblur={handleHeadroomUrlBlur}
        placeholder="http://localhost:8787"
        class="w-full px-3 py-2 rounded-lg bg-surface-2 border border-border font-mono text-sm focus:outline-none focus:ring-1 focus:ring-primary text-text-main"
      />
      <p class="text-xs text-text-muted">
        Use a local proxy for Start/Stop, or an external Docker sidecar like http://headroom:8787.
      </p>
    </div>

    <div class="flex flex-col gap-1">
      <p class="text-sm font-medium">Timeout (ms)</p>
      <input
        type="number"
        bind:value={headroomTimeoutMs}
        onblur={handleHeadroomTimeoutBlur}
        placeholder="3000"
        class="w-full px-3 py-2 rounded-lg bg-surface-2 border border-border font-mono text-sm focus:outline-none focus:ring-1 focus:ring-primary text-text-main"
      />
      <p class="text-xs text-text-muted">
        Request timeout in milliseconds. Defaults to 3000 ms.
      </p>
    </div>

    {#if headroomManaged}
      <Button
        onclick={handleHeadroomStop}
        variant="ghost"
        fullWidth
        disabled={headroomActionLoading}
      >
        {headroomActionLoading ? 'Stopping…' : 'Stop Headroom'}
      </Button>
    {:else if headroomRunning}
      <p class="text-sm text-success">
        Headroom proxy is reachable. You can enable the token saver.
      </p>
    {:else if headroomCanStart}
      <Button
        onclick={handleHeadroomStart}
        fullWidth
        disabled={headroomActionLoading}
      >
        {headroomActionLoading ? 'Starting…' : 'Start Headroom'}
      </Button>
    {:else if !headroomLocalUrl}
      <p class="text-sm text-warning">
        Start Headroom separately at the configured URL, then recheck.
      </p>
    {:else if !headroomStatus.python}
      <p class="text-sm text-warning">
        Python ≥ 3.10 required for local managed mode. Install Python first, or use an external proxy URL.
      </p>
    {:else}
      <div class="flex flex-col gap-1">
        <p class="text-sm font-medium">Install then click Start:</p>
        <div class="flex items-center gap-2">
          <pre
            class="flex-1 rounded bg-black/5 dark:bg-white/5 p-2 text-xs font-mono overflow-x-auto text-text-main"
          >{'pip install "headroom-ai[proxy]"'}</pre>
          <Button
            size="sm"
            variant="ghost"
            onclick={copyInstallCommand}
          >
            {copiedInstallCmd ? 'Copied' : 'Copy'}
          </Button>
        </div>
      </div>
    {/if}

    {#if headroomActionError}
      <p class="text-sm text-warning">{headroomActionError}</p>
    {/if}

    <div class="flex gap-2 pt-2">
      <Button
        onclick={() => refreshHeadroomStatus()}
        variant="ghost"
        fullWidth
      >
        Recheck
      </Button>
      <Button
        onclick={() => (isHeadroomModalOpen = false)}
        fullWidth
      >
        Done
      </Button>
    </div>
  </div>
</Modal>

<!-- MODAL: Confirmation for Extras -->
{#if extrasConfirm}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4">
    <div class="w-full max-w-sm p-6 rounded-2xl bg-surface border border-border shadow-2xl space-y-4">
      <div class="space-y-1">
        <h3 class="text-base font-bold text-text-main">{extrasConfirm.title}</h3>
        <p class="text-sm text-text-muted">{extrasConfirm.message}</p>
      </div>
      <div class="flex gap-2 justify-end pt-2">
        <button
          type="button"
          onclick={() => (extrasConfirm = null)}
          class="px-4 py-2 rounded-lg border border-border text-text-main hover:bg-surface-2 transition-colors text-sm font-medium cursor-pointer"
        >
          Cancel
        </button>
        <button
          type="button"
          onclick={() => {
            const fn = extrasConfirm?.onConfirm
            extrasConfirm = null
            fn?.()
          }}
          class="px-4 py-2 rounded-lg text-sm font-medium text-white transition-colors cursor-pointer {extrasConfirm.variant ===
          'danger'
            ? 'bg-danger hover:bg-danger/80'
            : 'bg-primary hover:bg-primary/90'}"
        >
          {extrasConfirm.confirmText}
        </button>
      </div>
    </div>
  </div>
{/if}
