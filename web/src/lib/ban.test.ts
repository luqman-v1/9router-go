import { describe, expect, test } from 'bun:test'
import { isBannedConnection, isAppealUrlValid, appealUrl, showsProviderRiskNotice } from './ban'
import type { ProviderConnection } from '../api/client'

function conn(overrides: Partial<ProviderConnection> = {}): ProviderConnection {
  return {
    id: 'c1',
    provider: 'antigravity',
    authType: 'oauth',
    isActive: 1,
    testStatus: 'error',
    lastError: null,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  } as ProviderConnection
}

describe('isBannedConnection', () => {
  test('flags the disabled-for-violation sentence', () => {
    expect(isBannedConnection(conn({ lastError: 'this service has been disabled in this account for violation' }))).toBe(true)
  })

  test('flags the shortened disabled sentence', () => {
    expect(isBannedConnection(conn({ lastError: 'this service has been disabled in this account' }))).toBe(true)
  })

  test('flags tos_violation together with cloudcode-pa', () => {
    expect(
      isBannedConnection(conn({ lastError: '403 TOS_VIOLATION cloudcode-pa: account disabled' }))
    ).toBe(true)
  })

  test('does not flag tos_violation alone', () => {
    expect(isBannedConnection(conn({ lastError: 'tos_violation: something else' }))).toBe(false)
  })

  test('does not flag cloudcode-pa alone', () => {
    expect(isBannedConnection(conn({ lastError: 'cloudcode-pa error' }))).toBe(false)
  })

  test('does not flag a regular error', () => {
    expect(isBannedConnection(conn({ lastError: 'Invalid API key' }))).toBe(false)
  })

  test('does not flag when lastError is absent', () => {
    expect(isBannedConnection(conn({ lastError: null }))).toBe(false)
  })

  test('is case insensitive', () => {
    expect(isBannedConnection(conn({ lastError: 'This Service Has Been Disabled In This Account' }))).toBe(true)
    expect(isBannedConnection(conn({ lastError: 'TOS_VIOLATION cloudcode-pa' }))).toBe(true)
  })
})

describe('isAppealUrlValid', () => {
  test('allows forms.gle', () => {
    expect(isAppealUrlValid('https://forms.gle/Abc123')).toBe(true)
  })

  test('allows accounts.google.com', () => {
    expect(isAppealUrlValid('https://accounts.google.com/signin/v2/identifier')).toBe(true)
  })

  test('allows docs.google.com under /forms/', () => {
    expect(isAppealUrlValid('https://docs.google.com/forms/d/e/1FAIpQL/viewform')).toBe(true)
  })

  test('rejects docs.google.com outside /forms/', () => {
    expect(isAppealUrlValid('https://docs.google.com/spreadsheets/d/1abc')).toBe(false)
  })

  test('rejects non-https', () => {
    expect(isAppealUrlValid('http://forms.gle/Abc123')).toBe(false)
  })

  test('rejects unknown hosts', () => {
    expect(isAppealUrlValid('https://evil.example.com/x')).toBe(false)
  })

  test('normalizes JSON-escaped \\/ before checking', () => {
    expect(isAppealUrlValid('https:\\/\\/forms.gle\\/Abc123')).toBe(true)
  })

  test('rejects empty and non-strings', () => {
    expect(isAppealUrlValid('')).toBe(false)
    expect(isAppealUrlValid(undefined as unknown as string)).toBe(false)
  })
})

describe('appealUrl', () => {
  test('returns the normalized verification url when valid', () => {
    const c = conn({ providerSpecificData: { verificationUrl: 'https:\\/\\/forms.gle\\/Abc123' } })
    expect(appealUrl(c)).toBe('https://forms.gle/Abc123')
  })

  test('returns undefined for an invalid url', () => {
    const c = conn({ providerSpecificData: { verificationUrl: 'https://evil.example.com/x' } })
    expect(appealUrl(c)).toBeUndefined()
  })

  test('returns undefined when verificationUrl is absent', () => {
    expect(appealUrl(conn({ providerSpecificData: {} }))).toBeUndefined()
    expect(appealUrl(conn({ providerSpecificData: undefined }))).toBeUndefined()
  })
})

describe('showsProviderRiskNotice', () => {
  test('shows for oauth connections', () => {
    expect(showsProviderRiskNotice(conn({ authType: 'oauth' }))).toBe(true)
  })

  test('does not show for api-key connections', () => {
    expect(showsProviderRiskNotice(conn({ authType: 'apikey' }))).toBe(false)
  })
})
