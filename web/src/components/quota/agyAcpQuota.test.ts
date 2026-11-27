import { describe, expect, it } from 'bun:test'
import { parseQuotaData } from './types'

describe('agy-acp quota parsing', () => {
  it('groups Google quota rows the same way as antigravity', () => {
    const data = {
      quotas: {
        gemini_session: { used: 1, total: 10, remainingPercentage: 90, resetAt: '2026-11-26T12:00:00Z' },
        gemini_weekly: { used: 4, total: 100, remainingPercentage: 96, resetAt: '2026-12-01T00:00:00Z' },
        claude_gpt_session: { used: 2, total: 10, remainingPercentage: 80, resetAt: '2026-11-26T12:00:00Z' },
        'gemini-4-hydrogen-high': { used: 0, total: 10, remainingPercentage: 100 },
      },
    }
    const acp = parseQuotaData('agy-acp', data)
    const antigravity = parseQuotaData('antigravity', data)
    expect(acp).toEqual(antigravity)
    expect(acp.map((q) => q.modelKey)).toEqual(['gemini_session', 'claude_gpt_session', 'gemini_weekly'])
  })
})
