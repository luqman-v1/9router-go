<script lang="ts">
  // Staging panel for "Import from /models": fetches a provider's catalog
  // without writing any rows, lets the operator search it, test individual
  // models and add or remove them from the active list one chip at a time.
  // Upstream parity: the fetch itself is GET /api/providers/[id]/models; the
  // add/remove/test actions reuse the same API calls the two import buttons
  // and the model rows below already use.
  import type { ModelTestStatus } from './modelCheckAll'
  import {
    filterImportPreviewModels,
    type ImportPreviewModel,
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
    onSearch: (q: string) => void
    onClose: () => void
    onAdd: (id: string) => void
    onRemove: (id: string) => void
    onTest: (id: string) => void
  }

  let {
    models,
    activeIds,
    search,
    busy = false,
    testStatuses,
    testErrors,
    testLatency,
    onSearch,
    onClose,
    onAdd,
    onRemove,
    onTest,
  }: Props = $props()

  let filtered = $derived(filterImportPreviewModels(models, search))

  function kindIcon(id: string): string {
    // Mirrors inferModelKind's lanes, kept visual-only: a chip needs a glyph,
    // not the service kind the gateway stores.
    if (/embed/i.test(id)) return 'database'
    if (/tts|speech|audio|voice/i.test(id)) return 'volume_up'
    if (/image|imagen|dall|flux|sdxl|banana/i.test(id)) return 'image'
    if (/veo|sora|video/i.test(id)) return 'movie'
    return 'smart_toy'
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
    <span class="text-sm font-semibold text-text-main">
      Preview Fetch Models ({filtered.length}{search.trim() ? ` of ${models.length}` : ''})
    </span>
    <div class="relative ml-auto">
      <span class="material-symbols-outlined pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-sm text-text-muted">search</span>
      <input
        type="text"
        value={search}
        oninput={(e) => onSearch((e.target as HTMLInputElement).value)}
        placeholder="Cari nama model (mis. dee, flash)..."
        class="h-7 w-56 rounded-md border border-border bg-background pl-7 pr-2 text-xs text-text-main placeholder:text-text-muted focus:border-primary focus:outline-none"
      />
    </div>
    <button
      type="button"
      onclick={onClose}
      title="Tutup panel preview"
      class="rounded p-1 text-text-muted hover:bg-sidebar hover:text-text-main cursor-pointer"
    >
      <span class="material-symbols-outlined text-base">close</span>
    </button>
  </div>
  {#if filtered.length === 0}
    <p class="py-8 text-center text-xs text-text-muted">Model tidak ditemukan</p>
  {:else}
    <div class="flex max-h-[55vh] flex-wrap gap-1.5 overflow-y-auto">
      {#each filtered as m (m.id)}
        {@const isActive = activeIds.has(m.id)}
        {@const status = testStatuses[m.id]}
        {@const err = testErrors[m.id]}
        {@const latency = testLatency[m.id]}
        <div
          class="flex items-center gap-1 rounded-full border py-0.5 pl-2 pr-1 font-mono text-xs transition-colors {isActive
            ? 'border-green-500/50 bg-green-500/10 text-text-main'
            : 'border-border bg-background text-text-main'}"
          title={err || (latency !== undefined ? `${latency} ms` : undefined)}
        >
          <span class="material-symbols-outlined text-sm {isActive ? 'text-green-500' : 'text-text-muted'}">{kindIcon(m.id)}</span>
          <span class="max-w-[240px] truncate">{m.id}</span>
          {#if isActive}
            <span class="rounded-full bg-green-500/20 px-1.5 text-[10px] font-sans font-semibold text-green-600 dark:text-green-400">Ada</span>
          {/if}
          <button
            type="button"
            onclick={() => onTest(m.id)}
            disabled={status === 'testing' || busy}
            title="Test model — sends a real completion and consumes account quota"
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
              title="Hapus dari daftar"
              class="rounded-full p-0.5 text-red-500 hover:bg-red-500/10 cursor-pointer"
            >
              <span class="material-symbols-outlined text-sm">delete</span>
            </button>
          {:else}
            <button
              type="button"
              onclick={() => onAdd(m.id)}
              disabled={busy}
              title="Tambah ke daftar"
              class="rounded-full p-0.5 text-text-muted hover:bg-brand-500/10 hover:text-brand-500 cursor-pointer"
            >
              <span class="material-symbols-outlined text-sm">add</span>
            </button>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
  </div>
</div>
