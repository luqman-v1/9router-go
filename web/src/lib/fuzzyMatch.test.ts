import { describe, expect, it } from 'vitest'
import { filterFuzzy, fuzzyMatch, matchRank } from './fuzzyMatch'

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
