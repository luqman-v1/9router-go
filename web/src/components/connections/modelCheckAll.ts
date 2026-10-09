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

  // Classify cooldown-blocked probes either from backend structured flag
  // or by status + cooldown message signature.
  const isCooldownSignature =
    (res.status === 502 && err.includes('all in cooldown')) ||
    (res.status === 429 && (err.includes('cooldown') || err.includes('rate limit')))

  if (res.blocked || isCooldownSignature) {
    let reset = res.resetAt
    if (!reset && err.includes('earliest reset ')) {
      const match = err.match(/earliest reset ([^\s\)]+)/)
      if (match) reset = match[1]
    }
    const msg = reset ? `Blocked (cooldown until ${reset})` : err
    return { status: 'blocked', error: msg }
  }

  return { status: 'error', error: err }
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
