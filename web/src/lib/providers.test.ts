import assert from 'node:assert'
import { describe, it } from 'node:test'
import {
  getProvidersByKind,
  isChatProvider,
  MEDIA_PROVIDER_KINDS,
  PROVIDER_CATALOG,
} from './providers'
import { getModelsByProviderId, PROVIDER_ID_TO_ALIAS } from './models'
import { pathToTab, TAB_ROUTES } from './router'

describe('providers & media separation', () => {
  it('correctly classifies chat vs pure media providers', () => {
    const openai = PROVIDER_CATALOG.find((p) => p.id === 'openai')
    const anthropic = PROVIDER_CATALOG.find((p) => p.id === 'anthropic')
    const elevenlabs = PROVIDER_CATALOG.find((p) => p.id === 'elevenlabs')
    const deepgram = PROVIDER_CATALOG.find((p) => p.id === 'deepgram')
    const bfl = PROVIDER_CATALOG.find((p) => p.id === 'black-forest-labs')
    const voyage = PROVIDER_CATALOG.find((p) => p.id === 'voyage-ai')
    const runway = PROVIDER_CATALOG.find((p) => p.id === 'runwayml')
    const brave = PROVIDER_CATALOG.find((p) => p.id === 'brave-search')

    assert.ok(openai && isChatProvider(openai))
    assert.ok(anthropic && isChatProvider(anthropic))
    assert.ok(elevenlabs && !isChatProvider(elevenlabs))
    assert.ok(deepgram && !isChatProvider(deepgram))
    assert.ok(bfl && !isChatProvider(bfl))
    assert.ok(voyage && !isChatProvider(voyage))
    assert.ok(runway && !isChatProvider(runway))
    assert.ok(brave && !isChatProvider(brave))
  })

  it('retrieves providers by kind', () => {
    const ttsProviders = getProvidersByKind('tts')
    assert.ok(ttsProviders.some((p) => p.id === 'elevenlabs'))
    assert.ok(ttsProviders.some((p) => p.id === 'fish-audio'))
    assert.ok(ttsProviders.some((p) => p.id === 'openai'))
    assert.strictEqual(ttsProviders.some((p) => p.id === 'anthropic'), false)

    const sttProviders = getProvidersByKind('stt')
    assert.ok(sttProviders.some((p) => p.id === 'assemblyai'))
    assert.ok(sttProviders.some((p) => p.id === 'deepgram'))
    assert.ok(sttProviders.some((p) => p.id === 'openai'))

    const imageProviders = getProvidersByKind('image')
    assert.ok(imageProviders.some((p) => p.id === 'black-forest-labs'))
    assert.ok(imageProviders.some((p) => p.id === 'fal-ai'))
    assert.ok(imageProviders.some((p) => p.id === 'runwayml'))

    const videoProviders = getProvidersByKind('video')
    assert.ok(videoProviders.some((p) => p.id === 'runwayml'))

    const embeddingProviders = getProvidersByKind('embedding')
    assert.ok(embeddingProviders.some((p) => p.id === 'voyage-ai'))
    assert.ok(embeddingProviders.some((p) => p.id === 'openai'))

    assert.ok(MEDIA_PROVIDER_KINDS.length >= 6)
  })

  it('maps media router routes correctly', () => {
    assert.strictEqual(pathToTab('/dashboard/media-providers/embedding'), 'media-embedding')
    assert.strictEqual(pathToTab('/dashboard/media-providers/image'), 'media-image')
    assert.strictEqual(pathToTab('/dashboard/media-providers/tts'), 'media-tts')
    assert.strictEqual(pathToTab('/dashboard/media-providers/stt'), 'media-stt')
    assert.strictEqual(pathToTab('/dashboard/media-providers/video'), 'media-video')
    assert.strictEqual(pathToTab('/dashboard/media-providers/web'), 'media-web')

    assert.strictEqual(TAB_ROUTES['media-embedding'], '/dashboard/media-providers/embedding')
    assert.strictEqual(TAB_ROUTES['media-image'], '/dashboard/media-providers/image')
    assert.strictEqual(TAB_ROUTES['media-tts'], '/dashboard/media-providers/tts')
    assert.strictEqual(TAB_ROUTES['media-stt'], '/dashboard/media-providers/stt')
    assert.strictEqual(TAB_ROUTES['media-video'], '/dashboard/media-providers/video')
    assert.strictEqual(TAB_ROUTES['media-web'], '/dashboard/media-providers/web')
  })

  it('marks clinepass as dual-auth (oauth + apikey, upstream parity)', () => {
    const clinepass = PROVIDER_CATALOG.find((p) => p.id === 'clinepass')
    assert.ok(clinepass)
    assert.strictEqual(clinepass.category, 'oauth')
    assert.ok(clinepass.authModes?.includes('oauth'))
    assert.ok(clinepass.authModes?.includes('apikey'))
    assert.strictEqual(clinepass.website, 'https://cline.bot')
    assert.strictEqual(clinepass.notice?.signupUrl, 'https://app.cline.bot')
  })

  it('marks all upstream dual-auth providers (oauth+apikey registry modes)', () => {
    const dual = [
      'clinepass',
      'codebuddy-cn',
      'codebuddy-intl',
      'kimchi',
      'kimi',
      'qoder',
      'windsurf',
      'xai',
      'xiaomi-mimo',
    ]
    for (const id of dual) {
      const p = PROVIDER_CATALOG.find((e) => e.id === id)
      assert.ok(p, `${id} missing from catalog`)
      assert.ok(p.authModes?.includes('oauth'), `${id} missing oauth mode`)
      assert.ok(p.authModes?.includes('apikey'), `${id} missing apikey mode`)
    }
  })

  it('matches upstream oauth category for trae and windsurf', () => {
    assert.strictEqual(PROVIDER_CATALOG.find((p) => p.id === 'trae')?.category, 'oauth')
    assert.strictEqual(PROVIDER_CATALOG.find((p) => p.id === 'windsurf')?.category, 'oauth')
  })

  it('carries upstream display links (website/notice) for all registry providers', () => {
    // No upstream registry counterpart (Go-only or pseudo header entries),
    // or no display link upstream by design (mimo-free, mmf, opencode).
    const exempt = new Set([
      'freebuff',
      'kimi-coding',
      'mimo-free',
      'mmf',
      'opencode',
      'zai-search',
      'x-codebuddy-request',
      'x-github-api-version',
      'x-requested-with',
      'x-vscode-user-agent-library-version',
      'anthropic-version',
      'openai-intent',
      'originator',
      'user-agent',
    ])
    for (const p of PROVIDER_CATALOG) {
      if (exempt.has(p.id)) continue
      assert.ok(
        p.website || p.notice || p.authType || p.authHint,
        `${p.id} missing upstream display data (website/notice/authType/authHint)`
      )
    }
    const grokWeb = PROVIDER_CATALOG.find((p) => p.id === 'grok-web')
    assert.strictEqual(grokWeb?.authType, 'cookie')
    assert.ok(grokWeb?.authHint)
    const openai = PROVIDER_CATALOG.find((p) => p.id === 'openai')
    assert.strictEqual(openai?.notice?.apiKeyUrl, 'https://platform.openai.com/api-keys')
  })

  it('keeps self-hosted/image providers on required API key (upstream parity)', () => {
    for (const id of ['comfyui', 'sdwebui', 'recraft']) {
      const p = PROVIDER_CATALOG.find((e) => e.id === id)
      assert.ok(p, `${id} missing from catalog`)
      assert.strictEqual(p.category, 'apikey')
      assert.strictEqual(p.noAuth, undefined)
    }
  })

  it('marks devin-cli as free noAuth (upstream parity)', () => {
    const p = PROVIDER_CATALOG.find((e) => e.id === 'devin-cli')
    assert.ok(p)
    assert.strictEqual(p.category, 'free')
    assert.strictEqual(p.noAuth, true)
  })

  it('matches upstream free category for gemini-cli', () => {
    assert.strictEqual(PROVIDER_CATALOG.find((p) => p.id === 'gemini-cli')?.category, 'free')
  })

  it('includes hidden mmf provider (upstream parity)', () => {
    const p = PROVIDER_CATALOG.find((e) => e.id === 'mmf')
    assert.ok(p)
    assert.strictEqual(p.category, 'apikey')
    assert.strictEqual(p.hidden, true)
  })
  it('wires the five v0.5.91 aggregators (tokenharbor, dahl, atria, agnes, bai)', () => {
    const expected: Record<string, { category: string; models: number; fetcher: boolean }> = {
      tokenharbor: { category: 'apikey', models: 6, fetcher: true },
      dahl: { category: 'apikey', models: 3, fetcher: true },
      atria: { category: 'apikey', models: 1, fetcher: false },
      agnes: { category: 'freeTier', models: 4, fetcher: false },
      bai: { category: 'apikey', models: 0, fetcher: true },
    }
    for (const [id, want] of Object.entries(expected)) {
      const p = PROVIDER_CATALOG.find((e) => e.id === id)
      assert.ok(p, `${id} missing from the dashboard catalog`)
      assert.strictEqual(p.category, want.category, `${id} category`)
      assert.ok(isChatProvider(p), `${id} should be a chat provider`)
      assert.strictEqual(p.alias, id, `${id} uiAlias equals its id upstream`)
      assert.strictEqual(getModelsByProviderId(id).length, want.models, `${id} model count`)
      assert.strictEqual(Boolean(p.modelsFetcher), want.fetcher, `${id} modelsFetcher`)
    }
  })

  it('resolves every provider alias to a catalog with an entry', () => {
    const missing = Object.keys(PROVIDER_ID_TO_ALIAS).filter(
      (id) => !PROVIDER_CATALOG.some((p) => p.id === id || p.alias === id)
    )
    assert.deepStrictEqual(missing, [])
  })
})
