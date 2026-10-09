<script lang="ts">
  import { api } from '../api/client'
  import ProviderIcon from './connections/ProviderIcon.svelte'
  import ChangelogModal from './ChangelogModal.svelte'
  import Menu from '../lib/ui/Menu.svelte'
  import MenuItem from '../lib/ui/MenuItem.svelte'
  import { type ActiveTab } from '../lib/router'
  import { promptInstall, subscribeInstallPrompt } from '../lib/pwa'

  let {
    activeTab = 'endpoint',
    pageTitle,
    pageDescription,
    selectedProvider = null,
    onBackToProviders,
    onMenuClick,
    onLogout,
  }: {
    activeTab?: ActiveTab
    pageTitle?: string
    pageDescription?: string
    selectedProvider?: { id: string; name: string; icon: string } | null
    onBackToProviders?: () => void
    onMenuClick?: () => void
    onLogout?: () => void
  } = $props()

  // The group invite rotates. Everything user-facing points at the redirect
  // page instead, so a revoked link is repaired by editing site/tg.html alone
  // and every already-installed dashboard follows the new one.
  const TELEGRAM_GROUP_URL = 'https://luqman-v1.github.io/9router-go/tg'

  // Theme state
  let isDark = $state(true)
  let isDonateOpen = $state(false)
  let isChangelogOpen = $state(false)
  let canInstall = $state(false)

  $effect(() => {
    return subscribeInstallPrompt((available) => {
      canInstall = available
    })
  })
  $effect(() => {
    if (typeof window !== 'undefined') {
      const stored = localStorage.getItem('9router-theme') || localStorage.getItem('theme')
      if (stored === 'light') {
        isDark = false
      } else if (stored === 'dark') {
        isDark = true
      } else {
        isDark = document.documentElement.classList.contains('dark')
      }
      applyTheme(isDark)
    }
  })

  function applyTheme(dark: boolean) {
    document.documentElement.classList.toggle('dark', dark)
    document.documentElement.classList.toggle('light', !dark)
    localStorage.setItem('9router-theme', dark ? 'dark' : 'light')
    localStorage.setItem('theme', dark ? 'dark' : 'light')
  }

  function toggleTheme() {
    isDark = !isDark
    applyTheme(isDark)
  }

  async function handleLogout() {
    try {
      await api.logout()
    } catch {}
    if (onLogout) {
      onLogout()
    } else {
      window.location.assign('/login')
    }
  }

  // Dynamic route meta matching upstream layout
  interface RouteMeta {
    title: string
    description: string
    icon: string
  }

  const routeMetaMap: Record<string, RouteMeta> = {
    endpoint: {
      title: 'Endpoint',
      description: 'API endpoint configuration',
      icon: 'api',
    },
    keys: {
      title: 'CLI Tools',
      description: 'Configure CLI tools',
      icon: 'terminal',
    },
    connections: {
      title: 'Providers',
      description: 'Manage your AI provider connections',
      icon: 'dns',
    },
    combos: {
      title: 'Combos',
      description: 'Model combos with fallback',
      icon: 'layers',
    },
    analytics: {
      title: 'Usage & Analytics',
      description: 'Monitor your API usage, token consumption, and request logs',
      icon: 'bar_chart',
    },
    quota: {
      title: 'Quota Tracker',
      description: 'Track and manage your API quota limits',
      icon: 'data_usage',
    },
    'token-saver': {
      title: 'Token Saver',
      description: 'Compress prompts and outputs to save tokens',
      icon: 'savings',
    },
    'cli-tools': {
      title: 'CLI Tools',
      description: 'Configure CLI tools',
      icon: 'terminal',
    },
    'media-embedding': {
      title: 'Embedding',
      description: 'Manage your Embedding providers',
      icon: 'data_array',
    },
    'media-image': {
      title: 'Text to Image',
      description: 'Manage your Text to Image providers',
      icon: 'brush',
    },
    'media-tts': {
      title: 'Text To Speech',
      description: 'Manage your Text To Speech providers',
      icon: 'record_voice_over',
    },
    'media-stt': {
      title: 'Speech To Text',
      description: 'Manage your Speech To Text providers',
      icon: 'mic',
    },
    'media-video': {
      title: 'Video',
      description: 'Manage your Video providers',
      icon: 'movie',
    },
    'media-web': {
      title: 'Web Fetch & Search',
      description: 'Configure web search and scrape tools',
      icon: 'travel_explore',
    },
    'proxy-pools': {
      title: 'Proxy Pools',
      description: 'Manage your proxy pool configurations',
      icon: 'lan',
    },
    skills: {
      title: 'Agent Skills',
      description: 'Copy a link and paste to your AI to use 9router-go — no install needed',
      icon: 'extension',
    },
    'console-log': {
      title: 'Console Log',
      description: 'Live server console output',
      icon: 'terminal',
    },
    terminal: {
      title: 'Console Log',
      description: 'Live server console output',
      icon: 'terminal',
    },
    settings: {
      title: 'Settings',
      description: 'Manage your preferences',
      icon: 'settings',
    },
    login: {
      title: 'Login',
      description: 'Authentication',
      icon: 'lock',
    },
  }

  let currentMeta = $derived(
    routeMetaMap[activeTab] || {
      title: pageTitle || 'Dashboard',
      description: pageDescription || '',
      icon: 'hub',
    }
  )

  let displayTitle = $derived(pageTitle || currentMeta.title)
  let displayDescription = $derived(
    pageDescription !== undefined ? pageDescription : currentMeta.description
  )
</script>

<header
  class="h-16 bg-vibrancy backdrop-blur-xl border-b border-border-subtle px-4 lg:px-8 flex items-center justify-between gap-4 flex-shrink-0 z-20 transition-colors"
>
  <!-- Left: Mobile menu toggle + Dynamic Title & Description -->
  <div class="flex items-center gap-3 min-w-0 flex-1">
    {#if onMenuClick}
      <button
        type="button"
        onclick={onMenuClick}
        class="lg:hidden p-1.5 rounded-lg text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
        aria-label="Toggle menu"
      >
        <span class="material-symbols-outlined text-[22px]">menu</span>
      </button>
    {/if}

    {#if activeTab === 'connections' && selectedProvider}
      <div class="flex items-center gap-2 min-w-0">
        <div class="flex items-center gap-2">
          <button
            type="button"
            onclick={onBackToProviders}
            class="text-text-muted hover:text-primary transition-colors cursor-pointer text-sm font-normal"
          >
            Providers
          </button>
        </div>
        <div class="flex items-center gap-2 min-w-0">
          <span class="material-symbols-outlined text-text-muted text-base">chevron_right</span>
          <div class="flex items-center gap-2 min-w-0">
            <ProviderIcon id={selectedProvider.id} size="sm" />
            <h1 class="text-base lg:text-2xl font-semibold text-text-main tracking-tight truncate">
              {selectedProvider.name}
            </h1>
          </div>
        </div>
      </div>
    {:else}
      <div class="flex items-center gap-2.5 min-w-0">
        <span class="material-symbols-outlined text-primary text-xl lg:text-2xl flex-shrink-0">
          {currentMeta.icon}
        </span>
        <div class="min-w-0">
          <h1 class="text-base lg:text-lg font-semibold text-text-main truncate leading-tight tracking-tight">
            {displayTitle}
          </h1>
          {#if displayDescription}
            <p class="hidden sm:block text-xs text-text-muted truncate leading-tight mt-0.5">
              {displayDescription}
            </p>
          {/if}
        </div>
      </div>
    {/if}
  </div>

  <!-- Right: one menu. Donate, install, theme, changelog and logout were five
       controls competing for the same strip, and on a phone they wrapped into
       two rows (issue #224). They now live behind a single trigger, which is
       why the separate language dropdown and app-drawer dropdown this file
       used to own are gone. The Language item is gone too: the dashboard ships
       English only, and the item's handler was an empty function, so clicking
       it did nothing (issue #240). -->
  <div class="flex items-center gap-1.5 sm:gap-2 shrink-0">
    <Menu label="Account and display options" triggerIcon="account_circle" minWidth="15rem">
      <MenuItem label="Donate" icon="volunteer_activism" onSelect={() => (isDonateOpen = true)} />

      {#if canInstall}
        <MenuItem label="Install App" icon="install_desktop" onSelect={promptInstall} />
      {/if}

      <div class="my-1 border-t border-border-subtle" role="separator"></div>

      <MenuItem
        label={isDark ? 'Switch to light mode' : 'Switch to dark mode'}
        icon={isDark ? 'light_mode' : 'dark_mode'}
        onSelect={toggleTheme}
      />
      <MenuItem label="Change Log" icon="history" onSelect={() => (isChangelogOpen = true)} />

      <div class="my-1 border-t border-border-subtle" role="separator"></div>

      <MenuItem label="Logout" icon="logout" danger onSelect={handleLogout} />
    </Menu>
  </div>
</header>

<!-- Escape closes the modal from anywhere on the page. The handler used to sit
     on the backdrop, which never receives focus, so it never fired; a
     <svelte:window> binding is the only place it actually runs. -->
<svelte:window onkeydown={(e) => e.key === 'Escape' && (isDonateOpen = false)} />

<!-- Donate Modal -->
{#if isDonateOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/40 backdrop-blur-sm"
      onclick={() => (isDonateOpen = false)}
      role="button"
      tabindex="-1"
      aria-label="Close background"
    ></div>

    <div
      class="relative w-full max-w-lg bg-surface border border-border-subtle rounded-2xl shadow-2xl p-6 flex flex-col gap-5 z-10 animate-in fade-in zoom-in-95"
    >
      <div class="flex items-center justify-between pb-3 border-b border-border-subtle">
        <h2 class="text-lg font-semibold text-text-main flex items-center gap-2">
          <span class="material-symbols-outlined text-pink-500">volunteer_activism</span>
          Support 9router-go
        </h2>
        <button
          type="button"
          onclick={() => (isDonateOpen = false)}
          class="p-1 rounded-lg text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
          aria-label="Close"
        >
          <span class="material-symbols-outlined text-[20px]">close</span>
        </button>
      </div>

      <p class="text-sm text-text-muted leading-relaxed">
        9router-go is a fast, lightweight and open-source high-throughput AI gateway in Go. If 9router-go saves you time and tokens, consider supporting the project!
      </p>

      <!-- Three destinations, stacked rather than tiled: a 2-column grid leaves
           the third card alone on its own row, and a full-width row per link
           reads as a list, which is what these are. -->
      <div class="flex flex-col gap-2.5">
        <a
          href="https://github.com/luqman-v1/9router-go"
          target="_blank"
          rel="noopener noreferrer"
          class="flex items-center gap-3 p-3.5 rounded-xl border border-border-subtle bg-surface-2 hover:border-brand-500/40 transition-colors group"
        >
          <div class="size-10 rounded-full flex items-center justify-center bg-brand-500/10 text-brand-500">
            <span class="material-symbols-outlined text-[22px]">star</span>
          </div>
          <div class="min-w-0">
            <div class="text-sm font-semibold text-text-main group-hover:text-brand-500 transition-colors">
              GitHub Repository
            </div>
            <div class="text-xs text-text-muted">Star & contribute on GitHub</div>
          </div>
        </a>

        <a
          href="https://github.com/luqman-v1/9router-go/releases"
          target="_blank"
          rel="noopener noreferrer"
          class="flex items-center gap-3 p-3.5 rounded-xl border border-border-subtle bg-surface-2 hover:border-pink-500/40 transition-colors group"
        >
          <div class="size-10 rounded-full flex items-center justify-center bg-pink-500/10 text-pink-500">
            <span class="material-symbols-outlined text-[22px]">rocket_launch</span>
          </div>
          <div class="min-w-0">
            <div class="text-sm font-semibold text-text-main group-hover:text-pink-500 transition-colors">
              Releases & Updates
            </div>
            <div class="text-xs text-text-muted">Latest releases & changelog</div>
          </div>
        </a>

        <!-- Points at the redirect page, not the invite, so a rotated or revoked
             group link is fixed in site/tg.html without shipping a new binary to
             everyone already running one. -->
        <a
          href={TELEGRAM_GROUP_URL}
          target="_blank"
          rel="noopener noreferrer"
          class="flex items-center gap-3 p-3.5 rounded-xl border border-border-subtle bg-surface-2 hover:border-sky-600/50 dark:hover:border-sky-400/50 transition-colors group"
        >
          <div class="size-10 rounded-full flex items-center justify-center bg-sky-500/10 text-sky-600 dark:text-sky-400">
            <span class="material-symbols-outlined text-[22px]">send</span>
          </div>
          <div class="min-w-0">
            <div class="text-sm font-semibold text-text-main group-hover:text-sky-700 dark:group-hover:text-sky-400 transition-colors">
              Telegram Group
            </div>
            <div class="text-xs text-text-muted">Release notes, questions, provider issues</div>
          </div>
        </a>
      </div>

      <div class="flex justify-end pt-2">
        <button
          type="button"
          onclick={() => (isDonateOpen = false)}
          class="px-4 py-2 text-sm rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main font-medium transition-colors cursor-pointer"
        >
          Close
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Change Log Modal -->
<ChangelogModal isOpen={isChangelogOpen} onClose={() => (isChangelogOpen = false)} />
