<script lang="ts">
  import { matchMemberRow, parseMember, sameProvider } from './comboMatch'
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

  /** Every member provider of this combo: tells a slug apart from a sibling. */
  let memberProviders = $derived(
    (definition?.members || []).map((raw) => parseMember(raw).provider).filter(Boolean)
  )

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
      const inFlight = matchMemberRow(active, provider, model, memberProviders)
      const finished = matchMemberRow(recent, provider, model, memberProviders)
      const finishedStatus = String(finished?.status || '')
      const latency = Number(finished?.latencyMs || 0)
      const resolved = String(inFlight?.provider || finished?.provider || provider)
      const refused = sameProvider(errorProvider, resolved) || sameProvider(errorProvider, provider)

      let status: Status = 'idle'
      let note = 'not used yet'
      if (inFlight) {
        status = 'trying'
        note = 'trying'
      } else if (finished && finishedStatus.toLowerCase() === 'ok') {
        status = 'connected'
        note = 'answered'
      } else if (refused) {
        status = 'failed'
        note = 'failed'
      } else if (finished) {
        status = 'failed'
        note = finishedStatus || 'failed'
      }
      return { key: raw, provider, model, status, note, color: colorFor(provider), latency }
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
        {#if counts.trying > 0}<span data-kind="trying">{counts.trying} trying</span>{/if}
        {#if counts.connected > 0}<span data-kind="connected">{counts.connected} answered</span>{/if}
        {#if counts.failed > 0}<span data-kind="failed">{counts.failed} failed</span>{/if}
        {#if counts.idle > 0}<span data-kind="idle">{counts.idle} remaining</span>{/if}
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
        </svg>
        {#if routeState === 'trying'}
          <span class="cf-dot"></span>
        {/if}
      </span>
      <div class="cf-chip" data-role="combo">
        <span class="material-symbols-outlined cf-chip-icon">account_tree</span>
        <span class="cf-chip-text">
          <strong>{liveCombo}</strong>
          <small>{strategy} · {steps.length} members</small>
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
                <span class="cf-latency" title="time this member took to answer">{fmtLatency(step.latency)}</span>
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
                failed, moving to #{i + 2} {steps[i + 1].provider}
              </p>
            {:else if step.status === 'connected'}
              <p class="cf-handoff cf-handoff-ok">
                <span class="material-symbols-outlined cf-ico">check</span>
                served here — member #{i + 2} and beyond are unused
              </p>
            {/if}
          </div>
        </li>
      {/each}
    </ol>

    <footer class="cf-legend">
      <span data-kind="trying">trying</span>
      <span data-kind="connected">answered</span>
      <span data-kind="failed">failed → moves to the next one</span>
      <span data-kind="idle">not used yet</span>
    </footer>
  </section>
{/if}
