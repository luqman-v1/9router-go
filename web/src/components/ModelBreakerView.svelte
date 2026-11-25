<script lang="ts">
  import { onMount } from 'svelte'
  import Badge from '../lib/ui/Badge.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import Input from '../lib/ui/Input.svelte'
  import { api, type ModelBreakerStatus } from '../api/client'
  import { notifications } from '../lib/notifications'

  // Per-model circuit breaker panel.
  //
  // This is NOT the provider breaker: a provider that is down parks every model
  // it serves, which is the provider layer's job and a different panel. This one
  // exists for the case where one model inside a healthy provider starts failing
  // deterministically — that model must sit out its cooldown while its siblings
  // keep serving. So the panel reads from the per-pair registry, and a row that
  // appears here means exactly one pair is being held back.

  let models = $state<ModelBreakerStatus[]>([])
  let loading = $state(true)
  let providerFilter = $state('')
  let search = $state('')
  let resettingPair = $state<string | null>(null)
  let resettingAll = $state(false)

  let heldBack = $derived(models.filter((m) => m.state === 'cooling' || m.state === 'disabled'))
  let visible = $derived(
    models.filter((m) => {
      if (providerFilter && m.provider.toLowerCase() !== providerFilter.toLowerCase()) return false
      if (search && !m.model.toLowerCase().includes(search.toLowerCase())) return false
      return true
    })
  )

  function formatRetry(ms: number): string {
    if (ms <= 0) return '—'
    const secs = Math.round(ms / 1000)
    if (secs < 60) return `${secs}s`
    return `${Math.floor(secs / 60)}m ${secs % 60}s`
  }

  function stateBadge(state: ModelBreakerStatus['state']): { tone: 'success' | 'warning' | 'danger'; label: string } {
    if (state === 'disabled') return { tone: 'danger', label: 'Disabled' }
    if (state === 'cooling') return { tone: 'warning', label: 'Cooling' }
    return { tone: 'success', label: 'Serving' }
  }

  async function load() {
    loading = true
    try {
      const resp = await api.getModelBreakers()
      models = resp.models || []
    } catch (err) {
      notifications.error('Failed to load model breakers')
      models = []
    } finally {
      loading = false
    }
  }

  async function resetPair(m: ModelBreakerStatus) {
    const key = `${m.provider}/${m.model}`
    resettingPair = key
    try {
      await api.resetModelBreaker(m.provider, m.model)
      notifications.success(`${key} is serving again`)
      await load()
    } catch {
      notifications.error(`Failed to reset ${key}`)
    } finally {
      resettingPair = null
    }
  }

  async function resetAll() {
    resettingAll = true
    try {
      await api.resetAllModelBreakers()
      notifications.success('Every held-back model is serving again')
      await load()
    } catch {
      notifications.error('Failed to reset all model breakers')
    } finally {
      resettingAll = false
    }
  }

  onMount(load)
</script>

<div class="space-y-4">
  <Card title="Model breakers" subtitle="Per-model circuit breakers — one bad model must not park the whole provider">
    {#snippet action()}
      <div class="flex items-center gap-2">
        <Input placeholder="Filter model…" bind:value={search} />
        <Input placeholder="Provider…" bind:value={providerFilter} />
        <Button variant="ghost" size="sm" loading={loading} onclick={load}>Refresh</Button>
        <Button variant="secondary" size="sm" loading={resettingAll} disabled={heldBack.length === 0} onclick={resetAll}>
          Reset all
        </Button>
      </div>
    {/snippet}

    {#if loading}
      <p class="text-sm text-text-muted">Loading…</p>
    {:else if models.length === 0}
      <p class="text-sm text-text-muted">
        No model has been tracked yet. A model appears here once it has failed.
      </p>
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-left text-text-muted border-b border-border-subtle">
              <th class="py-2 pr-4">Provider</th>
              <th class="py-2 pr-4">Model</th>
              <th class="py-2 pr-4">State</th>
              <th class="py-2 pr-4 text-right">Failures</th>
              <th class="py-2 pr-4 text-right">Cycles</th>
              <th class="py-2 pr-4 text-right">Retry in</th>
              <th class="py-2"></th>
            </tr>
          </thead>
          <tbody>
            {#each visible as m (`${m.provider}/${m.model}`)}
              {@const badge = stateBadge(m.state)}
              <tr class="border-b border-border-subtle last:border-0">
                <td class="py-2 pr-4 text-text-main">{m.provider}</td>
                <td class="py-2 pr-4 text-text-main font-mono text-xs">{m.model}</td>
                <td class="py-2 pr-4">
                  <Badge tone={badge.tone} size="sm">{badge.label}</Badge>
                </td>
                <td class="py-2 pr-4 text-right text-text-muted">{m.failures}</td>
                <td class="py-2 pr-4 text-right text-text-muted">{m.cooldowns}</td>
                <td class="py-2 pr-4 text-right text-text-muted">{formatRetry(m.retryAfterMs)}</td>
                <td class="py-2 text-right">
                  {#if m.state !== 'closed'}
                    <Button
                      variant="secondary"
                      size="sm"
                      loading={resettingPair === `${m.provider}/${m.model}`}
                      onclick={() => resetPair(m)}
                    >
                      Reset
                    </Button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if visible.length === 0}
        <p class="text-sm text-text-muted mt-2">No tracked model matches that filter.</p>
      {/if}
    {/if}
  </Card>

  <Card title="What the states mean">
    <ul class="space-y-2 text-sm text-text-muted">
      <li>
        <Badge tone="success" size="sm">Serving</Badge>
        — the pair is admitted. It is listed because it has failed before and recovered.
      </li>
      <li>
        <Badge tone="warning" size="sm">Cooling</Badge>
        — enough model-scoped failures opened a cooldown. Requests fail over instead of being
        dialled, and the pair is probed again once the timer elapses. Each further cycle doubles the
        wait, capped at 10 minutes.
      </li>
      <li>
        <Badge tone="danger" size="sm">Disabled</Badge>
        — the pair failed through every cooldown cycle, so it serves nothing until it is reset.
        Waiting does not fix it, which is why there is no timer: reset is the only way back.
      </li>
    </ul>
    <p class="text-xs text-text-muted mt-3">
      Only failures the model itself owns are counted — a missing model, or a prompt it cannot fit.
      A provider outage or a throttled account belongs to a different layer, so it never appears here.
    </p>
  </Card>
</div>