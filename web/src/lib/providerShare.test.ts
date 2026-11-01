import { describe, expect, it } from 'bun:test'
import {
  applyModelFilters,
  buildProviderShareURL,
  decodeSetPayload,
  encodeSetPayload,
  parseKeysBlock,
  parseProviderShareHash,
  SHARE_LINK_TIERS,
  shareLinkTier,
} from './providerShare'

describe('parseProviderShareHash — phase 1', () => {
  it('returns null without prov', () => {
    expect(parseProviderShareHash('#url=https://x.test/v1')).toBeNull()
  })

  it('accepts a leading ? inside the fragment', () => {
    const got = parseProviderShareHash('#?prov=openai-compatible-chat-myprov')
    expect(got?.prov).toBe('openai-compatible-chat-myprov')
  })

  it('splits a single credential and drops empty entries', () => {
    const got = parseProviderShareHash('#prov=p&api=sk-1,,sk-2,')
    expect(got?.api).toEqual(['sk-1', 'sk-2'])
  })

  it('treats auto/ask/none as keywords and a comma list as ids', () => {
    expect(parseProviderShareHash('#prov=p&models=auto')?.models).toBe('auto')
    expect(parseProviderShareHash('#prov=p&models=gpt-4,gpt-5')?.models).toEqual(['gpt-4', 'gpt-5'])
  })

  it('refuses a non-http url', () => {
    expect(parseProviderShareHash('#prov=p&url=javascript:alert(1)')?.url).toBe('')
  })

  it('derives type and apiType from the id', () => {
    expect(parseProviderShareHash('#prov=anthropic-compatible-x')?.type).toBe('anthropic-compatible')
    expect(parseProviderShareHash('#prov=openai-compatible-responses-x')?.apiType).toBe('responses')
  })

  it('returns null for an empty hash', () => {
    expect(parseProviderShareHash('')).toBeNull()
    expect(parseProviderShareHash('#')).toBeNull()
  })
})

describe('buildProviderShareURL — phase 1', () => {
  it('round-trips every accepted case', () => {
    const link = {
      prov: 'openai-compatible-chat-myprov',
      url: 'https://api.x.com/v1',
      api: ['sk-1'],
      keys: [],
      models: 'auto' as const,
      prefix: 'myprov',
      name: 'My Provider',
      note: '',
      type: 'openai-compatible',
      apiType: 'chat',
    }
    expect(parseProviderShareHash(buildProviderShareURL('https://g.test', link))).toEqual(link)
  })

  it('omits credentials entirely when the list is empty', () => {
    const url = buildProviderShareURL('https://g.test', {
      prov: 'p', url: 'https://api.x.com/v1', api: [], keys: [], models: 'none',
      prefix: 'p', name: '', note: '', type: 'openai-compatible', apiType: 'chat',
    })
    expect(url).not.toContain('api=')
  })

  it('normalises a trailing slash on the origin', () => {
    const url = buildProviderShareURL('https://g.test/', {
      prov: 'p', url: '', api: [], keys: [], models: 'none',
      prefix: '', name: '', note: '', type: 'openai-compatible', apiType: 'chat',
    })
    expect(url.startsWith('https://g.test/add#')).toBe(true)
  })
})

describe('models= filters — phase 2', () => {
  it('parses each keyword', () => {
    for (const kw of ['auto', 'ask', 'none', 'free']) {
      expect(parseProviderShareHash(`#prov=p&models=${kw}`)?.models).toBe(kw)
    }
  })

  it('parses caps, ctx and limit filters', () => {
    expect(parseProviderShareHash('#prov=p&models=caps:vision')?.models).toEqual({ caps: 'vision' })
    expect(parseProviderShareHash('#prov=p&models=ctx%3E=128k')?.models).toEqual({ ctxAtLeast: 131072 })
    expect(parseProviderShareHash('#prov=p&models=limit=50')?.models).toEqual({ limit: 50 })
  })

  it('falls back to ask for a malformed threshold', () => {
    expect(parseProviderShareHash('#prov=p&models=ctx%3E=banana')?.models).toBe('ask')
  })
})

describe('keys= block — phase 3', () => {
  it('round-trips named entries with pool and prio', () => {
    const url = buildProviderShareURL('https://g.test', {
      prov: 'openai-compatible-chat-p', url: 'https://api.x.com/v1', api: [],
      keys: [{ name: 'Office', key: 'sk-aaa' }, { name: 'Home', key: 'sk-bbb', pool: 'eu', prio: 2 }],
      models: 'auto', prefix: 'p', name: '', note: '',
      type: 'openai-compatible', apiType: 'chat',
    })
    const got = parseProviderShareHash(url)
    expect(got).not.toBeNull()
    expect(!Array.isArray(got) && got?.keys).toEqual([
      { name: 'Office', key: 'sk-aaa' },
      { name: 'Home', key: 'sk-bbb', pool: 'eu', prio: 2 },
    ])
    expect(!Array.isArray(got) && got?.api).toEqual([])
  })

  it('accepts an empty name', () => {
    const got = parseKeysBlock('|sk-aaa')
    expect(got).toEqual([{ name: '', key: 'sk-aaa' }])
  })

  it('keeps a key that carries no delimiter', () => {
    const got = parseKeysBlock('Office|sk-a.b_c-d')
    expect(got).toEqual([{ name: 'Office', key: 'sk-a.b_c-d' }])
  })

  it('rejects a key that carries a delimiter', () => {
    // "sk-a;b" is split into two entries by the block, so the second one has no
    // key — the parser cannot tell that from a genuinely keyless entry, and says
    // both in one message.
    expect(() => parseKeysBlock('Office|sk-a;b')).toThrow(/has no key/)
    expect(() => parseKeysBlock('Office|sk-a;b')).toThrow(/cannot be carried/)
  })

  it('rejects more than four fields', () => {
    expect(() => parseKeysBlock('a|b|c|d|e')).toThrow()
  })
})

describe('parseProviderShareHash — mutual exclusion', () => {
  it('refuses api and keys together', () => {
    expect(() => parseProviderShareHash('#prov=p&api=sk-1&keys=a%7Csk-2')).toThrow(/api= and keys=/)
  })

  it('refuses prov and set together', () => {
    expect(() => parseProviderShareHash('#prov=p&set=W10=')).toThrow(/prov= and set=/)
  })
})

describe('note= — phase 3', () => {
  it('is trimmed to 200 characters', () => {
    const long = 'x'.repeat(400)
    const url = buildProviderShareURL('https://g.test', {
      prov: 'p', url: '', api: [], keys: [], models: 'none',
      prefix: '', name: '', note: long, type: 'openai-compatible', apiType: 'chat',
    })
    expect(parseProviderShareHash(url.slice(url.indexOf('#')))?.note).toHaveLength(200)
  })
})

describe('set= — phase 4', () => {
  it('round-trips through base64url', () => {
    const entries = [
      { prov: 'openai-compatible-chat-a', url: 'https://a.test/v1', prefix: 'a', api: ['sk-a'] },
      { prov: 'anthropic-compatible-b', url: 'https://b.test/v1', prefix: 'b', models: ['m-1', 'm-2'] },
      { prov: 'deepseek', models: 'auto' },
    ]
    // decodeSetPayload normalises every entry (derives type/apiType, fills the
    // empty fields), so the round-trip is against the normalised shape.
    expect(decodeSetPayload(encodeSetPayload(entries))).toEqual([
      { prov: 'openai-compatible-chat-a', url: 'https://a.test/v1', prefix: 'a', api: ['sk-a'], keys: [], models: 'none', name: '', note: '', type: 'openai-compatible', apiType: 'chat' },
      { prov: 'anthropic-compatible-b', url: 'https://b.test/v1', prefix: 'b', api: [], keys: [], models: ['m-1', 'm-2'], name: '', note: '', type: 'anthropic-compatible', apiType: '' },
      { prov: 'deepseek', url: '', prefix: '', api: [], keys: [], models: 'auto', name: '', note: '', type: 'openai-compatible', apiType: 'chat' },
    ])
  })

  it('rejects more than eight entries', () => {
    const entries = Array.from({ length: 9 }, (_, i) => ({ prov: `p${i}`, url: '', api: [], models: 'none' as const, prefix: '', name: '', note: '', type: 'openai-compatible', apiType: 'chat' }))
    expect(() => encodeSetPayload(entries)).toThrow(/8/)
  })

  it('rejects a malformed payload', () => {
    expect(() => decodeSetPayload('not-base64!!')).toThrow()
  })

  it('parses a whole set link into one link per entry', () => {
    const links = [
      { prov: 'openai-compatible-chat-a', url: 'https://a.test/v1', prefix: 'a', api: ['sk-a'], models: 'auto', name: '', note: '', type: 'openai-compatible', apiType: 'chat', keys: [] },
      { prov: 'deepseek', url: '', prefix: '', api: [], models: 'auto', name: '', note: '', type: 'openai-compatible', apiType: 'chat', keys: [] },
    ]
    const hash = `#set=${encodeURIComponent(encodeSetPayload(links))}`
    expect(parseProviderShareHash(hash)).toEqual(links)
  })
})

describe('applyModelFilters — phase 2', () => {
  const catalog = [
    { id: 'with-vision', contextLength: 200000, capabilities: { vision: true, reasoning: true } },
    { id: 'no-caps-declared', contextLength: 100000 },
    { id: 'small', contextLength: 8000, capabilities: { reasoning: true } },
  ]

  it('keeps an entry that declares no capabilities', () => {
    // "no data" is not "no capability": dropping the row would hide a model the
    // receiver can use.
    expect(applyModelFilters(catalog, { caps: 'vision' }).map((m) => m.id)).toEqual([
      'with-vision',
      'no-caps-declared',
    ])
  })

  it('filters on the context floor', () => {
    expect(applyModelFilters(catalog, { ctxAtLeast: 131072 }).map((m) => m.id)).toEqual(['with-vision'])
  })

  it('caps the list', () => {
    expect(applyModelFilters(catalog, { limit: 2 }).map((m) => m.id)).toHaveLength(2)
  })

  it('passes a keyword or an id list straight through', () => {
    expect(applyModelFilters(catalog, 'auto')).toBe(catalog)
    expect(applyModelFilters(catalog, ['x'])).toBe(catalog)
  })
})

describe('shareLinkTier', () => {
  it('reports the three length tiers', () => {
    expect(shareLinkTier(150)).toBe('link')
    expect(shareLinkTier(2500)).toBe('split')
    expect(shareLinkTier(4500)).toBe('refuse')
  })

  it('pins the boundary constants', () => {
    expect(SHARE_LINK_TIERS.splitAt).toBe(2000)
    expect(SHARE_LINK_TIERS.refuseAt).toBe(4000)
  })
})
