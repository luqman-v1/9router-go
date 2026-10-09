export type ModelTestStatus = 'ok' | 'error' | 'blocked' | 'testing'

export interface ModelTestVerdict {
  status: 'ok' | 'error' | 'blocked'
  error: string | null
}

export function parseModelTestVerdict(res: {
  ok: boolean
  status?: number
  error?: string
  blocked?: boolean
  resetAt?: string
}): ModelTestVerdict {
  if (res.ok) {
    return { status: 'ok', error: null }
  }
  const err = res.error || 'Model test failed'

  // The backend owns this verdict. It knows whether the account was parked
  // (temporary, resolves on its own) or whether the model was actually asked
  // and refused — a distinction no amount of string matching in the SPA can
  // make reliably, since the gateway's own wording differs from a provider's
  // and a model-scoped quota refusal never clears by waiting.
  if (res.blocked) {
    const reset = formatResetAt(res.resetAt)
    return { status: 'blocked', error: reset ? `Blocked (cooldown until ${reset})` : err }
  }

  return { status: 'error', error: err }
}

// The gateway speaks RFC3339; operators read local time. Every other reset in
// this view (quota rows at :860) is rendered the same way, and a raw
// "2026-10-09T03:00:00Z" on a dashboard row is not something a human can act on.
function formatResetAt(resetAt?: string): string {
  if (!resetAt) return ''
  const parsed = new Date(resetAt)
  return Number.isNaN(parsed.getTime()) ? resetAt : parsed.toLocaleString()
}

export interface CheckAllSummary {
  passed: number
  failed: number
  blocked: number
  total: number
  severity: 'ok' | 'error' | 'blocked' | null
  message: string | null
}

export function formatCheckAllSummary(statuses: Record<string, ModelTestStatus>): CheckAllSummary {
  const vals = Object.values(statuses).filter((s) => s !== 'testing')
  const passed = vals.filter((s) => s === 'ok').length
  const failed = vals.filter((s) => s === 'error').length
  const blocked = vals.filter((s) => s === 'blocked').length
  const total = vals.length

  if (total === 0 || (failed === 0 && blocked === 0)) {
    return { passed, failed, blocked, total, severity: total > 0 ? 'ok' : null, message: null }
  }

  let severity: 'error' | 'blocked' = 'error'
  let msg = ''

  if (failed === 0 && blocked > 0) {
    severity = 'blocked'
    msg = `${passed} passed · ${blocked} blocked — see per-row status.`
  } else if (failed > 0 && blocked === 0) {
    severity = 'error'
    msg = `${passed} passed · ${failed} failed — see per-row status.`
  } else {
    severity = 'error'
    msg = `${passed} passed · ${failed} failed · ${blocked} blocked — see per-row status.`
  }

  return { passed, failed, blocked, total, severity, message: msg }
}

export function getBlockedModelIds(statuses: Record<string, ModelTestStatus>): string[] {
  return Object.entries(statuses)
    .filter(([_, s]) => s === 'blocked')
    .map(([id]) => id)
}
