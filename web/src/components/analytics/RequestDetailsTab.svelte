<script lang="ts">
  import { RefreshCw, X } from 'lucide-svelte'
  import Badge from '../../lib/ui/Badge.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'
  import { api, normalizeLastError, type ProviderConnection } from '../../api/client'
  import { copyToClipboard } from '../../lib/clipboard'
  import {
    cachedTokensFor,
    calculateTPS,
    fmt,
    formatDuration,
    providerDisplayName,
    timeAgo,
    formatLocalTimestamp,
    type RequestDetailItem,
  } from './types'

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
    connections?: ProviderConnection[]
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
    connections = [],
  }: Props = $props()

  let selectedDetail = $state<RequestDetailItem | null>(null)
  let searchFilter = $state('')
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
        selectedDetail = { ...selectedDetail, ...(res.detail as RequestDetailItem) }
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

  function resolveAccount(item: RequestDetailItem | null | undefined): { name: string; email?: string } {
    if (!item) return { name: 'Default' }
    if (item.account) {
      if (item.account.includes('@')) {
        return { name: item.connName || item.account, email: item.account }
      }
      return { name: item.account, email: item.connEmail }
    }
    if (item.connName) return { name: item.connName, email: item.connEmail }
    if (item.connEmail) return { name: item.connEmail, email: item.connEmail }
    if (item.connectionId) {
      const conn = connections.find((c) => c.id === item.connectionId)
      if (conn?.name) return { name: conn.name, email: conn.email || undefined }
      if (conn?.email) return { name: conn.email, email: conn.email }
      return { name: item.connectionId }
    }
    return { name: 'Default' }
  }

  let displayDetails = $derived.by(() => {
    if (!searchFilter.trim()) return details
    const q = searchFilter.toLowerCase()
    return details.filter((item) => {
      const acc = resolveAccount(item).name.toLowerCase()
      const prov = (item.provider || '').toLowerCase()
      const mdl = (item.model || '').toLowerCase()
      const id = (item.id || '').toLowerCase()
      return acc.includes(q) || prov.includes(q) || mdl.includes(q) || id.includes(q)
    })
  })
</script>

<Card padding="none" class="overflow-hidden border border-border">
  <div class="px-5 py-3 border-b border-border flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between bg-surface-2">
    <div>
      <h2 class="font-headline text-sm font-bold text-text-main">Recent Request Details</h2>
      <p class="font-body text-xs text-text-muted">Total recorded: {detailsTotal.toLocaleString()} requests</p>
    </div>

    <div class="flex items-center gap-2">
      <!-- Search Filter -->
      <div class="relative">
        <span class="material-symbols-outlined absolute left-2.5 top-1/2 -translate-y-1/2 text-[15px] text-text-subtle">
          search
        </span>
        <input
          type="text"
          placeholder="Filter model, account, provider..."
          bind:value={searchFilter}
          class="w-44 sm:w-56 rounded-xl border border-border bg-surface pl-8 pr-3 py-1 text-xs text-text-main focus:outline-none focus:border-brand-500"
        />
      </div>

      <Button variant="secondary" size="sm" onclick={onRefresh} disabled={detailsLoading}>
        <RefreshCw class="w-3.5 h-3.5 mr-1 {detailsLoading ? 'animate-spin' : ''}" />
        Refresh
      </Button>
    </div>
  </div>

  {#if detailsError}
    <div role="alert" class="flex items-start gap-2 border-b border-red-500/30 px-5 py-3 bg-red-500/5">
      <span class="material-symbols-outlined mt-px text-[18px] text-red-600 dark:text-red-400" aria-hidden="true">error</span>
      <div class="min-w-0 flex-1">
        <p class="font-headline text-xs font-semibold text-red-600 dark:text-red-400">Failed to load request history</p>
        <p class="mt-0.5 break-words font-body text-[11px] text-text-muted">{detailsError}</p>
      </div>
    </div>
  {/if}

  {#if detailsLoading}
    <div class="p-12 text-center text-text-muted text-sm flex items-center justify-center gap-2">
      <RefreshCw class="w-4 h-4 animate-spin" />
      Loading request history...
    </div>
  {:else if displayDetails.length === 0}
    <div class="p-12 text-center text-text-muted text-sm font-body">
      {#if detailsError}
        Nothing to show: the read failed, so the request history is unknown.
      {:else if searchFilter}
        No requests match filter "{searchFilter}".
      {:else}
        No request details recorded yet.
      {/if}
    </div>
  {:else}
    <div class="overflow-x-auto">
      <table class="w-full text-left border-collapse text-xs font-body">
        <thead class="bg-surface-2 border-b border-border text-text-muted uppercase text-[10px] font-semibold tracking-wider">
          <tr>
            <th class="py-3 px-4 w-2"></th>
            <th class="py-3 px-4">Time</th>
            <th class="py-3 px-4">Account</th>
            <th class="py-3 px-4">Provider</th>
            <th class="py-3 px-4">Model</th>
            <th class="py-3 px-4 text-right">TTFT</th>
            <th class="py-3 px-4 text-right">Duration</th>
            <th class="py-3 px-4 text-right">Prompt</th>
            <th class="py-3 px-4 text-right">Completion</th>
            <th class="py-3 px-4 text-right">Cached</th>
            <th class="py-3 px-4 text-right">Cost</th>
            <th class="py-3 px-4 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/60 font-code text-[11px]">
          {#each displayDetails as item}
            {@const acc = resolveAccount(item)}
            <tr
              class="hover:bg-surface-2 transition-colors cursor-pointer"
              onclick={() => openDetail(item)}
            >
              <td class="py-3 px-4">
                <span
                  class="block w-2 h-2 rounded-full {item.status === 'success' || item.status === 'ok'
                    ? 'bg-success'
                    : 'bg-danger'}"
                ></span>
              </td>
              <td class="py-3 px-4 text-text-muted whitespace-nowrap text-[11px]">
                {timeAgo(item.timestamp)}
              </td>
              <td class="py-3 px-4">
                <div class="flex items-center gap-1.5 max-w-[130px] truncate" title={acc.email ? `${acc.name} (${acc.email})` : acc.name}>
                  <span class="material-symbols-outlined text-[13px] text-text-subtle shrink-0">person</span>
                  <span class="truncate font-sans font-medium text-text-main text-[11px]">{acc.name}</span>
                </div>
              </td>
              <td class="py-3 px-4">
                <div class="flex items-center gap-1.5">
                  {#if item.provider}
                    <ProviderArtwork
                      id={item.provider}
                      alt={item.provider}
                      class="w-3.5 h-3.5 object-contain rounded shrink-0"
                    />
                  {/if}
                  <span title={item.provider || undefined} class="font-sans">
                    {providerDisplayName(item.provider, providerNodes)}
                  </span>
                </div>
              </td>
              <td class="py-3 px-4 font-mono text-[11px] text-text-muted">
                <span class="truncate block max-w-[120px]" title={item.model}>{item.model}</span>
              </td>
              <td class="py-3 px-4 text-right">
                {item.latency?.ttft && item.latency.ttft > 0 ? formatDuration(item.latency.ttft) : '—'}
              </td>
              <td class="py-3 px-4 text-right font-medium text-text-main">
                {formatDuration(item.latency?.total)}
              </td>
              <td class="py-3 px-4 text-right">
                <div class="flex flex-col items-end">
                  <span>{fmt(item.tokens?.prompt_tokens)}</span>
                  {#if item.tokens?.saved_tokens}
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
              <td class="py-3 px-4 text-right font-mono text-emerald-500 text-[11px]">
                {item.cost ? `$${item.cost.toFixed(4)}` : '—'}
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

    <!-- Pagination -->
    {#if detailsTotal > 50}
      <div class="px-4 py-3 border-t border-border flex items-center justify-between text-xs text-text-muted bg-surface-2 font-body">
        <div>
          Showing {details.length} of {detailsTotal.toLocaleString()} requests (Page {detailsPage})
        </div>
        <div class="flex items-center gap-1.5">
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
            disabled={detailsPage * 50 >= detailsTotal}
            onclick={() => onPageChange(detailsPage + 1)}
          >
            Next
          </Button>
        </div>
      </div>
    {/if}
  {/if}
</Card>

<!-- Detail Modal (OmniRoute style) -->
{#if selectedDetail}
  {@const acc = resolveAccount(selectedDetail)}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm">
    <div class="w-full max-w-4xl lg:max-w-5xl max-h-[90vh] rounded-2xl bg-surface border border-border shadow-2xl flex flex-col overflow-hidden">
      <!-- Modal Header -->
      <div class="px-6 py-4 border-b border-border flex items-center justify-between bg-surface-2">
        <div class="flex flex-wrap items-center gap-2">
          <span
            class="w-2.5 h-2.5 rounded-full {selectedDetail.status === 'success' || selectedDetail.status === 'ok'
              ? 'bg-success'
              : 'bg-danger'}"
          ></span>
          <h3 class="font-headline text-base font-bold text-text-main">{selectedDetail.model}</h3>
          <span title={selectedDetail.provider || undefined}>
            <Badge variant="neutral" size="sm">{providerDisplayName(selectedDetail.provider, providerNodes)}</Badge>
          </span>
          <span class="inline-flex items-center gap-1 px-2.5 py-0.5 rounded-full bg-surface border border-border text-xs font-medium text-text-main" title={acc.email ? `${acc.name} (${acc.email})` : acc.name}>
            <span class="material-symbols-outlined text-[13px] text-brand-500">person</span>
            {acc.name}
          </span>
          {#if selectedDetail.combo}
            <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full bg-purple-500/10 border border-purple-500/20 text-[11px] font-mono font-medium text-purple-400">
              <span class="material-symbols-outlined text-[12px]">layers</span>
              {selectedDetail.combo}
            </span>
          {/if}
        </div>
        <button
          type="button"
          onclick={closeDetail}
          class="p-1 rounded-lg text-text-muted hover:text-text-main hover:bg-surface-3 transition-colors cursor-pointer"
          aria-label="Close"
        >
          <X class="w-5 h-5" />
        </button>
      </div>

      <!-- Modal Body -->
      <div class="flex-1 overflow-y-auto p-6 space-y-4 font-body text-xs">
        <!-- Top Metrics / Execution Summary (OmniRoute style) -->
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Duration</div>
            <div class="font-code text-sm font-bold text-text-main mt-1" title={selectedDetail.latency?.total ? `${selectedDetail.latency.total}ms` : undefined}>
              {formatDuration(selectedDetail.latency?.total)}
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">TTFT</div>
            <div class="font-code text-sm font-bold text-text-main mt-1" title={selectedDetail.latency?.ttft ? `${selectedDetail.latency.ttft}ms` : undefined}>
              {selectedDetail.latency?.ttft && selectedDetail.latency.ttft > 0 ? formatDuration(selectedDetail.latency.ttft) : '— (non-stream)'}
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Speed</div>
            <div class="font-code text-sm font-bold text-emerald-500 mt-1">
              {calculateTPS(selectedDetail.tokens?.completion_tokens, selectedDetail.latency?.total, selectedDetail.latency?.ttft) || '—'}
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Status</div>
            <div class="font-code text-sm font-bold {selectedDetail.status === 'success' || selectedDetail.status === 'ok' ? 'text-success' : 'text-danger'} mt-1">
              {selectedDetail.status === 'success' || selectedDetail.status === 'ok' ? '200 OK' : (selectedDetail.status || 'Error')}
            </div>
          </div>
        </div>

        <!-- Token Group: Input (OmniRoute style) -->
        {#if selectedDetail.tokens}
          {@const promptTokens = selectedDetail.tokens.prompt_tokens ?? 0}
          {@const cacheRead = cachedTokensFor(selectedDetail)}
          {@const savedTokens = selectedDetail.tokens.saved_tokens ?? 0}
          {@const fromTokens = savedTokens > 0 ? (promptTokens + savedTokens) : promptTokens}
          {@const savedPct = fromTokens > 0 ? Math.round((savedTokens / fromTokens) * 100) : 0}
          {@const compTokens = selectedDetail.tokens.completion_tokens ?? 0}
          {@const reasoningTokens = selectedDetail.tokens.reasoning_tokens ?? 0}

          <div class="p-3.5 rounded-xl bg-surface-2/60 border border-border space-y-1.5">
            <div class="text-[10px] text-text-muted uppercase font-bold tracking-wider">
              Input
            </div>
            <div class="flex flex-wrap items-center gap-1.5 font-code">
              <span class="px-2 py-0.5 rounded bg-blue-500/20 text-blue-700 dark:text-blue-300 text-xs font-bold">
                Total In: {fmt(promptTokens)}
              </span>
              {#if cacheRead > 0}
                <span class="px-2 py-0.5 rounded bg-cyan-500/20 text-cyan-700 dark:text-cyan-300 text-xs font-bold">
                  Cache Read: {fmt(cacheRead)}
                  {#if promptTokens > 0}
                    <span class="font-normal opacity-85 font-sans text-[11px]">({Math.min(100, Math.round((cacheRead / promptTokens) * 100))}%)</span>
                  {/if}
                </span>
              {/if}
              <span class="px-2 py-0.5 rounded bg-surface-3 text-text-muted text-xs">
                Cache Write: {selectedDetail.tokens.cache_creation_input_tokens ? fmt(selectedDetail.tokens.cache_creation_input_tokens) : 'N/A'}
              </span>
              {#if savedTokens > 0}
                <span class="px-2 py-0.5 rounded bg-purple-500/20 text-purple-700 dark:text-purple-300 text-xs font-bold">
                  Compressed: {fmt(fromTokens)} &rarr; {fmt(promptTokens)} ({savedPct}% saved)
                </span>
              {/if}
            </div>
          </div>

          <!-- Token Group: Output (OmniRoute style) -->
          <div class="p-3.5 rounded-xl bg-surface-2/60 border border-border space-y-1.5">
            <div class="text-[10px] text-text-muted uppercase font-bold tracking-wider">
              Output
            </div>
            <div class="flex flex-wrap items-center gap-1.5 font-code">
              <span class="px-2 py-0.5 rounded bg-emerald-500/20 text-emerald-700 dark:text-emerald-400 text-xs font-bold">
                Total Out: {fmt(compTokens)}
              </span>
              {#if reasoningTokens > 0}
                <span class="px-2 py-0.5 rounded bg-amber-500/20 text-amber-700 dark:text-amber-300 text-xs font-bold">
                  Reasoning: {fmt(reasoningTokens)}
                </span>
              {/if}
            </div>
          </div>
        {/if}

        <!-- Routing & Account Details (OmniRoute style) -->
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 pt-1">
          <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Started At</div>
            <div class="font-code text-xs text-text-main font-medium">
              {formatLocalTimestamp(selectedDetail.startedAt || selectedDetail.timestamp)}
            </div>
          </div>
          <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Ended At</div>
            <div class="font-code text-xs text-text-main font-medium">
              {formatLocalTimestamp(selectedDetail.endedAt)}
            </div>
          </div>
          <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Requested Model</div>
            <div class="font-code text-xs text-text-main break-all font-medium">
              {selectedDetail.requestedModel || selectedDetail.model}
            </div>
          </div>
          <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Req Protocol</div>
            <div class="font-code text-xs text-text-main font-medium">
              {selectedDetail.protocol || 'OpenAI-Chat'}
            </div>
          </div>
          <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Cache Source</div>
            <div class="font-code text-xs text-cyan-400 font-medium">
              {selectedDetail.cacheSource || (cachedTokensFor(selectedDetail) > 0 ? 'Upstream (Provider)' : 'None')}
            </div>
          </div>
          <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Account</div>
            <div class="flex items-center justify-between gap-1">
              <div class="font-code text-xs text-text-main break-all font-medium" title={acc.name}>
                {acc.name}
              </div>
              <button
                type="button"
                onclick={() => copyToClipboard(acc.name)}
                class="text-text-subtle hover:text-text-main p-0.5 rounded hover:bg-surface-3 transition-colors shrink-0"
                title="Copy Account"
              >
                <span class="material-symbols-outlined text-[13px]">content_copy</span>
              </button>
            </div>
          </div>
          <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">API Key</div>
            <div class="font-code text-xs text-text-main break-all font-mono" title={selectedDetail.apiKey || 'Default'}>
              {selectedDetail.apiKey || 'Default'}
            </div>
          </div>
          <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Combo</div>
            <div class="font-code text-xs text-purple-400 font-mono font-medium">
              {selectedDetail.combo || 'Direct (None)'}
            </div>
          </div>
          <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Est. Cost</div>
            <div class="font-code text-xs text-emerald-500 font-bold">
              ${selectedDetail.cost ? selectedDetail.cost.toFixed(4) : '0.0000'}
            </div>
          </div>
          {#if selectedDetail.id}
            <div class="p-3 rounded-xl bg-surface-2 border border-border space-y-1 col-span-1 sm:col-span-2 lg:col-span-3">
              <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Request ID</div>
              <div class="font-code text-xs text-text-muted flex items-center justify-between gap-2" title={selectedDetail.id}>
                <span class="break-all select-all">{selectedDetail.id}</span>
                <button
                  type="button"
                  onclick={() => copyToClipboard(selectedDetail?.id || '')}
                  class="text-text-subtle hover:text-text-main p-0.5 rounded hover:bg-surface-3 transition-colors shrink-0"
                  title="Copy Request ID"
                >
                  <span class="material-symbols-outlined text-[14px]">content_copy</span>
                </button>
              </div>
            </div>
          {/if}
        </div>
        <!-- Raw JSON details inspector with loading & error states -->
        <div class="space-y-1.5 pt-2 border-t border-border/60">
          <div class="flex items-center justify-between text-xs text-text-muted">
            <span class="font-semibold text-text-main uppercase text-[10px] tracking-wider">Payload</span>
            <button
              type="button"
              onclick={() => copyToClipboard(JSON.stringify(selectedDetail, null, 2))}
              class="flex items-center gap-1 text-text-muted hover:text-text-main transition-colors lowercase font-normal"
            >
              <span class="material-symbols-outlined text-[13px]">content_copy</span>
              copy json
            </button>
          </div>
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
            <pre class="font-code text-[11px] p-3 rounded-lg bg-surface-3/80 border border-border-subtle max-h-60 overflow-y-auto overflow-x-auto text-text-main whitespace-pre-wrap">
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
