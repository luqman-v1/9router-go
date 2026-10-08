/**
 * Shared logic behind the connection row's probe actions.
 *
 * Two entry points probe exactly one account through `POST /api/connections/{id}/test`:
 * the header's *Test Connection One-by-One* sweep and the per-row *Refresh* button
 * (PR #196). Both classify the same response into the same badge state, so the
 * classification lives here instead of being restated at each call site.
 */

export type ProbeState = 'queued' | 'testing' | 'success' | 'failed'

export interface ProbeStatus {
  state: ProbeState
  error?: string | null
}

/** The shape `api.testConnection` resolves with. */
export interface ProbeResponse {
  valid?: boolean
  error?: string
}

/**
 * The backend's answer for a provider that has no probe registered. It is a
 * neutral verdict, not a failure, so it must never paint the row red.
 */
export const UNSUPPORTED_MESSAGE = 'Provider test not supported'

/**
 * Whether a single-row probe may start.
 *
 * Two conditions refuse it, and neither is redundant with the button's
 * `disabled` attribute: a one-by-one sweep already owns the row, and a second
 * click while this row's probe is in flight would send a duplicate request to
 * the same upstream account.
 *
 * The attribute is presentation — it disappears the moment a re-render lands.
 * This guard is what holds when a click arrives anyway (an activation queued
 * before the disable, a stale render, an assistive-technology activation), so
 * it has to be the real gate rather than a mirror of the markup.
 */
export function canProbeRow(
  isSweeping: boolean,
  current: ProbeStatus | undefined
): boolean {
  if (isSweeping) return false
  return current?.state !== 'testing'
}

/** The `testing` marker a probe paints while it is in flight. */
export function testingStatus(): ProbeStatus {
  return { state: 'testing', error: null }
}

/**
 * Classify a resolved probe response.
 *
 * An unknown or rejected outcome becomes `failed` rather than throwing, so a
 * caller never has to separate "the provider answered no" from "the request
 * itself failed" before it can paint a badge.
 */
export function probeOutcomeFrom(res: ProbeResponse | null | undefined): ProbeStatus {
  if (res?.valid) {
    return { state: 'success', error: null }
  }
  return { state: 'failed', error: res?.error || 'Test failed' }
}

/**
 * Classify a thrown probe. A rejection is a transport or auth failure rather
 * than a provider verdict, so its message is preserved verbatim when it is an
 * `Error` and falls back to the same default as an unverifiable reply.
 */
export function probeOutcomeFromError(err: unknown): ProbeStatus {
  return {
    state: 'failed',
    error: err instanceof Error ? err.message : 'Test failed'
  }
}

/**
 * The badge a row renders, given the live client-side probe state and the
 * connection's persisted `testStatus`.
 *
 * Precedence matters because the two can disagree. The probe map is the more
 * recent observation, so it wins while it has anything to say; once the server
 * has caught up, {@link reconcileProbeStatuses} drops the entry and this falls
 * through to the persisted verdict.
 */
export function badgeFor(
  probe: ProbeStatus | undefined,
  connTestStatus: string | null | undefined,
  lastError: string
): 'queued' | 'testing' | 'error' | 'active' {
  if (probe?.state === 'queued') return 'queued'
  if (probe?.state === 'testing') return 'testing'
  // An unsupported probe is not a broken key: providers without one answer
  // with it, and painting that red would report a credential that works.
  if (probe?.state === 'failed' && probe.error !== UNSUPPORTED_MESSAGE) return 'error'
  const serverFailed = connTestStatus === 'failed' || connTestStatus === 'error'
  if (serverFailed && lastError !== UNSUPPORTED_MESSAGE) return 'error'
  return 'active'
}

/**
 * Drop probe entries that no longer disagree with the server.
 *
 * `oneByOneStatuses` is written by two independent code paths — the sweep and
 * the per-row refresh — and is only wholesale-reset by the next sweep, so an
 * entry can outlive the state it was derived from. Entries in flight are always
 * kept: `testing` is what the spinner reads, and the server has no opinion
 * about a probe that has not landed.
 *
 * Reachability, verified against this backend: `HandleUpdateConnection` never
 * applies a `testStatus` sent by the Edit modal and `PUT` runs no probe, so
 * nothing marks a row healthy without one. The disagreement this clears is
 * therefore not currently reachable — it becomes reachable the moment the
 * update handler honours a key the modal already validated, at which point a
 * stale `failed` would outlive a repaired row and PR #196 has removed the Edit
 * modal as a way to re-probe one.
 *
 * Kept exported and tested rather than wired into a `$effect` today: hooking
 * it to the connection list would mean writing state on every render for a
 * path nothing reaches, which is exactly the kind of code that outlives its
 * reason.
 */
export function reconcileProbeStatuses(
  probes: Record<string, ProbeStatus>,
  connections: ReadonlyArray<{ id: string; testStatus?: string | null }>
): Record<string, ProbeStatus> {
  const byId = new Map(connections.map((c) => [c.id, c]))
  const kept: Record<string, ProbeStatus> = {}
  for (const [id, status] of Object.entries(probes)) {
    const conn = byId.get(id)
    // A row the server no longer lists has nothing left to reconcile against.
    if (!conn) continue
    if (status.state === 'queued' || status.state === 'testing') {
      kept[id] = status
      continue
    }
    const serverFailed = conn.testStatus === 'failed' || conn.testStatus === 'error'
    if (serverFailed === (status.state === 'failed')) {
      kept[id] = status
    }
    // Otherwise the server has caught up (an edit repaired the row, or another
    // probe replaced this verdict), so the entry is redundant and must go.
  }
  return kept
}