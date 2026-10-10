<!--
  Menu — the overflow menu this dashboard puts in place of a row of buttons.

  Six screens had the same problem: a header or a table row grew a control per
  verb until it wrapped on a narrow window (issue #224). Each collapsed into one
  "Menu" trigger, so this component carries the behaviour they share rather than
  each screen hand-rolling its own open/close.

  The panel is `position: fixed` and placed against the trigger's measured rect
  rather than `position: absolute` inside the trigger's wrapper. An absolute
  panel is clipped by any ancestor with `overflow` set, and these menus sit
  inside `overflow-x-auto` tables and an `overflow-y-auto` page — which is what
  pushed the Cache Analytics dropdown off the right edge on a phone.

  The panel is also portalled to `<body>` while it is open: `position: fixed`
  resolves against the nearest ancestor that establishes a containing block,
  and the top bar's `backdrop-blur-xl` `<header>` is exactly that, so its panel
  was laid out from the header's box and rendered off-screen (issue #235).

  The trigger is labelled "Menu" rather than icon-only: an icon alone gives a
  screen reader nothing to announce, and several different verbs hide behind it.
  The label collapses away from the glyph at the smallest widths, where the
  header is tightest.
-->
<script lang="ts">
  import type { Snippet } from 'svelte'
  import { portal } from './portal'
  import { maxPanelWidth, placePanel, placementStyle } from './menuPosition'

  interface Props {
    /** Names the menu for assistive technology; the visible label is "Menu". */
    label?: string
    disabled?: boolean
    /** Panel alignment against the trigger. Defaults to the right edge. */
    align?: 'left' | 'right'
    /** Smallest panel width; the panel grows past this to fit its content. */
    minWidth?: string
    /** Trigger glyph. Defaults to the "menu" glyph. */
    triggerIcon?: string
    /** Hides the "Menu" text even at desktop widths, for a tight row. */
    hideLabel?: boolean
    /**
     * Stretches the trigger to fill its row and keeps the "Menu" label at
     * every width. The API-key list renders a card instead of a table below
     * `md` (issue #224), and a card row has no room for anything but a
     * full-width button — an icon-only glyph that small is an ambiguous tap
     * target with several different verbs behind it.
     */
    fullWidth?: boolean
    children?: Snippet
  }

  let {
    label = 'Actions',
    disabled = false,
    align = 'right',
    minWidth = '13rem',
    triggerIcon = 'menu',
    hideLabel = false,
    fullWidth = false,
    children,
  }: Props = $props()

  let open = $state(false)
  let trigger: HTMLButtonElement | null = $state(null)
  let panel: HTMLDivElement | null = $state(null)
  let panelStyle = $state('')

  // The panel's natural width is only known after it renders, so placement runs
  // on every render while the menu is open rather than once on open.
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
  /**
   * The floor the caller asked for, capped against the window. CSS `min-width`
   * beats `width`, so an uncapped floor would render wider than the clamp and
   * overflow anyway — which is what `placePanel` used to compute in JS.
   */
  function panelMinWidth(): string {
    return `min(${minWidth}, ${maxPanelWidth()})`
  }

  function close(): void {
    open = false
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      close()
      // Return focus to the trigger so the keyboard path resumes where it left off.
      trigger?.focus()
      return
    }
    // A menu left open behind the next control makes the following focusable
    // element unreachable, so Tab closes it instead.
    if (event.key === 'Tab') close()
  }

  $effect(() => {
    if (!open) return

    function handleDocClick(e: MouseEvent): void {
      const target = e.target as HTMLElement | null
      if (!trigger?.contains(target) && !panel?.contains(target)) close()
    }
    // A scroll or a resize moves the trigger out from under a panel placed
    // against the old rect, so both re-measure while the menu is open.
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

<div class="relative shrink-0 {fullWidth ? 'w-full' : ''}">
  <button
    bind:this={trigger}
    type="button"
    aria-haspopup="menu"
    aria-expanded={open}
    aria-label={label}
    disabled={disabled}
    onclick={() => (open = !open)}
    onkeydown={onKeydown}
    class="inline-flex items-center gap-2 rounded-xl border border-border bg-surface px-2.5 py-1.5 text-xs font-medium text-text-main shadow-sm transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 disabled:opacity-50 sm:px-3 sm:text-sm {fullWidth
      ? 'w-full justify-center py-2.5 text-sm'
      : ''}"
  >
    <span class="material-symbols-outlined text-[18px] text-text-muted" aria-hidden="true">
      {triggerIcon}
    </span>
    {#if !hideLabel}
      <!-- Below `sm` a table row has no room for the label, so it collapses to
           the glyph. A full-width trigger is a card's whole row: the space is
           there, and an unlabelled glyph there would be a bare tap target. -->
      <span class={fullWidth ? '' : 'hidden sm:inline'}>Menu</span>
      {#if !fullWidth}
        <span class="material-symbols-outlined text-[16px] text-text-muted" aria-hidden="true">
          expand_more
        </span>
      {/if}
    {/if}
  </button>

  {#if open}
    <!-- The panel sizes itself: `w-max` to its labels, `max-width` to whatever
         the window can show. `placePanel` deliberately writes no width — see
         lib/ui/menuPosition — because the width it would write is the one it
         just measured, which pins the panel shut and ellipsises every label
         past that floor. That is what "Add Proxy P…" was on issue #261. -->
    <div
      use:portal
      bind:this={panel}
      role="menu"
      aria-label={label}
      tabindex="-1"
      style="{panelStyle};max-width:{maxPanelWidth()};min-width:{panelMinWidth()}"
      onkeydown={onKeydown}
      onclick={(e) => {
        // Every entry is a verb that runs and is done, so the menu closes on
        // pick. Delegating here rather than in each item keeps the entries
        // plain buttons that do not need to know about the menu above them.
        if ((e.target as HTMLElement).closest('button')) close()
      }}
      class="fixed z-50 w-max max-w-[calc(100vw-1rem)] rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)]"
    >
      {@render children?.()}
    </div>
  {/if}
</div>