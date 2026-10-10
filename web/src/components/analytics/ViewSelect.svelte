<!--
  ViewSelect — the sub-view picker inside a section header.

  Cache Analytics kept Prompt Cache and Semantic Cache in a two-button pill
  strip. With the section picker, the refresh and export controls, and the window
  selector sharing that row, the strip was the widest fixed-width thing left in
  the header, so it became a dropdown here like the pickers it sits beside
  (issue #209). The closed control keeps the active view's icon and label, so the
  section still says which view is on screen.

  The panel is placed by lib/ui/menuPosition: measured against the trigger and
  clamped to the viewport, so it stays on screen inside the section's scrolling
  header (issue #224).
-->
<script lang="ts">
  import { maxPanelWidth, placePanel, placementStyle } from '../../lib/ui/menuPosition'
  import type { ViewOption } from './types'

  interface Props {
    value: string
    options: ViewOption[]
    ariaLabel?: string
    onChange?: (view: string) => void
  }

  let { value, options, ariaLabel = 'View', onChange = () => {} }: Props = $props()

  let open = $state(false)
  let trigger: HTMLButtonElement | null = $state(null)
  let panel: HTMLDivElement | null = $state(null)
  let panelStyle = $state('')

  const current = $derived(options.find((o) => o.value === value) ?? options[0])

  function select(next: string) {
    if (next !== value) onChange(next)
    open = false
  }

  function place(): void {
    if (!trigger || !panel) return
    panelStyle = `${placementStyle(
      placePanel({
        align: 'right',
        rect: trigger.getBoundingClientRect(),
        panelWidth: panel.offsetWidth,
      }),
    )};max-width:${maxPanelWidth()}`
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      open = false
      trigger?.focus()
      return
    }
    // A menu left open behind the next control makes the following focusable
    // element unreachable, so Tab closes it instead.
    if (event.key === 'Tab') open = false
  }

  $effect(() => {
    if (!open) return
    function handleDocClick(e: MouseEvent): void {
      const target = e.target as HTMLElement | null
      if (!trigger?.contains(target) && !panel?.contains(target)) open = false
    }
    function reposition(): void {
      place()
    }
    document.addEventListener('click', handleDocClick)
    document.addEventListener('keydown', onKeydown)
    window.addEventListener('resize', reposition)
    window.addEventListener('scroll', reposition, true)
    return () => {
      document.removeEventListener('click', handleDocClick)
      document.removeEventListener('keydown', onKeydown)
      window.removeEventListener('resize', reposition)
      window.removeEventListener('scroll', reposition, true)
    }
  })

  $effect(() => {
    if (open && panel) place()
  })
</script>

<div class="relative shrink-0">
  <button
    bind:this={trigger}
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
      bind:this={panel}
      role="listbox"
      aria-label={ariaLabel}
      tabindex="-1"
      style={panelStyle}
      onkeydown={onKeydown}
      class="fixed z-50 rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)]"
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