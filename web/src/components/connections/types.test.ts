import { describe, expect, test } from 'bun:test'
import { getIconPath, getProviderStats } from './types'
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

describe('getIconPath', () => {
  test('resolves a catalog alias to the id its artwork is filed under', () => {
    expect(getIconPath('ag')).toBe('/providers/antigravity.png')
    expect(getIconPath('antigravity')).toBe('/providers/antigravity.png')
  })

  test('borrows another provider mark for an id that shares its brand', () => {
    // zai-search is a Go registry entry with no upstream counterpart, so
    // /providers/zai-search.png 404s; it resolves to GLM's mark instead.
    expect(getIconPath('zai-search')).toBe('/providers/glm.png')
  })

  test('keeps the generic glyphs for compatible nodes and unknown ids', () => {
    expect(getIconPath('openai-compatible-chat-x')).toBe('/providers/oai-cc.png')
    expect(getIconPath('openai-compatible-chat-x', 'responses')).toBe('/providers/oai-r.png')
    expect(getIconPath('anthropic-compatible-x')).toBe('/providers/anthropic-m.png')
    expect(getIconPath('my-reverse-proxy')).toBe('/providers/oai-cc.png')
    expect(getIconPath(undefined)).toBe('/providers/oai-cc.png')
  })
})
