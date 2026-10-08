<script lang="ts">
  import Badge from '../../lib/ui/Badge.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'
  import {
    fmt,
    fmtCost,
    timeAgo,
    TABLE_OPTIONS,
    type StatsData,
    type TableView,
    type ViewMode,
    type UsageItem
  } from './types'

  let { stats = {} }: { stats?: StatsData } = $props()

  let tableView = $state<TableView>('model')
  let viewMode = $state<ViewMode>('costs')

  interface ProcessedUsageRow extends UsageItem {
    key: string
    totalTokens: number
  }

  let tableData = $derived((): ProcessedUsageRow[] => {
    if (!stats) return []
    let sourceMap: Record<string, UsageItem> = {}
    if (tableView === 'model') sourceMap = stats.byModel || {}
    else if (tableView === 'account') sourceMap = stats.byAccount || {}
    else if (tableView === 'apiKey') sourceMap = stats.byApiKey || {}
    else if (tableView === 'endpoint') sourceMap = stats.byEndpoint || {}

    return Object.entries(sourceMap)
      .map(([key, item]) => {
        const totalTokens = (item.promptTokens || 0) + (item.completionTokens || 0)
        return {
          key,
          ...item,
          totalTokens,
        }
      })
      .sort((a, b) => (b.requests || 0) - (a.requests || 0))
  })
</script>

<div class="flex flex-col gap-3 pt-2">
  <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
    <!-- View selector dropdown -->
    <select
      bind:value={tableView}
      class="w-full sm:w-auto rounded-lg border border-border bg-surface px-3 py-1.5 text-sm font-semibold text-text-main focus:outline-none focus:ring-2 focus:ring-brand-500/50 cursor-pointer"
    >
      {#each TABLE_OPTIONS as opt}
        <option value={opt.value}>{opt.label}</option>
      {/each}
    </select>

    <!-- Toggle: Costs | Tokens -->
    <div class="inline-flex rounded-xl bg-surface border border-border p-1 shadow-sm self-start sm:self-auto">
      <button
        type="button"
        onclick={() => (viewMode = 'costs')}
        class="rounded-lg px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {viewMode === 'costs'
          ? 'bg-brand-500 text-white shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Costs
      </button>
      <button
        type="button"
        onclick={() => (viewMode = 'tokens')}
        class="rounded-lg px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {viewMode === 'tokens'
          ? 'bg-brand-500 text-white shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Tokens
      </button>
    </div>
  </div>

  <!-- Breakdown Table Card -->
  <Card padding="none" class="overflow-hidden border border-border">
    {#if tableData().length === 0}
      <div class="p-8 text-center text-text-muted text-sm font-body">
        No usage recorded for this period.
      </div>
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full text-left border-collapse text-xs font-body">
          <thead class="bg-surface-2 border-b border-border text-text-muted uppercase text-[10px] font-semibold tracking-wider">
            <tr>
              <th class="py-3 px-4">
                {tableView === 'model' ? 'Model' : tableView === 'account' ? 'Account' : tableView === 'apiKey' ? 'Key Name' : 'Endpoint'}
              </th>
              <th class="py-3 px-4">Provider</th>
              <th class="py-3 px-4 text-right">Requests</th>
              {#if viewMode === 'costs'}
                <th class="py-3 px-4 text-right">Total Cost</th>
              {:else}
                <th class="py-3 px-4 text-right">In Tokens</th>
                <th class="py-3 px-4 text-right">Out Tokens</th>
                <th class="py-3 px-4 text-right">Cached Tokens</th>
              {/if}
              <th class="py-3 px-4 text-right">Last Used</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border/60">
            {#each tableData() as row}
              <tr class="hover:bg-surface-2/60 transition-colors">
                <td class="py-3 px-4 font-mono font-medium text-text-main text-xs">
                  <div class="flex items-center gap-2">
                    {#if row.provider}
                      <ProviderArtwork
                        id={row.provider}
                        alt={row.provider}
                        class="w-4 h-4 object-contain rounded shrink-0 bg-surface-2 p-0.5 border border-border/40 text-[9px] leading-none font-semibold"
                      />
                    {/if}
                    <span class="truncate">{row.rawModel || row.accountName || row.keyName || row.endpoint || row.key}</span>
                  </div>
                </td>
                <td class="py-3 px-4">
                  <div class="flex items-center gap-1.5">
                    {#if row.provider}
                      <ProviderArtwork
                        id={row.provider}
                        alt={row.provider}
                        class="w-3.5 h-3.5 object-contain rounded shrink-0 text-[9px] leading-none font-semibold"
                      />
                    {/if}
                    <Badge variant="neutral" size="sm">{row.provider || 'unknown'}</Badge>
                  </div>
                </td>
                <td class="py-3 px-4 text-right font-mono font-semibold text-text-main">
                  {fmt(row.requests)}
                </td>
                {#if viewMode === 'costs'}
                  <td class="py-3 px-4 text-right font-mono font-bold text-warning">
                    {fmtCost(row.cost)}
                  </td>
                {:else}
                  <td class="py-3 px-4 text-right font-mono text-brand-500">
                    {fmt(row.promptTokens)}
                  </td>
                  <td class="py-3 px-4 text-right font-mono text-success">
                    {fmt(row.completionTokens)}
                  </td>
                  <td class="py-3 px-4 text-right font-mono text-info">
                    {fmt(row.cachedTokens)}
                  </td>
                {/if}
                <td class="py-3 px-4 text-right text-text-muted whitespace-nowrap text-[11px]">
                  {timeAgo(row.lastUsed)}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Card>
</div>
