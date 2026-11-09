import { describe, expect, it } from 'bun:test'
import { parseQuotaData } from './types'

describe('qoder quota normalization', () => {
  it('parses personal, organization and addOn quotas', () => {
    for (const provider of ['qoder', 'qoder-cn']) {
      const rows = parseQuotaData(provider, {
        quotas: {
          user: { used: 100, total: 1000, remaining: 900, resetAt: '2026-11-01T00:00:00Z' },
          organization: { used: 500, total: 5000, remaining: 4500, resetAt: '2026-11-01T00:00:00Z' },
          addOn: { used: 50, total: 2200, remaining: 2150, detailUrl: 'https://qoder.sh/wallet' },
        },
      })
      expect(rows).toHaveLength(3)
      expect(rows[0].name).toBe('Personal')
      expect(rows[1].name).toBe('Organization')
      expect(rows[2].name).toBe('Add-On Promo')
      expect(rows[2].total).toBe(2200)
      expect(rows[2].used).toBe(50)
      expect(rows[2].detailUrl).toBe('https://qoder.sh/wallet')
    }
  })

  it('omits organization quota when total is zero', () => {
    const rows = parseQuotaData('qoder', {
      quotas: {
        user: { used: 10, total: 100 },
        organization: { used: 0, total: 0 },
      },
    })
    expect(rows).toHaveLength(1)
    expect(rows[0].name).toBe('Personal')
  })
})
