<script lang="ts">
  import { RefreshCw, X, Zap } from 'lucide-svelte'
  import Badge from '../../lib/ui/Badge.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import { getIconPath } from '../connections/types'
  import { api, normalizeLastError } from '../../api/client'
  import { cachedTokensFor, fmt, providerDisplayName, timeAgo, type RequestDetailItem } from './types'

interface Props {
  details?: RequestDetailItem[]
  detailsTotal?: number
  detailsPage?: number
  detailsLoading?: boolean
  onPageChange: (page: number) => void
  /** Set when the read failed, so the table below is not read as current. */
  detailsError?: string
  onRefresh: () => void
  /** Custom provider nodes, so a synthetic id renders as its configured name. */
  providerNodes?: { id: string; name?: string }[]
}

let {
  details = [],
  detailsTotal = 0,
  detailsPage = 1,
  detailsLoading = false,
  detailsError = '',
  onPageChange,
  onRefresh,
  providerNodes = [],
}: Props = $props()

  let selectedDetail = $state<RequestDetailItem | null>(null)
  let detailLoading = $state(false)
  let detailError = $state('')

  // The list response carries only what the table renders — the request
  // messages and response body stay server-side until a row is opened, which
  // keeps a page at a few KB instead of a few hundred. Clicking View therefore
  // opens the modal on the summary fields and fills in the rest once the
  // by-id read lands.
  async function openDetail(item: RequestDetailItem) {
    selectedDetail = item
    detailError = ''
    const id = item.id
    if (!id) {
      detailError = 'This row has no id, so its full payload cannot be loaded.'
      return
    }
    detailLoading = true
    try {
      const res = await api.getRequestDetail(id)
      // A newer row opened while this read was in flight wins.
      if (selectedDetail?.id === id && res?.detail) {
        selectedDetail = res.detail as RequestDetailItem
      }
    } catch (err) {
      if (selectedDetail?.id === id) {
        detailError = normalizeLastError(err) || 'The full payload could not be loaded.'
      }
    } finally {
      detailLoading = false
    }
  }

  function closeDetail() {
    selectedDetail = null
    detailError = ''
  }

</script>

<Card padding="none" class="overflow-hidden border border-border">
  <div class="px-5 py-3 border-b border-border flex items-center justify-between bg-surface-2">
    <div>
      <h2 class="font-headline text-sm font-bold text-text-main">Recent Request Details</h2>
      <p class="font-body text-xs text-text-muted">Total recorded: {detailsTotal.toLocaleString()} requests</p>
    </div>
    <Button variant="secondary" size="sm" onclick={onRefresh} disabled={detailsLoading}>
      <RefreshCw class="w-3.5 h-3.5 mr-1 {detailsLoading ? 'animate-spin' : ''}" />
      Refresh
    </Button>
  </div>

  {#if detailsError}
    <!-- The refresh above is the recovery action, so the message points there
         rather than adding a second button beside it. -->
    <div role="alert" class="flex items-start gap-2 border-b border-red-500/30 px-5 py-3">
      <span class="material-symbols-outlined mt-px text-[18px] text-red-600 dark:text-red-400" aria-hidden="true">error</span>
      <div class="min-w-0">
        <p class="font-body text-sm font-medium text-text-main">
          Request details could not be loaded. {#if details.length > 0}The rows below are from the last successful read.{/if}
        </p>
        <p class="mt-0.5 break-words font-body text-[11px] text-text-muted">{detailsError}</p>
      </div>
    </div>
  {/if}

  {#if detailsLoading}
    <div class="p-12 text-center text-text-muted text-sm flex items-center justify-center gap-2">
      <span class="w-4 h-4 border-2 border-brand-500 border-t-transparent rounded-full animate-spin"></span>
      <span>Loading request history...</span>
    </div>
  {:else if details.length === 0}
    <div class="p-12 text-center text-text-muted text-sm">
      {#if detailsError}
        Nothing to show: the read failed, so the request history is unknown.
      {:else}
        No request logs found in the database.
      {/if}
    </div>
  {:else}
    <div class="overflow-x-auto">
      <table class="w-full text-left border-collapse text-xs font-body">
        <thead class="bg-surface-2 border-b border-border text-text-muted uppercase text-[10px] font-semibold tracking-wider">
          <tr>
            <th class="py-3 px-4 w-2"></th>
            <th class="py-3 px-4">Time</th>
            <th class="py-3 px-4">Provider</th>
            <th class="py-3 px-4">Model</th>
            <th class="py-3 px-4 text-right">TTFT</th>
            <th class="py-3 px-4 text-right">Total Latency</th>
            <th class="py-3 px-4 text-right">Prompt</th>
            <th class="py-3 px-4 text-right">Completion</th>
            <th class="py-3 px-4 text-right">Cached</th>
            <th class="py-3 px-4 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/60 font-code text-[11px]">
          {#each details as item}
            <tr class="hover:bg-surface-2 transition-colors cursor-pointer" onclick={() => openDetail(item)}>
              <td class="py-3 px-4">
                <span class="block w-2 h-2 rounded-full {item.status === 'success' || item.status === 'ok' ? 'bg-success' : 'bg-red-500'}"></span>
              </td>
              <td class="py-3 px-4 text-text-muted whitespace-nowrap text-[11px]">
                {timeAgo(item.timestamp)}
              </td>
              <td class="py-3 px-4">
                <div class="flex items-center gap-1.5">
                  {#if item.provider}
                    <img
                      src={getIconPath(item.provider)}
                      alt={item.provider}
                      class="w-3.5 h-3.5 object-contain rounded shrink-0"
                      onerror={(e) => {
                        (e.currentTarget as HTMLElement).style.display = 'none'
                      }}
                      loading="lazy"
                    />
                  {/if}
                  <span title={item.provider || undefined}>
                    <Badge variant="neutral" size="sm">{providerDisplayName(item.provider, providerNodes)}</Badge>
                  </span>
                </div>
              </td>
              <td class="py-3 px-4 font-bold text-text-main max-w-[140px] truncate">
                <div class="flex items-center gap-1.5">
                  {#if item.provider}
                    <img
                      src={getIconPath(item.provider)}
                      alt={item.model}
                      class="w-3.5 h-3.5 object-contain rounded shrink-0 bg-surface-2 p-0.5 border border-border/40"
                      onerror={(e) => {
                        (e.currentTarget as HTMLElement).style.display = 'none'
                      }}
                      loading="lazy"
                    />
                  {/if}
                  <span class="truncate">{item.model}</span>
                </div>
              </td>
              <td class="py-3 px-4 text-right text-text-muted">
                {item.latency?.ttft ? `${item.latency.ttft}ms` : '—'}
              </td>
              <td class="py-3 px-4 text-right text-text-main font-medium">
                {item.latency?.total ? `${item.latency.total}ms` : '—'}
              </td>
              <td class="py-3 px-4 text-right text-brand-500 whitespace-nowrap">
                <div class="inline-flex items-center justify-end gap-1.5">
                  <span>{fmt(item.tokens?.prompt_tokens)}</span>
                  {#if item.tokens?.saved_tokens && item.tokens.saved_tokens > 0}
                    <span title={`Saved ${fmt(item.tokens.saved_tokens)} tokens`}>
                      <Badge variant="success" size="sm" class="text-[10px] px-1.5 py-0 font-semibold">
                        {item.tokens.saved_percent ? `-${item.tokens.saved_percent}% RTK` : `saved ${fmt(item.tokens.saved_tokens)}`}
                      </Badge>
                    </span>
                  {/if}
                </div>
              </td>
              <td class="py-3 px-4 text-right text-success">
                {fmt(item.tokens?.completion_tokens)}
              </td>
              <td class="py-3 px-4 text-right text-info">
                {fmt(cachedTokensFor(item))}
              </td>
              <td class="py-3 px-4 text-right">
                <button
                  type="button"
                  onclick={(e) => {
                    e.stopPropagation()
                    openDetail(item)
                  }}
                  class="text-xs text-brand-500 hover:underline font-semibold cursor-pointer"
                >
                  View
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <!-- Pagination Controls -->
    <div class="px-4 py-3 border-t border-border flex items-center justify-between text-xs text-text-muted bg-surface-2">
      <span>Showing page {detailsPage} of {Math.ceil(detailsTotal / 20) || 1}</span>
      <div class="flex items-center gap-2">
        <Button
          variant="secondary"
          size="sm"
          disabled={detailsPage <= 1}
          onclick={() => onPageChange(detailsPage - 1)}
        >
          Previous
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={detailsPage * 20 >= detailsTotal}
          onclick={() => onPageChange(detailsPage + 1)}
        >
          Next
        </Button>
      </div>
    </div>
  {/if}
</Card>

<!-- Slide-over Request Inspector Modal -->
{#if selectedDetail}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm">
    <div class="w-full max-w-2xl max-h-[85vh] rounded-2xl bg-surface border border-border shadow-2xl flex flex-col overflow-hidden">
      <!-- Modal Header -->
      <div class="px-6 py-4 border-b border-border flex items-center justify-between bg-surface-2">
        <div class="flex items-center gap-2">
          <span class="w-2.5 h-2.5 rounded-full {selectedDetail.status === 'success' ? 'bg-success' : 'bg-red-500'}"></span>
          <h3 class="font-headline text-base font-bold text-text-main">{selectedDetail.model}</h3>
          <span title={selectedDetail.provider || undefined}>
            <Badge variant="neutral" size="sm">{providerDisplayName(selectedDetail.provider, providerNodes)}</Badge>
          </span>
        </div>
        <button
          type="button"
          onclick={closeDetail}
          class="p-1 rounded-lg text-text-muted hover:text-text-main hover:bg-surface-3 transition-colors cursor-pointer"
        >
          <X class="w-5 h-5" />
        </button>
      </div>

      <!-- Modal Body -->
      <div class="flex-1 overflow-y-auto p-6 space-y-4 font-body text-xs">
        <!-- Token Saver (RTK) Card -->
        {#if selectedDetail.tokens?.saved_tokens && selectedDetail.tokens.saved_tokens > 0}
          <div class="p-3.5 rounded-xl bg-success/10 border border-success/25 flex flex-col sm:flex-row sm:items-center justify-between gap-2.5">
            <div class="flex items-center gap-2">
              <Zap class="w-4 h-4 text-success shrink-0" />
              <Badge variant="success" size="sm" class="font-semibold">Token Saver (RTK)</Badge>
            </div>
            <div class="font-code text-xs text-text-main">
              Compressed: <span class="text-text-muted">{fmt(selectedDetail.tokens.original_input_tokens ?? ((selectedDetail.tokens.prompt_tokens || 0) + (selectedDetail.tokens.saved_tokens || 0)))}</span> → <span class="font-bold text-success">{fmt(selectedDetail.tokens.prompt_tokens)}</span> <span class="text-success font-medium">({selectedDetail.tokens.saved_percent ?? 0}% saved)</span>
            </div>
          </div>
        {/if}

        <!-- Metadata Row / Tokens Grid -->
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold">Latency</div>
            <div class="font-code text-sm font-bold text-text-main mt-1">
              {selectedDetail.latency?.total || 0}ms
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold">TTFT</div>
            <div class="font-code text-sm font-bold text-text-main mt-1">
              {selectedDetail.latency?.ttft || 0}ms
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold">Total Input</div>
            <div class="font-code text-sm font-bold text-brand-500 mt-1">
              {fmt(selectedDetail.tokens?.prompt_tokens)}
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold">Total Output</div>
            <div class="font-code text-sm font-bold text-success mt-1">
              {fmt(selectedDetail.tokens?.completion_tokens)}
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border col-span-1 sm:col-span-2">
            <div class="text-text-muted text-[10px] uppercase font-bold">Cached Tokens</div>
            <div class="font-code text-sm font-bold text-info mt-1">
              {fmt(cachedTokensFor(selectedDetail))}
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border col-span-1 sm:col-span-2">
            <div class="text-text-muted text-[10px] uppercase font-bold">Reasoning Tokens</div>
            <div class="font-code text-sm font-bold {(selectedDetail.tokens?.reasoning_tokens || 0) > 0 ? 'text-warning' : 'text-text-muted'} mt-1">
              {fmt(selectedDetail.tokens?.reasoning_tokens || 0)}
            </div>
          </div>
        </div>

        <!-- Raw JSON details inspector. The list omits request/response bodies,
             so this stays a loading state until the by-id read fills it in. -->
        <div class="space-y-1.5">
          <span class="font-semibold text-text-main uppercase text-[10px] tracking-wider">Payload</span>
          {#if detailError}
            <div role="alert" class="p-3 rounded-xl bg-red-500/10 border border-red-500/25 font-body text-[11px] text-red-600 dark:text-red-400 break-words">
              {detailError}
            </div>
          {:else if detailLoading}
            <div class="p-8 rounded-xl bg-bg border border-border flex items-center justify-center gap-2 font-body text-[11px] text-text-muted">
              <span class="w-3.5 h-3.5 border-2 border-brand-500 border-t-transparent rounded-full animate-spin"></span>
              Loading full payload...
            </div>
          {:else}
            <pre class="p-4 rounded-xl bg-bg border border-border font-code text-[11px] text-text-main overflow-x-auto max-h-80 leading-relaxed">
{JSON.stringify(selectedDetail, null, 2)}
            </pre>
          {/if}
        </div>
      </div>

      <!-- Modal Footer -->
      <div class="px-6 py-3 border-t border-border bg-surface-2 flex justify-end">
        <Button variant="secondary" size="sm" onclick={closeDetail}>
          Close
        </Button>
      </div>
    </div>
  </div>
{/if}
