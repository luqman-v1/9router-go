<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import Card from '../lib/ui/Card.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Input from '../lib/ui/Input.svelte'
  import { api, type Settings } from '../api/client'

  interface Props {
    settings?: Settings
    onRefresh?: () => void
  }

  let { settings = {}, onRefresh }: Props = $props()

  // State
  let rtkEnabled = $state(true)
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
  let cavemanLevel = $state('full')
  let ponytailEnabled = $state(false)
  let ponytailLevel = $state('full')
  let adhdEnabled = $state(false)
  let adhdLevel = $state('full')
  let locale = $state('en')

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
      if (typeof settings.headroomEnabled === 'boolean') headroomEnabled = settings.headroomEnabled
      if (typeof settings.headroomUrl === 'string' && settings.headroomUrl) headroomUrl = settings.headroomUrl
      if (typeof settings.headroomTimeoutMs === 'number' && settings.headroomTimeoutMs > 0) {
        headroomTimeoutMs = settings.headroomTimeoutMs
      }
      if (typeof settings.headroomCodeAware === 'boolean') codeAware = settings.headroomCodeAware
      if (typeof settings.headroomKompress === 'boolean') kompress = settings.headroomKompress
      if (typeof settings.cavemanEnabled === 'boolean') cavemanEnabled = settings.cavemanEnabled
      if (typeof settings.cavemanLevel === 'string' && settings.cavemanLevel) cavemanLevel = settings.cavemanLevel
      if (typeof settings.ponytailEnabled === 'boolean') ponytailEnabled = settings.ponytailEnabled
      if (typeof settings.ponytailLevel === 'string' && settings.ponytailLevel) ponytailLevel = settings.ponytailLevel
      if (typeof settings.adhdEnabled === 'boolean') adhdEnabled = settings.adhdEnabled
      if (typeof settings.adhdLevel === 'string' && settings.adhdLevel) adhdLevel = settings.adhdLevel
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
    navigator.clipboard.writeText('pip install "headroom-ai[proxy]"')
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
        headroomEnabled = !!data.headroomEnabled
        headroomUrl = data.headroomUrl || 'http://localhost:8787'
        if (typeof data.headroomTimeoutMs === 'number') headroomTimeoutMs = data.headroomTimeoutMs
        codeAware = data.headroomCodeAware === true
        kompress = data.headroomKompress !== false
        cavemanEnabled = !!data.cavemanEnabled
        cavemanLevel = data.cavemanLevel || 'full'
        ponytailEnabled = !!data.ponytailEnabled
        ponytailLevel = data.ponytailLevel || 'full'
        adhdEnabled = !!data.adhdEnabled
        adhdLevel = data.adhdLevel || 'full'
      }
    } catch {}

    refreshHeadroomStatus()
  })
</script>

<div class="space-y-6 p-6">
  <Card id="rtk">
    <div class="flex items-center justify-between mb-2">
      <h2 class="text-lg font-semibold flex items-center gap-2">
        <span class="material-symbols-outlined text-primary">bolt</span>
        Token Saver
      </h2>
    </div>

    <!-- 1. Compress tool output (RTK) -->
    <div class="flex items-center justify-between pt-2 pb-4 border-b border-border gap-4">
      <div class="min-w-0 flex-1">
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
        <p class="text-sm text-text-muted">
          git/grep/ls/tree/logs → 60-90% fewer input tokens
        </p>
      </div>
      <Toggle
        checked={rtkEnabled}
        onChange={() => handleToggleRTK(!rtkEnabled)}
      />
    </div>

    <!-- 2. Compress context (Headroom) -->
    <div class="flex items-center justify-between py-4 gap-4 flex-wrap">
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
      <Toggle
        checked={headroomEnabled}
        onChange={() => handleToggleHeadroom(!headroomEnabled)}
      />
    </div>

    {#if headroomStatus.installed}
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
                  class="ml-1 text-error underline hover:opacity-80 disabled:opacity-50 cursor-pointer"
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
          <p class="text-xs text-error mt-2">{extrasActionError}</p>
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
        <p class="text-sm text-text-muted">
          Terse-style system prompt → ~65% fewer output tokens (up to 87%)
        </p>
      </div>
      <div class="flex items-center gap-3 shrink-0">
        {#if cavemanEnabled}
          <div class="flex flex-col items-end gap-1">
            <div class="flex items-center gap-1.5 flex-wrap">
              {#each visibleCavemanLevels as lvl (lvl.id)}
                <button
                  type="button"
                  onclick={() => handleSelectCavemanLevel(lvl.id)}
                  class="px-3 py-1.5 rounded text-xs font-medium border transition-colors cursor-pointer {cavemanLevel === lvl.id
                    ? 'bg-primary text-white border-primary'
                    : 'bg-transparent border-border text-text-muted hover:bg-surface-2'}"
                  title={lvl.desc}
                >
                  {lvl.label}
                </button>
              {/each}
            </div>
            <p class="text-xs text-primary">
              {CAVEMAN_LEVELS.find((lvl) => lvl.id === cavemanLevel)?.desc || ''}
            </p>
          </div>
        {/if}
        <Toggle
          checked={cavemanEnabled}
          onChange={() => handleToggleCaveman(!cavemanEnabled)}
        />
      </div>
    </div>

    <!-- 4. Lazy senior dev (Ponytail) -->
    <div class="flex items-center justify-between pt-4 mt-4 border-t border-border gap-4 flex-wrap">
      <div class="min-w-0 flex-1">
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
        <p class="text-sm text-text-muted">
          Bias the model toward minimal code: YAGNI, reuse stdlib, deletion over addition
        </p>
      </div>
      <div class="flex items-center gap-3 shrink-0">
        {#if ponytailEnabled}
          <div class="flex flex-col items-end gap-1">
            <div class="flex items-center gap-1.5 flex-wrap">
              {#each PONYTAIL_LEVELS as lvl (lvl.id)}
                <button
                  type="button"
                  onclick={() => handleSelectPonytailLevel(lvl.id)}
                  class="px-3 py-1.5 rounded text-xs font-medium border transition-colors cursor-pointer {ponytailLevel === lvl.id
                    ? 'bg-primary text-white border-primary'
                    : 'bg-transparent border-border text-text-muted hover:bg-surface-2'}"
                  title={lvl.desc}
                >
                  {lvl.label}
                </button>
              {/each}
            </div>
            <p class="text-xs text-primary">
              {PONYTAIL_LEVELS.find((lvl) => lvl.id === ponytailLevel)?.desc || ''}
            </p>
          </div>
        {/if}
        <Toggle
          checked={ponytailEnabled}
          onChange={() => handleTogglePonytail(!ponytailEnabled)}
        />
      </div>
    </div>

    <!-- 5. Action-first output (I have ADHD) -->
    <div class="flex items-center justify-between pt-4 mt-4 border-t border-border gap-4 flex-wrap">
      <div class="min-w-0 flex-1">
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
        <p class="text-sm text-text-muted">
          Action-first output style: lead with command/code, numbered bounded steps, one concrete next step under 2 mins, no fluff/preamble.
        </p>
      </div>
      <div class="flex items-center gap-3 shrink-0">
        {#if adhdEnabled}
          <div class="flex flex-col items-end gap-1">
            <div class="flex items-center gap-1.5 flex-wrap">
              {#each ADHD_LEVELS as lvl (lvl.id)}
                <button
                  type="button"
                  onclick={() => handleSelectADHDLevel(lvl.id)}
                  class="px-3 py-1.5 rounded text-xs font-medium border transition-colors cursor-pointer {adhdLevel === lvl.id
                    ? 'bg-primary text-white border-primary'
                    : 'bg-transparent border-border text-text-muted hover:bg-surface-2'}"
                  title={lvl.desc}
                >
                  {lvl.label}
                </button>
              {/each}
            </div>
            <p class="text-xs text-primary">
              {ADHD_LEVELS.find((lvl) => lvl.id === adhdLevel)?.desc || ''}
            </p>
          </div>
        {/if}
        <Toggle
          checked={adhdEnabled}
          onChange={() => handleToggleADHD(!adhdEnabled)}
        />
      </div>
    </div>
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
            ? 'bg-error hover:bg-error/90'
            : 'bg-primary hover:bg-primary/90'}"
        >
          {extrasConfirm.confirmText}
        </button>
      </div>
    </div>
  </div>
{/if}
