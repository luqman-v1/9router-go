import { describe, expect, it } from 'bun:test'
import { getModelsByProviderId, PROVIDER_ID_TO_ALIAS, PROVIDER_MODELS } from './models'
import { PROVIDER_CATALOG } from './providers'

// PROVIDER_MODELS is keyed by provider id, while PROVIDER_ID_TO_ALIAS holds the
// short display prefix (uiAlias) used to render `cmc/<model>`. The two only
// coincide for some providers, so the resolver must try the id first and fall
// back to the alias instead of handing the dashboard an empty catalog.
describe('getModelsByProviderId', () => {
  it('resolves a provider whose display prefix differs from its id', () => {
    expect(PROVIDER_ID_TO_ALIAS.commandcode).toBe('cmc')

    const models = getModelsByProviderId('commandcode')

    expect(models.length).toBe(23)
    expect(models.map((m) => m.id)).toContain('deepseek/deepseek-v4-pro')
    expect(models.map((m) => m.id)).toContain('nvidia/nemotron-3-ultra-550b-a55b')
  })

  it('never returns an empty catalog for a provider that has one', () => {
    const broken = Object.keys(PROVIDER_ID_TO_ALIAS)
      .filter((id) => (PROVIDER_MODELS[id] ?? []).length > 0)
      .filter((id) => getModelsByProviderId(id).length === 0)

    expect(broken).toEqual([])
  })

  it('round-trips every catalog key that declares models', () => {
    // `zd` is a declared-but-empty entry: Zed's catalog is fetched live, so an
    // empty array is a valid catalog state, not a resolution failure.
    const declared = Object.keys(PROVIDER_MODELS).filter((id) => PROVIDER_MODELS[id].length > 0)

    for (const id of declared) {
      expect(getModelsByProviderId(id).length).toBe(PROVIDER_MODELS[id].length)
    }
  })

  it('returns an empty list for a provider with no static catalog', () => {
    expect(getModelsByProviderId('does-not-exist')).toEqual([])
  })
})

// The dashboard renders the rows in catalog order, so the catalog sequence is
// part of the contract with upstream: `open-sse/providers/registry/commandcode.js`
// lists the models in exactly this order. A reordering here silently changes
// what the two dashboards show.
describe('commandcode catalog', () => {
  const UPSTREAM_REGISTRY_ORDER = [
    'deepseek/deepseek-v4-pro',
    'deepseek/deepseek-v4-flash',
    'moonshotai/Kimi-K3.1-Pro',
    'moonshotai/Kimi-K2.7-Code',
    'moonshotai/Kimi-K2.7-Code-Highspeed',
    'moonshotai/Kimi-K2.6',
    'moonshotai/Kimi-K2.5',
    'zai-org/GLM-5.2',
    'zai-org/GLM-5.2-Fast',
    'zai-org/GLM-5.1',
    'zai-org/GLM-5',
    'MiniMaxAI/MiniMax-M3',
    'MiniMaxAI/MiniMax-M2.7',
    'MiniMaxAI/MiniMax-M2.5',
    'xiaomi/mimo-v2.5-pro',
    'xiaomi/mimo-v2.5',
    'Qwen/Qwen3.6-Max-Preview',
    'Qwen/Qwen3.6-Plus',
    'Qwen/Qwen3.7-Max',
    'Qwen/Qwen3.7-Plus',
    'stepfun/Step-3.7-Flash',
    'stepfun/Step-3.5-Flash',
    'nvidia/nemotron-3-ultra-550b-a55b',
  ]

  it('keeps the upstream registry order', () => {
    expect(getModelsByProviderId('commandcode').map((m) => m.id)).toEqual(UPSTREAM_REGISTRY_ORDER)
  })
})

// The 22 rows above are a snapshot of upstream's registry file, not of what
// Command Code serves. Its Provider API publishes the whole catalogue on
// /provider/v1/models — the free tiers (reflection/beam:free,
// stealth/glyph-cluster:free, poolside/laguna-s-2.1-free) and everything
// released after the snapshot (minimax/minimax-m3.1-flash, z-ai/glm-5.3-flash,
// moonshotai/Kimi-K3, …). Without a modelsFetcher the dashboard has no way to
// reach any of it: the "Suggested free models" list stays empty and "Check
// Latest Models" never renders.
describe('commandcode live catalog', () => {
  it('fetches the Provider API catalogue through the free-tier filter', () => {
    const p = PROVIDER_CATALOG.find((e) => e.id === 'commandcode')
    expect(p?.modelsFetcher).toEqual({
      url: 'https://api.commandcode.ai/provider/v1/models',
      type: 'commandcode-free',
    })
  })
})

// Of the five keyless catalogues wired on the backend, only Vercel AI Gateway
// gets a modelsFetcher here. The "Suggested free models" section is a free-tier
// surface, and probing each feed live shows the other four have no zero-priced
// tier to suggest: chutes prices every model, nvidia and ollama carry no
// pricing block at all, and openrouter already had its openrouter-free fetcher.
// A fetcher that returns nothing is worse than no fetcher — it renders an empty
// section the operator has to interpret.
describe('keyless live catalogs', () => {
  it('wires Vercel AI Gateway through its own free-tier filter', () => {
    const p = PROVIDER_CATALOG.find((e) => e.id === 'vercel-ai-gateway')
    expect(p?.modelsFetcher).toEqual({
      url: 'https://ai-gateway.vercel.sh/v1/models',
      type: 'vercel-free',
    })
  })

  it('leaves the four without a free tier unfetched', () => {
    for (const id of ['chutes', 'nvidia', 'ollama']) {
      const p = PROVIDER_CATALOG.find((e) => e.id === id)
      expect(p?.modelsFetcher, `${id} has no zero-priced tier to suggest`).toBeUndefined()
    }
  })

  it('keeps openrouter on the free-tier fetcher it already had', () => {
    const p = PROVIDER_CATALOG.find((e) => e.id === 'openrouter')
    expect(p?.modelsFetcher?.type).toBe('openrouter-free')
  })
})

