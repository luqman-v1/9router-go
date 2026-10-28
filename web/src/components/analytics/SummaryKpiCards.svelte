<script lang="ts">
  import { fmtTokens } from '../../lib/compactNumber'
  import { costIn } from '../../lib/currency.svelte'
  import { type StatsData } from './types'

  let { stats = {} }: { stats?: StatsData } = $props()
</script>

<div class="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-5 sm:gap-4">
  <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Total Requests</span>
    <span class="truncate text-2xl font-bold text-text-main" title={fmtTokens(stats.totalRequests)}>{fmtTokens(stats.totalRequests)}</span>
  </div>

  <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Total Input Tokens</span>
    <span class="truncate text-2xl font-bold text-primary" title={fmtTokens(stats.totalPromptTokens)}>{fmtTokens(stats.totalPromptTokens)}</span>
  </div>

  <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Cached Tokens</span>
    <span class="truncate text-2xl font-bold text-info" title={fmtTokens(stats.totalCachedTokens)}>{fmtTokens(stats.totalCachedTokens)}</span>
  </div>

  <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Output Tokens</span>
    <span class="truncate text-2xl font-bold text-success" title={fmtTokens(stats.totalCompletionTokens)}>{fmtTokens(stats.totalCompletionTokens)}</span>
  </div>

  <div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Est. Cost</span>
    <span class="truncate text-2xl font-bold text-warning">~{costIn(stats.totalCost ?? 0)}</span>
    <span class="text-[10px] text-text-muted">Estimated, not actual billing</span>
  </div>
</div>
