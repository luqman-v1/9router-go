<script lang="ts">
  // Global search / command palette.
  //
  // The query is staged in the input and only run when the operator asks for
  // it — Enter or the Search button — so typing never triggers the pass over
  // the index. A keystroke-driven filter on ~1,700 rows re-renders the list
  // between two keypresses, which reads as lag on the small hardware this
  // gateway serves; one deliberate pass does not.
  import Modal from '../lib/ui/Modal.svelte'
  import { searchPalette, type PaletteGroup, type PaletteRow } from '../lib/commandPalette'
  import { NAV_ORDER } from '../lib/nav'
  import { isNavEntryPinned, togglePinnedNavEntry } from '../lib/navFavorites.svelte'

  let {
    isOpen = $bindable(false),
    onClose,
    onNavigate,
  }: {
    isOpen?: boolean
    onClose?: () => void
    onNavigate?: (route: string) => void
  } = $props()

  // Staged, not applied: the input is a plain controlled field until the
  // operator presses Enter or Search.
  let draft = $state('')
  let applied = $state('')
  let rows = $state<PaletteRow[]>([])
  let total = $state(0)
  let activeIndex = $state(0)
  let inputEl = $state<HTMLInputElement | null>(null)

  // Editing the query retires the results on screen: they answer the previous
  // question, and leaving them up reads as a search that ran on its own. The
  // list comes back when the operator runs the new query.
  $effect(() => {
    if (draft !== applied) {
      rows = []
      total = 0
      activeIndex = 0
    }
  })

  const GROUP_LABELS: Record<PaletteGroup, string> = {
    nav: 'Pages',
    settings: 'Settings',
    provider: 'Providers',
    model: 'Models',
  }

  // A fresh open starts from nothing: the previous query's results are not
  // what the operator wants to see when they press Ctrl+K again.
  $effect(() => {
    if (!isOpen) return
    draft = ''
    applied = ''
    rows = []
    total = 0
    activeIndex = 0
    // The input is already in the DOM — the modal renders it the moment it
    // opens — so the focus is taken on the next frame rather than through
    // onMount, which belongs to component init and not to a state change.
    requestAnimationFrame(() => inputEl?.focus())
  })

  function runSearch() {
    const text = draft.trim()
    if (!text) return
    const result = searchPalette({ text })
    applied = text
    rows = result.rows
    total = result.total
    activeIndex = 0
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === 'Enter') {
      event.preventDefault()
      runSearch()
      return
    }
    if (rows.length === 0) return
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      activeIndex = (activeIndex + 1) % rows.length
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      activeIndex = (activeIndex - 1 + rows.length) % rows.length
    } else if (event.key === 'Tab') {
      // Tab walks the results rather than leaving the dialog, so the operator
      // can reach a row without taking a hand off the keyboard.
      event.preventDefault()
      activeIndex = (activeIndex + (event.shiftKey ? -1 : 1) + rows.length) % rows.length
    }
  }

  function openRow(row: PaletteRow) {
    onNavigate?.(row.route)
    onClose?.()
  }

  function pinRow(row: PaletteRow) {
    if (row.group !== 'nav') return
    togglePinnedNavEntry(row.id.slice('nav:'.length), NAV_ORDER)
  }
</script>

<Modal {isOpen} {onClose} title="Search" icon="search" size="lg">
  <div class="space-y-3">
    <div class="flex items-center gap-2">
      <div class="relative flex-1">
        <span
          class="material-symbols-outlined pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[16px] text-text-subtle"
          >search</span
        >
        <input
          bind:this={inputEl}
          bind:value={draft}
          onkeydown={handleKeydown}
          type="text"
          placeholder="Search models, providers, pages and settings…"
          aria-label="Search query"
          class="w-full rounded-lg border border-border bg-bg pl-8 pr-3 py-2 text-sm text-text-main placeholder:text-text-subtle focus:border-brand-500 focus:outline-none"
        />
      </div>
      <button
        type="button"
        onclick={runSearch}
        disabled={!draft.trim()}
        class="flex items-center gap-1.5 rounded-lg bg-brand-500 px-3 py-2 text-xs font-bold text-white transition hover:bg-brand-600 disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer"
      >
        <span class="material-symbols-outlined text-[16px]">search</span>
        Search
      </button>
    </div>

    <p class="text-[11px] text-text-subtle">
      Press <kbd class="rounded border border-border bg-surface-2 px-1 font-code text-[10px]">Enter</kbd>
      or the Search button to run the query — typing alone does not, so a long model list never stalls the input.
    </p>

    {#if applied}
      <div class="flex items-center justify-between text-[11px] text-text-muted">
        <span>
          {total} result{#if total === 1}{:else}s{/if} for “{applied}”
        </span>
        {#if total > rows.length}
          <span>showing the first {rows.length}</span>
        {/if}
      </div>
    {/if}

    {#if rows.length > 0}
      <ul class="max-h-80 space-y-0.5 overflow-y-auto custom-scrollbar" role="listbox">
        {#each rows as row, index (row.id)}
          <li>
            <div
              class="flex items-center gap-2 rounded-lg px-2 py-1.5 transition-colors {index ===
              activeIndex
                ? 'bg-primary/10 text-primary'
                : 'text-text-main hover:bg-surface-2'}"
            >
              <button
                type="button"
                onclick={() => openRow(row)}
                class="flex min-w-0 flex-1 items-center gap-2 text-left cursor-pointer"
              >
                <span class="material-symbols-outlined shrink-0 text-[16px] text-text-muted"
                  >{row.icon}</span
                >
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-xs font-medium">{row.label}</span>
                  <span class="block truncate text-[11px] text-text-subtle">{row.hint}</span>
                </span>
                <span
                  class="shrink-0 rounded bg-surface-2 px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider text-text-subtle"
                  >{GROUP_LABELS[row.group]}</span
                >
              </button>
              {#if row.group === 'nav'}
                <button
                  type="button"
                  onclick={() => pinRow(row)}
                  title={isNavEntryPinned(row.id.slice('nav:'.length))
                    ? 'Unpin from the sidebar'
                    : 'Pin to the sidebar'}
                  class="shrink-0 rounded p-1 text-text-subtle transition-colors hover:text-brand-500 cursor-pointer"
                >
                  <span class="material-symbols-outlined text-[16px]"
                    >{isNavEntryPinned(row.id.slice('nav:'.length)) ? 'star' : 'star_outline'}</span
                  >
                </button>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
    {:else if applied}
      <p class="py-6 text-center text-xs text-text-muted">
        Nothing matches “{applied}”. Try a model id, a provider name, or a page.
      </p>
    {/if}
  </div>
</Modal>
