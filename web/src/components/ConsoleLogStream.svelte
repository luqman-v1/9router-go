<script lang="ts">
  // Renders the console buffer: filter by level, search, and read the rows.
  // The server stamps each row's level (internal/log.ConsoleEntry), so colour
  // follows the emitted severity — never a guess made from the message text.
  import { ArrowDown, Copy, Download, Search, X } from 'lucide-svelte'
  import Button from '../lib/ui/Button.svelte'
  import { notifications } from '../lib/notifications'
  import {
    countByLevel,
    downloadFilename,
    filterEntries,
    formatClock,
    splitTag,
    toPlainText,
    type ConsoleEntry,
    type LogLevel,
  } from '../lib/consoleLog'

  interface Props {
    entries: ConsoleEntry[]
  }

  let { entries }: Props = $props()

  // Paired light/dark values: each pair is checked against the panel surface
  // for 4.5:1 contrast, so a level stays legible in both themes.
  const LEVEL_META: Record<LogLevel, { label: string; text: string; chip: string; dot: string }> = {
    error: {
      label: 'ERR',
      text: 'text-red-700 dark:text-red-400',
      chip: 'border-red-500/40 bg-red-500/10 text-red-700 dark:text-red-400',
      dot: 'bg-red-500',
    },
    warn: {
      label: 'WRN',
      text: 'text-amber-800 dark:text-amber-400',
      chip: 'border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-400',
      dot: 'bg-amber-500',
    },
    info: {
      label: 'INF',
      text: 'text-emerald-700 dark:text-emerald-400',
      chip: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
      dot: 'bg-emerald-500',
    },
    debug: {
      label: 'DBG',
      text: 'text-sky-800 dark:text-sky-300',
      chip: 'border-sky-500/40 bg-sky-500/10 text-sky-800 dark:text-sky-300',
      dot: 'bg-sky-500',
    },
  }

  const LEVEL_ORDER: LogLevel[] = ['error', 'warn', 'info', 'debug']

  let query = $state('')
  let wrap = $state(false)
  let following = $state(true)
  let mutedLevels = $state<Set<LogLevel>>(new Set())
  let viewport = $state<HTMLDivElement | null>(null)

  const counts = $derived(countByLevel(entries))
  const kept = $derived(entries.filter((entry) => !mutedLevels.has(entry.level)))
  const visible = $derived(filterEntries(kept, query))

  function toggleLevel(level: LogLevel) {
    const next = new Set(mutedLevels)
    if (next.has(level)) next.delete(level)
    else next.add(level)
    mutedLevels = next
  }

  function scrollToLatest() {
    if (viewport) viewport.scrollTop = viewport.scrollHeight
  }

  // Scrolling up is how an operator pins the line they are reading; yanking
  // the view back down mid-inspection loses it.
  function handleScroll() {
    if (!viewport) return
    following = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight < 24
  }

  async function copyVisible() {
    try {
      await navigator.clipboard.writeText(toPlainText(visible))
      notifications.success(`Copied ${visible.length} log ${visible.length === 1 ? 'line' : 'lines'}`)
    } catch {
      notifications.error('Clipboard is unavailable in this browser')
    }
  }

  function downloadVisible() {
    const blob = new Blob([toPlainText(visible)], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = downloadFilename()
    link.click()
    URL.revokeObjectURL(url)
    notifications.success(`Downloaded ${visible.length} log ${visible.length === 1 ? 'line' : 'lines'}`)
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && query) {
      query = ''
      e.preventDefault()
    }
  }

  $effect(() => {
    void entries.length
    if (following && viewport) viewport.scrollTop = viewport.scrollHeight
  })
</script>

<svelte:window onkeydown={handleKeydown} />

<div class="flex flex-col">
  <div class="flex flex-wrap items-center gap-2 border-b border-border-subtle px-4 py-2.5">
    <!-- Level counts double as filters: the operator's first question on this
         page is "how many of these went wrong". -->
    <div class="flex flex-wrap items-center gap-1.5">
      {#each LEVEL_ORDER as level (level)}
        {@const meta = LEVEL_META[level]}
        {@const muted = mutedLevels.has(level)}
        <button
          type="button"
          aria-pressed={!muted}
          title={muted ? `Show ${meta.label} lines` : `Hide ${meta.label} lines`}
          onclick={() => toggleLevel(level)}
          class="inline-flex h-11 sm:h-6 items-center gap-1.5 rounded-full border px-3 sm:px-2 text-[11px] font-semibold transition-colors duration-150 {meta.chip} {muted
            ? 'opacity-40'
            : ''}"
        >
          <span class="h-1.5 w-1.5 rounded-full {meta.dot}"></span>
          {meta.label}
          <span class="tabular-nums font-normal opacity-80">{counts[level]}</span>
        </button>
      {/each}
    </div>

    <div class="relative ml-auto w-full sm:w-64">
      <Search class="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-text-muted" />
      <input
        type="search"
        bind:value={query}
        placeholder="Search lines…"
        aria-label="Search console log lines"
        class="w-full rounded-[8px] border border-border bg-surface py-1.5 pl-8 pr-7 text-xs text-text-main placeholder:text-text-muted focus:border-brand-500 focus:outline-none focus:ring-2 focus:ring-brand-500/30"
      />
      {#if query}
        <button
          type="button"
          aria-label="Clear search"
          onclick={() => (query = '')}
          class="absolute right-2 top-1/2 -translate-y-1/2 cursor-pointer text-text-muted hover:text-text-main"
        >
          <X class="h-3.5 w-3.5" />
        </button>
      {/if}
    </div>

    <button
      type="button"
      aria-pressed={wrap}
      title={wrap ? 'Disable line wrap' : 'Wrap long lines'}
      onclick={() => (wrap = !wrap)}
      class="h-11 sm:h-7 cursor-pointer rounded-[8px] border border-border px-3 sm:px-2.5 text-xs text-text-main transition-colors duration-150 hover:bg-surface-2 {wrap
        ? 'border-brand-500/40 bg-brand-500/10 text-brand-600 dark:text-brand-400'
        : ''}"
    >
      Wrap
    </button>

    <Button size="sm" variant="outline" title="Copy visible lines" disabled={visible.length === 0} onclick={copyVisible}>
      <Copy class="h-3.5 w-3.5" />
      Copy
    </Button>
    <Button size="sm" variant="outline" title="Download visible lines" disabled={visible.length === 0} onclick={downloadVisible}>
      <Download class="h-3.5 w-3.5" />
      Export
    </Button>
  </div>

  <div
    bind:this={viewport}
    onscroll={handleScroll}
    class="custom-scrollbar relative h-[calc(100vh-260px)] min-h-[320px] overflow-y-auto bg-surface-2/50 font-mono text-xs"
  >
    {#if entries.length === 0}
      <div class="flex h-full flex-col items-center justify-center gap-1.5 px-6 text-center">
        <p class="text-sm text-text-main">No console output yet</p>
        <p class="max-w-md text-xs text-text-muted">
          Nothing has been logged since the server started or the last clear. Send a request, or set the level to
          <span class="font-mono">debug</span>, and rows appear here live.
        </p>
      </div>
    {:else if visible.length === 0}
      <div class="flex h-full flex-col items-center justify-center gap-1.5 px-6 text-center">
        <p class="text-sm text-text-main">Nothing matches the current filter</p>
        <p class="max-w-md text-xs text-text-muted">
          {#if mutedLevels.size > 0}
            {[...mutedLevels].map((level) => LEVEL_META[level].label).join(', ')} rows are hidden by the level
            chips.
          {:else}
            No line contains “{query.trim()}”.
          {/if}
        </p>
        <Button
          size="sm"
          variant="outline"
          onclick={() => {
            query = ''
            mutedLevels = new Set()
          }}
        >
          Show all {entries.length} lines
        </Button>
      </div>
    {:else}
      <div class="divide-y divide-border-subtle/60">
        {#each visible as entry (entry.seq)}
          {@const meta = LEVEL_META[entry.level]}
          {@const parts = splitTag(entry.line)}
          <div class="flex items-baseline gap-3 px-3 py-1 hover:bg-surface-3/40">
            <time class="shrink-0 tabular-nums text-[11px] text-text-muted">{formatClock(entry.time)}</time>
            <span class="w-8 shrink-0 text-[11px] font-bold {meta.text}">{meta.label}</span>
            <span class="min-w-0 flex-1 {wrap ? 'break-words' : 'whitespace-pre'} {meta.text}">
              {#if parts}<span class="opacity-70">{parts.tag}</span>{' '}{:else}{/if}{parts?.message ?? entry.line}
            </span>
          </div>
        {/each}
      </div>
    {/if}

    {#if !following && entries.length > 0}
      <button
        type="button"
        onclick={() => {
          following = true
          scrollToLatest()
        }}
        class="absolute bottom-3 left-1/2 inline-flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-border bg-surface px-3 py-1.5 text-xs font-medium text-text-main shadow-[var(--shadow-elev)] transition-colors duration-150 hover:bg-surface-2"
      >
        <ArrowDown class="h-3.5 w-3.5" />
        Jump to latest
      </button>
    {/if}
  </div>

  <div class="flex items-center justify-between border-t border-border-subtle px-4 py-2 text-[11px] text-text-muted">
    <span class="tabular-nums">
      Showing {visible.length} of {entries.length} buffered {entries.length === 1 ? 'line' : 'lines'}
    </span>
    <span class="tabular-nums">
      {counts.error} error · {counts.warn} warn · {counts.info} info · {counts.debug} debug
    </span>
  </div>
</div>