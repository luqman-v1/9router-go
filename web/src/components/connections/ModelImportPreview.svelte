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
    importPreviewSelectedIds,
    IMPORT_PREVIEW_SORTS,
    IMPORT_PREVIEW_STATE_FILTERS,
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

  // Render ceiling. A 460-model catalog draws ~3,000 DOM nodes if every chip
  // mounts at once, which is a phone-unfriendly first paint on the small box
  // this dashboard serves from. The search is the way to reach the rest: it
  // filters the full list client-side, so nothing is unreachable: the tail
  // is one query away.
  const CHIP_RENDER_LIMIT = 60
  let visibleCount = $state(CHIP_RENDER_LIMIT)
  let visible = $derived(filtered.slice(0, visibleCount))
  let hiddenCount = $derived(filtered.length - visible.length)

  // A new search, filter, sort or catalog starts from the top of the list.
  $effect(() => {
    void search
    void models
    void sort
    void stateFilter
    visibleCount = CHIP_RENDER_LIMIT
  })

  let addableIds = $derived(filtered.filter((m) => !activeIds.has(m.id)).map((m) => m.id))
  let removableIds = $derived(filtered.filter((m) => activeIds.has(m.id)).map((m) => m.id))

  let selection = $derived(importPreviewSelectionState(selectedIds, filtered))
  let selectedList = $derived(importPreviewSelectedIds(selectedIds, filtered))
  let selectedAddable = $derived(selectedList.filter((id) => !activeIds.has(id)))
  let selectedRemovable = $derived(selectedList.filter((id) => activeIds.has(id)))

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
    class="relative flex max-h-[85vh] w-full max-w-3xl flex-col overflow-hidden rounded-[14px] border border-border-subtle bg-surface shadow-[var(--shadow-elevated)] fade-in"
    role="dialog"
    aria-modal="true"
    aria-labelledby="import-preview-title"
    onkeydown={(e) => { if (e.key === 'Escape') onClose() }}
  >
    <!-- Header: identity on the left, search + close on the right. -->
    <header class="flex shrink-0 flex-wrap items-center gap-2 border-b border-border-subtle px-4 py-3">
      <span class="material-symbols-outlined text-base text-brand-500">download_for_offline</span>
      <h2 id="import-preview-title" class="shrink-0 whitespace-nowrap text-sm font-semibold text-text-main">
        Preview Fetch Models
      </h2>
      <span class="shrink-0 rounded-full bg-surface-2 px-2 py-0.5 text-[11px] font-semibold tabular-nums text-text-muted">
        {models.length}
      </span>
      <!-- On a narrow viewport the search takes a row of its own instead of
           being squeezed to an icon: it is the primary control in a 460-model
           panel, and a 60px box reads as decoration. -->
      <div class="relative order-last w-full min-w-0 sm:order-none sm:ml-auto sm:w-auto sm:flex-1 sm:max-w-72">
        <span class="material-symbols-outlined pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-sm text-text-muted">search</span>
        <input
          type="text"
          bind:this={searchInput}
          value={search}
          oninput={(e) => onSearch((e.target as HTMLInputElement).value)}
          placeholder="Search models (e.g. dee, flash)..."
          aria-label="Search models by id"
          class="h-8 w-full rounded-md border border-border bg-background pl-7 pr-7 text-xs text-text-main placeholder:text-text-muted focus:border-primary focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
        />
        {#if search.trim()}
          <button
            type="button"
            onclick={() => onSearch('')}
            title="Clear the search"
            class="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-0.5 text-text-muted hover:text-text-main cursor-pointer"
          >
            <span class="material-symbols-outlined text-sm">close</span>
          </button>
        {/if}
      </div>
      <button
        type="button"
        onclick={onClose}
        title="Close the preview panel"
        aria-label="Close the preview panel"
        class="rounded p-1 text-text-muted hover:bg-surface-2 hover:text-text-main cursor-pointer"
      >
        <span class="material-symbols-outlined text-base">close</span>
      </button>
    </header>

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

      <label class="flex items-center gap-1 text-[11px] text-text-muted">
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

      <label class="ml-auto flex items-center gap-1.5 text-[11px] text-text-muted cursor-pointer">
        <input
          type="checkbox"
          checked={selection.allSelected}
          indeterminate={selection.indeterminate}
          onchange={(e) => (selectedIds = toggleImportPreviewSelectAll(selectedIds, filtered, (e.target as HTMLInputElement).checked))}
          class="size-3.5 shrink-0 accent-brand-500 cursor-pointer"
        />
        Select all ({filtered.length})
      </label>
    </div>

    <!-- Batch bar: acts on the filtered set (not the whole catalogue), which is
         the selective import this panel exists for. The whole-list case is just
         an empty search. -->
    <div class="shrink-0 border-b border-border-subtle bg-surface-2/40 px-4 py-2">
      {#if selection.selected > 0}
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-[11px] font-semibold text-text-main">{selection.selected} selected</span>
          <button
            type="button"
            onclick={() => onTestSelected(selectedList)}
            disabled={testing || busy}
            title="Test every selected model; each probe consumes account quota"
            class="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-surface px-2 text-[11px] font-medium text-text-main transition-colors hover:bg-surface-3 disabled:opacity-50 cursor-pointer"
          >
            <span class="material-symbols-outlined text-sm" style={testing ? 'animation: spin 1s linear infinite' : undefined}>
              {testing ? 'progress_activity' : 'science'}
            </span>
            {testing ? 'Testing...' : `Test selected (${selectedList.length})`}
          </button>
          {#if selectedAddable.length > 0}
            <button
              type="button"
              onclick={() => onAddAll(selectedAddable)}
              disabled={busy}
              title="Add the {selectedAddable.length} selected models to the list"
              class="inline-flex h-7 items-center gap-1 rounded-md border border-brand-500/40 bg-brand-500/10 px-2 text-[11px] font-medium text-brand-600 transition-colors hover:bg-brand-500/20 disabled:opacity-50 cursor-pointer dark:text-brand-400"
            >
              <span class="material-symbols-outlined text-sm">library_add</span>
              Add selected ({selectedAddable.length})
            </button>
          {/if}
          {#if selectedRemovable.length > 0}
            <button
              type="button"
              onclick={() => onRemoveAll(selectedRemovable)}
              disabled={busy}
              title="Remove the {selectedRemovable.length} selected models from the list"
              class="inline-flex h-7 items-center gap-1 rounded-md border border-red-500/40 bg-red-500/10 px-2 text-[11px] font-medium text-red-600 transition-colors hover:bg-red-500/20 disabled:opacity-50 cursor-pointer dark:text-red-400"
            >
              <span class="material-symbols-outlined text-sm">delete_sweep</span>
              Remove selected ({selectedRemovable.length})
            </button>
          {/if}
          <button
            type="button"
            onclick={clearSelection}
            class="ml-auto inline-flex h-7 items-center gap-1 rounded-md px-2 text-[11px] text-text-muted hover:text-text-main cursor-pointer"
            title="Clear the selection"
          >
            <span class="material-symbols-outlined text-sm">deselect</span>
            Clear
          </button>
        </div>
      {:else}
        <div class="flex flex-wrap items-center gap-2">
          {#if addableIds.length > 0}
            <button
              type="button"
              onclick={() => onAddAll(addableIds)}
              disabled={busy}
              title="Add all {addableIds.length} models matching the current filter"
              class="inline-flex h-7 items-center gap-1 rounded-md border border-brand-500/40 bg-brand-500/10 px-2.5 text-[11px] font-medium text-brand-600 transition-colors hover:bg-brand-500/20 disabled:opacity-50 cursor-pointer dark:text-brand-400"
            >
              <span class="material-symbols-outlined text-sm">library_add</span>
              Add all ({addableIds.length})
            </button>
          {/if}
          {#if removableIds.length > 0}
            <button
              type="button"
              onclick={() => onRemoveAll(removableIds)}
              disabled={busy}
              title="Remove all {removableIds.length} models matching the current filter"
              class="inline-flex h-7 items-center gap-1 rounded-md border border-red-500/40 bg-red-500/10 px-2.5 text-[11px] font-medium text-red-600 transition-colors hover:bg-red-500/20 disabled:opacity-50 cursor-pointer dark:text-red-400"
            >
              <span class="material-symbols-outlined text-sm">delete_sweep</span>
              Remove all ({removableIds.length})
            </button>
          {/if}
          <span class="ml-auto text-[11px] text-text-muted">
            Click a chip to select it, or use its + and trash buttons
          </span>
        </div>
      {/if}
    </div>

    <p class="sr-only" aria-live="polite">{testSummary}</p>

    <!-- Body: the only scrolling region. -->
    <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
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
          {#if hiddenCount > 0}
            <button
              type="button"
              onclick={() => (visibleCount += CHIP_RENDER_LIMIT)}
              title="Show {hiddenCount} more models"
              class="rounded-md border border-dashed border-border px-3 py-1 text-xs text-text-muted hover:border-primary hover:text-primary cursor-pointer"
            >
              Show {Math.min(CHIP_RENDER_LIMIT, hiddenCount)} more ({hiddenCount} left)
            </button>
          {/if}
        </div>
      {/if}
    </div>

    <!-- Footer: the running totals, so the operator can see what the panel did
         to the list without closing it. -->
    <footer class="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-t border-border-subtle bg-surface-2/40 px-4 py-2 text-[11px] text-text-muted">
      <span><span class="font-semibold text-text-main tabular-nums">{filtered.length}</span> shown</span>
      <span><span class="font-semibold text-green-600 tabular-nums dark:text-green-400">{addedCount}</span> added</span>
      <span><span class="font-semibold text-text-main tabular-nums">{availableCount}</span> available</span>
      {#if testedCount > 0}
        <span class="flex items-center gap-1">
          <span class="material-symbols-outlined text-sm text-green-500">check_circle</span>{passedCount}
          <span class="material-symbols-outlined ml-1 text-sm text-red-500">cancel</span>{failedCount}
        </span>
      {/if}
      <button
        type="button"
        onclick={onClose}
        class="ml-auto inline-flex h-7 items-center gap-1 rounded-md border border-border bg-surface px-2.5 text-[11px] font-medium text-text-main hover:bg-surface-3 cursor-pointer"
      >
        Done
      </button>
    </footer>
  </div>
</div>
