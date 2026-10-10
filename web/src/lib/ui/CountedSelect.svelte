<!--
  CountedSelect — a dropdown whose options each carry a count.

  The proxy-pool toolbar rendered its status filter as a strip of four pills
  (All / Active / Passed / Failed), each with its count in brackets. Beside the
  sort picker and the two bulk-cleanup buttons that row was the widest fixed
  thing left on the page, and it wrapped to three lines on a phone (issue #261).
  Collapsing it to one control is what this primitive is for.

  It is the same dropdown as analytics/ViewSelect — measured trigger, viewport-
  clamped panel, portalled, Escape/Tab/outside-click dismissal — differing only
  in what each row carries: a count instead of a glyph. Analytics keeps its own
  component because an option there is a *view*, and naming that differently
  from "the one under the header" is not worth a third copy of this logic.
-->
<script lang="ts">
  import { maxPanelWidth, placePanel, placementStyle } from './menuPosition'
  import { portal } from './portal'

  export interface CountedOption {
    value: string
    label: string
    count: number
  }

  interface Props {
    value: string
    options: CountedOption[]
    ariaLabel?: string
    /** Panel alignment against the trigger. Defaults to the left edge, which
        is where a filter that starts a row belongs. */
    align?: 'left' | 'right'
    minWidth?: string
    onChange?: (value: string) => void
  }

  let {
    value,
    options,
    ariaLabel = 'Filter',
    align = 'left',
    minWidth = '11rem',
    onChange = () => {},
  }: Props = $props()

  let open = $state(false)
  let trigger: HTMLButtonElement | null = $state(null)
  let panel: HTMLDivElement | null = $state(null)
  let panelStyle = $state('')

  const current = $derived(options.find((o) => o.value === value) ?? options[0])

  function select(next: string) {
    if (next !== value) onChange(next)
    open = false
  }

  /**
   * The floor the caller asked for, capped against the window. CSS `min-width`
   * beats `width`, so an uncapped floor would render wider than the clamp and
   * overflow anyway.
   */
  function panelMinWidth(): string {
    return `min(${minWidth}, ${maxPanelWidth()})`
  }

  function place(): void {
    if (!trigger || !panel) return
    panelStyle = placementStyle(
      placePanel({
        align,
        rect: trigger.getBoundingClientRect(),
        panelWidth: panel.offsetWidth,
      }),
    )
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      open = false
      trigger?.focus()
      return
    }
    // A panel left open behind the next control makes the following focusable
    // element unreachable, so Tab closes it instead.
    if (event.key === 'Tab') open = false
  }

  $effect(() => {
    if (!open) return
    function handleDocClick(e: MouseEvent): void {
      const target = e.target as HTMLElement | null
      if (!trigger?.contains(target) && !panel?.contains(target)) open = false
    }
    // A scroll or a resize moves the trigger out from under a panel placed
    // against the old rect, so both re-measure while the panel is open.
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
    aria-label={ariaLabel}
    onclick={() => (open = !open)}
    onkeydown={onKeydown}
    class="inline-flex max-w-full items-center gap-2 rounded-xl border border-border bg-surface px-3 py-1.5 text-xs font-medium text-text-main shadow-sm transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 sm:text-sm"
  >
    <span class="truncate">{current?.label}</span>
    {#if current}
      <span class="text-text-muted">({current.count})</span>
    {/if}
    <span class="material-symbols-outlined text-[16px] text-text-muted" aria-hidden="true">
      expand_more
    </span>
  </button>

  {#if open}
    <!-- The panel sizes itself: `w-max` to its labels, the min/max widths to the
         caller's floor and the window. `placePanel` writes no width — see
         lib/ui/menuPosition — because writing back the width it just measured
         pins the panel shut and ellipsises every label past that floor. -->
    <div
      use:portal
      bind:this={panel}
      role="listbox"
      aria-label={ariaLabel}
      tabindex="-1"
      style="{panelStyle};max-width:{maxPanelWidth()};min-width:{panelMinWidth()}"
      onkeydown={onKeydown}
      class="fixed z-50 w-max rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)]"
    >
      {#each options as o (o.value)}
        <button
          type="button"
          role="option"
          aria-selected={value === o.value}
          onclick={() => select(o.value)}
          class="flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2 text-left text-xs text-text-main transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 sm:text-sm"
        >
          <span class="truncate">{o.label}</span>
          <span class="flex items-center gap-2">
            <span class="text-text-muted">{o.count}</span>
            {#if value === o.value}
              <span class="material-symbols-outlined text-[16px] text-brand-500" aria-hidden="true">check</span>
            {/if}
          </span>
        </button>
      {/each}
    </div>
  {/if}
</div>
