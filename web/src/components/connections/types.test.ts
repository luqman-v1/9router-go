import { describe, expect, test } from 'bun:test'
import { getProviderStats } from './types'
import type { ProviderConnection } from '../../api/client'

function conn(overrides: Partial<ProviderConnection> = {}): ProviderConnection {
  return {
    id: 'conn-1',
    provider: 'antigravity',
    authType: 'oauth',
    isActive: 1,
    testStatus: 'active',
    lastError: null,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  } as ProviderConnection
}

describe('getProviderStats', () => {
  test('counts active connection as connected even when soft lastError is present', () => {
    const list = [
      conn({ id: 'c1', testStatus: 'active', lastError: 'Model mimo-v2.5-free is not supported' }),
      conn({ id: 'c2', testStatus: 'active', lastError: null }),
    ]
    const stats = getProviderStats(list, 'antigravity')
    expect(stats.connected).toBe(2)
    expect(stats.errorCount).toBe(0)
  })

  test('counts failed or error status in errorCount', () => {
    const list = [
      conn({ id: 'c1', testStatus: 'error', lastError: 'Invalid API key' }),
      conn({ id: 'c2', testStatus: 'failed', lastError: 'Network timeout' }),
      conn({ id: 'c3', testStatus: 'active', lastError: null }),
    ]
    const stats = getProviderStats(list, 'antigravity')
    expect(stats.connected).toBe(1)
    expect(stats.errorCount).toBe(2)
  })

  test('counts inactive connections correctly', () => {
    const list = [
      conn({ id: 'c1', isActive: 0, testStatus: 'active' }),
      conn({ id: 'c2', isActive: 0, testStatus: 'active' }),
    ]
    const stats = getProviderStats(list, 'antigravity')
    expect(stats.allDisabled).toBe(true)
    expect(stats.connected).toBe(0)
    expect(stats.errorCount).toBe(0)
  })
})
