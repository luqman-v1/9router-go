import { describe, expect, it } from 'bun:test'
import { maskEmail, formatEmailLabel } from './privacy'

describe('privacy email masking', () => {
  it('masks standard email addresses', () => {
    expect(maskEmail('luqman.hakim@gmail.com')).toBe('l***m@gmail.com')
    expect(maskEmail('john.doe@sub.example.co.uk')).toBe('j***e@sub.example.co.uk')
  })

  it('masks short username emails safely', () => {
    expect(maskEmail('a@domain.com')).toBe('a***@domain.com')
    expect(maskEmail('ab@domain.com')).toBe('a***@domain.com')
  })

  it('leaves non-email strings intact', () => {
    expect(maskEmail('My Anthropic Account')).toBe('My Anthropic Account')
    expect(maskEmail('OAuth Account Slot 1')).toBe('OAuth Account Slot 1')
    expect(maskEmail('')).toBe('')
    expect(maskEmail(null)).toBe('')
  })

  it('masks email embedded inside a display name', () => {
    expect(maskEmail('Account (test@example.com)')).toBe('Account (t***t@example.com)')
  })

  it('formatEmailLabel respects isMasked flag', () => {
    expect(formatEmailLabel('user@gmail.com', false)).toBe('user@gmail.com')
    expect(formatEmailLabel('user@gmail.com', true)).toBe('u***r@gmail.com')
    expect(formatEmailLabel('Normal Name', true)).toBe('Normal Name')
  })
})
