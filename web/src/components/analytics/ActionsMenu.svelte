<!--
  ActionsMenu — the overflow menu for a section header.

  Cache Analytics and Compression Analytics each carried four standalone
  buttons in their header — auto-refresh, refresh, CSV, JSON — next to the
  sub-view picker and the window selector. Six controls in one row is what
  forced the header to wrap on a narrow window, so the refresh and export
  actions moved into one menu (issue #209).

  The trigger is labelled "Menu" rather than an icon-only button: an icon alone
  gives a screen reader nothing to announce, and four different verbs behind one
  glyph need a name. The label collapses away from the glyph at the smallest
  widths, where the header is tightest.
-->
<script lang="ts">
  import type { Snippet } from 'svelte'

  interface Props {
    /** Names the menu for assistive technology; the visible label is "Menu". */
    label?: string
    disabled?: boolean
    children?: Snippet
  }

  let { label = 'Section actions', disabled = false, children }: Props = $props()

  let open = $state(false)
  let root: HTMLDivElement | null = $state(null)

  function close() {
    open = false
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      close()
      return
    }
    // A menu left open behind the next control makes the following focusable
    // element unreachable, so Tab closes it instead.
    if (event.key === 'Tab') close()
  }

  $effect(() => {
    if (!open) return
    function handleDocClick(e: MouseEvent): void {
      if (!root?.contains(e.target as HTMLElement | null)) close()
    }
    document.addEventListener('click', handleDocClick)
    return () => document.removeEventListener('click', handleDocClick)
  })
</script>

<div class="relative shrink-0" bind:this={root}>
  <button
    type="button"
    aria-haspopup="menu"
    aria-expanded={open}
    aria-label={label}
    disabled={disabled}
    onclick={() => (open = !open)}
    onkeydown={onKeydown}
    class="inline-flex items-center gap-2 rounded-xl border border-border bg-surface px-2.5 py-1.5 text-xs font-medium text-text-main shadow-sm transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 disabled:opacity-50 sm:px-3 sm:text-sm"
  >
    <span class="material-symbols-outlined text-[18px] text-text-muted" aria-hidden="true">menu</span>
    <span class="hidden sm:inline">Menu</span>
    <span class="material-symbols-outlined text-[16px] text-text-muted" aria-hidden="true">expand_more</span>
  </button>

  {#if open}
    <div
      role="menu"
      aria-label={label}
      tabindex="-1"
      onkeydown={onKeydown}
      onclick={(e) => {
        // Every entry is a verb that runs and is done, so the menu closes on
        // pick. Delegating here rather than in each item keeps the entries
        // plain buttons that do not need to know about the menu above them.
        if ((e.target as HTMLElement).closest('button')) close()
      }}
      class="absolute right-0 top-full z-30 mt-1 w-56 rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)]"
    >
      {@render children?.()}
    </div>
  {/if}
</div>