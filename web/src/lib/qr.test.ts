import { describe, expect, it } from 'bun:test'
import { encodeQR } from './qr'

/** Reads a module out of a matrix. */
function at(m: boolean[][], r: number, c: number): boolean {
  return m[r][c] === true
}

/**
 * Counts the dark modules in the three finder patterns.
 *
 * One 7×7 finder holds 33 dark modules — the outer ring (24), the inner ring
 * (16) and the 3×3 core (9) — so three of them hold 99. The number is measured
 * from the encoder's own output rather than assumed, because an invented figure
 * passes for a spec and then fails against the real symbol.
 */
function finderDarkCount(m: boolean[][]): number {
  let n = 0
  const corners: Array<[number, number]> = [
    [0, 0],
    [0, m.length - 7],
    [m.length - 7, 0],
  ]
  for (const [r0, c0] of corners) {
    for (let r = 0; r < 7; r++) for (let c = 0; c < 7; c++) if (at(m, r0 + r, c0 + c)) n++
  }
  return n
}

describe('encodeQR', () => {
  it('produces a square matrix for a short token', () => {
    const m = encodeQR('9router://abc')
    expect(m.length).toBe(m[0].length)
    // Version 1 holds 16 bytes at level M; "9router://abc" is 13.
    expect(m.length).toBe(21)
  })

  it('draws all three finder patterns', () => {
    const m = encodeQR('9router://abc')
    expect(finderDarkCount(m)).toBe(3 * 33)
  })

  it('draws the finder centres dark and their rings light', () => {
    const m = encodeQR('9router://abc')
    // Top-left finder: dark outer ring, light separator, dark inner ring,
    // light separator, dark 3×3 core.
    expect(at(m, 0, 0)).toBe(true)
    expect(at(m, 1, 1)).toBe(false)
    expect(at(m, 2, 2)).toBe(true)
    expect(at(m, 3, 3)).toBe(true)
    expect(at(m, 5, 5)).toBe(false)
    // Row 6 inside the finder (columns 0–6) is its outer ring, so it is dark;
    // the light separator is row 7, not row 6. Asserting row 6 light here is the
    // mistake that made this test fail against a correct encoder.
    expect(at(m, 6, 0)).toBe(true)
    expect(at(m, 7, 1)).toBe(false)
    // The timing line resumes past the separator and alternates.
    expect(at(m, 6, 8)).toBe(true)
    expect(at(m, 6, 10)).toBe(true)
    // The timing line itself starts at column 8 (column 6–7 are the separator
    // and the format area) and alternates.
    expect(at(m, 6, 8)).toBe(true)
    expect(at(m, 6, 9)).toBe(false)
  })

  it('draws the timing pattern as alternating modules', () => {
    const m = encodeQR('9router://abc')
    for (let c = 8; c < m.length - 8; c++) expect(at(m, 6, c)).toBe(c % 2 === 0)
  })

  it('scales to a bigger version for a longer payload', () => {
    const short = encodeQR('x'.repeat(20))
    const long = encodeQR('x'.repeat(120))
    expect(long.length).toBeGreaterThan(short.length)
  })

  it('picks the smallest version that fits, header included', () => {
    // size = version*4 + 17, so the assertions below pin the version directly.
    const sizeOf = (bytes: number) => encodeQR('x'.repeat(bytes)).length
    expect(sizeOf(13)).toBe(21)  // v1: 14 data codewords + 2 header
    expect(sizeOf(20)).toBe(25)  // v2
    expect(sizeOf(60)).toBe(33)  // v4 — first multi-block version at level M
    expect(sizeOf(220)).toBe(61) // v11 — past the 8-bit length field
  })

  it('crosses the block boundary without corrupting the finders', () => {
    // Version 4 is the first multi-block version at level M, so a payload past
    // the v3 ceiling exercises the interleaving path.
    const v4 = encodeQR('x'.repeat(60))
    expect(v4.length).toBe(33)
    expect(finderDarkCount(v4)).toBe(3 * 33)
  })

  it('handles a payload that needs version 10 or later', () => {
    const m = encodeQR('x'.repeat(220))
    expect(m.length).toBe(61)
    expect(finderDarkCount(m)).toBe(3 * 33)
  })

  it('handles a realistic 9router:// token', () => {
    // A token for two providers, the size the share modal actually produces.
    const token = '9router://' + btoa(JSON.stringify([
      { prov: 'openai-compatible-chat-alpha', url: 'https://alpha.example.com/v1', api: [], keys: [], models: 'auto', prefix: 'alpha', name: 'Alpha Provider', note: '', type: 'openai-compatible', apiType: 'chat' },
      { prov: 'anthropic-compatible-delta', url: 'https://delta.example.com/v1', api: [], keys: [], models: 'ask', prefix: 'delta', name: 'Delta Provider', note: '', type: 'anthropic-compatible', apiType: '' },
    ])).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
    const m = encodeQR(token)
    expect(m.length).toBe(93) // v19: a 558-byte token
    expect(finderDarkCount(m)).toBe(3 * 33)
  })

  it('rejects a payload over the version-40 limit', () => {
    // 2331 bytes is the level-M ceiling for versions 1–40.
    expect(() => encodeQR('x'.repeat(2332))).toThrow(/over the 2331-byte limit/)
  })

  it('is deterministic for the same input', () => {
    const a = encodeQR('9router://same').map((r) => r.join(''))
    const b = encodeQR('9router://same').map((r) => r.join(''))
    expect(a).toEqual(b)
  })

  it('produces a different matrix for different input', () => {
    const a = encodeQR('9router://one').map((r) => r.join(''))
    const b = encodeQR('9router://two').map((r) => r.join(''))
    expect(a).not.toEqual(b)
  })
})
