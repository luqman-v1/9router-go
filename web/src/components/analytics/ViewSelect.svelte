<!--
  ViewSelect — the sub-view picker inside a section header.

  Cache Analytics kept Prompt Cache and Semantic Cache in a two-button pill
  strip. With the section picker, the refresh and export controls, and the window
  selector sharing that row, the strip was the widest fixed-width thing left in
  the header, so it became a dropdown here like the pickers it sits beside
  (issue #209). The closed control keeps the active view's icon and label, so the
  section still says which view is on screen.
-->
<script lang="ts">
  import type { ViewOption } from './types'

  interface Props {
    value: string
    options: ViewOption[]
    ariaLabel?: string
    onChange?: (view: string) => void
  }

  let { value, options, ariaLabel = 'View', onChange = () => {} }: Props = $props()

  let open = $state(false)
  let root: HTMLDivElement | null = $state(null)

  const current = $derived(options.find((o) => o.value === value) ?? options[0])

  function select(next: string) {
    if (next !== value) onChange(next)
    open = false
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      open = false
      return
    }
    // A menu left open behind the next control makes the following focusable
    // element unreachable, so Tab closes it instead.
    if (event.key === 'Tab') open = false
  }

  $effect(() => {
    if (!open) return
    function handleDocClick(e: MouseEvent): void {
      if (!root?.contains(e.target as HTMLElement | null)) open = false
    }
    document.addEventListener('click', handleDocClick)
    return () => document.removeEventListener('click', handleDocClick)
  })
</script>

<div class="relative shrink-0" bind:this={root}>
  <button
    type="button"
    aria-haspopup="listbox"
    aria-expanded={open}
    onclick={() => (open = !open)}
    onkeydown={onKeydown}
    class="inline-flex max-w-full items-center gap-2 rounded-xl border border-border bg-surface px-3 py-1.5 text-xs font-medium text-text-main shadow-sm transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 sm:text-sm"
  >
    <span class="material-symbols-outlined text-[18px] text-brand-500" aria-hidden="true">
      {current.icon}
    </span>
    <span class="truncate">{current.label}</span>
    <span class="material-symbols-outlined text-[16px] text-text-muted" aria-hidden="true">expand_more</span>
  </button>

  {#if open}
    <div
      role="listbox"
      aria-label={ariaLabel}
      tabindex="-1"
      onkeydown={onKeydown}
      class="absolute right-0 top-full z-30 mt-1 w-56 rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)]"
    >
      {#each options as o (o.value)}
        <button
          type="button"
          role="option"
          aria-selected={value === o.value}
          onclick={() => select(o.value)}
          class="flex w-full items-center justify-between gap-2 rounded-lg px-3 py-2 text-left text-xs text-text-main transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 sm:text-sm"
        >
          <span class="flex min-w-0 items-center gap-2">
            <span class="material-symbols-outlined text-[18px] text-text-muted" aria-hidden="true">
              {o.icon}
            </span>
            <span class="truncate">{o.label}</span>
          </span>
          {#if value === o.value}
            <span class="material-symbols-outlined text-[16px] text-brand-500" aria-hidden="true">check</span>
          {/if}
        </button>
      {/each}
    </div>
  {/if}
</div>