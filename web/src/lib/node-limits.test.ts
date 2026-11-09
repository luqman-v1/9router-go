// Guards the conversions between the limits form (text), the API (numbers and
// RFC3339) and the read-only summary. A wrong conversion here is silent: a limit
// that should be unlimited becomes 1, or a contract that ends on the 31st is
// stored as ending on the 30th, and nothing in the UI says so.
import assert from 'node:assert'
import { describe, it } from 'node:test'
import {
  EMPTY_NODE_LIMITS,
  hasAnyNodeLimit,
  nodeLimitsDraftOf,
  nodeLimitsPatchOf,
  nodeLimitsSummary,
  nodeLimitsUsageSummary,
  type NodeLimitsDraft,
} from './node-limits'
import type { ProviderNodeLimits } from '../api/client'

const draft = (overrides: Partial<NodeLimitsDraft> = {}): NodeLimitsDraft => ({ ...EMPTY_NODE_LIMITS, ...overrides })

describe('nodeLimitsPatchOf', () => {
  it('sends numbers, not the strings the form holds', () => {
    const patch = nodeLimitsPatchOf(draft({ rpm: '60', tpm: '90000', concurrency: '4', tokenQuota: '5000000', queueTimeoutMs: '15000' }))
    assert.deepStrictEqual(patch, {
      rpm: 60,
      tpm: 90000,
      concurrency: 4,
      tokenQuota: 5000000,
      queueTimeoutMs: 15000,
      contractExpiresAt: '',
    })
  })

  it('treats anything that is not a positive integer as unlimited', () => {
    for (const value of ['', '0', '-5', 'abc', '1.5.2']) {
      assert.strictEqual(nodeLimitsPatchOf(draft({ rpm: value })).rpm, 0, `rpm "${value}"`)
    }
  })

  it('stores the chosen day as that day\'s end in UTC, because the date is inclusive', () => {
    assert.strictEqual(nodeLimitsPatchOf(draft({ contractExpiresAt: '2027-01-31' })).contractExpiresAt, '2027-01-31T23:59:59Z')
  })

  it('clears the expiry with an empty string rather than omitting it', () => {
    // An omitted field means "keep the stored value" on the server, so clearing has
    // to be said explicitly.
    assert.strictEqual(nodeLimitsPatchOf(draft()).contractExpiresAt, '')
  })
})

// Svelte binds `<input type="number">` as a number, so a field the operator types
// into holds 700 while the seeded same field holds '700'. Converting only strings
// threw `e.trim is not a function` inside the submit path, so the request never
// left the browser and every save with a typed limit failed without saying why.
describe('a draft holding numbers from number inputs', () => {
  const typed = (): NodeLimitsDraft => ({
    ...EMPTY_NODE_LIMITS,
    rpm: 60,
    tpm: 90000,
    concurrency: 4,
    tokenQuota: 5000000,
    queueTimeoutMs: 15000,
  })

  it('converts without throwing', () => {
    assert.deepStrictEqual(nodeLimitsPatchOf(typed()), {
      rpm: 60,
      tpm: 90000,
      concurrency: 4,
      tokenQuota: 5000000,
      queueTimeoutMs: 15000,
      contractExpiresAt: '',
    })
  })

  it('summarises and gates the same as the string form', () => {
    assert.ok(hasAnyNodeLimit(typed()))
    assert.strictEqual(nodeLimitsSummary(typed()), '60 rpm · 90k tpm · 4 conc · 15000 ms queue · 5M tok')
  })

  it('treats 0, negatives and fractions as unlimited', () => {
    assert.strictEqual(nodeLimitsPatchOf({ ...EMPTY_NODE_LIMITS, rpm: 0 }).rpm, 0)
    assert.strictEqual(nodeLimitsPatchOf({ ...EMPTY_NODE_LIMITS, rpm: -3 }).rpm, 0)
    assert.strictEqual(nodeLimitsPatchOf({ ...EMPTY_NODE_LIMITS, rpm: 1.5 }).rpm, 0)
  })
})

describe('nodeLimitsDraftOf', () => {
  const limits = (overrides: Partial<ProviderNodeLimits> = {}): ProviderNodeLimits => ({
    rpm: 0,
    tpm: 0,
    concurrency: 0,
    tokenQuota: 0,
    queueTimeoutMs: 0,
    quotaResetId: 0,
    usedTokens: 0,
    expired: false,
    rpmUsedWindow: 0,
    activeConcurrency: 0,
    ...overrides,
  })

  it('shows an unset limit as an empty field, not as 0', () => {
    assert.deepStrictEqual(nodeLimitsDraftOf(limits()), EMPTY_NODE_LIMITS)
  })

  it('turns the stored timestamp into the date the input shows', () => {
    assert.strictEqual(nodeLimitsDraftOf(limits({ contractExpiresAt: '2027-01-31T23:59:59Z' })).contractExpiresAt, '2027-01-31')
  })

  it('survives a timestamp it cannot parse', () => {
    assert.strictEqual(nodeLimitsDraftOf(limits({ contractExpiresAt: 'not-a-date' })).contractExpiresAt, 'not-a-date')
  })

  it('has an empty draft when nothing was read', () => {
    assert.deepStrictEqual(nodeLimitsDraftOf(null), EMPTY_NODE_LIMITS)
  })

  it('round-trips a full policy', () => {
    const stored = limits({ rpm: 60, concurrency: 4, tokenQuota: 5000000, contractExpiresAt: '2027-01-31T23:59:59Z' })
    const patch = nodeLimitsPatchOf(nodeLimitsDraftOf(stored))
    assert.strictEqual(patch.rpm, 60)
    assert.strictEqual(patch.concurrency, 4)
    assert.strictEqual(patch.tokenQuota, 5000000)
    assert.strictEqual(patch.contractExpiresAt, '2027-01-31T23:59:59Z')
  })
})

describe('hasAnyNodeLimit', () => {
  it('is false for a node that was never limited', () => {
    assert.strictEqual(hasAnyNodeLimit(EMPTY_NODE_LIMITS), false)
    assert.strictEqual(hasAnyNodeLimit(draft({ rpm: '0', tpm: '' })), false)
  })

  it('is true for any ceiling or for an expiry on its own', () => {
    assert.strictEqual(hasAnyNodeLimit(draft({ concurrency: '2' })), true)
    assert.strictEqual(hasAnyNodeLimit(draft({ contractExpiresAt: '2027-01-31' })), true)
  })
})

describe('nodeLimitsSummary', () => {
  it('says unlimited when nothing is set', () => {
    assert.strictEqual(nodeLimitsSummary(draft()), 'Unlimited')
  })

  it('names only what is actually limited', () => {
    assert.strictEqual(nodeLimitsSummary(draft({ rpm: '60', concurrency: '4' })), '60 rpm · 4 conc')
  })

  it('compacts large token counts and shortens the date', () => {
    assert.strictEqual(
      nodeLimitsSummary(draft({ tokenQuota: '5000000', contractExpiresAt: '2027-01-31' })),
      '5M tok · ends 31 Jan 2027',
    )
  })
})

describe('nodeLimitsUsageSummary', () => {
  const base: ProviderNodeLimits = {
    rpm: 0,
    tpm: 0,
    concurrency: 0,
    tokenQuota: 0,
    quotaResetId: 0,
    usedTokens: 0,
    expired: false,
    rpmUsedWindow: 0,
    activeConcurrency: 0,
  }

  it('shows a bare count when the matching ceiling is unlimited', () => {
    assert.strictEqual(nodeLimitsUsageSummary({ ...base, usedTokens: 620000 }), '620k tok · 0 this minute · 0 in flight')
  })

  it('shows spend against the ceiling when there is one', () => {
    assert.strictEqual(
      nodeLimitsUsageSummary({ ...base, usedTokens: 620000, tokenQuota: 5000000, rpm: 60, rpmUsedWindow: 3, concurrency: 4, activeConcurrency: 1 }),
      '620k / 5M tok · 3/60 this minute · 1/4 in flight',
    )
  })
})
