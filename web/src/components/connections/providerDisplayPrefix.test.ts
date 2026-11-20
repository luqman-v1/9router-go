import { describe, expect, test } from 'bun:test'
import { providerDisplayPrefix } from './types'

// The prefix a provider's model ids are published under.
//
// Issue: the provider detail view copied "opencode/nemotron-3-ultra-free"
// where the gateway serves "oc/nemotron-3-ultra-free". The copy button
// handed out an id the router does not resolve, so pasting it back into a
// client failed. The prefix has to be the one /v1/models publishes: the
// registry alias for a registered provider, the operator's prefix for a
// compatible node.

describe('providerDisplayPrefix', () => {
  test('publishes the registry alias of a registered provider', () => {
    // opencode's registry alias is "oc" (internal/providers/registry_aliases.go
    // and the upstream registry entry's alias).
    expect(providerDisplayPrefix('opencode')).toBe('oc')
    expect(providerDisplayPrefix('commandcode')).toBe('cmc')
    expect(providerDisplayPrefix('deepseek')).toBe('ds')
  })

  test('never returns the raw provider id when the provider has an alias', () => {
    // The bug in one line: the raw id was published where the gateway serves
    // the alias.
    expect(providerDisplayPrefix('opencode')).not.toBe('opencode')
  })

  test('falls back to the provider id for a provider without an alias', () => {
    expect(providerDisplayPrefix('openai')).toBe('openai')
    expect(providerDisplayPrefix('openrouter')).toBe('openrouter')
  })

  test('prefers the published prefix over the built-in table', () => {
    // The published prefix is what /v1/models serves, so it is what a copied
    // id has to be.
    expect(providerDisplayPrefix('opencode', undefined, 'oc')).toBe('oc')
  })

  test('the published prefix wins over the registry alias of the same provider', () => {
    // freebuff's upstream alias is "fb" but the gateway publishes it as
    // "freebuff"; the published value is the one that resolves.
    expect(providerDisplayPrefix('freebuff', undefined, 'freebuff')).toBe('freebuff')
  })

  test("uses a compatible node's own prefix", () => {
    expect(providerDisplayPrefix('openai-compatible-chat-abc', { prefix: 'ocu' })).toBe('ocu')
  })

  test('falls back to the node id when a node carries no prefix', () => {
    expect(providerDisplayPrefix('openai-compatible-chat-abc', {})).toBe('openai-compatible-chat-abc')
  })

  test('a node prefix wins over the registry alias of the same provider id', () => {
    // A node is addressed by its own prefix; the provider alias of the id it
    // was created under must not leak into the published model id.
    expect(providerDisplayPrefix('opencode', { prefix: 'myoc' }, 'oc')).toBe('myoc')
  })
})
