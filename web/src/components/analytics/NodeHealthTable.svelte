<script lang="ts">
  // Node Health table for the Details section (issue #280): providers ranked by
  // a 7-day health score, expandable to the models under each node. Port of
  // techysy/10router's ScoreTable in UsageDashboard.
  import Badge from '../../lib/ui/Badge.svelte'
  import type { UsageNodeHealth } from '../../api/client'

  interface Props {
    nodes: UsageNodeHealth[]
    models: UsageNodeHealth[]
  }

  let { nodes = [], models = [] }: Props = $props()

  let expanded = $state<Set<string>>(new Set())

  // A node with zero latency observations has no ranking signal at all and
  // stays off the list (display-level filter; the API still returns it).
  const ranked = $derived(nodes.filter((n) => n.score != null))

  function toggle(name: string) {
    const next = new Set(expanded)
    if (next.has(name)) next.delete(name)
    else next.add(name)
    expanded = next
  }

  function scoreVariant(score: number): 'success' | 'warning' | 'error' {
    if (score >= 90) return 'success'
    if (score >= 70) return 'warning'
    return 'error'
  }

  function fmtLatency(ms: number | null): string {
    if (ms == null) return '—'
    if (ms < 1000) return `${ms}ms`
    return `${(ms / 1000).toFixed(1)}s`
  }

  function fmtSpeed(tps: number | null): string {
    if (tps == null || Number.isNaN(tps)) return '—'
    return `${tps.toLocaleString()} tok/s`
  }

  function fmtLastUsed(iso: string): string {
    if (!iso) return '—'
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return '—'
    return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  }

  // Model/provider strings come from live gateway responses and custom-channel
  // imports; normalize whitespace for display only.
  function cleanName(v?: string): string {
    return String(v ?? '').trim().replace(/\s+/g, ' ')
  }

  function rateClass(rate: number): string {
    if (rate >= 90) return 'text-success'
    if (rate >= 70) return 'text-warning'
    return 'text-red-500'
  }

  function modelsFor(node: UsageNodeHealth): UsageNodeHealth[] {
    return models.filter((m) => m.provider === node.provider)
  }
</script>

{#if ranked.length === 0}
  <p class="py-6 text-center text-sm text-text-muted">No nodes with 50+ requests in this period</p>
{:else}
  <div class="overflow-x-auto">
    <table class="w-full min-w-[760px] text-left border-collapse text-xs">
      <thead class="bg-surface-2 border-b border-border text-text-muted uppercase text-[10px] font-semibold tracking-wider">
        <tr>
          <th class="py-2.5 px-3">Name</th>
          <th class="py-2.5 px-3 text-center">Health</th>
          <th class="py-2.5 px-3 text-right">Success</th>
          <th class="py-2.5 px-3 text-right">Avg TTFT</th>
          <th class="py-2.5 px-3 text-right">Avg Latency</th>
          <th class="py-2.5 px-3 text-right">Avg Speed</th>
          <th class="py-2.5 px-3 text-right">Requests</th>
          <th class="py-2.5 px-3 text-right">Last Used</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border/60">
        {#each ranked as node (node.provider)}
          {@const open = expanded.has(node.provider)}
          {@const children = modelsFor(node)}
          <tr
            class="hover:bg-surface-2/60 transition-colors cursor-pointer"
            onclick={() => toggle(node.provider)}
            aria-expanded={open}
          >
            <td class="py-2.5 px-3 min-w-0">
              <div class="flex min-w-0 items-center gap-1">
                <span class="material-symbols-outlined shrink-0 text-[16px] leading-none text-text-muted" aria-hidden="true">
                  {open ? 'expand_more' : 'chevron_right'}
                </span>
                <span class="truncate font-medium text-text-main" title={cleanName(node.name)}>{cleanName(node.name)}</span>
              </div>
            </td>
            <td class="py-2.5 px-3 text-center">
              <Badge variant={scoreVariant(node.score as number)} size="sm">{node.score}</Badge>
            </td>
            <td class="py-2.5 px-3 text-right font-mono {rateClass(node.successRate)}">{node.successRate}%</td>
            <td class="py-2.5 px-3 text-right font-mono text-text-main">{fmtLatency(node.avgTtftMs)}</td>
            <td class="py-2.5 px-3 text-right font-mono text-text-main">{fmtLatency(node.avgLatencyMs)}</td>
            <td class="py-2.5 px-3 text-right font-mono text-text-main">{fmtSpeed(node.avgSpeed)}</td>
            <td class="py-2.5 px-3 text-right font-mono text-text-main">{node.requests.toLocaleString()}</td>
            <td class="py-2.5 px-3 text-right text-text-muted whitespace-nowrap">{fmtLastUsed(node.lastUsed)}</td>
          </tr>
          {#if open}
            {#if children.length === 0}
              <tr>
                <td colspan="8" class="py-3 px-6 text-text-muted italic">No models with 10+ requests under this node</td>
              </tr>
            {:else}
              {#each children as m (m.provider + '|' + m.model)}
                <tr class="bg-bg/40">
                  <td class="py-2 pl-10 pr-3 min-w-0">
                    <div class="truncate font-medium text-text-main" title={cleanName(m.model)}>{cleanName(m.model)}</div>
                    <div class="truncate text-[10px] text-text-muted">{cleanName(m.provider)}</div>
                  </td>
                  <td class="py-2 px-3 text-center">
                    {#if m.score != null}
                      <Badge variant={scoreVariant(m.score)} size="sm">{m.score}</Badge>
                    {:else}
                      <span class="text-text-muted">—</span>
                    {/if}
                  </td>
                  <td class="py-2 px-3 text-right font-mono {rateClass(m.successRate)}">{m.successRate}%</td>
                  <td class="py-2 px-3 text-right font-mono text-text-main">{fmtLatency(m.avgTtftMs)}</td>
                  <td class="py-2 px-3 text-right font-mono text-text-main">{fmtLatency(m.avgLatencyMs)}</td>
                  <td class="py-2 px-3 text-right font-mono text-text-main">{fmtSpeed(m.avgSpeed)}</td>
                  <td class="py-2 px-3 text-right font-mono text-text-main">{m.requests.toLocaleString()}</td>
                  <td class="py-2 px-3 text-right text-text-muted whitespace-nowrap">{fmtLastUsed(m.lastUsed)}</td>
                </tr>
              {/each}
            {/if}
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
{/if}
