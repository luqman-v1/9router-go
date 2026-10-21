<script lang="ts">
  import { onDestroy, onMount } from 'svelte'
  import {
    api,
    type ModelTestRow,
    type ModelTestRun,
    type ModelTestTarget,
    type ProviderConnection,
    type ProviderNode,
  } from '../../api/client'
  import { getModelKind, getModelsByProviderId, PROVIDER_ID_TO_ALIAS } from '../../lib/models'
  import { PROVIDER_CATALOG } from '../../lib/providers'
  import { parseCustomModelsResponse } from '../../lib/customModels'

  interface Props {
    connections: ProviderConnection[]
    providerNodes: ProviderNode[]
  }

  let { connections, providerNodes }: Props = $props()

  // The page follows the JRouters testing layout: provider chips scope the
  // sweep, one table row per model, statuses banded by latency. The sweep
  // itself still runs server-side (POST /api/model-tests/runs), so it
  // survives navigation and probes every kind, not only chat; verdicts are
  // merged into a per-model map (and kept in localStorage) the way the
  // JRouters store accumulates them, so a per-row Retry never wipes the
  // rest of the table.

  interface Testable {
    key: string
    model: string
    modelId: string
    providerId: string
    providerName: string
    providerAlias: string
    kind: string
    badge: string
    target: ModelTestTarget
  }

  interface ProviderGroup {
    id: string
    name: string
    count: number
  }

  type DisplayStatus =
    | 'healthy'
    | 'slow'
    | 'error'
    | 'blocked'
    | 'running'
    | 'pending'
    | 'canceled'

  // The kinds the gateway's probe dispatcher accepts (model_test_ping.go
  // isProbeKind). Anything else — web search/fetch — is not probeable and is
  // left off the list rather than offered as a button that always fails.
  const PROBE_KINDS = new Set(['embedding', 'image', 'tts', 'stt', 'video', 'systemone'])

  const KIND_LABELS: Record<string, string> = {
    '': 'Chat',
    embedding: 'Embedding',
    image: 'Image',
    tts: 'TTS',
    stt: 'STT',
    video: 'Video',
    systemone: 'System One',
  }

  const SLOW_TOTAL_THRESHOLD = 8000
  const ERROR_TOTAL_THRESHOLD = 15000
  const VERDICTS_KEY = '9router-model-test-verdicts'

  const STATUS_LABELS: Record<DisplayStatus, string> = {
    healthy: 'HEALTHY',
    slow: 'SLOW',
    error: 'ERROR',
    blocked: 'BLOCKED',
    running: 'RUNNING',
    pending: 'PENDING',
    canceled: 'CANCELED',
  }

  function probeKind(model: unknown): string | null {
    const kind = getModelKind(model)
    if (!kind || kind === 'llm' || kind === 'chat') return ''
    return PROBE_KINDS.has(kind) ? kind : null
  }

  function entryKey(target: ModelTestTarget): string {
    return `${target.provider ?? target.model ?? ''}::${target.kind ?? ''}`
  }

  function rowKey(row: ModelTestRow): string {
    return `${row.model}::${row.kind ?? ''}`
  }

  // Which providers have at least one active connection, the alias each is
  // addressed by on the wire, and the name it was registered under. A custom
  // node is addressed by its prefix ("omop/..."); its raw id
  // ("openai-compatible-chat-<uuid>") is an internal handle, never a label.
  function activeProviders(): { id: string; alias: string; name: string }[] {
    const ids = new Set<string>()
    for (const c of connections) {
      if (c.isActive === 0 || !c.provider) continue
      ids.add(c.provider)
    }
    return [...ids].map((id) => {
      const node = providerNodes.find((n) => n.id === id)
      const catalog = PROVIDER_CATALOG.find((p) => p.id === id)
      const alias = node?.prefix || catalog?.alias || PROVIDER_ID_TO_ALIAS[id] || node?.id || id
      return { id, alias, name: node?.name || catalog?.name || alias }
    })
  }

  let models = $state<Testable[]>([])
  let loading = $state(true)
  let loadError = $state('')
  let selectedProviders = $state<Set<string>>(new Set())
  let verdicts = $state<Map<string, ModelTestRow>>(new Map())
  let run = $state<ModelTestRun | null>(null)
  let actionError = $state('')
  let starting = $state(false)
  let deleting = $state(false)
  let clearedRunId = $state('')
  let pollTimer: ReturnType<typeof setInterval> | null = null

  let groups = $derived.by(() => {
    const byId = new Map<string, ProviderGroup>()
    for (const m of models) {
      const g = byId.get(m.providerId)
      if (g) g.count++
      else byId.set(m.providerId, { id: m.providerId, name: m.providerName, count: 1 })
    }
    return [...byId.values()].sort((a, b) => a.name.localeCompare(b.name))
  })

  let filtered = $derived(models.filter((m) => selectedProviders.has(m.providerId)))

  function displayStatus(row?: ModelTestRow): DisplayStatus {
    if (!row) return 'pending'
    switch (row.state) {
      case 'running':
        return 'running'
      case 'blocked':
        return 'blocked'
      case 'fail':
        return 'error'
      case 'canceled':
        return 'canceled'
      case 'pass':
        if (row.latencyMs > ERROR_TOTAL_THRESHOLD) return 'error'
        if (row.latencyMs > SLOW_TOTAL_THRESHOLD) return 'slow'
        return 'healthy'
      default:
        return 'pending'
    }
  }

  let stats = $derived.by(() => {
    let healthy = 0
    let slow = 0
    let error = 0
    let tested = 0
    for (const m of filtered) {
      const row = verdicts.get(m.key)
      if (!row || row.state === 'pending' || row.state === 'running') continue
      tested++
      const s = displayStatus(row)
      if (s === 'healthy') healthy++
      else if (s === 'slow') slow++
      else if (s !== 'canceled') error++
    }
    return { total: filtered.length, healthy, slow, error, tested }
  })

  let progressPct = $derived(
    run?.running && run.total ? Math.round((run.done / run.total) * 100) : 0
  )

  let anyTested = $derived(
    [...verdicts.values()].some((r) => r.state !== 'pending' && r.state !== 'running')
  )

  function statusClass(s: DisplayStatus): string {
    switch (s) {
      case 'healthy':
        return 'bg-emerald-500/15 text-emerald-600'
      case 'slow':
      case 'blocked':
        return 'bg-amber-500/15 text-amber-600'
      case 'error':
        return 'bg-red-500/15 text-red-600'
      case 'running':
        return 'bg-brand-500/15 text-brand-600'
      default:
        return 'bg-surface-3 text-text-muted'
    }
  }

  function rowTint(s: DisplayStatus): string {
    switch (s) {
      case 'healthy':
        return 'bg-emerald-500/5'
      case 'slow':
      case 'blocked':
        return 'bg-amber-500/5'
      case 'error':
        return 'bg-red-500/5'
      default:
        return ''
    }
  }

  function providerDot(providerId: string): string {
    let slow = false
    let healthy = false
    for (const m of models) {
      if (m.providerId !== providerId) continue
      const s = displayStatus(verdicts.get(m.key))
      if (s === 'error' || s === 'blocked') return 'bg-red-500'
      if (s === 'slow') slow = true
      if (s === 'healthy') healthy = true
    }
    if (slow) return 'bg-amber-500'
    if (healthy) return 'bg-emerald-500'
    return 'bg-text-subtle/40'
  }

  function persistVerdicts() {
    try {
      localStorage.setItem(VERDICTS_KEY, JSON.stringify([...verdicts.values()]))
    } catch {
      // Storage unavailable: verdicts still live for this page load.
    }
  }

  function loadVerdicts() {
    try {
      const raw = localStorage.getItem(VERDICTS_KEY)
      if (!raw) return
      const rows = JSON.parse(raw) as ModelTestRow[]
      verdicts = new Map(rows.map((r) => [rowKey(r), r]))
    } catch {
      // A corrupted cache is worse than none.
    }
  }

  function mergeRun(snapshot: ModelTestRun | null) {
    if (!snapshot || snapshot.id === clearedRunId) return
    const next = new Map(verdicts)
    let changed = false
    for (const row of snapshot.entries) {
      if (row.provider) continue
      next.set(rowKey(row), row)
      changed = true
    }
    if (changed) {
      verdicts = next
      persistVerdicts()
    }
  }

  async function loadModels() {
    loading = true
    loadError = ''
    try {
      const customsRes = await api.getCustomModels().catch(() => ({}))
      const customs = parseCustomModelsResponse(customsRes) as Array<{
        id: string
        providerAlias?: string
        type?: string
      }>

      const seen = new Set<string>()
      const built: Testable[] = []
      for (const { id: providerId, alias, name } of activeProviders()) {
        const catalogModels = getModelsByProviderId(providerId)
        const customModels = customs.filter(
          (m) => m.providerAlias === alias || m.providerAlias === providerId
        )

        for (const m of [...catalogModels, ...customModels]) {
          const kind = probeKind(m)
          if (kind === null) continue
          const model = `${alias}/${m.id}`
          const target: ModelTestTarget = { model, kind: kind || undefined }
          const key = entryKey(target)
          if (seen.has(key)) continue
          seen.add(key)
          built.push({
            key,
            model,
            modelId: m.id,
            providerId,
            providerName: name,
            providerAlias: alias,
            kind,
            badge: KIND_LABELS[kind] ?? kind,
            target,
          })
        }
      }

      built.sort(
        (a, b) =>
          a.providerName.localeCompare(b.providerName) || a.modelId.localeCompare(b.modelId)
      )
      models = built
    } catch (e) {
      loadError = e instanceof Error ? e.message : 'Failed to load models'
    } finally {
      loading = false
    }
  }

  async function refresh() {
    try {
      run = await api.getModelTestRun()
      mergeRun(run)
      if (!run.running) stopPolling()
    } catch {
      // A missed poll is not fatal: the next tick retries while a run is live.
    }
  }

  function startPolling() {
    if (pollTimer) return
    pollTimer = setInterval(refresh, 1500)
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  async function startRun(targets: ModelTestTarget[]) {
    if (targets.length === 0) return
    starting = true
    actionError = ''
    try {
      run = await api.startModelTestRun(targets)
      clearedRunId = ''
      mergeRun(run)
      startPolling()
    } catch (e) {
      actionError = e instanceof Error ? e.message : 'Failed to start the test run'
    } finally {
      starting = false
    }
  }

  function testSelected() {
    void startRun(filtered.map((m) => m.target))
  }

  function retryOne(m: Testable) {
    void startRun([m.target])
  }

  async function cancelRun() {
    deleting = true
    try {
      run = await api.cancelModelTestRun()
      mergeRun(run)
      stopPolling()
    } catch (e) {
      actionError = e instanceof Error ? e.message : 'Failed to cancel the run'
    } finally {
      deleting = false
    }
  }

  function clearResults() {
    verdicts = new Map()
    clearedRunId = run?.id ?? ''
    try {
      localStorage.removeItem(VERDICTS_KEY)
    } catch {
      // Nothing to clear if storage is unavailable.
    }
  }

  function toggleProvider(id: string) {
    const next = new Set(selectedProviders)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    selectedProviders = next
  }

  function downloadReport() {
    const tested = models
      .map((m) => ({ m, row: verdicts.get(m.key) }))
      .filter((x) => x.row && x.row.state !== 'pending' && x.row.state !== 'running')
    if (tested.length === 0) return

    let healthy = 0
    let slow = 0
    let error = 0
    for (const { row } of tested) {
      const s = displayStatus(row)
      if (s === 'healthy') healthy++
      else if (s === 'slow') slow++
      else if (s !== 'canceled') error++
    }
    const pct = (n: number) => Math.round((n / tested.length) * 100)

    let md = '# 9Router Model Testing Report\n\n'
    md += `**Generated:** ${new Date().toLocaleString('id-ID')}  \n`
    md += `**Total Tested:** ${tested.length} models  \n\n`
    md += '## Summary\n\n'
    md += '| Status | Count | Percentage |\n'
    md += '|--------|-------|------------|\n'
    md += `| ✓ Healthy | ${healthy} | ${pct(healthy)}% |\n`
    md += `| ⚠ Slow | ${slow} | ${pct(slow)}% |\n`
    md += `| ✗ Error | ${error} | ${pct(error)}% |\n\n`
    md += '## Results by Provider\n\n'

    const byProvider = new Map<string, typeof tested>()
    for (const x of tested) {
      const list = byProvider.get(x.m.providerName) ?? []
      list.push(x)
      byProvider.set(x.m.providerName, list)
    }
    const order: Record<string, number> = { healthy: 0, slow: 1, error: 2, blocked: 2 }
    for (const [provider, items] of byProvider) {
      md += `### ${provider}\n\n`
      md += '| Model | Status | Total | HTTP | Error |\n'
      md += '|-------|--------|-------|------|-------|\n'
      items.sort(
        (a, b) => (order[displayStatus(a.row)] ?? 3) - (order[displayStatus(b.row)] ?? 3)
      )
      for (const { m, row } of items) {
        if (!row) continue
        const s = displayStatus(row)
        const icon = s === 'healthy' ? '✓' : s === 'slow' ? '⚠' : '✗'
        md += `| \`${m.model}\` | ${icon} ${s.toUpperCase()} | ${row.latencyMs}ms | ${row.httpStatus ?? '-'} | ${row.error || '-'} |\n`
      }
      md += '\n'
    }
    md += '---\n'
    md += '*Report generated by 9Router Model Testing*\n'

    const blob = new Blob([md], { type: 'text/markdown' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `9router-testing-report-${new Date().toISOString().slice(0, 10)}.md`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  }

  onMount(() => {
    loadVerdicts()
    loadModels()
    refresh().then(() => {
      if (run?.running) startPolling()
    })
  })

  onDestroy(stopPolling)
</script>

<div class="p-4 sm:p-6 max-w-6xl mx-auto space-y-4">
  <header class="space-y-1">
    <h2 class="text-lg font-semibold text-text-main">Model Testing</h2>
    <p class="text-sm text-text-muted">
      Test models and check health status across providers. The run continues server-side, so you
      can leave this page and come back to it.
    </p>
  </header>

  {#if loadError}
    <p class="text-sm text-red-600">{loadError}</p>
  {/if}

  <section class="rounded-[10px] border border-border bg-surface p-4 space-y-3">
    <div class="flex items-center justify-between gap-3">
      <h3 class="text-sm font-semibold text-text-main">Filter by Provider</h3>
      <div class="flex gap-2">
        <button
          type="button"
          class="rounded-[8px] px-2 py-1 text-xs text-text-muted hover:bg-surface-2 hover:text-text-main"
          onclick={() => (selectedProviders = new Set(groups.map((g) => g.id)))}
        >
          Select All
        </button>
        <button
          type="button"
          class="rounded-[8px] px-2 py-1 text-xs text-text-muted hover:bg-surface-2 hover:text-text-main"
          onclick={() => (selectedProviders = new Set())}
        >
          Clear
        </button>
      </div>
    </div>
    <div class="flex flex-wrap gap-2">
      {#each groups as g (g.id)}
        {@const isSelected = selectedProviders.has(g.id)}
        <button
          type="button"
          aria-pressed={isSelected}
          onclick={() => toggleProvider(g.id)}
          class="inline-flex items-center gap-2 rounded-[8px] border px-3 py-1.5 text-xs font-medium transition-colors {isSelected
            ? 'border-brand-500 bg-brand-500/10 text-brand-600'
            : 'border-border bg-surface-2 text-text-muted hover:bg-surface'}"
        >
          <span class="h-2 w-2 rounded-full {providerDot(g.id)}"></span>
          {g.name}
          <span class="text-[10px] opacity-70">({g.count})</span>
        </button>
      {/each}
      {#if groups.length === 0 && !loading}
        <p class="text-xs text-text-muted">No active providers. Connect a provider first.</p>
      {/if}
    </div>
  </section>

  <div class="grid grid-cols-2 gap-3 sm:grid-cols-5">
    <div class="rounded-[10px] border border-border bg-surface p-4 flex items-center gap-3">
      <span class="text-lg font-semibold text-text-main">{stats.total}</span>
      <div>
        <div class="text-xs text-text-muted">Selected</div>
        <div class="text-xs text-text-subtle">Models</div>
      </div>
    </div>
    <div class="rounded-[10px] border border-border bg-surface p-4 flex items-center gap-3">
      <span class="text-lg font-semibold text-emerald-600">{stats.healthy}</span>
      <div class="text-xs text-text-muted">Healthy</div>
    </div>
    <div class="rounded-[10px] border border-border bg-surface p-4 flex items-center gap-3">
      <span class="text-lg font-semibold text-amber-600">{stats.slow}</span>
      <div class="text-xs text-text-muted">Slow</div>
    </div>
    <div class="rounded-[10px] border border-border bg-surface p-4 flex items-center gap-3">
      <span class="text-lg font-semibold text-red-600">{stats.error}</span>
      <div class="text-xs text-text-muted">Error</div>
    </div>
    <div class="rounded-[10px] border border-border bg-surface p-4 flex items-center gap-3">
      <span class="text-lg font-semibold text-text-main">{stats.tested}</span>
      <div>
        <div class="text-xs text-text-muted">Tested</div>
        <div class="text-xs text-text-subtle">Progress</div>
      </div>
    </div>
  </div>

  {#if run?.running}
    <div class="space-y-1">
      <div class="flex items-center justify-between text-xs text-text-muted">
        <span>Testing in progress — continues if you leave this page</span>
        <span class="flex items-center gap-3">
          <span>{progressPct}%</span>
          <button
            type="button"
            class="text-text-main underline disabled:opacity-50"
            disabled={deleting}
            onclick={cancelRun}
          >
            Cancel
          </button>
        </span>
      </div>
      <div class="h-2 overflow-hidden rounded-full bg-surface-3">
        <div
          class="h-full rounded-full bg-brand-500 transition-all duration-300"
          style="width: {progressPct}%"
        ></div>
      </div>
    </div>
  {/if}

  <div class="flex items-center gap-3 flex-wrap">
    <button
      type="button"
      class="rounded-[10px] bg-primary px-4 py-2 text-sm font-medium text-white hover:bg-primary-hover disabled:opacity-50"
      disabled={starting || (run?.running ?? false) || filtered.length === 0}
      onclick={testSelected}
    >
      {run?.running ? `Testing... (${run.done}/${run.total})` : 'Test Selected Models'}
    </button>
    <button
      type="button"
      class="rounded-[10px] border border-border px-4 py-2 text-sm text-text-main hover:bg-surface-2 disabled:opacity-50"
      disabled={verdicts.size === 0}
      onclick={clearResults}
    >
      Clear Results
    </button>
    <button
      type="button"
      class="rounded-[10px] border border-border px-4 py-2 text-sm text-text-main hover:bg-surface-2 disabled:opacity-50"
      disabled={!anyTested}
      onclick={downloadReport}
    >
      Download Report (MD)
    </button>
  </div>

  <div class="flex flex-wrap gap-4 text-xs text-text-muted">
    <span class="flex items-center gap-1.5">
      <span class="h-2 w-2 rounded-full bg-emerald-500"></span>
      Healthy (&lt;{SLOW_TOTAL_THRESHOLD}ms)
    </span>
    <span class="flex items-center gap-1.5">
      <span class="h-2 w-2 rounded-full bg-amber-500"></span>
      Slow ({SLOW_TOTAL_THRESHOLD}-{ERROR_TOTAL_THRESHOLD}ms)
    </span>
    <span class="flex items-center gap-1.5">
      <span class="h-2 w-2 rounded-full bg-red-500"></span>
      Error (&gt;{ERROR_TOTAL_THRESHOLD}ms or failed)
    </span>
  </div>

  {#if actionError}
    <p class="text-sm text-red-600">{actionError}</p>
  {/if}

  <section class="rounded-[10px] border border-border bg-surface overflow-hidden">
    <div class="overflow-x-auto">
      <table class="w-full text-xs">
        <thead>
          <tr
            class="border-b border-border bg-surface-2/50 text-left text-[11px] text-text-muted"
          >
            <th class="px-4 py-2.5 font-medium">Status</th>
            <th class="px-4 py-2.5 font-medium">Model</th>
            <th class="px-4 py-2.5 font-medium">Provider</th>
            <th class="px-4 py-2.5 font-medium text-right">Total</th>
            <th class="px-4 py-2.5 font-medium text-right">HTTP</th>
            <th class="px-4 py-2.5 font-medium">Error</th>
            <th class="px-4 py-2.5 font-medium text-right">Action</th>
          </tr>
        </thead>
        <tbody>
          {#each filtered as m (m.key)}
            {@const row = verdicts.get(m.key)}
            {@const s = displayStatus(row)}
            <tr class="border-b border-border/60 last:border-b-0 {rowTint(s)}">
              <td class="px-4 py-2.5">
                <span
                  class="inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium {statusClass(
                    s
                  )}"
                  title={row?.note ?? ''}
                >
                  {STATUS_LABELS[s]}
                </span>
              </td>
              <td class="px-4 py-2.5">
                <code class="font-mono text-[11px] text-text-main">{m.model}</code>
                {#if m.kind}
                  <span class="text-text-subtle">· {m.badge}</span>
                {/if}
              </td>
              <td class="px-4 py-2.5 text-text-muted">{m.providerName}</td>
              <td class="px-4 py-2.5 text-right text-text-main">
                {row && row.state !== 'pending' && row.state !== 'running'
                  ? `${row.latencyMs}ms`
                  : '-'}
              </td>
              <td class="px-4 py-2.5 text-right text-text-muted">{row?.httpStatus ?? '-'}</td>
              <td class="max-w-48 truncate px-4 py-2.5 text-red-600" title={row?.error ?? ''}>
                {row?.blocked
                  ? `Account parked, resets ${row.resetAt || 'unknown'}`
                  : (row?.error ?? '')}
              </td>
              <td class="px-4 py-2.5 text-right">
                <button
                  type="button"
                  class="rounded-[8px] px-2 py-1 text-xs text-text-muted hover:bg-surface-2 hover:text-text-main disabled:opacity-50"
                  disabled={(run?.running ?? false) || starting}
                  onclick={() => retryOne(m)}
                >
                  Retry
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if loading}
      <p class="py-12 text-center text-sm text-text-muted">Loading models…</p>
    {:else if models.length === 0}
      <p class="py-12 text-center text-sm text-text-muted">
        No active models. Connect a provider to test its models.
      </p>
    {:else if filtered.length === 0}
      <p class="py-12 text-center text-sm text-text-muted">
        No models selected. Choose providers above to test.
      </p>
    {/if}
  </section>
</div>
