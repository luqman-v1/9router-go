import { describe, expect, it } from 'bun:test'
import {
  cachedTokensFor,
  calculateTPS,
  extractDetailError,
  fmt,
  formatDetailStatus,
  formatDuration,
  providerDisplayName,
  type RequestDetailItem,
} from './types'

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

  it('matches mixed-case custom node ids even when caller lowercases the id', () => {
    const nodes = [{ id: 'openai-compatible-chat-Wildy', name: 'wildy.ai.id' }]

    // Both exact case (stored key) and lowercased (topology/stream key) resolve
    expect(providerDisplayName('openai-compatible-chat-Wildy', nodes)).toBe('wildy.ai.id')
    expect(providerDisplayName('openai-compatible-chat-wildy', nodes)).toBe('wildy.ai.id')
  })

  it('resolves catalog providers case-insensitively', () => {
    expect(providerDisplayName('DeepSeek')).toBe('DeepSeek')
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

describe('formatDetailStatus and extractDetailError', () => {
  it('formats status for success and ok rows', () => {
    expect(formatDetailStatus(null)).toBe('—')
    expect(formatDetailStatus({ status: 'success' })).toBe('200 OK')
    expect(formatDetailStatus({ status: 'ok' })).toBe('200 OK')
  })

  it('formats HTTP status code when present in response or attempts', () => {
    expect(formatDetailStatus({ status: 'error', response: { status: 404 } })).toBe('404 Not Found')
    expect(formatDetailStatus({ status: 'error', response: { status: 429 } })).toBe('429 Too Many Requests')
    expect(formatDetailStatus({ status: 'error', response: { status: 500 } })).toBe('500 Internal Error')
    expect(formatDetailStatus({ status: 'error', response: { status: 502 } })).toBe('502 Bad Gateway')
    expect(formatDetailStatus({ status: 'error', response: { status: 504 } })).toBe('504 Gateway Timeout')
    expect(formatDetailStatus({ status: 'error', response: { status: 418 } })).toBe('418 Error')
    expect(
      formatDetailStatus({
        status: 'error',
        attempts: [{ statusCode: 200 }, { statusCode: 503 }],
      })
    ).toBe('503 Service Unavailable')
  })

  it('falls back to capitalized status when no numeric code is available', () => {
    expect(formatDetailStatus({ status: 'error' })).toBe('Error')
    expect(formatDetailStatus({ status: 'failed' })).toBe('Failed')
    expect(formatDetailStatus({})).toBe('Error')
  })

  it('extractDetailError returns null for successful requests', () => {
    expect(extractDetailError(null)).toBeNull()
    expect(extractDetailError({ status: 'success' })).toBeNull()
    expect(extractDetailError({ status: 'ok' })).toBeNull()
    expect(extractDetailError({ status: 'error' })).toBeNull()
  })

  it('extractDetailError pulls error message and status from response or attempts', () => {
    expect(
      extractDetailError({
        status: 'error',
        response: { status: 404, error: 'model not found' },
      })
    ).toEqual({ status: 404, message: 'model not found' })

    expect(
      extractDetailError({
        status: 'error',
        error: 'gateway timeout reached',
        response: { status: 504 },
      })
    ).toEqual({ status: 504, message: 'gateway timeout reached' })

    expect(
      extractDetailError({
        status: 'error',
        attempts: [{ statusCode: 429, error: 'rate limited' }],
      })
    ).toEqual({ status: 429, message: 'rate limited' })

    expect(
      extractDetailError({
        status: 'error',
        response: { status: 500, content: 'upstream raw failure' },
      })
    ).toEqual({ status: 500, message: 'upstream raw failure' })
  })
})
