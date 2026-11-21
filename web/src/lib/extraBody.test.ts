import { describe, expect, it } from 'bun:test'
import { EXTRA_BODY_ALLOWED_KEYS, EXTRA_BODY_EXAMPLE, parseExtraBodyInput } from './extraBody'

const err = (raw: string) => parseExtraBodyInput(raw).error

describe('parseExtraBodyInput', () => {
  it('treats a blank field as no extraBody', () => {
    expect(parseExtraBodyInput('')).toEqual({ error: '' })
    expect(parseExtraBodyInput('   \n')).toEqual({ error: '' })
  })

  it('accepts the documented example', () => {
    expect(parseExtraBodyInput(` ${EXTRA_BODY_EXAMPLE} `)).toEqual({
      value: { temperature: 0.7, reasoning: { effort: 'high' } },
      error: '',
    })
  })

  it('lists 16 allowed keys', () => {
    expect(EXTRA_BODY_ALLOWED_KEYS.length).toBe(16)
  })

  it.each([
    '{"temperature":0.5}', '{"top_p":1}', '{"top_k":3}', '{"min_p":0.1}', '{"top_a":0.1}',
    '{"repetition_penalty":1.1}', '{"frequency_penalty":-1}', '{"presence_penalty":1}',
    '{"seed":5}', '{"stop":"x"}', '{"stop":["a"]}', '{"logit_bias":{"1":2}}', '{"logprobs":true}',
    '{"top_logprobs":3}', '{"max_completion_tokens":9}', '{"reasoning_effort":"low"}',
    '{"reasoning":{"effort":"max","enabled":true,"exclude":false,"summary":"auto","context":"all_turns","mode":"pro"}}',
    '{"reasoning":{"max_tokens":10}}', '{"temperature":null}', '{}',
  ])('accepts %s', (raw) => {
    expect(err(raw)).toBe('')
  })

  it.each(['[1,2]', '"x"', '1', 'null', 'true'])('refuses non-object %s', (raw) => {
    const got = parseExtraBodyInput(raw)
    expect(got.value).toBeUndefined()
    expect(got.error).toBe('Custom request body must be a JSON object.')
  })

  it('reports malformed JSON', () => {
    const got = parseExtraBodyInput('{"a":')
    expect(got.value).toBeUndefined()
    expect(got.error).toStartWith('Invalid JSON:')
  })

  it.each(['provider', 'plugins', 'model', 'messages', 'max_tokens', 'Temperature'])('names unknown key %s', (k) => {
    expect(err(`{"${k}":1}`)).toStartWith(`"${k}" is not accepted`)
  })

  it.each([
    ['temperature', '"hot"'], ['top_k', '1.5'], ['seed', '"1"'], ['stop', '1'], ['stop', '["a",1]'],
    ['logit_bias', '[1]'], ['logit_bias', '{"1":"x"}'], ['logprobs', '1'], ['reasoning_effort', '"extreme"'],
    ['reasoning', '"high"'],
  ])('rejects wrong type for %s=%s', (k, v) => {
    expect(err(`{"${k}":${v}}`)).toStartWith(`"${k}" must`)
  })

  it('validates reasoning sub-keys', () => {
    expect(err('{"reasoning":{"foo":1}}')).toStartWith('"reasoning.foo" is not accepted')
    expect(err('{"reasoning":{"effort":"x"}}')).toStartWith('"reasoning.effort" must')
    expect(err('{"reasoning":{"max_tokens":1.5}}')).toStartWith('"reasoning.max_tokens" must')
    expect(err('{"reasoning":{"enabled":"y"}}')).toStartWith('"reasoning.enabled" must')
  })

  it('rejects max_tokens together with an effort', () => {
    expect(err('{"reasoning":{"max_tokens":5,"effort":"low"}}')).toContain('cannot be combined')
    expect(err('{"reasoning":{"max_tokens":5},"reasoning_effort":"low"}')).toContain('cannot be combined')
  })
})
