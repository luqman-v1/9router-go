import { describe, expect, it } from 'vitest'
import { buildFuzzyIndex, filterFuzzy, filterFuzzyIndexed, fuzzyMatch, matchRank } from './fuzzyMatch'

describe('fuzzyMatch', () => {
  it('matches an exact substring', () => {
    expect(fuzzyMatch('glm', 'zhipu/glm-5.3')).toBe(true)
  })

  it('matches when stripping separators closes the gap', () => {
    expect(fuzzyMatch('deepseek chat', 'deepseek/chat-v3')).toBe(true)
    expect(fuzzyMatch('glm 5.3', 'zhipu/glm-5.3')).toBe(true)
    expect(fuzzyMatch('glm.5.3', 'zhipu/glm-5.3')).toBe(true)
    expect(fuzzyMatch('glm 5', 'zhipu/glm-5.3')).toBe(true)
  })

  it('matches a two-character slip inside a short id', () => {
    expect(fuzzyMatch('glm-5.4', 'zhipu/glm-5.3')).toBe(true) // last digit off
    expect(fuzzyMatch('glm-4.6', 'zhipu/glm-5.3')).toBe(true) // decimal and digit off
  })

  it('does not tolerate any miss for a one or two character query', () => {
    expect(fuzzyMatch('qq', 'zhipu/glm-5.3')).toBe(false)
    expect(fuzzyMatch('m5', 'zhipu/glm-5.3')).toBe(true) // separator-free, not a typo
  })

  it('refuses to fuzz a long query, so the filter keeps filtering', () => {
    const long = 'anthropic/claude-sonnet-4-5-20260929-thinking-preview'
    expect(fuzzyMatch('claude-sonnet-4-5-x', long)).toBe(false)
    expect(fuzzyMatch('claude-sonnet', long)).toBe(true)
  })

  it('treats a blank query as no constraint', () => {
    expect(fuzzyMatch('   ', 'anything')).toBe(true)
    expect(filterFuzzy('   ', ['a', 'b'])).toEqual(['a', 'b'])
  })

  it('does not match an unrelated id', () => {
    expect(fuzzyMatch('glm', 'openai/gpt-5-mini')).toBe(false)
    expect(fuzzyMatch('gpt', 'openai/glm-5')).toBe(false) // reshuffled, not a typo
  })
})

describe('matchRank', () => {
  it('ranks exact above separator-equal above tolerance', () => {
    expect(matchRank('glm', 'zhipu/glm-5.3')).toBe(0)
    expect(matchRank('glm 5.3', 'zhipu/glm-5.3')).toBe(1)
    expect(matchRank('glm-5.4', 'zhipu/glm-5.3')).toBe(2)
  })

  it('ranks the literal substring above a separator-equal sibling', () => {
    expect(matchRank('glm 5.3', 'glm 5.3')).toBe(0)
  })
})

describe('filterFuzzy', () => {
  const ids = [
    'zhipu/glm-5.3',
    'zhipu/glm-4.6',
    'deepseek/deepseek-chat-v3',
    'openai/gpt-5-mini',
    'anthropic/claude-sonnet-4-5-20260929',
  ]

  it('puts the best-ranked hit first', () => {
    expect(filterFuzzy('glm', ids)[0]).toBe('zhipu/glm-5.3')
  })

  it('keeps every matching row, in rank order', () => {
    expect(filterFuzzy('glm', ids)).toEqual(['zhipu/glm-5.3', 'zhipu/glm-4.6'])
  })

  it('survives a split provider and model', () => {
    expect(filterFuzzy('deepseek chat', ids)).toEqual(['deepseek/deepseek-chat-v3'])
    expect(filterFuzzy('glm 5', ids)).toEqual(['zhipu/glm-5.3'])
  })

  it('caps the result count', () => {
    expect(filterFuzzy('', ids, 2)).toEqual(['zhipu/glm-5.3', 'zhipu/glm-4.6'])
  })
})

describe('filterFuzzyIndexed', () => {
  const ids = [
    'zhipu/glm-5.3',
    'zhipu/glm-4.6',
    'deepseek/deepseek-chat-v3',
    'openai/gpt-5-mini',
    'anthropic/claude-sonnet-4-5-20260929',
  ]
  const index = buildFuzzyIndex(ids)

  it('agrees with filterFuzzy on every query shape', () => {
    // The index is a performance path, not a behaviour change: the two must
    // return the same list for a blank query, a substring, a separator-split
    // id, a one-character slip and a no-match.
    for (const q of ['', '   ', 'glm', 'glm 5.3', 'glm-5.4', 'deepseek chat', 'zzzzz', 'a', '/']) {
      expect(filterFuzzyIndexed(q, index)).toEqual(filterFuzzy(q, ids))
    }
  })

  it('returns ids, not index rows', () => {
    expect(filterFuzzyIndexed('glm', index)).toEqual(['zhipu/glm-5.3', 'zhipu/glm-4.6'])
  })

  it('caps the result count like filterFuzzy', () => {
    expect(filterFuzzyIndexed('', index, 2)).toEqual(['zhipu/glm-5.3', 'zhipu/glm-4.6'])
  })

  it('survives a split provider and model', () => {
    expect(filterFuzzyIndexed('deepseek chat', index)).toEqual(['deepseek/deepseek-chat-v3'])
    expect(filterFuzzyIndexed('glm 5', index)).toEqual(['zhipu/glm-5.3'])
  })

  it('refuses to fuzz a long query, so the filter keeps filtering', () => {
    expect(filterFuzzyIndexed('claude-sonnet-4-5-x', index)).toEqual([])
    expect(filterFuzzyIndexed('claude-sonnet', index)).toEqual(['anthropic/claude-sonnet-4-5-20260929'])
  })

  it('remains a pure query-time pass: the index is built once', () => {
    // A keystroke pattern: same index, different queries, no rebuild. The
    // returned lists stay correct across repeated calls, which is what the
    // component does on every input event.
    const first = filterFuzzyIndexed('glm', index)
    const second = filterFuzzyIndexed('glm', index)
    expect(second).toEqual(first)
    expect(filterFuzzyIndexed('zzzzz', index)).toEqual([])
    expect(filterFuzzyIndexed('glm', index)).toEqual(first)
  })

  it('agrees with filterFuzzy over a live-sized catalog', () => {
    // The perf path is a speed change, not a behaviour change, so the agreement
    // claim has to be tested at the size the component actually runs it: the
    // shared-character gate could silently drop a match that only a 5-id list
    // never exercises. Build a catalog the shape of a real one — many
    // providers, many model families, several builds each — and compare every
    // query shape against the reference implementation.
    const providers = [
      'openai', 'anthropic', 'deepseek', 'zhipu', 'google', 'meta', 'mistral',
      'qwen', 'xai', 'cohere', 'moonshot', 'minimax', 'opencode',
      'antigravity', 'cline', 'freebuff', 'qoder', 'dahl', 'nous', 'groq',
    ]
    const models = [
      'chat', 'chat-v3', 'pro', 'flash', 'mini', 'sonnet', 'opus', 'haiku',
      'coder', 'reasoner', 'ultra', 'vision', 'instruct', 'turbo', 'lite',
    ]
    const catalog: string[] = []
    for (const p of providers) {
      for (const m of models) {
        for (let n = 0; n < 3; n++) catalog.push(`${p}/${m}-${n}`)
      }
    }
    const big = buildFuzzyIndex(catalog)
    for (const q of ['', 'glm', 'glm 5.3', 'glm-5.4', 'deepseek chat', 'chat', 'a', 'zzzzz', 'qqqq', 'sonnet']) {
      expect(filterFuzzyIndexed(q, big)).toEqual(filterFuzzy(q, catalog))
    }
  })
})
