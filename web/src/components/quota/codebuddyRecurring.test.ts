import { describe, expect, it } from 'bun:test'
import { parseQuotaData } from './types'

// Issue #101: codebuddy-intl is a codebuddy provider, and the backend already
// sends `recurring` for it — the frontend switch simply had no case for it, so
// its bonus packs were labelled "Reset in" instead of "Expires in".
describe('codebuddy quota normalization', () => {
  it('forwards recurring for both codebuddy providers', () => {
    for (const provider of ['codebuddy-cn', 'codebuddy-intl']) {
      const rows = parseQuotaData(provider, {
        quotas: {
          bonus: { used: 0, total: 100, resetAt: '2026-10-01T00:00:00Z', recurring: false },
        },
      })
      expect(rows).toHaveLength(1)
      expect(rows[0].recurring).toBe(false)
    }
  })

  it('defaults a codebuddy quota without the flag to recurring', () => {
    const rows = parseQuotaData('codebuddy-intl', {
      quotas: { plan: { used: 1, total: 2, resetAt: '2026-10-01T00:00:00Z' } },
    })
    expect(rows[0].recurring).toBe(true)
  })
})
