import assert from 'node:assert'
import { describe, it } from 'node:test'
import {
  MAX_DERIVED_PREFIX_LENGTH,
  MAX_NODE_ID_SUFFIX_LENGTH,
  slugifyProviderName,
  suggestPrefixFromName,
  suggestUrlSuffixFromName,
} from './provider-name-slug'

describe('slugifyProviderName', () => {
  const cases: Array<[string, string, string]> = [
    ['lowercases', 'OpenAI', 'openai'],
    ['replaces spaces with dashes', 'My OpenAI Prod', 'my-openai-prod'],
    ['collapses a run of separators into one dash', 'Foo   __  Bar..Baz', 'foo-bar-baz'],
    ['folds accents to their base letter', 'Café Interne', 'cafe-interne'],
    ['drops leading and trailing separators', '  -Acme-  ', 'acme'],
    ['keeps digits and inner dots turned into dashes', 'GPT 4.1 Turbo', 'gpt-4-1-turbo'],
    ['returns empty for a name with no usable characters', '  —  ', ''],
    ['returns empty for an empty name', '', ''],
  ]

  for (const [label, input, want] of cases) {
    it(label, () => {
      assert.strictEqual(slugifyProviderName(input), want)
    })
  }
})

describe('suggestPrefixFromName', () => {
  it('derives a prefix from the name', () => {
    assert.strictEqual(suggestPrefixFromName('Acme Gateway'), 'acme-gateway')
  })

  it('caps the prefix at the length the Go fill helper allows', () => {
    const prefix = suggestPrefixFromName('a'.repeat(80))
    assert.strictEqual(prefix.length, MAX_DERIVED_PREFIX_LENGTH)
  })

  it('does not leave a trailing dash after capping', () => {
    // Cuts mid-word, so the slug ends on a separator that must be trimmed.
    const prefix = suggestPrefixFromName('abcdefghijklmnopqrstuvw xyz')
    assert.ok(!prefix.endsWith('-'), `prefix ${JSON.stringify(prefix)} ends with a dash`)
  })

  it('refuses a registry alias instead of trimming it into another name', () => {
    // `anthropic` is owned; `anthropi` is not a namespace the operator asked for.
    const reserved = new Set(['anthropic', 'gh', 'oc'])
    assert.strictEqual(suggestPrefixFromName('Anthropic', reserved), '')
  })

  it('refuses a reserved slug case-insensitively through the slug, not the raw name', () => {
    const reserved = new Set(['gh'])
    assert.strictEqual(suggestPrefixFromName('GH', reserved), '')
  })

  it('returns empty for a name that slugs to nothing, so the server fill decides', () => {
    assert.strictEqual(suggestPrefixFromName('—'), '')
  })
})

describe('suggestUrlSuffixFromName', () => {
  it('derives the suffix from the name', () => {
    assert.strictEqual(suggestUrlSuffixFromName('Acme Gateway'), 'acme-gateway')
  })

  it('allows a suffix the reserved set owns, because a suffix names an id not a prefix', () => {
    // anthropic-compatible-anthropic is a legal id; only the *prefix* anthropic is refused.
    assert.strictEqual(suggestUrlSuffixFromName('Anthropic'), 'anthropic')
  })

  it('caps the suffix at the pattern limit', () => {
    const suffix = suggestUrlSuffixFromName('a'.repeat(100))
    assert.strictEqual(suffix.length, MAX_NODE_ID_SUFFIX_LENGTH)
  })

  it('satisfies the server pattern', () => {
    const pattern = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/
    for (const name of ['Acme Gateway', 'My OpenAI Prod', 'a'.repeat(100), 'GPT 4.1']) {
      const suffix = suggestUrlSuffixFromName(name)
      assert.ok(pattern.test(suffix), `${JSON.stringify(name)} -> ${JSON.stringify(suffix)} fails the suffix pattern`)
    }
  })

})
