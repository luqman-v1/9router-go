import { describe, expect, test } from 'bun:test'
import { nodeFormSeed } from './nodeFormSeed'
import type { ProviderNode } from '../../api/client'

function node(overrides: Partial<ProviderNode> = {}): ProviderNode {
  return {
    id: 'openai-compatible-chat-prod',
    type: 'openai-compatible',
    name: 'OpenAI Compatible (Prod)',
    prefix: 'oc',
    apiType: 'chat',
    baseUrl: 'https://api.example.com/v1',
    urlSuffix: 'prod',
    urlSuffixGenerated: false,
    ...overrides,
  } as ProviderNode
}

describe('nodeFormSeed', () => {
  // Issue #177: the edit modal showed an empty Prefix field (placeholder only),
  // so the user could not tell what was configured and Save stayed disabled.
  test('seeds every stored field, prefix included', () => {
    expect(nodeFormSeed(node())).toEqual({
      name: 'OpenAI Compatible (Prod)',
      prefix: 'oc',
      apiType: 'chat',
      baseUrl: 'https://api.example.com/v1',
      urlSuffix: 'prod',
    })
  })

  test('a stored prefix survives verbatim, including a multi-character one', () => {
    expect(nodeFormSeed(node({ prefix: 'my-prod-2' })).prefix).toBe('my-prod-2')
  })

  // The blank-submit contract: an unknown prefix is not invented, and the
  // field stays editable rather than silently saving an empty namespace.
  test('a node stored without a prefix opens blank rather than guessed', () => {
    expect(nodeFormSeed(node({ prefix: undefined })).prefix).toBe('')
  })

  test('a generated id tail is not offered as a suffix the user never chose', () => {
    const seed = nodeFormSeed(
      node({ urlSuffix: '0489217b', urlSuffixGenerated: true })
    )
    expect(seed.urlSuffix).toBe('')
  })

  test('a chosen suffix is seeded', () => {
    expect(nodeFormSeed(node({ urlSuffix: 'prod' })).urlSuffix).toBe('prod')
  })

  test('apiType only reads as responses when the node says so', () => {
    expect(nodeFormSeed(node({ apiType: 'responses' })).apiType).toBe('responses')
    expect(nodeFormSeed(node({ apiType: undefined })).apiType).toBe('chat')
  })

  test('the default base URL follows the node flavour when none was stored', () => {
    expect(nodeFormSeed(node({ baseUrl: undefined }), true).baseUrl).toBe(
      'https://api.anthropic.com/v1'
    )
    expect(nodeFormSeed(node({ baseUrl: undefined })).baseUrl).toBe(
      'https://api.openai.com/v1'
    )
  })
})