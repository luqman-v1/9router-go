import { describe, expect, test } from 'bun:test'
import {
  headerNameError,
  headerValueError,
  MAX_OVERRIDE_VALUE_LENGTH,
} from './headerOverrideValidation'

describe('headerNameError', () => {
  test('accepts a name built from the RFC 7230 token punctuation', () => {
    expect(headerNameError("X-!#$%&'*+-.^_`|~")).toBe('')
    expect(headerNameError('X-Custom-Header')).toBe('')
    expect(headerNameError('x-token')).toBe('')
  })

  test('rejects a name with a space', () => {
    expect(headerNameError('X Custom')).not.toBe('')
  })

  test('rejects a name with a colon', () => {
    expect(headerNameError('X-Token: v')).not.toBe('')
  })

  test('rejects a name with a parenthesis', () => {
    expect(headerNameError('X-Token(v2)')).not.toBe('')
  })

  test('rejects a name with a non-ASCII letter', () => {
    expect(headerNameError('X-Tökén')).not.toBe('')
  })

  test('an empty name is a row being typed, not an error', () => {
    expect(headerNameError('')).toBe('')
    expect(headerNameError('   ')).toBe('')
  })
})

describe('headerValueError', () => {
  test('rejects a value carrying CR', () => {
    expect(headerValueError('a\r b')).not.toBe('')
  })

  test('rejects a value carrying LF', () => {
    expect(headerValueError('a\nb')).not.toBe('')
  })

  test('rejects a value carrying NUL', () => {
    expect(headerValueError('a\u0000b')).not.toBe('')
  })

  test('rejects a value carrying DEL', () => {
    expect(headerValueError('a\u007fb')).not.toBe('')
  })

  test('rejects the other C0 controls too', () => {
    expect(headerValueError('a\u0001b')).not.toBe('')
  })

  test('accepts a value carrying TAB', () => {
    expect(headerValueError('a\tb')).toBe('')
  })

  test('accepts an ordinary value', () => {
    expect(headerValueError('Bearer sk-abc123')).toBe('')
  })

  test('rejects a value longer than the cap', () => {
    expect(headerValueError('a'.repeat(MAX_OVERRIDE_VALUE_LENGTH))).toBe('')
    expect(headerValueError('a'.repeat(MAX_OVERRIDE_VALUE_LENGTH + 1))).not.toBe('')
  })

  // The cap is measured in UTF-8 bytes, the unit the server counts in
  // (Go's `len(value)`). `value.length` counts UTF-16 code units, so a
  // non-ASCII value is shorter in code units than in bytes — the check
  // below would have accepted every one of these values, and the server
  // would have answered 400.
  test('measures a multi-byte value in UTF-8 bytes, not UTF-16 code units', () => {
    // 5000 "é" = 5000 code units = 10000 bytes
    expect('é'.repeat(5000).length).toBe(5000)
    expect(headerValueError('é'.repeat(5000))).not.toBe('')

    // 4096 "é" = 4096 code units = 8192 bytes — exactly at the cap
    expect('é'.repeat(4096).length).toBe(4096)
    expect(headerValueError('é'.repeat(4096))).toBe('')
  })

  test('measures an emoji value in UTF-8 bytes, not UTF-16 code units', () => {
    // 2000 "😀" = 4000 UTF-16 code units = 8000 bytes
    expect('😀'.repeat(2000).length).toBe(4000)
    expect(headerValueError('😀'.repeat(2000))).toBe('')

    // 3000 "😀" = 6000 UTF-16 code units = 12000 bytes
    expect('😀'.repeat(3000).length).toBe(6000)
    expect(headerValueError('😀'.repeat(3000))).not.toBe('')
  })

  test('the length message counts bytes', () => {
    expect(headerValueError('a'.repeat(MAX_OVERRIDE_VALUE_LENGTH + 1))).toBe(
      `Max ${MAX_OVERRIDE_VALUE_LENGTH} bytes.`,
    )
  })
})
