<script lang="ts">
  import type { ModelListFilter, SortMode } from './modelListFilter'

  interface Props {
    filter: ModelListFilter
    onChange: (next: ModelListFilter) => void
    /** Compatible nodes have no disabled-models concept; hide those chips. */
    showStatus?: boolean
    /**
     * Capabilities at least one visible row actually has. A chip whose
     * capability no row carries is hidden: on a compatible node whose
     * imported models declare no flags, "Vision" would filter everything
     * out and read as a bug rather than as "none of these have it".
     */
    availableCaps?: { thinking: boolean; vision: boolean; search: boolean; free: boolean; jailbreak: boolean }
  }

  let { filter, onChange, showStatus = true, availableCaps }: Props = $props()

  interface Chip {
    key: string
    label: string
    on: boolean
    title: string
    toggle: () => void
  }

  // Chips are derived, not stored: the filter object is the single source
  // of truth, so a "reset" that clears the object clears the chips too.
  // A capability no visible row carries is not offered: the chip would
  // empty the list and read as a bug. `availableCaps` undefined means
  // "not computed yet" — show everything until the caller knows.
  let capChips = $derived<Chip[]>([
    {
      key: 'thinking',
      label: 'Thinking',
      on: filter.caps.thinking,
      title: 'Models that accept a reasoning level',
      toggle: () => onChange({ ...filter, caps: { ...filter.caps, thinking: !filter.caps.thinking } }),
    },
    {
      key: 'vision',
      label: 'Vision',
      on: filter.caps.vision,
      title: 'Models that accept image input',
      toggle: () => onChange({ ...filter, caps: { ...filter.caps, vision: !filter.caps.vision } }),
    },
    {
      key: 'search',
      label: 'Web search',
      on: filter.caps.search,
      title: 'Models with live web search',
      toggle: () => onChange({ ...filter, caps: { ...filter.caps, search: !filter.caps.search } }),
    },
    {
      key: 'free',
      label: 'Free tier',
      on: filter.caps.free,
      title: 'Models on the provider free tier',
      toggle: () => onChange({ ...filter, caps: { ...filter.caps, free: !filter.caps.free } }),
    },
    {
      key: 'jailbreak',
      label: 'Jailbreak',
      on: filter.caps.jailbreak,
      title: 'Uncensored / unrestricted models — accept prompts a filtered model refuses',
      toggle: () => onChange({ ...filter, caps: { ...filter.caps, jailbreak: !filter.caps.jailbreak } }),
    },
  ].filter((chip) => !availableCaps || availableCaps[chip.key as keyof typeof availableCaps]))

  let statusChips = $derived<Chip[]>([
    {
      key: 'available',
      label: 'Available',
      on: filter.status.available,
      title: 'Enabled and usable — not disabled, not failed, not deprecated',
      toggle: () => onChange({ ...filter, status: { ...filter.status, available: !filter.status.available } }),
    },
    {
      key: 'unavailable',
      label: 'Unavailable',
      on: filter.status.unavailable,
      title: 'Listed but not usable: disabled, failed its last probe, or deprecated',
      toggle: () => onChange({ ...filter, status: { ...filter.status, unavailable: !filter.status.unavailable } }),
    },
    {
      key: 'disabled',
      label: 'Disabled',
      on: filter.status.disabled,
      title: 'Switched off by you',
      toggle: () => onChange({ ...filter, status: { ...filter.status, disabled: !filter.status.disabled } }),
    },
    {
      key: 'error',
      label: 'Error',
      on: filter.status.error,
      title: 'Last probe failed, or the account is in cooldown',
      toggle: () => onChange({ ...filter, status: { ...filter.status, error: !filter.status.error } }),
    },
    {
      key: 'untested',
      label: 'Untested',
      on: filter.status.untested,
      title: 'Never probed',
      toggle: () => onChange({ ...filter, status: { ...filter.status, untested: !filter.status.untested } }),
    },
    {
      key: 'deprecated',
      label: 'Deprecated',
      on: filter.status.deprecated,
      title: 'Retired upstream',
      toggle: () => onChange({ ...filter, status: { ...filter.status, deprecated: !filter.status.deprecated } }),
    },
  ])

  // Every order is offered. The earlier build hid the latency orders until
  // something had been probed, which meant the control changed shape between
  // visits — an operator who had not tested yet could not discover the option
  // existed. The register-date orders are always meaningful on a compatible
  // node (every row there was added by someone) and harmless elsewhere.
  const SORT_LABELS: Record<SortMode, string> = {
    registry: 'Registry order',
    'added-desc': 'Register date — newest',
    'added-asc': 'Register date — oldest',
    az: 'A–Z',
    za: 'Z–A',
    'latency-asc': 'Latency — fastest',
    'latency-desc': 'Latency — slowest',
  }

  function setSort(sort: SortMode) {
    onChange({ ...filter, sort })
  }

  function chipClass(on: boolean): string {
    // Off chips stay legible against the card surface; on chips take the
    // brand tint so the active set reads at a glance.
    return on
      ? 'inline-flex items-center gap-1 rounded-full border border-brand-500/40 bg-brand-500/10 px-2 py-0.5 text-[11px] font-medium text-brand-500 cursor-pointer transition-colors hover:bg-brand-500/20'
      : 'inline-flex items-center gap-1 rounded-full border border-border bg-surface-2 px-2 py-0.5 text-[11px] font-medium text-text-muted cursor-pointer transition-colors hover:text-text-main hover:border-text-muted/40'
  }
</script>

<!--
  Two labelled rows instead of one wrapped run. The first version put every
  chip in a single flex-wrap, so on a wide provider the status chips landed
  beside the capability chips with nothing saying which was which — and
  "Available" read as another capability. A group label also gives the eye a
  fixed anchor: the sort control stays on the status row, so it does not
  drift up and down as capability chips wrap.
-->
<div class="flex flex-col gap-1.5">
  <div class="flex flex-wrap items-center gap-1.5">
    <span class="w-14 shrink-0 text-[10px] font-semibold uppercase tracking-wide text-text-muted/70">Caps</span>
    {#each capChips as chip (chip.key)}
      <button type="button" class={chipClass(chip.on)} onclick={chip.toggle} title={chip.title}>
        {#if chip.on}<span class="material-symbols-outlined text-[12px] leading-none">check</span>{/if}
        {chip.label}
      </button>
    {/each}
    {#if capChips.length === 0}
      <span class="text-[11px] italic text-text-muted/60">No capabilities on this list</span>
    {/if}
  </div>

  {#if showStatus}
    <div class="flex flex-wrap items-center gap-1.5">
      <span class="w-14 shrink-0 text-[10px] font-semibold uppercase tracking-wide text-text-muted/70">Status</span>
      {#each statusChips as chip (chip.key)}
        <button type="button" class={chipClass(chip.on)} onclick={chip.toggle} title={chip.title}>
          {#if chip.on}<span class="material-symbols-outlined text-[12px] leading-none">check</span>{/if}
          {chip.label}
        </button>
      {/each}
    </div>
  {/if}
  <!-- The sort control lives outside the status row on purpose: a compatible
       node renders no status chips at all, and nesting the dropdown inside
       that block left it with no way to reorder its list. -->
  <div class="flex items-center gap-1">
    <span class="material-symbols-outlined text-[14px] text-text-muted">sort</span>
    <select
      aria-label="Sort models"
      value={filter.sort}
      onchange={(e) => setSort((e.target as HTMLSelectElement).value as SortMode)}
      class="rounded-md border border-border bg-background px-1.5 py-0.5 text-[11px] text-text-main focus:border-primary focus:outline-none cursor-pointer"
    >
      {#each Object.entries(SORT_LABELS) as [mode, label] (mode)}
        <option value={mode}>{label}</option>
      {/each}
    </select>
  </div>
</div>
