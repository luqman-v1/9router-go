import { describe, expect, it } from 'bun:test'
import {
  mediaProviderPath,
  parseMediaProvider,
  parseProviderId,
  pathToTab,
  providerPath,
  TAB_ROUTES,
  USAGE_SECTIONS,
  USAGE_SECTION_BY_TAB,
  USAGE_SECTION_BY_VALUE,
} from './router'

describe('router', () => {
  it('maps all upstream routes correctly', () => {
    expect(pathToTab('/login')).toBe('login')
    expect(pathToTab('/dashboard/endpoint')).toBe('endpoint')
    expect(pathToTab('/dashboard')).toBe('endpoint')
    expect(pathToTab('/')).toBe('endpoint')
    expect(pathToTab('/dashboard/providers')).toBe('connections')
    expect(pathToTab('/dashboard/combos')).toBe('combos')
    expect(pathToTab('/dashboard/usage')).toBe('analytics')
    expect(pathToTab('/dashboard/quota')).toBe('quota')
    expect(pathToTab('/dashboard/token-saver')).toBe('token-saver')
    expect(pathToTab('/dashboard/usage/cache')).toBe('usage-cache')
    expect(pathToTab('/dashboard/usage/compression')).toBe('usage-compression')
    // The paths from before the move still land on the Usage section (#200)
    // rather than falling through to the endpoint view.
    expect(pathToTab('/dashboard/cache')).toBe('usage-cache')
    expect(pathToTab('/dashboard/analytics/compression')).toBe('usage-compression')
    expect(pathToTab('/dashboard/cli-tools')).toBe('cli-tools')
    expect(pathToTab('/dashboard/media-providers/embedding')).toBe('media-embedding')
    expect(pathToTab('/dashboard/media-providers/image')).toBe('media-image')
    expect(pathToTab('/dashboard/media-providers/tts')).toBe('media-tts')
    expect(pathToTab('/dashboard/media-providers/stt')).toBe('media-stt')
    expect(pathToTab('/dashboard/media-providers/video')).toBe('media-video')
    expect(pathToTab('/dashboard/media-providers/web')).toBe('media-web')
    expect(pathToTab('/dashboard/proxy-pools')).toBe('proxy-pools')
    expect(pathToTab('/dashboard/skills')).toBe('skills')
    expect(pathToTab('/dashboard/console-log')).toBe('console-log')
    expect(pathToTab('/dashboard/terminal')).toBe('console-log')
    expect(pathToTab('/dashboard/profile')).toBe('settings')
  })

  it('has canonical routes for all tabs in TAB_ROUTES', () => {
    expect(TAB_ROUTES.endpoint).toBe('/dashboard/endpoint')
    expect(TAB_ROUTES.connections).toBe('/dashboard/providers')
    expect(TAB_ROUTES.combos).toBe('/dashboard/combos')
    expect(TAB_ROUTES.analytics).toBe('/dashboard/usage')
    expect(TAB_ROUTES.quota).toBe('/dashboard/quota')
    expect(TAB_ROUTES['token-saver']).toBe('/dashboard/token-saver')
    expect(TAB_ROUTES['usage-cache']).toBe('/dashboard/usage/cache')
    expect(TAB_ROUTES['usage-compression']).toBe('/dashboard/usage/compression')
    expect(TAB_ROUTES['cli-tools']).toBe('/dashboard/cli-tools')
    expect(TAB_ROUTES['media-embedding']).toBe('/dashboard/media-providers/embedding')
    expect(TAB_ROUTES['media-image']).toBe('/dashboard/media-providers/image')
    expect(TAB_ROUTES['media-tts']).toBe('/dashboard/media-providers/tts')
    expect(TAB_ROUTES['media-stt']).toBe('/dashboard/media-providers/stt')
    expect(TAB_ROUTES['media-video']).toBe('/dashboard/media-providers/video')
    expect(TAB_ROUTES['media-web']).toBe('/dashboard/media-providers/web')
    expect(TAB_ROUTES['proxy-pools']).toBe('/dashboard/proxy-pools')
    expect(TAB_ROUTES.skills).toBe('/dashboard/skills')
    expect(TAB_ROUTES['console-log']).toBe('/dashboard/console-log')
    expect(TAB_ROUTES.settings).toBe('/dashboard/profile')
    expect(TAB_ROUTES.login).toBe('/login')
  })

  it('parses provider ID from route pathname', () => {
    expect(parseProviderId('/dashboard/providers/antigravity')).toBe('antigravity')
    expect(parseProviderId('/dashboard/providers/deepseek')).toBe('deepseek')
    expect(parseProviderId('/providers/antigravity')).toBe('antigravity')
    expect(parseProviderId('/dashboard/providers')).toBeNull()
    expect(parseProviderId('/dashboard/providers/')).toBeNull()
    expect(parseProviderId('/dashboard/providers/new')).toBeNull()
    expect(parseProviderId('/dashboard/endpoint')).toBeNull()
    expect(parseProviderId('/')).toBeNull()
    expect(providerPath('antigravity')).toBe('/dashboard/providers/antigravity')
  })

  it('parses media provider route from route pathname', () => {
    expect(parseMediaProvider('/dashboard/media-providers/webSearch/antigravity')).toEqual({
      kind: 'webSearch',
      providerId: 'antigravity'
    })
    expect(parseMediaProvider('/dashboard/media-providers/webFetch/ollama')).toEqual({
      kind: 'webFetch',
      providerId: 'ollama'
    })
    expect(parseMediaProvider('/dashboard/media-providers/image/openai')).toEqual({
      kind: 'image',
      providerId: 'openai'
    })
    expect(parseMediaProvider('/dashboard/media-providers/combo/123')).toBeNull()
    expect(parseMediaProvider('/dashboard/media-providers/web')).toBeNull()
    expect(mediaProviderPath('webSearch', 'antigravity')).toBe('/dashboard/media-providers/webSearch/antigravity')
    expect(pathToTab('/dashboard/media-providers/webSearch/antigravity')).toBe('media-web')
    expect(pathToTab('/dashboard/media-providers/webFetch/ollama')).toBe('media-web')
  })
})

describe('usage sections', () => {
  it('offers the four sections the issue asked to merge under Usage', () => {
    expect(USAGE_SECTIONS.map((s) => s.value)).toEqual([
      'overview',
      'cache',
      'compression',
      'details',
    ])
  })

  it('gives every section that owns a path a tab, and resolves it back', () => {
    for (const section of USAGE_SECTIONS) {
      const tab = USAGE_SECTION_BY_VALUE[section.value]
      if (!tab) continue
      expect(USAGE_SECTION_BY_TAB[tab]).toBe(section.value)
      expect(pathToTab(TAB_ROUTES[tab])).toBe(tab)
    }
  })

  it('keeps Overview and Details on the shared analytics path', () => {
    // Both sections live behind one tab, so a URL cannot distinguish them and
    // neither may claim a tab of its own.
    expect(USAGE_SECTION_BY_VALUE.overview).toBeUndefined()
    expect(USAGE_SECTION_BY_VALUE.details).toBeUndefined()
    expect(TAB_ROUTES.analytics).toBe('/dashboard/usage')
  })

  it('resolves both aliases of a moved section to the same tab', () => {
    expect(pathToTab('/cache')).toBe(pathToTab('/dashboard/usage/cache'))
    expect(pathToTab('/analytics/compression')).toBe(
      pathToTab('/dashboard/usage/compression')
    )
  })
})
