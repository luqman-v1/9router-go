export type ModelTestStatus = 'ok' | 'error' | 'blocked' | 'testing'

export interface ModelTestVerdict {
  status: 'ok' | 'error' | 'blocked'
  error: string | null
}

export function parseModelTestVerdict(res: {
  ok: boolean
  error?: string
  blocked?: boolean
  resetAt?: string
}): ModelTestVerdict {
  if (res.ok) {
    return { status: 'ok', error: null }
  }
  if (res.blocked) {
    const msg = res.resetAt
      ? `Blocked (cooldown until ${res.resetAt})`
      : res.error || 'Blocked by cooldown'
    return { status: 'blocked', error: msg }
  }
  return { status: 'error', error: res.error || 'Model test failed' }
}

export interface CheckAllSummary {
  passed: number
  failed: number
  blocked: number
  total: number
  message: string | null
}

export function formatCheckAllSummary(statuses: Record<string, ModelTestStatus>): CheckAllSummary {
  const vals = Object.values(statuses).filter((s) => s !== 'testing')
  const passed = vals.filter((s) => s === 'ok').length
  const failed = vals.filter((s) => s === 'error').length
  const blocked = vals.filter((s) => s === 'blocked').length
  const total = vals.length

  if (total === 0 || (failed === 0 && blocked === 0)) {
    return { passed, failed, blocked, total, message: null }
  }

  let msg = `${passed} passed · ${failed} failed`
  if (blocked > 0) {
    msg += ` · ${blocked} blocked`
  }
  msg += ' — see per-row status.'
  return { passed, failed, blocked, total, message: msg }
}

export function getBlockedModelIds(statuses: Record<string, ModelTestStatus>): string[] {
  return Object.entries(statuses)
    .filter(([_, s]) => s === 'blocked')
    .map(([id]) => id)
}
