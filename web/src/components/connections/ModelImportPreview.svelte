<script lang="ts">
  // Staging panel for "Import from /models": fetches a provider's catalogue
  // without writing any rows, lets the operator search, filter, sort, select,
  // test and add or remove models, one chip at a time, or as a batch.
  //
  // Two operations are deliberately kept apart:
  //   - Selection is free and reversible: clicking a chip's body toggles it in
  //     a local set, and the batch bar acts on that set (test / add / remove).
  //   - The chip's own + and trash buttons act on that one model immediately.
  // Collapsing them would make a stray click on a model name write to the
  // store, with no undo.
  import type { ModelTestStatus } from './modelCheckAll'
  import {
    filterImportPreviewModels,
    filterImportPreviewByState,
    sortImportPreviewModels,
    importPreviewSelectionState,
    toggleImportPreviewSelectAll,
    IMPORT_PREVIEW_SORTS,
    IMPORT_PREVIEW_STATE_FILTERS,
    IMPORT_PREVIEW_PAGE_SIZE,
    importPreviewPageCount,
    importPreviewPageItems,
    type ImportPreviewModel,
    type ImportPreviewSort,
    type ImportPreviewStateFilter,
  } from './modelImportPreview'

  interface Props {
    models: ImportPreviewModel[]
    /** Model ids currently on the node's active list (drives chip state B/A). */
    activeIds: Set<string>
    search: string
    busy?: boolean
    testStatuses: Record<string, ModelTestStatus>
    testErrors: Record<string, string | null>
    testLatency: Record<string, number | undefined>
    /** True while a batch probe is running, so the bar can show progress. */
    testing?: boolean
    onSearch: (q: string) => void
    onClose: () => void
    onAdd: (id: string) => void
    onRemove: (id: string) => void
    onTest: (id: string) => void
    /** Bulk-add every currently filtered model that is not on the list yet. */
    onAddAll: (ids: string[]) => void
    /** Bulk-remove every currently filtered model that is on the list. */
    onRemoveAll: (ids: string[]) => void
    /** Probe a batch of selected models. */
    onTestSelected: (ids: string[]) => void
  }

  let {
    models,
    activeIds,
    search,
    busy = false,
    testStatuses,
    testErrors,
    testLatency,
    testing = false,
    onSearch,
    onClose,
    onAdd,
    onRemove,
    onTest,
    onAddAll,
    onRemoveAll,
    onTestSelected,
  }: Props = $props()

  let sort = $state<ImportPreviewSort>('az')
  let stateFilter = $state<ImportPreviewStateFilter>('all')
  let selectedIds = $state<Set<string>>(new Set())

  // State split first, search second, sort last: the segmented counts describe
  // the whole catalogue, while the search narrows inside that split.
  let addedCount = $derived(models.filter((m) => activeIds.has(m.id)).length)
  let availableCount = $derived(models.length - addedCount)

  let searched = $derived(
    filterImportPreviewModels(
      filterImportPreviewByState(models, stateFilter, activeIds),
      search
    )
  )
  let filtered = $derived(sortImportPreviewModels(searched, sort, testLatency))

  // The header badge reports the catalogue size, and the match count while a
  // search is active so the title answers "how many did my query hit".
  let headerCount = $derived(search.trim() ? filtered.length : models.length)

  // Pagination, not a growing list: a 460-model catalog draws ~3,000 DOM nodes
  // if every chip mounts at once, which is a phone-unfriendly first paint on
  // the small box this dashboard serves from. The search, the state filter and
  // the sort are the way to reach the rest, and the page numbers reach it too.
  let page = $state(1)
  let pageCount = $derived(importPreviewPageCount(filtered.length))
  let pageItems = $derived(importPreviewPageItems(page, pageCount))
  let visible = $derived(
    filtered.slice((page - 1) * IMPORT_PREVIEW_PAGE_SIZE, page * IMPORT_PREVIEW_PAGE_SIZE)
  )

  // A new search, filter, sort or catalog starts from the first page, so the
  // operator is never left on a page that no longer exists.
  $effect(() => {
    void search
    void models
    void sort
    void stateFilter
    page = 1
  })

  let addableIds = $derived(filtered.filter((m) => !activeIds.has(m.id)).map((m) => m.id))
  let removableIds = $derived(filtered.filter((m) => activeIds.has(m.id)).map((m) => m.id))

  let selection = $derived(importPreviewSelectionState(selectedIds, filtered))

  // Selecting chips narrows every bulk action to them. Without this the
  // selection only tinted rows: "Test selected" tested the whole filter and
  // "Add all" ignored a hand-picked subset, so a click on a chip's name
  // changed nothing an operator could act on.
  let selectedAddable = $derived(addableIds.filter((id) => selectedIds.has(id)))
  let selectedRemovable = $derived(removableIds.filter((id) => selectedIds.has(id)))
  let hasSelection = $derived(selection.selected > 0)

  let bulkAddIds = $derived(hasSelection ? selectedAddable : addableIds)
  let bulkRemoveIds = $derived(hasSelection ? selectedRemovable : removableIds)
  let bulkTestIds = $derived(
    hasSelection ? filtered.filter((m) => selectedIds.has(m.id)).map((m) => m.id) : filtered.map((m) => m.id)
  )
  let bulkScope = $derived(hasSelection ? 'selected' : 'matching the current filter')

  let stateCounts = $derived<Record<ImportPreviewStateFilter, number>>({
    all: models.length,
    available: availableCount,
    added: addedCount,
  })

  function toggleChip(id: string) {
    const next = new Set(selectedIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    selectedIds = next
  }

  function clearSelection() {
    selectedIds = new Set()
  }

  // Verdict summary for screen readers: a probe finishes silently, so the
  // region re-announces the tally instead of leaving the result icon-only.
  let testedCount = $derived(
    Object.values(testStatuses).filter((s) => s !== 'testing').length
  )
  let testSummary = $derived.by(() => {
    const done = Object.values(testStatuses).filter((s) => s !== 'testing')
    const ok = done.filter((s) => s === 'ok').length
    const failed = done.filter((s) => s === 'error' || s === 'blocked').length
    return `${ok} passed, ${failed} failed of ${done.length} tested`
  })
  let passedCount = $derived(Object.values(testStatuses).filter((s) => s === 'ok').length)
  let failedCount = $derived(
    Object.values(testStatuses).filter((s) => s === 'error' || s === 'blocked').length
  )

  let searchInput = $state<HTMLInputElement | null>(null)
  // Autofocus: the search is the first thing an operator does with a catalog
  // this size, and a modal that opens without a focused control costs a click.
  $effect(() => {
    searchInput?.focus()
  })

  function kindIcon(id: string): string {
    // Mirrors inferModelKind's lanes, kept visual-only: a chip needs a glyph,
    // not the service kind the gateway stores.
    if (/embed/i.test(id)) return 'database'
    if (/tts|speech|audio|voice/i.test(id)) return 'volume_up'
    if (/image|imagen|dall|flux|sdxl|banana/i.test(id)) return 'image'
    if (/veo|sora|video/i.test(id)) return 'movie'
    return 'smart_toy'
  }

  function latencyClass(ms: number): string {
    if (ms < 1000) return 'text-green-600 dark:text-green-400'
    if (ms < 3000) return 'text-amber-600 dark:text-amber-400'
    return 'text-red-500'
  }

  // Upstream Modal parity: Escape dismisses, backdrop click dismisses.
  $effect(() => {
    if (typeof window === 'undefined') return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })
</script>

<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
  <div
    class="absolute inset-0 bg-black/60 backdrop-blur-[2px] fade-in"
    onclick={onClose}
    role="presentation"
  ></div>
  <!-- A fixed header / scrolling body / fixed footer column: with 460 chips the
       controls must stay reachable without scrolling back to the top, and the
       footer is where the running totals belong. -->
  <div
    class="relative flex max-h-[96vh] w-full max-w-5xl flex-col overflow-hidden rounded-[14px] border border-border-subtle bg-surface shadow-[var(--shadow-elevated)] fade-in"
    role="dialog"
    aria-modal="true"
    aria-labelledby="import-preview-title"
    onkeydown={(e) => { if (e.key === 'Escape') onClose() }}
  >
    <!-- Header: identity only. The search owns the row below it, full width,
         because it is the primary way through a catalogue this size. -->
    <header class="flex shrink-0 items-center gap-2 border-b border-border-subtle px-4 py-2.5">
      <span class="material-symbols-outlined text-base text-brand-500">download_for_offline</span>
      <h2 id="import-preview-title" class="shrink-0 whitespace-nowrap text-sm font-semibold text-text-main">
        Preview Fetch Models
      </h2>
      <span class="shrink-0 rounded-full bg-surface-2 px-2 py-0.5 text-[11px] font-semibold tabular-nums text-text-muted">
        {headerCount}
      </span>
      <button
        type="button"
        onclick={onClose}
        title="Close the preview panel"
        aria-label="Close the preview panel"
        class="ml-auto rounded p-1 text-text-muted hover:bg-surface-2 hover:text-text-main cursor-pointer"
      >
        <span class="material-symbols-outlined text-base">close</span>
      </button>
    </header>

    <!-- Search: its own full-width row. -->
    <div class="shrink-0 px-4 pt-3">
      <div class="relative">
        <span class="material-symbols-outlined pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-sm text-text-muted">search</span>
        <input
          type="text"
          bind:this={searchInput}
          value={search}
          oninput={(e) => onSearch((e.target as HTMLInputElement).value)}
          placeholder="Search models (e.g. dee, flash)..."
          aria-label="Search models by id"
          class="h-9 w-full rounded-md border border-border bg-background pl-8 pr-8 text-xs text-text-main placeholder:text-text-muted focus:border-primary focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
        />
        {#if search.trim()}
          <button
            type="button"
            onclick={() => onSearch('')}
            title="Clear the search"
            aria-label="Clear the search"
            class="absolute right-2 top-1/2 -translate-y-1/2 rounded p-0.5 text-text-muted hover:text-text-main cursor-pointer"
          >
            <span class="material-symbols-outlined text-sm">close</span>
          </button>
        {/if}
      </div>
    </div>

    <!-- Toolbar: state split, sort, and the select-all control. -->
    <div class="flex shrink-0 flex-wrap items-center gap-2 border-b border-border-subtle px-4 py-2">
      <div
        class="flex items-center gap-0.5 rounded-lg border border-border bg-background p-0.5"
        role="group"
        aria-label="Filter by state"
      >
        {#each IMPORT_PREVIEW_STATE_FILTERS as opt (opt.value)}
          <button
            type="button"
            onclick={() => (stateFilter = opt.value)}
            aria-pressed={stateFilter === opt.value}
            class="inline-flex h-6 items-center gap-1 rounded-md px-2 text-[11px] font-medium transition-colors cursor-pointer {stateFilter ===
            opt.value
              ? 'bg-surface text-text-main shadow-[var(--shadow-soft)]'
              : 'text-text-muted hover:text-text-main'}"
          >
            {opt.label}
            <span class="tabular-nums opacity-60">{stateCounts[opt.value]}</span>
          </button>
        {/each}
      </div>

      <label class="ml-auto flex items-center gap-1 text-[11px] text-text-muted">
        <span class="material-symbols-outlined text-sm">sort</span>
        <select
          value={sort}
          onchange={(e) => (sort = (e.target as HTMLSelectElement).value as ImportPreviewSort)}
          aria-label="Sort the list"
          class="h-7 cursor-pointer rounded-md border border-border bg-background px-1.5 text-[11px] text-text-main focus:border-primary focus:outline-none"
        >
          {#each IMPORT_PREVIEW_SORTS as opt (opt.value)}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </select>
      </label>

    </div>

    <!-- One action row. Every control acts on the filtered set (not the whole
         catalogue), which is the selective import this panel exists for; the
         whole-catalogue case is just an empty search. Select-all sits here
         rather than in the toolbar because it is the first step of exactly
         these three actions, and it is drawn as a button so it reads as one
         more thing you do to the list, not as a form field. -->
    <div class="flex shrink-0 flex-wrap items-center gap-2 border-b border-border-subtle bg-surface-2/40 px-4 py-2">
      <button
        type="button"
        onclick={() => (selectedIds = toggleImportPreviewSelectAll(selectedIds, filtered, !selection.allSelected))}
        disabled={filtered.length === 0 || busy}
        title={selection.allSelected ? 'Deselect every visible model' : 'Select every model matching the current filter'}
        aria-pressed={selection.allSelected}
        class="inline-flex h-7 items-center gap-1 rounded-md border px-2.5 text-[11px] font-medium transition-colors disabled:opacity-50 cursor-pointer {selection.selected > 0
          ? 'border-primary/50 bg-primary/10 text-primary'
          : 'border-border bg-surface text-text-main hover:bg-surface-3'}"
      >
        <span class="material-symbols-outlined text-sm">{selection.allSelected ? 'check_box' : selection.indeterminate ? 'indeterminate_check_box' : 'check_box_outline_blank'}</span>
        {selection.allSelected ? 'Deselect all' : 'Select all'}
        <span class="tabular-nums opacity-60">{selection.selected > 0 ? `${selection.selected}/${filtered.length}` : `(${filtered.length})`}</span>
      </button>

      {#if bulkAddIds.length > 0}
        <button
          type="button"
          onclick={() => onAddAll(bulkAddIds)}
          disabled={busy}
          title="Add {bulkAddIds.length} models {bulkScope}"
          class="inline-flex h-7 items-center gap-1 rounded-md border border-brand-500/40 bg-brand-500/10 px-2.5 text-[11px] font-medium text-brand-600 transition-colors hover:bg-brand-500/20 disabled:opacity-50 cursor-pointer dark:text-brand-400"
        >
          <span class="material-symbols-outlined text-sm">library_add</span>
          Add {hasSelection ? 'selected' : 'all'} ({bulkAddIds.length})
        </button>
      {/if}

      {#if bulkTestIds.length > 0}
        <button
          type="button"
          onclick={() => onTestSelected(bulkTestIds)}
          disabled={testing || busy}
          title="Test {bulkTestIds.length} models {bulkScope}; each probe consumes account quota"
          class="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-surface px-2.5 text-[11px] font-medium text-text-main transition-colors hover:bg-surface-3 disabled:opacity-50 cursor-pointer"
        >
          <span class="material-symbols-outlined text-sm" style={testing ? 'animation: spin 1s linear infinite' : undefined}>
            {testing ? 'progress_activity' : 'science'}
          </span>
          {testing ? 'Testing...' : `Test ${hasSelection ? 'selected' : 'all'} (${bulkTestIds.length})`}
        </button>
      {/if}

      {#if bulkRemoveIds.length > 0}
        <button
          type="button"
          onclick={() => onRemoveAll(bulkRemoveIds)}
          disabled={busy}
          title="Remove {bulkRemoveIds.length} models {bulkScope}"
          class="inline-flex h-7 items-center gap-1 rounded-md border border-red-500/40 bg-red-500/10 px-2.5 text-[11px] font-medium text-red-600 transition-colors hover:bg-red-500/20 disabled:opacity-50 cursor-pointer dark:text-red-400"
        >
          <span class="material-symbols-outlined text-sm">delete_sweep</span>
          Remove {hasSelection ? 'selected' : 'all'} ({bulkRemoveIds.length})
        </button>
      {/if}

      {#if selection.selected > 0}
        <button
          type="button"
          onclick={clearSelection}
          class="ml-auto inline-flex h-7 items-center gap-1 rounded-md px-2 text-[11px] text-text-muted hover:text-text-main cursor-pointer"
          title="Clear the selection"
        >
          <span class="material-symbols-outlined text-sm">deselect</span>
          Clear
        </button>
      {/if}
    </div>

    <p class="sr-only" aria-live="polite">{testSummary}</p>

    <!-- Body: the only scrolling region. The hint sits on top of the chips and
         scrolls with them, so the sentence is read where it applies instead of
         being parked at the far end of the action row where it read as a label
         for whatever button preceded it. -->
    <div class="min-h-0 flex-1 overflow-y-auto px-4 pb-3 pt-2">
      <p class="mb-2 flex items-center gap-1.5 rounded-md bg-surface-2/60 px-2 py-1.5 text-[11px] text-text-muted">
        <span class="material-symbols-outlined text-sm shrink-0">tips_and_updates</span>
        <span>
          Click a chip's name to select it. Its
          <span class="font-semibold text-text-main">+</span> adds it to the list,
          <span class="font-semibold text-text-main">trash</span> removes it, and the
          <span class="font-semibold text-text-main">flask</span> tests it.
        </span>
      </p>
      {#if busy && models.length === 0}
        <!-- The panel opens before the fetch resolves, so the wait is a skeleton
             inside it rather than an inert button outside it. -->
        <div class="flex flex-wrap gap-1.5" aria-busy="true" aria-label="Fetching models">
          {#each Array(18) as _, i (i)}
            <span class="h-6 w-36 animate-pulse rounded-md bg-surface-3" style="opacity: {1 - i * 0.04}"></span>
          {/each}
        </div>
      {:else if filtered.length === 0}
        <div class="flex flex-col items-center gap-1 py-12 text-center">
          <span class="material-symbols-outlined text-2xl text-text-subtle">search_off</span>
          <p class="text-xs text-text-muted">No models found</p>
          {#if search.trim() || stateFilter !== 'all'}
            <button
              type="button"
              onclick={() => { onSearch(''); stateFilter = 'all' }}
              class="mt-1 text-[11px] font-medium text-brand-500 hover:underline cursor-pointer"
            >
              Reset search and filter
            </button>
          {/if}
        </div>
      {:else}
        <div class="flex flex-wrap gap-1.5">
          {#each visible as m (m.id)}
            {@const isActive = activeIds.has(m.id)}
            {@const isSelected = selectedIds.has(m.id)}
            {@const status = testStatuses[m.id]}
            {@const err = testErrors[m.id]}
            {@const latency = testLatency[m.id]}
            <div
              class="flex items-center gap-1 rounded-md border py-1 pl-2 pr-1 font-mono text-xs transition-colors {isSelected
                ? 'border-primary bg-primary/10 text-text-main ring-1 ring-primary/40'
                : isActive
                  ? 'border-green-500/50 bg-green-500/10 text-text-main'
                  : 'border-border bg-background text-text-main hover:border-primary/40'}"
              title={err || (latency !== undefined ? `${latency} ms` : undefined)}
            >
              <!-- The chip body toggles selection; the buttons inside it act on
                   the model, so a click on +/- never both selects and writes. -->
              <button
                type="button"
                onclick={() => toggleChip(m.id)}
                class="flex min-w-0 items-center gap-1 cursor-pointer"
                title={isSelected ? 'Deselect this model' : 'Select this model'}
                aria-pressed={isSelected}
              >
                <span class="material-symbols-outlined text-sm {isActive ? 'text-green-500' : 'text-text-muted'}">{kindIcon(m.id)}</span>
                <span class="max-w-[240px] truncate" class:font-bold={isSelected}>{m.id}</span>
              </button>
              <!-- Latency is kept beside the id, the same number the latency
                   sort orders by: a chip that reads "fast" only in a tooltip
                   hides the one number the operator is choosing on. -->
              {#if latency !== undefined}
                <span
                  class="shrink-0 rounded bg-surface-2 px-1 py-px font-sans text-[10px] font-semibold tabular-nums {latencyClass(latency)}"
                  title="Round-trip {latency} ms"
                >{latency >= 1000 ? `${(latency / 1000).toFixed(1)}s` : `${latency}ms`}</span>
              {/if}
              {#if isActive}
                <span class="rounded bg-green-500/20 px-1 py-px font-sans text-[10px] font-semibold text-green-600 dark:text-green-400">Added</span>
              {/if}
              <button
                type="button"
                onclick={() => onTest(m.id)}
                disabled={status === 'testing' || busy}
                title="Test model; sends a real completion and consumes account quota"
                aria-label="Test {m.id}"
                class="rounded p-0.5 text-text-muted hover:text-primary cursor-pointer disabled:cursor-wait"
              >
                <span class="material-symbols-outlined text-sm" style={status === 'testing' ? 'animation: spin 1s linear infinite' : undefined}>
                  {status === 'testing' ? 'progress_activity' : status === 'ok' ? 'check_circle' : status === 'error' || status === 'blocked' ? 'cancel' : 'science'}
                </span>
              </button>
              {#if isActive}
                <button
                  type="button"
                  onclick={() => onRemove(m.id)}
                  disabled={busy}
                  title="Remove from the list"
                  aria-label="Remove {m.id} from the list"
                  class="rounded p-0.5 text-red-500 hover:bg-red-500/10 cursor-pointer"
                >
                  <span class="material-symbols-outlined text-sm">delete</span>
                </button>
              {:else}
                <button
                  type="button"
                  onclick={() => onAdd(m.id)}
                  disabled={busy}
                  title="Add to the list"
                  aria-label="Add {m.id} to the list"
                  class="rounded p-0.5 text-text-muted hover:bg-brand-500/10 hover:text-brand-500 cursor-pointer"
                >
                  <span class="material-symbols-outlined text-sm">add</span>
                </button>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- Footer: running totals on the left, page control on the right. No Done
         button: the header's X and Escape already close the panel, and a second
         close control in the corner the eye lands on last was a duplicate. -->
    <footer class="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-t border-border-subtle bg-surface-2/40 px-4 py-2 text-[11px] text-text-muted">
      <span><span class="font-semibold text-text-main tabular-nums">{filtered.length}</span> shown</span>
      <span><span class="font-semibold text-green-600 tabular-nums dark:text-green-400">{addedCount}</span> added</span>
      <span><span class="font-semibold text-text-main tabular-nums">{availableCount}</span> available</span>
      {#if testedCount > 0}
        <span class="flex items-center gap-1">
          <span class="material-symbols-outlined text-sm text-green-500">check_circle</span>{passedCount}
          <span class="material-symbols-outlined ml-1 text-sm text-red-500">cancel</span>{failedCount}
        </span>
      {/if}

      {#if pageCount > 1}
        <nav class="ml-auto flex flex-wrap items-center gap-1" aria-label="Pagination">
          <button
            type="button"
            onclick={() => (page = Math.max(1, page - 1))}
            disabled={page <= 1}
            title="Previous page"
            aria-label="Previous page"
            class="inline-flex h-7 items-center gap-0.5 rounded-md border border-border bg-surface px-2 text-[11px] font-medium text-text-main transition-colors hover:bg-surface-3 disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer"
          >
            <span class="material-symbols-outlined text-sm">chevron_left</span>
            Prev
          </button>

          {#each pageItems as item, i (i)}
            {#if item === 'gap'}
              <span class="px-0.5 text-text-subtle" aria-hidden="true">…</span>
            {:else}
              <button
                type="button"
                onclick={() => (page = item as number)}
                aria-current={page === item ? 'page' : undefined}
                aria-label="Page {item}"
                class="inline-flex h-7 min-w-7 items-center justify-center rounded-md border px-1.5 text-[11px] font-medium tabular-nums transition-colors cursor-pointer {page ===
                item
                  ? 'border-brand-500 bg-brand-500/15 text-brand-600 dark:text-brand-400'
                  : 'border-border bg-surface text-text-main hover:bg-surface-3'}"
              >
                {item}
              </button>
            {/if}
          {/each}

          <button
            type="button"
            onclick={() => (page = Math.min(pageCount, page + 1))}
            disabled={page >= pageCount}
            title="Next page"
            aria-label="Next page"
            class="inline-flex h-7 items-center gap-0.5 rounded-md border border-border bg-surface px-2 text-[11px] font-medium text-text-main transition-colors hover:bg-surface-3 disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer"
          >
            Next
            <span class="material-symbols-outlined text-sm">chevron_right</span>
          </button>

          <!-- Jump straight to a page: on an 8-page catalogue the window hides
               middle pages, so typing the number is the direct route. -->
          <label class="flex items-center gap-1 pl-1 text-[11px] text-text-muted">
            <span class="hidden sm:inline">Page</span>
            <input
              type="number"
              min="1"
              max={pageCount}
              value={page}
              onchange={(e) => {
                const n = Number((e.target as HTMLInputElement).value)
                if (Number.isFinite(n)) page = Math.min(pageCount, Math.max(1, Math.trunc(n)))
                ;(e.target as HTMLInputElement).value = String(page)
              }}
              aria-label="Go to page"
              class="h-7 w-12 rounded-md border border-border bg-background px-1 text-center text-[11px] tabular-nums text-text-main focus:border-primary focus:outline-none"
            />
            <span class="tabular-nums">/ {pageCount}</span>
          </label>
        </nav>
      {:else if filtered.length > 0}
        <span class="ml-auto tabular-nums text-text-muted">
          {IMPORT_PREVIEW_PAGE_SIZE} per page · 1 page
        </span>
      {/if}
    </footer>
  </div>
</div>
