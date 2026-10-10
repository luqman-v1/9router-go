import { describe, expect, it } from 'bun:test'
import {
  activeRoleModels,
  hermesConfigYaml,
  hermesEnvContent,
  hermesHomeDir,
  isKnown9RouterEndpoint,
  normalizeHermesEndpoint,
  profileStatus,
  type HermesProfileSummary,
} from './hermesConfig'

const LOCAL = 'http://127.0.0.1:20130'
const ctx = {
  localOrigin: LOCAL,
  tunnelUrl: 'https://abc.trycloudflare.com',
  tailscaleUrl: 'http://box.tail1234.ts.net',
}

describe('hermesHomeDir', () => {
  it('resolves the default profile to ~/.hermes', () => {
    expect(hermesHomeDir('default')).toBe('~/.hermes')
  })

  it('nests a named profile under profiles/', () => {
    expect(hermesHomeDir('work')).toBe('~/.hermes/profiles/work')
  })
})

describe('normalizeHermesEndpoint', () => {
  it('falls back to the local origin when the field is blank', () => {
    expect(normalizeHermesEndpoint('  ', LOCAL)).toBe(LOCAL)
  })

  it('rewrites localhost to 127.0.0.1', () => {
    expect(normalizeHermesEndpoint('http://localhost:20130', LOCAL)).toBe(LOCAL)
  })

  it('strips /v1 and trailing slashes — the server appends /v1 itself', () => {
    expect(normalizeHermesEndpoint('http://127.0.0.1:20130/v1', LOCAL)).toBe(LOCAL)
    expect(normalizeHermesEndpoint('http://127.0.0.1:20130/', LOCAL)).toBe(LOCAL)
  })

  it('leaves a foreign origin intact', () => {
    expect(normalizeHermesEndpoint('https://openrouter.ai/api/v1/', LOCAL)).toBe('https://openrouter.ai/api')
  })
})

describe('isKnown9RouterEndpoint', () => {
  it('accepts the local origin, with or without /v1', () => {
    expect(isKnown9RouterEndpoint(LOCAL, ctx)).toBe(true)
    expect(isKnown9RouterEndpoint(`${LOCAL}/v1`, ctx)).toBe(true)
  })

  it('accepts the tunnel, Tailscale and configured public endpoints', () => {
    expect(isKnown9RouterEndpoint('https://abc.trycloudflare.com', ctx)).toBe(true)
    expect(isKnown9RouterEndpoint('http://box.tail1234.ts.net', ctx)).toBe(true)
    expect(isKnown9RouterEndpoint('https://gw.example.com', { ...ctx, publicEndpoints: ['https://gw.example.com'] })).toBe(true)
  })

  it('rejects a foreign provider URL — applying it everywhere would leak the key', () => {
    expect(isKnown9RouterEndpoint('https://openrouter.ai/api/v1', ctx)).toBe(false)
    expect(isKnown9RouterEndpoint('http://10.0.0.9:20130', ctx)).toBe(false)
  })

  it('rejects an empty endpoint', () => {
    expect(isKnown9RouterEndpoint('  ', ctx)).toBe(false)
  })
})

const profile = (over: Partial<HermesProfileSummary> = {}): HermesProfileSummary => ({
  name: 'work',
  isDefault: false,
  displayName: null,
  command: 'hermes -p work',
  baseUrl: null,
  has9Router: false,
  ...over,
})

describe('profileStatus', () => {
  it('is configured when the backend flagged it as 9router-wired', () => {
    expect(profileStatus(profile({ has9Router: true }), ctx)).toBe('configured')
  })

  it('is configured when its base_url points at a known 9router endpoint', () => {
    expect(profileStatus(profile({ baseUrl: `${LOCAL}/v1` }), ctx)).toBe('configured')
  })

  it('is "other" for a foreign endpoint', () => {
    expect(profileStatus(profile({ baseUrl: 'https://openrouter.ai/api/v1' }), ctx)).toBe('other')
  })

  it('is not_configured without any endpoint', () => {
    expect(profileStatus(profile(), ctx)).toBe('not_configured')
  })
})

describe('activeRoleModels', () => {
  it('drops blank models and the reserved default role', () => {
    expect(activeRoleModels({ default: 'p/m', delegation: ' p/m ', vision: '   ', compression: 'p/c' })).toEqual([
      { role: 'delegation', model: 'p/m' },
      { role: 'compression', model: 'p/c' },
    ])
  })
})

describe('hermesConfigYaml', () => {
  it('always writes the main model block with provider "custom"', () => {
    const yaml = hermesConfigYaml({ default: 'p/m', roles: {} }, LOCAL, 'hermes')
    expect(yaml).toContain('# Run this profile: hermes')
    expect(yaml).toContain('model:\n  default: "p/m"\n  provider: "custom"')
    expect(yaml).toContain(`base_url: "${LOCAL}"`)
    expect(yaml).toContain('api_key: ${OPENAI_API_KEY}')
  })

  it('omits auxiliary: entirely when no role is set', () => {
    expect(hermesConfigYaml({ default: 'p/m', roles: {} }, LOCAL, 'hermes')).not.toContain('auxiliary:')
    expect(hermesConfigYaml({ default: 'p/m', roles: { vision: '  ' } }, LOCAL, 'hermes')).not.toContain('auxiliary:')
  })

  it('writes delegation as its own top-level block, not under auxiliary:', () => {
    const yaml = hermesConfigYaml({ default: 'p/m', roles: { delegation: 'p/d' } }, LOCAL, 'hermes -p work')
    expect(yaml).toContain('delegation:\n  model: "p/d"\n  provider: "custom"')
    expect(yaml).not.toContain('auxiliary:')
  })

  it('writes every other role under auxiliary: with provider "custom"', () => {
    const yaml = hermesConfigYaml({ default: 'p/m', roles: { compression: 'p/c', title_generation: 'p/t' } }, LOCAL, 'hermes')
    expect(yaml).toContain('auxiliary:\n  compression:\n    provider: "custom"\n    model: "p/c"')
    expect(yaml).toContain('  title_generation:\n    provider: "custom"\n    model: "p/t"')
  })
})

describe('hermesEnvContent', () => {
  it('exports the key the yaml references by name', () => {
    expect(hermesEnvContent('sk-9router-local-token')).toBe('OPENAI_API_KEY=sk-9router-local-token\n')
  })
})