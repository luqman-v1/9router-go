<script lang="ts">
  import { api, type SystemVersionInfo } from '../api/client'
  import { copyToClipboard } from '../lib/clipboard'
  import { TAB_ROUTES, type ActiveTab } from '../lib/router'
  import { isNavLinkActive, NAV_GROUPS, orderedNavGroups } from '../lib/nav'
  import { navBadgeFor } from '../lib/navBadges'
  import {
    isFavoritesEnabled,
    isIconRailEnabled,
    loadSidebarPrefs,
  } from '../lib/sidebarPrefs.svelte'
  import { pinnedNavEntries } from '../lib/navFavorites.svelte'

  export type { ActiveTab }
  let {
    activeTab = $bindable('endpoint'),
    navigate = (tab: ActiveTab) => {
      activeTab = tab
    },
    activeConnections = 0,
    totalConnections = 0,
    comboCount = 0,
    apiKeyCount = 0,
    failingConnections = 0,
    onClose,
    showUpdateModal = $bindable(false),
    updateInfo = $bindable(null),
    version = $bindable(''),
    hiddenNav,
    navOrder,
    brandName = '',
    brandLogo = '',
  }: {
    activeTab: ActiveTab
    navigate?: (tab: ActiveTab, replace?: boolean) => void
    activeConnections?: number
    totalConnections?: number
    comboCount?: number
    apiKeyCount?: number
    failingConnections?: number
    onClose?: () => void
    showUpdateModal?: boolean
    updateInfo?: SystemVersionInfo | null
    version?: string
    hiddenNav?: string[]
    navOrder?: string[]
    brandName?: string
    brandLogo?: string
  } = $props()
  // The white-label branding card in Settings writes these two; an empty value
  // falls back to the project's own name and favicon, so an operator who never
  // opens that card sees exactly what the sidebar showed before.
  const displayBrandName = $derived(brandName || '9router-go')
  const displayBrandLogo = $derived(brandLogo || '/favicon.svg')
  const INSTALL_CMD = '9router-go update'
  let copied = $state(false)

  // Which accordions are open, by id, and which labelled groups are collapsed.
  // Two sets rather than one, because the two answer opposite questions: an
  // accordion starts folded and the set says what was opened, while a group
  // starts open and its set says what was collapsed. One set holding both would
  // need a naming convention to tell them apart, and the day someone adds a
  // third kind it breaks.
  //
  // Both are persisted because an operator who folds Media Providers once means
  // it — the sidebar is the surface they arrange, not a dialog they dismiss.
  const ACCORDION_STATE_KEY = '9router-nav-accordions'
  const GROUP_STATE_KEY = '9router-nav-groups'
  let openAccordions = $state<Set<string>>(new Set())
  let collapsedGroups = $state<Set<string>>(new Set())
  $effect(() => {
    try {
      const raw = localStorage.getItem(ACCORDION_STATE_KEY)
      if (raw) {
        const ids: unknown = JSON.parse(raw)
        if (Array.isArray(ids)) {
          openAccordions = new Set(ids.filter((id): id is string => typeof id === 'string'))
        }
      }
    } catch {
      // An unreadable value is the same as none: every accordion starts folded.
    }
    try {
      const raw = localStorage.getItem(GROUP_STATE_KEY)
      if (raw) {
        const ids: unknown = JSON.parse(raw)
        if (Array.isArray(ids)) {
          collapsedGroups = new Set(ids.filter((id): id is string => typeof id === 'string'))
        }
      }
    } catch {
      // An unreadable value is the same as none: every group starts open.
    }
  })
  function toggleAccordion(id: string) {
    const next = new Set(openAccordions)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    openAccordions = next
    try {
      localStorage.setItem(ACCORDION_STATE_KEY, JSON.stringify([...next]))
    } catch {
      // A private-mode browser that refuses the write still gets the toggle.
    }
  }
  /** Folds or unfolds a labelled group. Groups start open, so this inverts. */
  function toggleGroup(id: string) {
    const next = new Set(collapsedGroups)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    collapsedGroups = next
    try {
      localStorage.setItem(GROUP_STATE_KEY, JSON.stringify([...next]))
    } catch {
      // A private-mode browser that refuses the write still gets the toggle.
    }
  }

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
      if (e.ctrlKey || e.metaKey || e.shiftKey || e.altKey || (e.button !== undefined && e.button !== 0)) return
      e.preventDefault()
    }
    navigate(tab)
    onClose?.()
  }

  // The sidebar and the Settings > Sidebar switch read the same entry list
  // (web/src/lib/nav.ts), so a hidden entry is hidden in both.
  const navGroups = $derived(orderedNavGroups(hiddenNav, navOrder))
  // Favorites: the pinned rows, drawn above the groups. The section is off
  // when the operator turns it off in Settings, and empty until they pin
  // something, so an untouched install shows no change.
  // Read once at boot, then re-render on the same tick the Settings switch
  // writes: the store is shared, so no event or poll has to carry the change
  // between two siblings in the same tab.
  $effect(() => {
    loadSidebarPrefs()
  })
  // Derived rather than copied: a local $state would need a second effect to
  // stay in step, and the store is already reactive.
  const favoritesEnabled = $derived(isFavoritesEnabled())
  const iconRail = $derived(isIconRailEnabled())
  // Every entry this build draws, so a pin naming a removed id is dropped
  // rather than rendered as a dead row.
  const allEntries = $derived(
    NAV_GROUPS.flatMap((group) =>
      group.entries.flatMap((entry) => (entry.kind === 'accordion' ? entry.children : [entry]))
    )
  )
  // The counts beside an entry. Built once per change rather than per row, and
  // null for most entries: a badge is a fact the operator would otherwise open
  // a page to learn, so a number that only restates the label is decoration.
  const badgeInput = $derived({
    totalConnections,
    activeConnections,
    comboCount,
    apiKeyCount,
    failingConnections,
  })
  function badgeFor(id: string) {
    return navBadgeFor(id, badgeInput)
  }
  const pinnedRows = $derived(
    pinnedNavEntries()
      .map((id) => allEntries.find((entry) => entry.id === id))
      .filter((entry): entry is NonNullable<typeof entry> => entry !== undefined)
  )
  // A pinned row the operator later hid is not drawn: the hide list is the
  // stronger statement, and the pin survives for when the entry comes back.
  const visiblePins = $derived(
    pinnedRows.filter((entry) => !(hiddenNav ?? []).includes(entry.id))
  )
</script>

<aside
  class="flex h-full max-h-screen {iconRail
    ? 'w-[56px]'
    : 'w-72'} flex-col overflow-hidden border-r border-border-subtle bg-sidebar backdrop-blur-xl transition-all duration-300 flex-shrink-0 select-none z-30"
>
  <!-- Window control / traffic lights -->
  {#if !iconRail}
    <div class="flex items-center gap-2 px-6 pt-5 pb-2 shrink-0">
      <div class="w-3 h-3 rounded-full bg-[#FF5F56]"></div>
      <div class="w-3 h-3 rounded-full bg-[#FFBD2E]"></div>
      <div class="w-3 h-3 rounded-full bg-[#27C93F]"></div>
    </div>
  {/if}

  <!-- Brand header: 9router-go with official favicon.svg logo -->
  <div class="{iconRail ? 'px-2 py-4' : 'px-6 py-4'} flex flex-col gap-2 shrink-0">
    <a
      href={TAB_ROUTES.endpoint}
      onclick={(e) => handleNav('endpoint', e)}
      class="flex items-center {iconRail ? 'justify-center' : 'gap-3'} cursor-pointer group"
      title={iconRail ? displayBrandName : undefined}
    >
      <div
        class="flex items-center justify-center size-9 rounded-[10px] bg-surface-2 border border-border-subtle shadow-[var(--shadow-warm)] flex-shrink-0 group-hover:scale-105 transition-transform overflow-hidden p-1.5"
      >
        <img
          src={displayBrandLogo}
          alt={displayBrandName}
          class="w-full h-full object-contain"
        />
      </div>
      {#if !iconRail}
        <div class="flex flex-col min-w-0">
          <h1 class="text-lg font-semibold tracking-tight text-text-main truncate leading-snug">
            {displayBrandName}
          </h1>
          <span class="text-xs text-text-muted leading-tight">
            {version ? `v${version}` : 'v1.9.9'}
          </span>
        </div>
      {/if}
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
  <nav
    class="flex-1 min-h-0 {iconRail ? 'px-2' : 'px-4'} py-2 space-y-0.5 overflow-y-auto custom-scrollbar"
  >
    {#if favoritesEnabled && visiblePins.length > 0}
      <div class="{iconRail ? '' : 'pt-1'} space-y-0.5">
        {#if !iconRail}
          <p class="px-3 text-xs font-semibold text-text-muted/60 uppercase tracking-wider mb-2">
            Pinned
          </p>
        {/if}
        {#each visiblePins as entry (entry.id)}
          {@const pinActive = isNavLinkActive(entry.id, activeTab)}
          {@const pinBadge = badgeFor(entry.id)}
          <a
            href={TAB_ROUTES[entry.id]}
            onclick={(e) => handleNav(entry.id, e)}
            title={iconRail ? entry.label : undefined}
            class="flex items-center {iconRail
              ? 'justify-center'
              : 'gap-3'} px-3 py-1.5 rounded-lg transition-all group cursor-pointer {pinActive
              ? 'bg-primary/10 text-primary font-medium'
              : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
          >
            <span
              class="material-symbols-outlined text-[18px] {pinActive
                ? 'fill-1'
                : 'group-hover:text-primary transition-colors'}">{entry.icon}</span
            >
            {#if !iconRail}
              <span class="text-[13px] flex-1">{entry.label}</span>
            {/if}
            {#if pinBadge && !iconRail}
              <span
                title={pinBadge.title}
                class="shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-semibold tabular-nums {pinBadge.tone ===
                'warn'
                  ? 'bg-warning/15 text-warning'
                  : 'bg-surface-2 text-text-subtle'}">{pinBadge.text}</span
              >
            {/if}
          </a>
        {/each}
        {#if !iconRail}
          <div class="my-2 border-t border-border-subtle"></div>
        {/if}
      </div>
    {/if}
    {#each navGroups as group (group.id)}
      <!-- A labelled group folds like an accordion does, so an operator on a
           short screen can press the sections they are not using. Open is the
           default: the set holds the *collapsed* ids, so a build that never
           wrote one draws every group the way it always did. The group holding
           the active entry stays open whatever was stored, because folding it
           would hide the page the operator is looking at. -->
      {@const holdsActiveTab = group.entries.some((entry) =>
        entry.kind === 'accordion'
          ? entry.children.some((child) => isNavLinkActive(child.id, activeTab))
          : isNavLinkActive(entry.id, activeTab)
      )}
      {@const groupOpen =
        !group.label ||
        iconRail ||
        holdsActiveTab ||
        !collapsedGroups.has(`group:${group.id}`)}
      <div class="{group.label ? 'pt-3 mt-2' : ''} space-y-0.5">
        {#if group.label && !iconRail}
          <!-- A labelled group folds like an accordion does, so an operator on
               a short screen can press the sections they are not using. The
               group holding the active entry stays open whatever was stored:
               folding it would hide the page the operator is looking at. -->
          <button
            type="button"
            onclick={() => toggleGroup(`group:${group.id}`)}
            aria-expanded={groupOpen}
            class="w-full flex items-center gap-1.5 px-3 mb-2 text-text-muted/60 uppercase tracking-wider cursor-pointer"
          >
            <span class="text-xs font-semibold">{group.label}</span>
            <span
              class="material-symbols-outlined text-[14px] transition-transform duration-200 {groupOpen
                ? ''
                : '-rotate-90'}"
              >expand_more</span
            >
          </button>
        {/if}

        {#if groupOpen}
          {#each group.entries as entry (entry.id)}
            {#if entry.kind === 'accordion'}
            {@const isOpen = openAccordions.has(entry.id)}
            {@const accordionActive = entry.children.some((child) =>
              isNavLinkActive(child.id, activeTab)
            )}
            <button
              type="button"
              onclick={() => toggleAccordion(entry.id)}
              aria-expanded={isOpen}
              title={iconRail ? entry.label : undefined}
              class="w-full flex items-center {iconRail
                ? 'justify-center'
                : 'gap-3'} px-3 py-1.5 rounded-lg transition-all group cursor-pointer {accordionActive
                ? 'bg-primary/10 text-primary font-medium'
                : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
            >
              <span class="material-symbols-outlined text-[18px]">{entry.icon}</span>
              {#if !iconRail}
                <span class="text-[13px] flex-1 text-left">{entry.label}</span>
                <span
                  class="material-symbols-outlined text-[14px] transition-transform duration-200"
                  style:transform={isOpen ? 'rotate(180deg)' : 'rotate(0deg)'}
                >
                  expand_more
                </span>
              {/if}
            </button>
            {#if isOpen && !iconRail}
              <div class="pl-4 space-y-0.5">
                {#each entry.children as child (child.id)}
                  {@const childActive = isNavLinkActive(child.id, activeTab)}
                  <a
                    href={TAB_ROUTES[child.id]}
                    onclick={(e) => handleNav(child.id, e)}
                    class="flex items-center gap-3 px-3 py-1.5 rounded-lg transition-all group cursor-pointer {childActive
                      ? 'bg-primary/10 text-primary font-medium'
                      : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
                  >
                    <span
                      class="material-symbols-outlined text-[16px] {childActive
                        ? 'fill-1'
                        : 'group-hover:text-primary transition-colors'}"
                    >
                      {child.icon}
                    </span>
                    <span class="text-[13px]">{child.label}</span>
                  </a>
                {/each}
              </div>
            {/if}
          {:else}
            {@const active = isNavLinkActive(entry.id, activeTab)}
            {@const badge = badgeFor(entry.id)}
            <a
              href={TAB_ROUTES[entry.id]}
              onclick={(e) => handleNav(entry.id, e)}
              title={iconRail ? entry.label : undefined}
              class="flex items-center {iconRail
                ? 'justify-center'
                : 'gap-3'} px-3 py-1.5 rounded-lg transition-all group cursor-pointer {active
                ? 'bg-primary/10 text-primary font-medium'
                : 'text-text-muted hover:bg-surface-2 hover:text-text-main'}"
            >
              <span
                class="material-symbols-outlined text-[18px] {active
                  ? 'fill-1'
                  : 'group-hover:text-primary transition-colors'}"
              >
                {entry.icon}
              </span>
              {#if !iconRail}
                <span class="text-[13px] flex-1">{entry.label}</span>
              {/if}
              {#if badge && !iconRail}
                <span
                  title={badge.title}
                  class="shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-semibold tabular-nums {badge.tone ===
                  'warn'
                    ? 'bg-warning/15 text-warning'
                    : 'bg-surface-2 text-text-subtle'}">{badge.text}</span
                >
              {/if}
            </a>
          {/if}
          {/each}
        {/if}
      </div>
    {/each}
  </nav>

  <!-- Bottom connection summary -->
  {#if !iconRail}
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
  {:else}
    <div class="p-2 border-t border-border-subtle shrink-0 flex justify-center" title="{activeConnections} / {totalConnections} active">
      <span class="material-symbols-outlined text-primary text-[18px]">radio_button_checked</span>
    </div>
  {/if}
</aside>

