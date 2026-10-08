<script lang="ts">
  import { Clock } from 'lucide-svelte'
  import type { ProviderConnection } from '../../api/client'
  import Card from '../../lib/ui/Card.svelte'
  import Toggle from '../../lib/ui/Toggle.svelte'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'

  interface Props {
    conn: ProviderConnection
    latency: number | null | undefined
    isTesting: boolean
    onTestLatency: () => void
    onToggle: () => void
  }

  let { conn, latency, isTesting, onTestLatency, onToggle }: Props = $props()
</script>

<Card class="p-4 flex items-center justify-between gap-3 {conn.isActive === 1 ? 'border-border' : 'border-border opacity-60 bg-surface-2/40'}">
  <div class="flex items-center gap-3 min-w-0">
    <div class="w-2.5 h-2.5 rounded-full shrink-0 {conn.isActive === 1 ? ((conn.testStatus === 'error' || conn.testStatus === 'failed' || (conn.testStatus !== 'active' && conn.testStatus !== 'passed' && conn.testStatus !== 'success' && conn.lastError)) ? 'bg-red-500' : 'bg-emerald-500') : 'bg-text-muted/40'}"></div>
    <div class="min-w-0">
      <p class="text-sm font-medium text-text-main truncate">
        {formatEmailLabel(conn.displayName || conn.name || conn.email || conn.id, $emailPrivacy)}
      </p>
      <div class="flex items-center gap-2 text-xs text-text-muted mt-0.5">
        <span class="font-mono text-[11px]">{conn.authType}</span>
        {#if latency !== undefined && latency !== null}
          <span class="text-emerald-600 dark:text-emerald-400 font-mono text-[11px]">• {latency}ms</span>
        {/if}
        {#if conn.lastError}
          <span class="text-red-500 truncate max-w-[150px]">• {conn.lastError}</span>
        {/if}
      </div>
    </div>
  </div>

  <div class="flex items-center gap-2 shrink-0">
    <button
      type="button"
      title="Test connection latency"
      disabled={isTesting}
      onclick={onTestLatency}
      class="px-2 py-1 text-xs rounded-lg border border-border bg-surface hover:bg-surface-2 text-text-muted hover:text-text-main transition-colors flex items-center gap-1 cursor-pointer disabled:opacity-50"
    >
      <Clock class="w-3.5 h-3.5 {isTesting ? 'animate-spin' : ''}" />
      <span>{isTesting ? 'Pinging…' : latency !== undefined && latency !== null ? `${latency}ms` : 'Ping'}</span>
    </button>
    <Toggle size="sm" checked={conn.isActive === 1} onChange={onToggle} />
  </div>
</Card>
