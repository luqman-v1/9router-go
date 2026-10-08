<!--
  PeriodSelect — the window picker shared by the Usage sections.

  The Overview already had a dropdown with a custom-window input; Compression
  Analytics had a four-button strip of uppercase labels (issue #200). Both
  sections render this control now, so they read as one page and neither
  depends on how wide the header happens to be.

  The option list is a prop because the endpoints disagree on what a window may
  be: /api/usage/stats accepts custom <n>d / <n>h windows, while
  /api/analytics/compression resolves only 24h/7d/30d/all and answers anything
  else with its 24h default — so compression passes showCustom={false} rather
  than offering a control that would silently lie.
-->
<script lang="ts">
  import {
    PERIODS,
    normalizeCustomPeriod,
    periodLabel,
    type Period,
    type PeriodPreset,
  } from './types'

  interface Props {
    value: Period
    options?: { value: PeriodPreset; label: string }[]
    showCustom?: boolean
    busy?: boolean
    onChange?: (period: Period) => void
  }

  let {
    value,
    options = PERIODS,
    showCustom = true,
    busy = false,
    onChange = () => {},
  }: Props = $props()

  let open = $state(false)
  let customInput = $state('')
  let customError = $state('')
  let root: HTMLDivElement | null = $state(null)

  const selectedLabel = $derived(periodLabel(value))
  const isPreset = $derived(options.some((o) => o.value === value))

  function select(next: Period) {
    onChange(next)
    open = false
    customError = ''
  }

  function applyCustom() {
    const normalized = normalizeCustomPeriod(customInput)
    if (!normalized) {
      customError = 'Enter a number of days or hours, like 14d or 12h.'
      return
    }
    select(normalized)
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

<div
  class="relative flex w-full items-center gap-1.5 sm:w-auto sm:self-auto"
  bind:this={root}
>
  <button
    type="button"
    disabled={busy}
    aria-haspopup="listbox"
    aria-expanded={open}
    onclick={() => (open = !open)}
    onkeydown={onKeydown}
    class="inline-flex items-center gap-2 rounded-xl border border-border bg-surface px-3 py-1.5 text-xs font-medium text-text-main shadow-sm transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 disabled:opacity-50 sm:text-sm"
  >
    <span>{selectedLabel}</span>
    {#if !isPreset}
      <span class="rounded-md bg-surface-3 px-1.5 py-0.5 font-code text-[10px] text-text-muted">{value}</span>
    {/if}
    <span class="material-symbols-outlined text-[16px] text-text-muted" aria-hidden="true">
      {open ? 'expand_less' : 'expand_more'}
    </span>
  </button>

  {#if open}
    <div
      role="listbox"
      aria-label="Time window"
      tabindex="-1"
      onkeydown={onKeydown}
      class="absolute left-1/2 top-full z-30 mt-1 w-64 -translate-x-1/2 rounded-xl border border-border bg-surface p-1.5 shadow-[var(--shadow-elev)] sm:left-auto sm:right-0 sm:translate-x-0"
    >
      {#each options as o (o.value)}
        <button
          type="button"
          role="option"
          aria-selected={value === o.value}
          onclick={() => select(o.value)}
          class="flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-xs text-text-main transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 sm:text-sm"
        >
          <span>{o.label}</span>
          {#if value === o.value}
            <span class="material-symbols-outlined text-[16px] text-brand-500" aria-hidden="true">check</span>
          {/if}
        </button>
      {/each}

      {#if showCustom}
        <div class="mt-1 border-t border-border-subtle px-3 pt-2 pb-1">
          <label for="custom-period" class="text-[11px] font-medium text-text-muted">Custom window</label>
          <div class="mt-1.5 flex items-center gap-1.5">
            <input
              id="custom-period"
              type="text"
              placeholder="14d"
              bind:value={customInput}
              onkeydown={(e) => e.key === 'Enter' && applyCustom()}
              aria-describedby={customError ? 'period-custom-error' : undefined}
              aria-invalid={customError ? 'true' : undefined}
              class="min-w-0 flex-1 rounded-lg border border-border bg-bg px-2 py-1.5 font-code text-xs text-text-main outline-none transition-colors placeholder:text-text-subtle focus:border-brand-500"
            />
            <button
              type="button"
              onclick={applyCustom}
              class="shrink-0 rounded-lg bg-brand-500 px-2.5 py-1.5 text-xs font-semibold text-white transition-colors hover:bg-primary-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50"
            >
              Set
            </button>
          </div>
          {#if customError}
            <p id="period-custom-error" class="mt-1.5 text-[11px] text-red-600 dark:text-red-400" role="alert">
              {customError}
            </p>
          {:else}
            <p class="mt-1.5 text-[11px] text-text-muted">Days or hours, for example 14d or 12h.</p>
          {/if}
        </div>
      {/if}
    </div>
  {/if}

  {#if busy}
    <span class="h-2 w-2 animate-ping rounded-full bg-brand-500" aria-hidden="true"></span>
  {/if}
</div>