<script lang="ts">
  // Staging panel for "Import from /models": fetches a provider's catalog
  // without writing any rows, lets the operator search, sort, select, test and
  // add or remove models, one chip at a time, or as a batch.
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
    sortImportPreviewModels,
    importPreviewSelectionState,
    toggleImportPreviewSelectAll,
    importPreviewSelectedIds,
    IMPORT_PREVIEW_SORTS,
    type ImportPreviewModel,
    type ImportPreviewSort,
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
  let selectedIds = $state<Set<string>>(new Set())

  let filtered = $derived(
    sortImportPreviewModels(filterImportPreviewModels(models, search), sort, testLatency)
  )

  // Render ceiling. A 460-model catalog draws ~3,000 DOM nodes if every chip
  // mounts at once, which is a phone-unfriendly first paint on the small box
  // this dashboard serves from. The search is the way to reach the rest: it
  // filters the full list client-side, so nothing is unreachable: the tail
  // is one query away.
  const CHIP_RENDER_LIMIT = 60
  let visibleCount = $state(CHIP_RENDER_LIMIT)
  let visible = $derived(filtered.slice(0, visibleCount))
  let hiddenCount = $derived(filtered.length - visible.length)

  // A new search, sort or catalog starts from the top of the list again.
  $effect(() => {
    void search
    void models
    void sort
    visibleCount = CHIP_RENDER_LIMIT
  })

  let addableIds = $derived(filtered.filter((m) => !activeIds.has(m.id)).map((m) => m.id))
  let removableIds = $derived(filtered.filter((m) => activeIds.has(m.id)).map((m) => m.id))

  let selection = $derived(importPreviewSelectionState(selectedIds, filtered))
  let selectedList = $derived(importPreviewSelectedIds(selectedIds, filtered))
  let selectedAddable = $derived(selectedList.filter((id) => !activeIds.has(id)))
  let selectedRemovable = $derived(selectedList.filter((id) => activeIds.has(id)))

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
  let testSummary = $derived.by(() => {
    const done = Object.values(testStatuses).filter((s) => s !== 'testing')
    const ok = done.filter((s) => s === 'ok').length
    const failed = done.filter((s) => s === 'error' || s === 'blocked').length
    return `${ok} passed, ${failed} failed of ${done.length} tested`
  })

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
    class="absolute inset-0 bg-black/50 backdrop-blur-[2px] fade-in"
    onclick={onClose}
    role="presentation"
  ></div>
  <div
    class="relative w-full max-w-2xl bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-elev)] fade-in p-5"
    role="dialog"
    aria-label="Preview Fetch Models"
    onkeydown={(e) => { if (e.key === 'Escape') onClose() }}
  >
  <div class="mb-2 flex items-center gap-2">
    <input
      type="checkbox"
      checked={selection.allSelected}
      indeterminate={selection.indeterminate}
      onchange={(e) => (selectedIds = toggleImportPreviewSelectAll(selectedIds, filtered, (e.target as HTMLInputElement).checked))}
      title="Select every model matching the current filter"
      class="size-3.5 shrink-0 accent-brand-500 cursor-pointer"
      aria-label="Select every model matching the current filter"
    />
    <span class="text-sm font-semibold text-text-main">
      Preview Fetch Models ({filtered.length}{search.trim() ? ` of ${models.length}` : ''})
    </span>
    <div class="relative ml-auto">
      <span class="material-symbols-outlined pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-sm text-text-muted">search</span>
      <input
        type="text"
        bind:this={searchInput}
        value={search}
        oninput={(e) => onSearch((e.target as HTMLInputElement).value)}
        placeholder="Search model name (e.g. dee, flash)..."
        class="h-7 w-56 rounded-md border border-border bg-background pl-7 pr-2 text-xs text-text-main placeholder:text-text-muted focus:border-primary focus:outline-none"
      />
    </div>
    <select
      value={sort}
      onchange={(e) => (sort = (e.target as HTMLSelectElement).value as ImportPreviewSort)}
      title="Sort the list"
      aria-label="Sort the list"
      class="h-7 rounded-md border border-border bg-background px-1.5 text-xs text-text-main focus:border-primary focus:outline-none cursor-pointer"
    >
      {#each IMPORT_PREVIEW_SORTS as opt (opt.value)}
        <option value={opt.value}>{opt.label}</option>
      {/each}
    </select>
    <button
      type="button"
      onclick={onClose}
      title="Close the preview panel"
      class="rounded p-1 text-text-muted hover:bg-sidebar hover:text-text-main cursor-pointer"
    >
      <span class="material-symbols-outlined text-base">close</span>
    </button>
  </div>
  <!-- Selection bar: appears only when something is selected, so the header
       stays quiet in the common one-chip-at-a-time case. -->
  {#if selection.selected > 0}
    <div class="mb-2 flex flex-wrap items-center gap-2 rounded-lg border border-primary/30 bg-primary/5 px-2.5 py-1.5">
      <span class="text-xs font-medium text-text-main">{selection.selected} selected</span>
      <button
        type="button"
        onclick={() => onTestSelected(selectedList)}
        disabled={testing || busy}
        title="Test every selected model; each probe consumes account quota"
        class="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-surface-2 px-2 text-xs font-medium text-text-main transition-colors hover:bg-surface-3 disabled:opacity-50 cursor-pointer"
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
          class="inline-flex h-7 items-center gap-1 rounded-md border border-brand-500/40 bg-brand-500/10 px-2 text-xs font-medium text-brand-600 transition-colors hover:bg-brand-500/20 disabled:opacity-50 cursor-pointer dark:text-brand-400"
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
          class="inline-flex h-7 items-center gap-1 rounded-md border border-red-500/40 bg-red-500/10 px-2 text-xs font-medium text-red-600 transition-colors hover:bg-red-500/20 disabled:opacity-50 cursor-pointer dark:text-red-400"
        >
          <span class="material-symbols-outlined text-sm">delete_sweep</span>
          Remove selected ({selectedRemovable.length})
        </button>
      {/if}
      <button
        type="button"
        onclick={clearSelection}
        class="ml-auto inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-text-muted hover:text-text-main cursor-pointer"
        title="Kosongkan pilihan"
      >
        <span class="material-symbols-outlined text-sm">deselect</span>
        Clear
      </button>
    </div>
  {/if}
  <!-- Bulk actions act on the filtered set, not the whole catalog: "add all"
       after a search adds what the search matched, which is the selective
       import this panel exists for. The whole-list case is just an empty
       search. -->
  {#if filtered.length > 0 && selection.selected === 0}
    <div class="mb-2 flex items-center gap-2">
      {#if addableIds.length > 0}
        <button
          type="button"
          onclick={() => onAddAll(addableIds)}
          disabled={busy}
          title="Add all {addableIds.length} models matching the filter to the list"
          class="inline-flex h-7 items-center gap-1 rounded-md border border-brand-500/40 bg-brand-500/10 px-2.5 text-xs font-medium text-brand-600 transition-colors hover:bg-brand-500/20 disabled:opacity-50 cursor-pointer dark:text-brand-400"
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
          title="Remove all {removableIds.length} models matching the filter from the list"
          class="inline-flex h-7 items-center gap-1 rounded-md border border-red-500/40 bg-red-500/10 px-2.5 text-xs font-medium text-red-600 transition-colors hover:bg-red-500/20 disabled:opacity-50 cursor-pointer dark:text-red-400"
        >
          <span class="material-symbols-outlined text-sm">delete_sweep</span>
          Remove all ({removableIds.length})
        </button>
      {/if}
    </div>
  {/if}
  <p class="sr-only" aria-live="polite">{testSummary}</p>
  {#if filtered.length === 0}
    <p class="py-8 text-center text-xs text-text-muted">No models found</p>
  {:else}
    <div class="flex max-h-[55vh] flex-wrap gap-1.5 overflow-y-auto">
      {#each visible as m (m.id)}
        {@const isActive = activeIds.has(m.id)}
        {@const isSelected = selectedIds.has(m.id)}
        {@const status = testStatuses[m.id]}
        {@const err = testErrors[m.id]}
        {@const latency = testLatency[m.id]}
        <div
          class="flex items-center gap-1 rounded-full border py-0.5 pl-2 pr-1 font-mono text-xs transition-colors {isSelected
            ? 'border-primary ring-1 ring-primary/40 bg-primary/5 text-text-main'
            : isActive
              ? 'border-green-500/50 bg-green-500/10 text-text-main'
              : 'border-border bg-background text-text-main'}"
          title={err || (latency !== undefined ? `${latency} ms` : undefined)}
        >
          <!-- The chip body toggles selection; the buttons inside it act on the
               model, so a click on +/- never both selects and writes. -->
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
          <!-- Latency is kept beside the id, the same number the latency sort
               orders by on the main list: a chip that reads "fast" only in a
               tooltip hides the one number the operator is choosing on. -->
          {#if latency !== undefined}
            <span
              class="shrink-0 rounded-full bg-sidebar px-1.5 py-px font-sans text-[10px] font-semibold tabular-nums {latencyClass(latency)}"
              title="Round-trip {latency} ms"
            >{latency >= 1000 ? `${(latency / 1000).toFixed(1)}s` : `${latency}ms`}</span>
          {/if}
          {#if isActive}
            <span class="rounded-full bg-green-500/20 px-1.5 text-[10px] font-sans font-semibold text-green-600 dark:text-green-400">Added</span>
          {/if}
          <button
            type="button"
            onclick={() => onTest(m.id)}
            disabled={status === 'testing' || busy}
            title="Test model; sends a real completion and consumes account quota"
            class="rounded-full p-0.5 text-text-muted hover:text-primary cursor-pointer disabled:cursor-wait"
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
              class="rounded-full p-0.5 text-red-500 hover:bg-red-500/10 cursor-pointer"
            >
              <span class="material-symbols-outlined text-sm">delete</span>
            </button>
          {:else}
            <button
              type="button"
              onclick={() => onAdd(m.id)}
              disabled={busy}
              title="Add to the list"
              class="rounded-full p-0.5 text-text-muted hover:bg-brand-500/10 hover:text-brand-500 cursor-pointer"
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
          class="rounded-full border border-dashed border-border px-3 py-1 text-xs text-text-muted hover:border-primary hover:text-primary cursor-pointer"
        >
          Show {Math.min(CHIP_RENDER_LIMIT, hiddenCount)} more ({hiddenCount} left)
        </button>
      {/if}
    </div>
  {/if}
  </div>
</div>
