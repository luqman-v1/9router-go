<!--
  SectionNav — the Usage page's section picker.

  The pill row this replaces grew to four sections (Overview, Cache Analytics,
  Compression Analytics, Details), and four labels in one
  `inline-flex rounded-xl p-1` strip do not fit a 374px-wide phone: the strip
  either wraps or overflows (issue #200). A dropdown keeps one control in the
  header at every width, and the closed control shows the current section's
  label, so the section stays identifiable.

  Navigation stays with the caller: this component knows nothing about router
  state, it only reports the section that was picked.
-->
<script lang="ts">
  import { USAGE_SECTIONS, type UsageSection } from '../../lib/router'

  interface Props {
    section?: UsageSection
    onSectionChange?: (section: UsageSection) => void
  }

  let { section = 'overview', onSectionChange = () => {} }: Props = $props()

  let open = $state(false)
  let root: HTMLDivElement | null = $state(null)

  const current = $derived(
    USAGE_SECTIONS.find((s) => s.value === section) ?? USAGE_SECTIONS[0]
  )

  function select(next: UsageSection) {
    if (next !== section) onSectionChange(next)
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

<div class="relative" bind:this={root}>
  <button
    type="button"
    aria-haspopup="listbox"
    aria-expanded={open}
    onclick={() => (open = !open)}
    onkeydown={onKeydown}
    class="inline-flex w-full items-center justify-between gap-2 rounded-xl border border-border bg-surface px-3 py-1.5 text-xs font-medium text-text-main shadow-sm transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 sm:w-auto sm:text-sm"
  >
    <span class="flex min-w-0 items-center gap-2">
      <span class="material-symbols-outlined text-[18px] text-brand-500" aria-hidden="true">
        {current.icon}
      </span>
      <span class="truncate">{current.label}</span>
    </span>
    <span class="material-symbols-outlined text-[16px] text-text-muted" aria-hidden="true">
      {open ? 'expand_less' : 'expand_more'}
    </span>
  </button>

  {#if open}
    <div
      role="listbox"
      aria-label="Usage section"
      tabindex="-1"
      onkeydown={onKeydown}
      class="absolute left-0 top-full z-30 mt-1 w-64 rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)] sm:right-0"
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