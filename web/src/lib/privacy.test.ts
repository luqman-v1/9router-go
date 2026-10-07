import { describe, expect, it } from 'bun:test'
import { maskEmail, formatEmailLabel, submittedConnectionName } from './privacy'

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
    // A trailing address-like run keeps the regex from ending at the ")", so
    // this cannot pass with the replace callback doing nothing.
    expect(maskEmail('Account (test@example.com)/v2')).toBe('Account (t***t@example.com)/v2')
  })

  it('masks every address in a label, not just the first', () => {
    // The regex is global; without /g the second address would be shown in
    // the clear, which defeats the point of masking during a screen share.
    expect(maskEmail('a@x.com and b@y.com')).toBe('a***@x.com and b***@y.com')
    expect(maskEmail('a@x.com, b@y.com, c@z.com')).toBe('a***@x.com, b***@y.com, c***@z.com')
  })

  it('is idempotent, so a stored masked name is not masked twice', () => {
    expect(maskEmail(maskEmail('luqman.hakim@gmail.com'))).toBe('l***m@gmail.com')
    expect(maskEmail('l***m@gmail.com')).toBe('l***m@gmail.com')
  })

  it('formatEmailLabel respects isMasked flag', () => {
    expect(formatEmailLabel('user@gmail.com', false)).toBe('user@gmail.com')
    expect(formatEmailLabel('user@gmail.com', true)).toBe('u***r@gmail.com')
    expect(formatEmailLabel('Normal Name', true)).toBe('Normal Name')
  })
})

// A masked label shown in an edit form must never be written back as the
// connection's real name. These are the exact inputs that reach the save
// handler; each one previously had a path to persist "l***m@gmail.com".
describe('submittedConnectionName', () => {
  const original = 'luqman.hakim@gmail.com'
  const seeded = formatEmailLabel(original, true)

  it('submits nothing when a masked field is untouched', () => {
    expect(submittedConnectionName(seeded, original, seeded)).toBeUndefined()
  })

  it('submits nothing when masking is toggled off while the form is open', () => {
    // The field still shows the mask even though the store now says off, so
    // comparing against a freshly derived (unmasked) value would see a rename.
    expect(submittedConnectionName(seeded, original, seeded)).toBeUndefined()
  })

  it('submits nothing for a stored name padded with whitespace', () => {
    const padded = '  john.doe@gmail.com  '
    const paddedSeed = formatEmailLabel(padded, true)
    expect(submittedConnectionName(paddedSeed, padded, paddedSeed)).toBeUndefined()
  })

  it('submits nothing for a name that is not an email', () => {
    expect(submittedConnectionName('Prod Key 1', 'Prod Key 1', 'Prod Key 1')).toBeUndefined()
  })

  it('submits nothing for an email embedded in a longer name', () => {
    const label = 'Account (test@example.com)'
    const labelSeed = formatEmailLabel(label, true)
    expect(submittedConnectionName(labelSeed, label, labelSeed)).toBeUndefined()
  })

  it('leaves an already-masked stored name alone', () => {
    expect(submittedConnectionName('l***m@gmail.com', 'l***m@gmail.com', 'l***m@gmail.com'))
      .toBeUndefined()
  })

  it('submits a genuine rename', () => {
    expect(submittedConnectionName('Work Account', original, seeded)).toBe('Work Account')
  })

  it('submits a genuine rename typed over a masked value', () => {
    expect(submittedConnectionName('Work Account', seeded, seeded)).toBe('Work Account')
  })

  it('treats a whitespace-only edit as unchanged', () => {
    expect(submittedConnectionName('  Work Account  ', 'Work Account', 'Work Account'))
      .toBeUndefined()
  })
})
