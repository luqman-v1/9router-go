<script lang="ts">
  import type { ActiveRequestItem, RecentRequestItem } from './types'

  interface Props {
    /** Combo definitions: name, member ids ("provider/model"), strategy. */
    combos?: { name: string; members: string[]; strategy?: string }[]
    activeRequests?: ActiveRequestItem[]
    recentRequests?: RecentRequestItem[]
    /** Provider that last failed, straight from the usage stream. */
    errorProvider?: string
  }

  let {
    combos = [],
    activeRequests = [],
    recentRequests = [],
    errorProvider = ''
  }: Props = $props()

  type Status = 'idle' | 'trying' | 'connected' | 'failed'

  interface MemberStep {
    key: string
    provider: string
    model: string
    resolved: string
    status: Status
    note: string
    color: string
    /** Time the member took, when the stream recorded a completed request. */
    latency: number
  }

  // A finished combo run stays on screen long enough to read the outcome.
  const RECENT_WINDOW_MS = 25000

  const PALETTE = ['#22d3ee', '#a78bfa', '#f472b6', '#facc15', '#34d399', '#fb923c', '#60a5fa', '#f87171']

  /** Stable colour per provider, so a member keeps its identity between renders. */
  function colorFor(seed: string): string {
    let h = 0
    for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) % 997
    return PALETTE[h % PALETTE.length]
  }

  /** 820 -> "820ms", 1240 -> "1.2s", 13400 -> "13s". */
  function fmtLatency(ms: number): string {
    if (!ms || ms <= 0) return ''
    if (ms < 1000) return `${Math.round(ms)}ms`
    return ms >= 10000 ? `${Math.round(ms / 1000)}s` : `${(ms / 1000).toFixed(1)}s`
  }

  function initials(name: string): string {
    const s = (name || '').replace(/[^A-Za-z0-9]/g, ' ').trim()
    if (!s) return '?'
    const parts = s.split(/\s+/)
    return (parts.length > 1 ? parts[0][0] + parts[1][0] : s.slice(0, 2)).toUpperCase()
  }

  function parseMember(raw: string): { provider: string; model: string } {
    const s = String(raw || '').trim()
    const i = s.indexOf('/')
    return i > 0 ? { provider: s.slice(0, i), model: s.slice(i + 1) } : { provider: s, model: '' }
  }

  function sameProvider(a?: string, b?: string): boolean {
    const x = (a || '').toLowerCase().trim()
    const y = (b || '').toLowerCase().trim()
    if (!x || !y) return false
    return x === y || x.startsWith(y) || y.startsWith(x)
  }

  /** Last path segment of a model id, e.g. "cbcn/deepseek-v4.1-flash" -> "deepseek-v4.1-flash". */
  function modelTail(model?: string): string {
    const parts = (model || '').toLowerCase().trim().split('/')
    return parts[parts.length - 1]
  }

  /**
   * The stream row recorded for a combo member. A member is written with the name
   * the operator uses while the stream reports the provider slug the gateway
   * resolved, so the heads differ for custom providers: provider+tail first, then
   * provider, and only then the model tail.
   */
  function memberRow(
    rows: ActiveRequestItem[] | RecentRequestItem[],
    provider: string,
    model: string
  ): ActiveRequestItem | RecentRequestItem | undefined {
    const tail = modelTail(model)
    const byProvider = rows.find((r) => sameProvider(r.provider, provider))
    const byProviderAndTail = rows.find(
      (r) => sameProvider(r.provider, provider) && tail && modelTail(r.model) === tail
    )
    // Provider first, so two members that differ only by provider
    // (oc/space-bunny-free vs ocz/space-bunny-free) never swap rows; the model
    // tail is only the fallback for providers the stream reports by slug.
    return byProviderAndTail || byProvider || (tail ? rows.find((r) => modelTail(r.model) === tail) : undefined)
  }

  /** The combo in use right now, or the one that finished within the window. */
  let liveCombo = $derived.by(() => {
    for (const r of activeRequests) {
      if (r.comboName) return r.comboName
    }
    const cutoff = Date.now() - RECENT_WINDOW_MS
    for (const r of recentRequests) {
      if (!r.comboName) continue
      const t = Date.parse(r.timestamp || '')
      if (Number.isNaN(t) || t >= cutoff) return r.comboName
    }
    return ''
  })

  let definition = $derived(
    (combos || []).find((c) => (c?.name || '').toLowerCase().trim() === (liveCombo || '').toLowerCase().trim())
  )

  let strategy = $derived((definition?.strategy || '').trim() || 'fallback')

  /**
   * One row per member, in the combo's own order, each carrying what the usage
   * stream says about it: in flight now, answered, or refused. A member that was
   * skipped because an earlier one answered stays "idle".
   */
  let steps = $derived.by((): MemberStep[] => {
    const def = definition
    const combo = (liveCombo || '').toLowerCase().trim()
    if (!def || !combo) return []

    const active = activeRequests.filter((r) => (r.comboName || '').toLowerCase().trim() === combo)
    const recent = recentRequests.filter((r) => (r.comboName || '').toLowerCase().trim() === combo)

    return (def.members || []).map((raw) => {
      const { provider, model } = parseMember(raw)
      const inFlight = memberRow(active, provider, model)
      const finished = memberRow(recent, provider, model) as RecentRequestItem | undefined
      const finishedStatus = String(finished?.status || '')
      const latency = Number(finished?.latencyMs || 0)
      const resolved = String(inFlight?.provider || finished?.provider || provider)
      const refused = sameProvider(errorProvider, resolved) || sameProvider(errorProvider, provider)

      let status: Status = 'idle'
      let note = 'belum dipakai'
      if (inFlight) {
        status = 'trying'
        note = 'sedang dicoba'
      } else if (finished && finishedStatus.toLowerCase() === 'ok') {
        status = 'connected'
        note = 'menjawab'
      } else if (refused) {
        status = 'failed'
        note = 'gagal'
      } else if (finished) {
        status = 'failed'
        note = finishedStatus || 'gagal'
      }
      return { key: raw, provider, model, resolved, status, note, color: colorFor(provider), latency }
    })
  })

  let counts = $derived({
    trying: steps.filter((s) => s.status === 'trying').length,
    connected: steps.filter((s) => s.status === 'connected').length,
    failed: steps.filter((s) => s.status === 'failed').length,
    idle: steps.filter((s) => s.status === 'idle').length
  })

  // The route colour: red the moment anything refused, green once a member
  // answered, amber while one is still being tried.
  let routeState = $derived(
    counts.failed > 0 ? 'failed' : counts.connected > 0 ? 'connected' : counts.trying > 0 ? 'trying' : 'idle'
  )

  /** Where the run stopped, if it has: everything after it was not used. */
  let answeredIndex = $derived(steps.findIndex((s) => s.status === 'connected'))
</script>

{#if liveCombo && steps.length > 0}
  <section
    class="cf bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] min-w-0 overflow-hidden"
    aria-label="Combo flow"
  >
    <header class="cf-header">
      <div class="cf-heading">
        <span class="cf-kicker">Combo flow</span>
        <span class="cf-name">{liveCombo}</span>
        <span class="cf-strategy">{strategy}</span>
      </div>
      <div class="cf-counters">
        {#if counts.trying > 0}<span data-kind="trying">{counts.trying} dicoba</span>{/if}
        {#if counts.connected > 0}<span data-kind="connected">{counts.connected} menjawab</span>{/if}
        {#if counts.failed > 0}<span data-kind="failed">{counts.failed} gagal</span>{/if}
        {#if counts.idle > 0}<span data-kind="idle">{counts.idle} sisa</span>{/if}
      </div>
    </header>

    <!-- the route: gateway into the combo, one hop -->
    <div class="cf-route">
      <div class="cf-chip" data-role="router" data-state={routeState}>
        <img src="/favicon.svg" alt="" loading="lazy" decoding="async" />
        <span>9router-go</span>
      </div>
      <span class="cf-route-arrow" data-state={routeState} aria-hidden="true">
        <svg viewBox="0 0 40 12" preserveAspectRatio="none">
          <path d="M1 6 H37" />
          <circle cx="1" cy="6" r="2.2">
            <animate attributeName="cx" values="1;37" dur="1.1s" repeatCount="indefinite" />
          </circle>
        </svg>
      </span>
      <div class="cf-chip" data-role="combo">
        <span class="material-symbols-outlined cf-chip-icon">account_tree</span>
        <span class="cf-chip-text">
          <strong>{liveCombo}</strong>
          <small>{strategy} · {steps.length} anggota</small>
        </span>
      </div>
    </div>

    <!-- the members, in the combo's own order, numbered so the order is explicit -->
    <ol class="cf-list">
      {#each steps as step, i (step.key)}
        <li
          class="cf-row"
          data-status={step.status}
          data-used={answeredIndex < 0 || i <= answeredIndex ? 'true' : 'false'}
        >
          <span class="cf-order" aria-hidden="true">
            <span class="cf-order-disc">{i + 1}</span>
          </span>

          <div class="cf-row-card">
            <div class="cf-row-main">
              <span class="cf-avatar" style="--tint: {step.color};">{initials(step.provider)}</span>
              <span class="cf-row-names">
                <strong>{step.provider}</strong>
                <small title={step.model || step.provider}>{step.model || '—'}</small>
              </span>
              {#if step.latency > 0}
                <span class="cf-latency" title="waktu anggota ini menjawab">{fmtLatency(step.latency)}</span>
              {/if}
              <span class="cf-status">
                {#if step.status === 'connected'}
                  <span class="material-symbols-outlined cf-ico">check_circle</span>
                {:else if step.status === 'failed'}
                  <span class="material-symbols-outlined cf-ico">cancel</span>
                {:else if step.status === 'trying'}
                  <span class="material-symbols-outlined cf-ico cf-spin">progress_activity</span>
                {:else}
                  <span class="material-symbols-outlined cf-ico">radio_button_unchecked</span>
                {/if}
                <span class="cf-status-text">{step.note}</span>
              </span>
            </div>

            {#if step.status === 'failed' && steps[i + 1]}
              <p class="cf-handoff">
                <span class="material-symbols-outlined cf-ico">south</span>
                gagal, lanjut ke #{i + 2} {steps[i + 1].provider}
              </p>
            {:else if step.status === 'connected'}
              <p class="cf-handoff cf-handoff-ok">
                <span class="material-symbols-outlined cf-ico">check</span>
                dilayani di sini — anggota #{i + 2} dan seterusnya tidak dipakai
              </p>
            {/if}
          </div>
        </li>
      {/each}
    </ol>

    <footer class="cf-legend">
      <span data-kind="trying">sedang dicoba</span>
      <span data-kind="connected">menjawab</span>
      <span data-kind="failed">gagal → lanjut ke nomor berikutnya</span>
      <span data-kind="idle">belum dipakai</span>
    </footer>
  </section>
{/if}
