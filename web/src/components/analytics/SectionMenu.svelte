<!--
  SectionMenu — the Usage page's section picker, refactored out of AnalyticsView so
  a section can put it in its own header row.

  SectionNav (which this replaces) rendered the pill row itself, so AnalyticsView
  could only place it in the one fixed row above the sections. Cache Analytics and
  Compression Analytics each need that row for their own controls — the cache
  sub-view picker, the window selector — so the picker moved down into the
  section that uses it and AnalyticsView hands it down as a snippet (issue #209).

  The closed control shows the current section's label, so the section stays
  identifiable at every width. Navigation stays with the caller: this component
  knows nothing about router state, it only reports the section that was picked.

  The panel is placed by lib/ui/menuPosition so it is measured against the trigger
  and clamped to the viewport (issue #224).
-->
<script lang="ts">
  import { maxPanelWidth, placePanel, placementStyle } from '../../lib/ui/menuPosition'
  import { portal } from '../../lib/ui/portal'
  import { USAGE_SECTIONS, type UsageSection } from '../../lib/router'

  interface Props {
    section?: UsageSection
    onSectionChange?: (section: UsageSection) => void
  }

  let { section = 'overview', onSectionChange = () => {} }: Props = $props()

  let open = $state(false)
  let trigger: HTMLButtonElement | null = $state(null)
  let panel: HTMLDivElement | null = $state(null)
  let panelStyle = $state('')


/** The panel's preferred width, capped against the viewport at placement. */
  const MIN_WIDTH = '15rem'

  const current = $derived(
    USAGE_SECTIONS.find((s) => s.value === section) ?? USAGE_SECTIONS[0]
  )

  function select(next: UsageSection) {
    if (next !== section) onSectionChange(next)
    open = false
  }

  function place(): void {
    if (!trigger || !panel) return
    panelStyle = `${placementStyle(
      placePanel({
        align: 'left',
        rect: trigger.getBoundingClientRect(),
        panelWidth: panel.offsetWidth,
      }),
    )};max-width:${maxPanelWidth()};min-width:min(${MIN_WIDTH}, ${maxPanelWidth()})`
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
      use:portal
      bind:this={panel}
      role="listbox"
      aria-label="Usage section"
      tabindex="-1"
      style={panelStyle}
      onkeydown={onKeydown}
      class="fixed z-50 rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)]"
    >
      {#each USAGE_SECTIONS as s (s.value)}
        <button
          type="button"
          role="option"
          aria-selected={section === s.value}
          onclick={() => select(s.value)}
          class="flex w-full items-center justify-between gap-2 rounded-lg px-3 py-2 text-left text-xs text-text-main transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 sm:text-sm"
        >
          <span class="flex min-w-0 items-center gap-2">
            <span class="material-symbols-outlined text-[18px] text-text-muted" aria-hidden="true">
              {s.icon}
            </span>
            <span class="truncate">{s.label}</span>
          </span>
          {#if section === s.value}
            <span class="material-symbols-outlined text-[16px] text-brand-500" aria-hidden="true">check</span>
          {/if}
        </button>
      {/each}
    </div>
  {/if}
</div>