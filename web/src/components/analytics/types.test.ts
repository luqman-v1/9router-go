import { describe, expect, it } from 'bun:test'
import { cachedTokensFor, calculateTPS, fmt, formatDuration, providerDisplayName, type RequestDetailItem } from './types'

describe('request detail token formatting', () => {
  it('prefers canonical cached_tokens', () => {
    const detail: RequestDetailItem = {
      tokens: { cached_tokens: 120, cache_read_input_tokens: 90 },
    }

    expect(cachedTokensFor(detail)).toBe(120)
    expect(fmt(cachedTokensFor(detail))).toBe('120')
  })

  it('supports reasoning tokens and token compression details', () => {
    const detail: RequestDetailItem = {
      tokens: {
        prompt_tokens: 203897,
        completion_tokens: 1500,
        reasoning_tokens: 800,
        original_input_tokens: 369000,
        saved_tokens: 165103,
        saved_percent: 45,
      },
    }

    expect(detail.tokens?.prompt_tokens).toBe(203897)
    expect(detail.tokens?.original_input_tokens).toBe(369000)
    expect(detail.tokens?.saved_tokens).toBe(165103)
    expect(detail.tokens?.saved_percent).toBe(45)
    expect(detail.tokens?.reasoning_tokens).toBe(800)
    expect(
      `Compressed: ${fmt(detail.tokens?.original_input_tokens)} → ${fmt(detail.tokens?.prompt_tokens)} (${detail.tokens?.saved_percent}% saved)`
    ).toBe('Compressed: 369,000 → 203,897 (45% saved)')
  })

  it('falls back to legacy cache_read_input_tokens', () => {
    expect(cachedTokensFor({ tokens: { cache_read_input_tokens: 80 } })).toBe(80)
  })

  it('renders missing cache usage as zero', () => {
    expect(fmt(cachedTokensFor({}))).toBe('0')
    expect(fmt(cachedTokensFor({ tokens: { cached_tokens: 0, cache_read_input_tokens: 25 } }))).toBe('0')
  })
})

describe('providerDisplayName', () => {
  it('maps a custom compatible node id to its configured name', () => {
    const nodes = [{ id: 'openai-compatible-chat-abc', name: 'My vLLM' }]

    expect(providerDisplayName('openai-compatible-chat-abc', nodes)).toBe('My vLLM')
  })

  it('resolves catalog providers by id', () => {
    expect(providerDisplayName('deepseek')).toBe('DeepSeek')
  })

  it('passes an unknown id through unchanged', () => {
    expect(providerDisplayName('some-unknown-provider')).toBe('some-unknown-provider')
  })

  it('renders missing provider as unknown', () => {
    expect(providerDisplayName(undefined)).toBe('unknown')
    expect(providerDisplayName('')).toBe('unknown')
  })
})

describe('formatDuration and calculateTPS', () => {
  it('formats ms and seconds', () => {
    expect(formatDuration(null)).toBe('—')
    expect(formatDuration(450)).toBe('450ms')
    expect(formatDuration(5376)).toBe('5.4s')
    expect(formatDuration(13300)).toBe('13.3s')
  })

  it('calculates tokens per second', () => {
    expect(calculateTPS(null, null, null)).toBeNull()
    expect(calculateTPS(1241, 13300, 1100)).toBe('101.7 tps')
    expect(calculateTPS(1241, 13300, 0)).toBe('93.3 tps')
  })
})
