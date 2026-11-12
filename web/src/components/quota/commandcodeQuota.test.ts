import { describe, expect, it } from 'bun:test'
import { parseQuotaData } from './types'

describe('commandcode quota parsing and order', () => {
  it('parses and orders 5-hour, weekly, and monthly windows correctly for commandcode-goat', () => {
    const data = {
      plan: 'Go',
      quotas: {
        Credits: {
          name: 'Monthly',
          used: 3,
          total: 10,
          remaining: 7,
          percentage: 30,
          colorPercentage: 70,
          percentageIsUsed: true,
          unlimited: false,
          resetAt: '2026-12-01T00:00:00Z',
        },
        'Session (5h)': {
          name: '5-hour limit',
          used: 2,
          total: 10,
          remaining: 8,
          percentage: 20,
          colorPercentage: 80,
          percentageIsUsed: true,
        },
        Weekly: {
          name: 'Weekly limit',
          used: 1,
          total: 10,
          remaining: 9,
          percentage: 10,
          colorPercentage: 90,
          percentageIsUsed: true,
        },
      },
    }

    for (const provider of ['commandcode-goat', 'cmc-goat', 'commandcode', 'cmc']) {
      const quotas = parseQuotaData(provider, data)
      expect(quotas.length).toBe(3)
      expect(quotas[0].name).toBe('5-hour limit')
      expect(quotas[1].name).toBe('Weekly limit')
      expect(quotas[2].name).toBe('Monthly')
      expect(quotas[0].used).toBe(2)
      expect(quotas[0].total).toBe(10)
    }
  })
})
