import { test } from 'node:test'
import assert from 'node:assert'

import {
  JAILBREAK_KEYWORD_PARTIAL,
  JAILBREAK_KEYWORD_REGEX,
  JAILBREAK_KEYWORD_WORD,
  MAX_JAILBREAK_KEYWORDS,
  matchJailbreakKeyword,
  matchesAnyJailbreakKeyword,
  normalizeJailbreakKeywords,
} from './jailbreakKeywords'

test('word mode matches a whole token across separators', () => {
  const rule = { text: 'deepseek', mode: JAILBREAK_KEYWORD_WORD }
  assert.equal(matchJailbreakKeyword('providerx/deepseek-4-pro', rule), true)
  assert.equal(matchJailbreakKeyword('deepseek-v4', rule), true)
  assert.equal(matchJailbreakKeyword('deepseek', rule), true)
  assert.equal(matchJailbreakKeyword('providerx:deepseek', rule), true)
})

test('word mode rejects a substring that is not a whole token', () => {
  const rule = { text: 'deepseek', mode: JAILBREAK_KEYWORD_WORD }
  assert.equal(matchJailbreakKeyword('deepseekia', rule), false)
  assert.equal(matchJailbreakKeyword('notdeepseek', rule), false)
  assert.equal(matchJailbreakKeyword('seek', rule), false)
})

test('word mode is case-insensitive on both sides', () => {
  assert.equal(matchJailbreakKeyword('providerx/DeepSeek-4-Pro', { text: 'deepseek' }), true)
  assert.equal(matchJailbreakKeyword('providerx/deepseek-4-pro', { text: 'DeepSeek' }), true)
})

test('partial mode matches any substring', () => {
  const rule = { text: 'seek', mode: JAILBREAK_KEYWORD_PARTIAL }
  assert.equal(matchJailbreakKeyword('deepseek', rule), true)
  assert.equal(matchJailbreakKeyword('DEEPSEEK', rule), true)
  assert.equal(matchJailbreakKeyword('gpt-4o', rule), false)
})

test('regex mode matches unanchored and case-insensitively', () => {
  const rule = { text: '^deepseek', mode: JAILBREAK_KEYWORD_REGEX }
  assert.equal(matchJailbreakKeyword('deepseek-v4', rule), true)
  assert.equal(matchJailbreakKeyword('providerx/deepseek', rule), false)
  assert.equal(matchJailbreakKeyword('DeepSeek-V4', rule), true)
})

test('regex mode honours the (?-i) opt-out', () => {
  const rule = { text: '(?-i)Deepseek', mode: JAILBREAK_KEYWORD_REGEX }
  assert.equal(matchJailbreakKeyword('Deepseek-v4', rule), true)
  assert.equal(matchJailbreakKeyword('deepseek-v4', rule), false)
})

test('an empty rule or id never matches', () => {
  assert.equal(matchJailbreakKeyword('', { text: 'deepseek' }), false)
  assert.equal(matchJailbreakKeyword('deepseek', { text: '' }), false)
  assert.equal(matchJailbreakKeyword('deepseek', { text: '   ' }), false)
})

test('matchesAny reports across a mixed list', () => {
  const rules = [
    { text: 'gpt', mode: JAILBREAK_KEYWORD_WORD },
    { text: 'seek', mode: JAILBREAK_KEYWORD_PARTIAL },
  ]
  assert.equal(matchesAnyJailbreakKeyword('providerx/deepseek-4-pro', rules), true)
  assert.equal(matchesAnyJailbreakKeyword('openai/gpt-4o', rules), true)
  assert.equal(matchesAnyJailbreakKeyword('anthropic/claude-3', rules), false)
})

test('normalize lowercases word and partial but keeps a regex verbatim', () => {
  const result = normalizeJailbreakKeywords([
    { text: '  DeepSeek ' },
    { text: 'SEEK', mode: JAILBREAK_KEYWORD_PARTIAL },
    { text: '^Deep\\Seek$', mode: JAILBREAK_KEYWORD_REGEX },
  ])
  assert.equal(result.ok, true)
  if (!result.ok) return
  assert.deepEqual(result.rules, [
    { text: 'deepseek', mode: JAILBREAK_KEYWORD_WORD },
    { text: 'seek', mode: JAILBREAK_KEYWORD_PARTIAL },
    { text: '^Deep\\Seek$', mode: JAILBREAK_KEYWORD_REGEX },
  ])
})

test('normalize drops duplicates but keeps the first mode', () => {
  const result = normalizeJailbreakKeywords([
    { text: 'deepseek' },
    { text: 'DeepSeek' },
    { text: 'seek', mode: JAILBREAK_KEYWORD_PARTIAL },
    { text: 'seek', mode: JAILBREAK_KEYWORD_PARTIAL },
  ])
  assert.equal(result.ok, true)
  if (!result.ok) return
  assert.equal(result.rules.length, 2)
})

test('normalize rejects what the gateway rejects', () => {
  const empty = normalizeJailbreakKeywords([{ text: '   ' }])
  assert.equal(empty.ok, false)
  if (empty.ok) return
  assert.match(empty.error, /empty/)

  const badRegex = normalizeJailbreakKeywords([{ text: '(unclosed', mode: JAILBREAK_KEYWORD_REGEX }])
  assert.equal(badRegex.ok, false)
  if (badRegex.ok) return
  assert.match(badRegex.error, /invalid regex/)

  const badMode = normalizeJailbreakKeywords([{ text: 'deepseek', mode: 'fuzzy' }])
  assert.equal(badMode.ok, false)
  if (badMode.ok) return
  assert.match(badMode.error, /unknown mode/)

  const tooLong = normalizeJailbreakKeywords([{ text: 'a'.repeat(201) }])
  assert.equal(tooLong.ok, false)
  if (tooLong.ok) return
  assert.match(tooLong.error, /too long/)

  const tooMany = normalizeJailbreakKeywords(
    Array.from({ length: MAX_JAILBREAK_KEYWORDS + 1 }, (_, i) => ({ text: `rule-${i}` })),
  )
  assert.equal(tooMany.ok, false)
  if (tooMany.ok) return
  assert.match(tooMany.error, /too many/)
})

test('normalize accepts an empty list', () => {
  const result = normalizeJailbreakKeywords([])
  assert.equal(result.ok, true)
  if (!result.ok) return
  assert.deepEqual(result.rules, [])
})
