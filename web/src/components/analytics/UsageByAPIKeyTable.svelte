<script lang="ts">
  /**
   * Usage per client API key — the keys created inside the gateway.
   *
   * This is not the Overview's "Usage by API Key" table: that one groups the
   * upstream credential the router spent, while this groups the key the caller
   * presented. An operator who resells the gateway needs the second one — it is
   * the key they sold.
   *
   * The panel reads its own data instead of taking it from the Overview's
   * stats read, because the stats endpoint groups by a different dimension
   * entirely and cannot answer this question.
   */
  import { api, normalizeLastError, type UsageByAPIKeyItem } from '../../api/client'
  import { fmtTokens } from '../../lib/compactNumber'
  import { costIn } from '../../lib/currency.svelte'
  import { timeAgo, fmt } from './types'
  import Card from '../../lib/ui/Card.svelte'
  import Badge from '../../lib/ui/Badge.svelte'

  interface Props {
    period?: string
  }

  let { period = '7d' }: Props = $props()

  let items = $state<UsageByAPIKeyItem[]>([])
  let totals = $state<{
    requests: number
    promptTokens: number
    completionTokens: number
    cachedTokens: number
    cost: number
  } | null>(null)
  let loading = $state(true)
  let failed = $state(false)
  let errorText = $state('')

  const READ_TIMEOUT_MS = 20000

  function totalTokens(item: UsageByAPIKeyItem): number {
    return (item.promptTokens || 0) + (item.completionTokens || 0)
  }

  function tokenShare(item: UsageByAPIKeyItem): number {
    if (!totals) return 0
    const all = totals.promptTokens + totals.completionTokens
    if (all <= 0) return 0
    return Math.round((totalTokens(item) / all) * 1000) / 10
  }

  $effect(() => {
    // The window is read inside the effect so a period change refetches.
    const window = period
    let cancelled = false
    let settled = false
    loading = true
    failed = false
    errorText = ''
    const timer = setTimeout(() => {
      if (settled || cancelled) return
      settled = true
      failed = true
      loading = false
    }, READ_TIMEOUT_MS)

    api
      .getUsageByAPIKey(window)
      .then((res) => {
        if (cancelled || settled) return
        settled = true
        clearTimeout(timer)
        items = Array.isArray(res?.keys) ? res.keys : []
        totals = res?.totals ?? null
      })
      .catch((err) => {
        if (cancelled || settled) return
        settled = true
        clearTimeout(timer)
        failed = true
        errorText = normalizeLastError(err) || 'Usage per API key could not be loaded.'
      })
      .finally(() => {
        // The spinner is cleared whenever the read settles, whether it answered
        // or not. The settled flag is not consulted here: the handlers above
        // have already set it, so testing it left the panel spinning forever.
        if (!cancelled) loading = false
      })

    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  })
</script>

<Card padding="none" class="overflow-hidden border border-border">
  <div
    class="flex flex-col gap-3 bg-surface-2 px-5 py-3 sm:flex-row sm:items-center sm:justify-between border-b border-border"
  >
    <div class="flex min-w-0 items-center gap-2">
      <span class="material-symbols-outlined text-[18px] text-text-muted" aria-hidden="true">key</span>
      <div class="min-w-0">
        <h2 class="font-headline text-sm font-bold text-text-main">Usage per API key</h2>
        <p class="font-body text-xs text-text-muted">Spend by the key your callers present</p>
      </div>
    </div>
    {#if totals && totals.requests > 0}
      <div class="flex items-center gap-3 text-[11px] text-text-muted font-mono">
        <span>{fmtTokens(totals.promptTokens + totals.completionTokens)} tok</span>
        {#if totals.cachedTokens > 0}
          <span class="text-info">{fmtTokens(totals.cachedTokens)} cached</span>
        {/if}
        <span>{costIn(totals.cost || 0)}</span>
      </div>
    {/if}
  </div>

  {#if loading}
    <div class="flex items-center justify-center gap-2 py-8 text-text-muted text-sm font-body">
      <span class="material-symbols-outlined animate-spin text-[20px]" aria-hidden="true">progress_activity</span>
      Loading API key usage...
    </div>
  {:else if failed}
    <div class="p-8 text-center text-text-muted text-sm font-body">
      {errorText || 'Usage per API key could not be loaded.'}
    </div>
  {:else if items.length === 0}
    <div class="p-8 text-center text-text-muted text-sm font-body">No usage recorded for this period.</div>
  {:else}
    <div class="overflow-x-auto">
      <table class="w-full text-left border-collapse text-xs font-body">
        <thead class="bg-surface-2 border-b border-border uppercase font-semibold">
          <tr>
            <th class="py-3 px-4">API Key</th>
            <th class="py-3 px-4">Status</th>
            <th class="py-3 px-4 text-right">Requests</th>
            <th class="py-3 px-4 text-right">In Tokens</th>
            <th class="py-3 px-4 text-right">Out Tokens</th>
            <th class="py-3 px-4 text-right">Cached Tokens</th>
            <th class="py-3 px-4 text-right">Total Cost</th>
            <th class="py-3 px-4 text-right">Share</th>
            <th class="py-3 px-4 text-right">Last Used</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/60">
          {#each items as item (item.key + item.id)}
            <tr class="hover:bg-surface-2/60 transition-colors">
              <td class="py-3 px-4">
                <div class="flex flex-col gap-0.5 min-w-0">
                  <span class="truncate font-medium text-text-main">{item.name || item.key || 'Unknown key'}</span>
                  {#if item.keyDisplay && item.keyDisplay !== item.name}
                    <span class="truncate font-mono text-[10px] text-text-muted">{item.keyDisplay}</span>
                  {/if}
                </div>
              </td>
              <td class="py-3 px-4">
                {#if item.id && item.active}
                  <Badge variant="success" size="sm">Active</Badge>
                {:else if item.id}
                  <Badge variant="warning" size="sm">Disabled</Badge>
                {:else}
                  <Badge variant="neutral" size="sm">Untracked</Badge>
                {/if}
              </td>
              <td class="py-3 px-4 text-right font-mono font-semibold text-text-main">
                {fmt(item.requests || 0)}
              </td>
              <td class="py-3 px-4 text-right font-mono text-brand-500">
                {fmtTokens(item.promptTokens || 0)}
              </td>
              <td class="py-3 px-4 text-right font-mono text-success">
                {fmtTokens(item.completionTokens || 0)}
              </td>
              <td class="py-3 px-4 text-right font-mono text-info">
                {fmtTokens(item.cachedTokens || 0)}
              </td>
              <td class="py-3 px-4 text-right font-mono font-bold text-warning">
                {costIn(item.cost || 0)}
              </td>
              <td class="py-3 px-4 text-right font-mono text-text-muted">{tokenShare(item)}%</td>
              <td class="py-3 px-4 text-right text-text-muted whitespace-nowrap text-[11px]">
                {timeAgo(item.lastUsed)}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Card>
