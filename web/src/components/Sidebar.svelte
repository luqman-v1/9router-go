<script lang="ts">
  import { api, type SystemVersionInfo } from '../api/client'
  import { copyToClipboard } from '../lib/clipboard'
  import { TAB_ROUTES, type ActiveTab } from '../lib/router'

  export type { ActiveTab }
  let {
    activeTab = $bindable('endpoint'),
    navigate = (tab: ActiveTab) => {
      activeTab = tab
    },
    activeConnections = 0,
    totalConnections = 0,
    onClose,
    showUpdateModal = $bindable(false),
    updateInfo = $bindable(null),
    version = $bindable(''),
  }: {
    activeTab: ActiveTab
    navigate?: (tab: ActiveTab, replace?: boolean) => void
    activeConnections?: number
    totalConnections?: number
    onClose?: () => void
    showUpdateModal?: boolean
    updateInfo?: SystemVersionInfo | null
    version?: string
  } = $props()
  const INSTALL_CMD = '9router-go update'
  let copied = $state(false)

  // Media providers accordion (collapsed by default)
  let isMediaOpen = $state(false)

  $effect(() => {
    api
      .getSystemVersion()
      .then((v) => {
        if (v) {
          version = v.currentVersion || ''
          if (v.hasUpdate) {
            updateInfo = v
          }
        }
      })
      .catch(() => {})

    const timer = setTimeout(() => {
      api
        .checkUpdate()
        .then((v) => {
          if (v) {
            version = v.currentVersion || version
            if (v.hasUpdate) {
              updateInfo = v
            }
          }
        })
        .catch(() => {})
    }, 2500)

    return () => clearTimeout(timer)
  })

  async function copyInstallCmd() {
    await copyToClipboard(INSTALL_CMD)
    copied = true
    setTimeout(() => {
      copied = false
    }, 2000)
  }

  function handleNav(tab: ActiveTab, e?: MouseEvent) {
    if (e) {
      if (e.ctrlKey || e.metaKey || e.shiftKey || e.altKey || e.button !== 0) return
      e.preventDefault()
    }
    navigate(tab)
    onClose?.()
  }

  const mainNavLinks = [
    { tab: 'endpoint' as ActiveTab, label: 'Endpoint & Key', icon: 'api' },
    { tab: 'connections' as ActiveTab, label: 'Providers', icon: 'dns' },
    { tab: 'combos' as ActiveTab, label: 'Combo & Vision Adapter', icon: 'layers' },
    { tab: 'analytics' as ActiveTab, label: 'Usage', icon: 'bar_chart' },
    { tab: 'quota' as ActiveTab, label: 'Quota Tracker', icon: 'data_usage' },
    { tab: 'token-saver' as ActiveTab, label: 'Token Saver', icon: 'savings' },
    { tab: 'cli-tools' as ActiveTab, label: 'CLI Tools', icon: 'terminal' },
  ] as const

  const mediaNavLinks = [
    { tab: 'media-embedding' as ActiveTab, label: 'Embedding', icon: 'data_array' },
    { tab: 'media-image' as ActiveTab, label: 'Text to Image', icon: 'brush' },
    { tab: 'media-tts' as ActiveTab, label: 'Text To Speech', icon: 'record_voice_over' },
    { tab: 'media-stt' as ActiveTab, label: 'Speech To Text', icon: 'mic' },
    { tab: 'media-video' as ActiveTab, label: 'Video', icon: 'movie' },
    { tab: 'media-systemone' as ActiveTab, label: 'System One', icon: 'psychology' },
    { tab: 'media-web' as ActiveTab, label: 'Web Fetch & Search', icon: 'travel_explore' },
  ] as const

  const systemNavLinks = [
    { tab: 'proxy-pools' as ActiveTab, label: 'Proxy Pools', icon: 'lan' },
    { tab: 'skills' as ActiveTab, label: 'Skills', icon: 'extension' },
    { tab: 'console-log' as ActiveTab, label: 'Console Log', icon: 'terminal' },
  ] as const

  function isLinkActive(tab: ActiveTab): boolean {
    if (tab === 'endpoint') {
      return activeTab === 'endpoint' || activeTab === 'keys'
    }
    if (tab === 'console-log') {
      return activeTab === 'console-log' || activeTab === 'terminal'
    }
    // Cache and Compression Analytics are Usage sections (#200): they left the
    // sidebar, so the Usage entry has to stay lit while one of them is open.
    if (tab === 'analytics') {
      return (
        activeTab === 'analytics' ||
        activeTab === 'usage-cache' ||
        activeTab === 'usage-compression'
      )
    }
    return activeTab === tab
  }
</script>

<aside
  class="flex h-full max-h-screen w-72 flex-col overflow-hidden border-r border-border-subtle bg-sidebar backdrop-blur-xl transition-colors duration-300 flex-shrink-0 select-none z-30"
>
  <!-- Window control / traffic lights -->
  <div class="flex items-center gap-2 px-6 pt-5 pb-2 shrink-0">
    <div class="w-3 h-3 rounded-full bg-[#FF5F56]"></div>
    <div class="w-3 h-3 rounded-full bg-[#FFBD2E]"></div>
    <div class="w-3 h-3 rounded-full bg-[#27C93F]"></div>
  </div>

  <!-- Brand header: 9router-go with official favicon.svg logo -->
  <div class="px-6 py-4 flex flex-col gap-2 shrink-0">
    <a
      href={TAB_ROUTES.endpoint}
      onclick={(e) => handleNav('endpoint', e)}
      class="flex items-center gap-3 cursor-pointer group"
    >
      <div
        class="flex items-center justify-center size-9 rounded-[10px] bg-surface-2 border border-border-subtle shadow-[var(--shadow-warm)] flex-shrink-0 group-hover:scale-105 transition-transform overflow-hidden p-1.5"
      >
        <img
          src="/favicon.svg"
          alt="9router-go"
          class="w-full h-full object-contain"
        />
      </div>
      <div class="flex flex-col min-w-0">
        <h1 class="text-lg font-semibold tracking-tight text-text-main truncate leading-snug">
          9router-go
        </h1>
        <span class="text-xs text-text-muted leading-tight">
          {version ? `v${version}` : 'v1.9.9'}
        </span>
      </div>
    </a>

    <!-- Update notification banner below version (matches upstream 9router) -->
    {#if updateInfo && updateInfo.hasUpdate}
      <div
        class="flex flex-col gap-1.5 rounded-lg p-2 bg-green-500/10 dark:bg-amber-500/10 border border-green-500/30 dark:border-amber-500/30 text-green-700 dark:text-amber-400 mt-0.5 animate-in fade-in duration-200"
      >
        <span class="text-[11px] font-semibold text-green-700 dark:text-amber-400 flex items-center gap-1">
          <span class="material-symbols-outlined text-[14px]">arrow_upward</span>
          <span class="truncate">New version available: v{updateInfo.latestVersion}</span>
        </span>
        <div class="flex items-center gap-1.5">
          <button
            type="button"
            onclick={() => (showUpdateModal = true)}
            class="px-2 py-0.5 rounded bg-green-600 hover:bg-green-700 dark:bg-amber-500 dark:hover:bg-amber-600 text-white text-[11px] font-semibold transition-colors cursor-pointer shrink-0"
          >
            Update now
          </button>
          <button
            type="button"
            onclick={copyInstallCmd}
            title="Copy install command"
            class="flex-1 text-left hover:opacity-80 transition-opacity cursor-pointer min-w-0"
          >
            <code class="block text-[10px] text-green-700 dark:text-amber-300 font-mono truncate bg-surface/70 px-1 py-0.5 rounded">
              {copied ? '✓ copied!' : INSTALL_CMD}
            </code>
          </button>
        </div>
      </div>
    {/if}
  </div>

  <!-- Navigation -->
  <nav class="flex-1 min-h-0 px-4 py-2 space-y-0.5 overflow-y-auto custom-scrollbar">
    <!-- 1-7 Main navigation links -->
    {#each mainNavLinks as item (item.tab)}
      {@const active = isLinkActive(item.tab)}
      <a
        href={TAB_ROUTES[item.tab]}
        onclick={(e) => handleNav(item.tab, e)}
        class="flex items-center gap-3 px-3 py-1.5 rounded-lg transition-all group cursor-pointer {active
          ? 'bg-primary/10 text-primary font-medium'
          : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
      >
        <span
          class="material-symbols-outlined text-[18px] {active
            ? 'fill-1'
            : 'group-hover:text-primary transition-colors'}"
        >
          {item.icon}
        </span>
        <span class="text-[13px]">{item.label}</span>
      </a>
    {/each}

    <!-- System section header -->
    <div class="pt-3 mt-2 space-y-0.5">
      <p class="px-3 text-xs font-semibold text-text-muted/60 uppercase tracking-wider mb-2">
        System
      </p>

      <!-- 8. Media Providers accordion -->
      <button
        type="button"
        onclick={() => (isMediaOpen = !isMediaOpen)}
        class="w-full flex items-center gap-3 px-3 py-1.5 rounded-lg transition-all group cursor-pointer {activeTab.startsWith(
          'media-'
        )
          ? 'bg-primary/10 text-primary font-medium'
          : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
      >
        <span class="material-symbols-outlined text-[18px]">perm_media</span>
        <span class="text-[13px] flex-1 text-left">Media Providers</span>
        <span
          class="material-symbols-outlined text-[14px] transition-transform duration-200"
          style:transform={isMediaOpen ? 'rotate(180deg)' : 'rotate(0deg)'}
        >
          expand_more
        </span>
      </button>

      {#if isMediaOpen}
        <div class="pl-4 space-y-0.5">
          {#each mediaNavLinks as item (item.tab)}
            {@const active = isLinkActive(item.tab)}
            <a
              href={TAB_ROUTES[item.tab]}
              onclick={(e) => handleNav(item.tab, e)}
              class="flex items-center gap-3 px-3 py-1.5 rounded-lg transition-all group cursor-pointer {active
                ? 'bg-primary/10 text-primary font-medium'
                : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
            >
              <span
                class="material-symbols-outlined text-[16px] {active
                  ? 'fill-1'
                  : 'group-hover:text-primary transition-colors'}"
              >
                {item.icon}
              </span>
              <span class="text-[13px]">{item.label}</span>
            </a>
          {/each}
        </div>
      {/if}

      <!-- 9-11 System links: Proxy Pools, Skills, Console Log -->
      {#each systemNavLinks as item (item.tab)}
        {@const active = isLinkActive(item.tab)}
        <a
          href={TAB_ROUTES[item.tab]}
          onclick={(e) => handleNav(item.tab, e)}
          class="flex items-center gap-3 px-3 py-1.5 rounded-lg transition-all group cursor-pointer {active
            ? 'bg-primary/10 text-primary font-medium'
            : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
        >
          <span
            class="material-symbols-outlined text-[18px] {active
              ? 'fill-1'
              : 'group-hover:text-primary transition-colors'}"
          >
            {item.icon}
          </span>
          <span class="text-[13px]">{item.label}</span>
        </a>
      {/each}

      <!-- 12. Settings -->
      <a
        href={TAB_ROUTES.settings}
        onclick={(e) => handleNav('settings', e)}
        class="flex items-center gap-3 px-3 py-1.5 rounded-lg transition-all group cursor-pointer {isLinkActive('settings')
          ? 'bg-primary/10 text-primary font-medium'
          : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
      >
        <span
          class="material-symbols-outlined text-[18px] {isLinkActive('settings')
            ? 'fill-1'
            : 'group-hover:text-primary transition-colors'}"
        >
          settings
        </span>
        <span class="text-[13px]">Settings</span>
      </a>

      <a
        href={TAB_ROUTES.security}
        onclick={(e) => handleNav('security', e)}
        class="flex items-center gap-3 px-3 py-1.5 rounded-lg transition-all group cursor-pointer {isLinkActive('security')
          ? 'bg-primary/10 text-primary font-medium'
          : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
      >
        <span
          class="material-symbols-outlined text-[18px] {isLinkActive('security')
            ? 'fill-1'
            : 'group-hover:text-primary transition-colors'}"
        >
          shield_lock
        </span>
        <span class="text-[13px]">Security</span>
      </a>
    </div>
  </nav>

  <!-- Bottom connection summary -->
  <div class="p-4 border-t border-border-subtle shrink-0">
    <div
      class="p-2.5 rounded-[10px] bg-surface border border-border-subtle flex items-center justify-between"
    >
      <div class="min-w-0">
        <p class="text-[11px] text-text-muted uppercase tracking-wide">Providers</p>
        <p class="text-xs font-semibold text-text-main mt-0.5 truncate">
          {activeConnections} <span class="text-text-muted font-normal">/ {totalConnections} active</span>
        </p>
      </div>
      <span class="material-symbols-outlined text-primary text-[18px]">radio_button_checked</span>
    </div>
  </div>
</aside>

