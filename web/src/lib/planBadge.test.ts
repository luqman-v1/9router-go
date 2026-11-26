import { describe, expect, it } from 'vitest'
import { formatPlanExpiry, getPlanBadge, planLabel } from './planBadge'

const NOW = new Date('2026-11-26T00:00:00Z').getTime()

describe('planLabel', () => {
  it('labels the tiers both providers report', () => {
    expect(planLabel('plus')).toBe('Plus')
    expect(planLabel('PRO')).toBe('Pro')
    expect(planLabel(' team ')).toBe('Team')
    expect(planLabel('claude_max')).toBe('Claude_max')
    expect(planLabel('Max 20x')).toBe('Max 20x')
  })

  // The compact ids the accounts/check endpoint answers must not reach the
  // screen as "Chatgptplusplan".
  it('folds the compact plan ids the endpoint returns', () => {
    expect(planLabel('chatgptplusplan')).toBe('Plus')
    expect(planLabel('ChatGPTProPlan')).toBe('Pro')
    expect(planLabel('chatgptbusinessplan')).toBe('Business')
    expect(planLabel('chatgptfreeplan')).toBe('Free')
  })

  it('draws nothing for an absent tier rather than a raw code', () => {
    expect(planLabel(null)).toBeNull()
    expect(planLabel('')).toBeNull()
    expect(planLabel('   ')).toBeNull()
  })
})

describe('formatPlanExpiry', () => {
  it('gives the date and a countdown while the plan is live', () => {
    const got = formatPlanExpiry('2026-12-09T15:04:05Z', 'en-GB', NOW)
    expect(got.absolute).toBeTruthy()
    expect(got.expired).toBe(false)
    expect(got.relative).toBe('14d')
  })

  it('reads a past date as ended instead of a negative countdown', () => {
    const got = formatPlanExpiry('2026-09-17T11:05:59Z', 'en-GB', NOW)
    expect(got.expired).toBe(true)
    expect(got.relative).toBeNull()
    expect(got.absolute).toBeTruthy()
  })

  // A Claude seat carries no expiry at all; the badge must stay tier-only.
  it('says nothing when there is no expiry', () => {
    expect(formatPlanExpiry(null, 'en-GB', NOW)).toEqual({
      absolute: null,
      relative: null,
      expired: false,
    })
    expect(formatPlanExpiry('not-a-date', 'en-GB', NOW).absolute).toBeNull()
  })
})

describe('getPlanBadge', () => {
  it('reads the block the server attaches', () => {
    expect(getPlanBadge({ subscription: { plan: 'plus', expiresAt: '2026-12-09T15:04:05Z' } })).toEqual(
      { plan: 'plus', expiresAt: '2026-12-09T15:04:05Z' },
    )
  })

  it('accepts a tier with no expiry, and a response with no block', () => {
    expect(getPlanBadge({ subscription: { plan: 'Max 20x' } })).toEqual({
      plan: 'Max 20x',
      expiresAt: null,
    })
    expect(getPlanBadge({ quotas: {} })).toBeNull()
    expect(getPlanBadge(undefined)).toBeNull()
  })
})
