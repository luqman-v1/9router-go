import { describe, expect, test } from 'bun:test'
import { buildEditedRow, extraBodyToText, pruneDisabledAfterDelete } from './customModelEdit'

const stored = {
  id: 'm1',
  name: 'My M1',
  providerAlias: 'openrouter',
  type: 'llm',
  contextWindow: 1000,
  maxOutput: 100,
  extraBody: { provider: { order: ['a'] } },
}
const form = { caps: { vision: true, reasoning: false, jailbreak: false }, contextWindow: 2000, maxOutput: 0 }

describe('customModelEdit', () => {
  test('prefill text is pretty JSON', () => {
    expect(extraBodyToText({ a: 1 })).toBe('{\n  "a": 1\n}')
    expect(extraBodyToText('{"a":1}')).toBe('{\n  "a": 1\n}')
    expect(extraBodyToText(undefined)).toBe('')
  })
  test('keeps unedited fields and stored type', () => {
    const row = buildEditedRow(stored, { ...form, extraBody: { x: 1 } })
    expect(row.name).toBe('My M1')
    expect(row.type).toBe('llm')
    expect(row.contextWindow).toBe(2000)
    expect('maxOutput' in row).toBe(false)
    expect(row.extraBody).toEqual({ x: 1 })
  })
  test('cleared extraBody sends null', () => {
    expect(buildEditedRow(stored, form).extraBody).toBeNull()
  })
  test('non-llm keeps no caps', () => {
    expect('caps' in buildEditedRow({ ...stored, type: 'tts' }, form)).toBe(false)
  })
})

describe('pruneDisabledAfterDelete', () => {
  test('drops deleted custom ids, keeps others', () => {
    expect(pruneDisabledAfterDelete(['a', 'b', 'c'], ['b'], [])).toEqual(['a', 'c'])
  })
  test('keeps an id that is also built-in', () => {
    expect(pruneDisabledAfterDelete(['a', 'b'], ['a', 'b'], ['a'])).toEqual(['a'])
  })
})
