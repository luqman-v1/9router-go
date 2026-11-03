/**
 * Minimal QR encoder (byte mode, versions 1–10, error-correction level M).
 *
 * Exists so the share modal can render a QR for a `9router://` token without
 * adding a runtime dependency to the bundle (§6.F: a new dependency has to pay
 * for itself, and a QR encoder used by one modal does not).
 *
 * Scope is deliberately narrow and stated rather than implied:
 * - byte mode only, ECC level M — what a share token needs
 * - versions 1–10, so up to 271 bytes at level M
 * - no Kanji, no structured append, no mixed modes
 *
 * A token longer than that throws; the caller falls back to "copy the token".
 * Reference: ISO/IEC 18004, and the widely used qrcodegen (Project Nayuki)
 * algorithm for the mask-penalty and Reed–Solomon steps.
 */

// ---------------------------------------------------------------------------
// Galois field arithmetic for Reed–Solomon
// ---------------------------------------------------------------------------

const GF_EXP = new Uint8Array(512)
const GF_LOG = new Uint8Array(256)
;(() => {
  let x = 1
  for (let i = 0; i < 255; i++) {
    GF_EXP[i] = x
    GF_LOG[x] = i
    x <<= 1
    if (x & 0x100) x ^= 0x11d
  }
  for (let i = 255; i < 512; i++) GF_EXP[i] = GF_EXP[i - 255]
})()

function gfMul(a: number, b: number): number {
  if (a === 0 || b === 0) return 0
  return GF_EXP[GF_LOG[a] + GF_LOG[b]]
}

/** Generator polynomial for `degree` error-correction codewords. */
function rsGeneratorPoly(degree: number): Uint8Array {
  let poly = new Uint8Array([1])
  for (let i = 0; i < degree; i++) {
    const next = new Uint8Array(poly.length + 1)
    for (let j = 0; j < poly.length; j++) {
      next[j] ^= poly[j]
      next[j + 1] ^= gfMul(poly[j], GF_EXP[i])
    }
    poly = next
  }
  return poly
}

function rsEncode(data: Uint8Array, eccLen: number): Uint8Array {
  const gen = rsGeneratorPoly(eccLen)
  const result = new Uint8Array(eccLen)
  for (const byte of data) {
    const factor = byte ^ result[0]
    result.copyWithin(0, 1)
    result[eccLen - 1] = 0
    for (let i = 0; i < eccLen; i++) result[i] ^= gfMul(gen[i + 1], factor)
  }
  return result
}

// ---------------------------------------------------------------------------
// Capacity tables (byte mode, ECC level M), versions 1–40
// ---------------------------------------------------------------------------
// Total codewords, ECC codewords per block, block count, and the first
// alignment-pattern centre, per version at ECC level M. From ISO/IEC 18004
// Table 9. Data codewords are the total minus the ECC blocks.
//
// Level M uses more than one block from version 4 on, so the data codewords are
// interleaved across blocks and each block carries its own ECC — a flat
// single-block encode produces a corrupt symbol for those versions, which is
// why the block counts live here rather than being derived.
const VERSION_M: Array<{ total: number; eccPerBlock: number; blocks: number; align: number }> = [
  { total: 26, eccPerBlock: 10, blocks: 1, align: -1 },   // v1
  { total: 44, eccPerBlock: 16, blocks: 1, align: 18 },   // v2
  { total: 70, eccPerBlock: 26, blocks: 1, align: 22 },   // v3
  { total: 100, eccPerBlock: 18, blocks: 2, align: 26 },  // v4
  { total: 134, eccPerBlock: 24, blocks: 2, align: 30 },  // v5
  { total: 172, eccPerBlock: 16, blocks: 4, align: 34 },  // v6
  { total: 196, eccPerBlock: 18, blocks: 4, align: 22 },  // v7
  { total: 242, eccPerBlock: 22, blocks: 4, align: 24 },  // v8
  { total: 292, eccPerBlock: 22, blocks: 5, align: 26 },  // v9
  { total: 346, eccPerBlock: 26, blocks: 5, align: 28 },  // v10
  { total: 404, eccPerBlock: 30, blocks: 5, align: 30 },  // v11
  { total: 466, eccPerBlock: 22, blocks: 8, align: 32 },  // v12
  { total: 532, eccPerBlock: 22, blocks: 9, align: 24 },  // v13
  { total: 581, eccPerBlock: 24, blocks: 9, align: 24 },  // v14
  { total: 655, eccPerBlock: 24, blocks: 10, align: 28 }, // v15
  { total: 733, eccPerBlock: 28, blocks: 10, align: 30 }, // v16
  { total: 815, eccPerBlock: 28, blocks: 11, align: 32 }, // v17
  { total: 901, eccPerBlock: 30, blocks: 13, align: 26 }, // v18
  { total: 991, eccPerBlock: 28, blocks: 14, align: 28 }, // v19
  { total: 1085, eccPerBlock: 28, blocks: 16, align: 30 },// v20
  { total: 1156, eccPerBlock: 28, blocks: 17, align: 28 },// v21
  { total: 1258, eccPerBlock: 30, blocks: 17, align: 30 },// v22
  { total: 1364, eccPerBlock: 30, blocks: 18, align: 30 },// v23
  { total: 1474, eccPerBlock: 30, blocks: 20, align: 32 },// v24
  { total: 1588, eccPerBlock: 26, blocks: 21, align: 34 },// v25
  { total: 1706, eccPerBlock: 28, blocks: 23, align: 26 },// v26
  { total: 1828, eccPerBlock: 30, blocks: 23, align: 26 },// v27
  { total: 1921, eccPerBlock: 30, blocks: 25, align: 30 },// v28
  { total: 2051, eccPerBlock: 30, blocks: 26, align: 30 },// v29
  { total: 2185, eccPerBlock: 30, blocks: 28, align: 34 },// v30
  { total: 2323, eccPerBlock: 30, blocks: 29, align: 34 },// v31
  { total: 2465, eccPerBlock: 30, blocks: 31, align: 34 },// v32
  { total: 2611, eccPerBlock: 30, blocks: 33, align: 34 },// v33
  { total: 2761, eccPerBlock: 30, blocks: 35, align: 34 },// v34
  { total: 2876, eccPerBlock: 30, blocks: 37, align: 34 },// v35
  { total: 3034, eccPerBlock: 30, blocks: 38, align: 34 },// v36
  { total: 3196, eccPerBlock: 30, blocks: 40, align: 34 },// v37
  { total: 3362, eccPerBlock: 30, blocks: 43, align: 34 },// v38
  { total: 3532, eccPerBlock: 30, blocks: 45, align: 34 },// v39
  { total: 3706, eccPerBlock: 30, blocks: 47, align: 34 },// v40
]

/** Max byte-mode payload per version at level M. */
/**
 * Max byte-mode payload per version at level M, minus the 4-bit mode indicator
 * and the 8/16-bit length field.
 */
const CAPACITY_M = VERSION_M.map((v) => v.total - v.eccPerBlock * v.blocks - 2)

// ---------------------------------------------------------------------------
// Bit buffer
// ---------------------------------------------------------------------------

class BitBuffer {
  bits: number[] = []
  push(value: number, length: number) {
    for (let i = length - 1; i >= 0; i--) this.bits.push((value >>> i) & 1)
  }
  get length() {
    return this.bits.length
  }
  /**
   * Packs the bits into bytes and pads to the version's DATA codeword count
   * with the standard alternating 0xEC / 0x11 bytes.
   *
   * The target is the data count, not the total: the ECC codewords are derived
   * from these bytes afterwards, so padding into them would leave the reader
   * with ECC computed over padding.
   */
  toCodewords(): number[] {
    const out: number[] = []
    for (let i = 0; i < this.bits.length; i += 8) {
      let byte = 0
      for (let j = 0; j < 8; j++) {
        const bit = i + j < this.bits.length ? this.bits[i + j] : 0
        byte = (byte << 1) | bit
      }
      out.push(byte)
    }
    const spec = VERSION_M[this.version - 1]
    const target = spec.total - spec.eccPerBlock * spec.blocks
    let padIndex = 0
    while (out.length < target) {
      out.push(padIndex % 2 === 0 ? 0xec : 0x11)
      padIndex++
    }
    return out
  }
  /** 1-based; set by encodeQR before toCodewords is called. */
  version = 1
}

// ---------------------------------------------------------------------------
// Matrix construction
// ---------------------------------------------------------------------------

/**
 * Picks the smallest version that holds `byteLength` bytes plus the header.
 *
 * The header is 4 bits of mode indicator and 8 or 16 bits of length, and the
 * length field widens at version 10 — so the payload that fits is not simply
 * the table's capacity, it is the capacity minus what the header will occupy.
 * Choosing without that subtraction picks a version one step too small, and
 * the padding then silently hides the mismatch.
 */
function chooseVersion(byteLength: number): number {
  for (let v = 0; v < VERSION_M.length; v++) {
    const version = v + 1
    const headerBytes = version >= 10 ? 3 : 2 // 4-bit mode + 8/16-bit length
    if (byteLength + headerBytes <= CAPACITY_M[v] + 2) return version
  }
  throw new Error(
    `QR: payload is ${byteLength} bytes, over the 2331-byte limit for versions 1–40 at ECC level M`
  )
}

type Matrix = { size: number; modules: (boolean | null)[][]; reserved: boolean[][] }

function newMatrix(size: number): Matrix {
  return {
    size,
    modules: Array.from({ length: size }, () => Array<boolean | null>(size).fill(null)),
    reserved: Array.from({ length: size }, () => Array<boolean>(size).fill(false)),
  }
}

function setFunction(m: Matrix, row: number, col: number, dark: boolean) {
  m.modules[row][col] = dark
  m.reserved[row][col] = true
}

function drawFinder(m: Matrix, row: number, col: number) {
  for (let r = -1; r <= 7; r++) {
    for (let c = -1; c <= 7; c++) {
      const rr = row + r
      const cc = col + c
      if (rr < 0 || cc < 0 || rr >= m.size || cc >= m.size) continue
      const inRing = r >= 0 && r <= 6 && c >= 0 && c <= 6
      const dark = inRing && (r === 0 || r === 6 || c === 0 || c === 6 || (r >= 2 && r <= 4 && c >= 2 && c <= 4))
      setFunction(m, rr, cc, dark)
    }
  }
}

function drawAlignment(m: Matrix, row: number, col: number) {
  for (let r = -2; r <= 2; r++) {
    for (let c = -2; c <= 2; c++) {
      const dark = Math.max(Math.abs(r), Math.abs(c)) !== 1
      setFunction(m, row + r, col + c, dark)
    }
  }
}

/** Timing patterns: alternating modules along row 6 and column 6. */
function drawTiming(m: Matrix) {
  for (let i = 0; i < m.size; i++) {
    setFunction(m, 6, i, i % 2 === 0)
    setFunction(m, i, 6, i % 2 === 0)
  }
}

/** The dark module, which fixes the orientation for level M. */
function drawDarkModule(m: Matrix) {
  setFunction(m, m.size - 8, 8, true)
}

/** Reserves format/version areas so data placement skips them. */
function reserveFormatAreas(m: Matrix) {
  for (let i = 0; i < 9; i++) {
    m.reserved[8][i] = true
    m.reserved[i][8] = true
  }
  for (let i = 0; i < 8; i++) {
    m.reserved[8][m.size - 1 - i] = true
    m.reserved[m.size - 1 - i][8] = true
  }
}

function buildMatrix(version: number, codewords: number[]): Matrix {
  const spec = VERSION_M[version - 1]
  const size = version * 4 + 17
  const m = newMatrix(size)
  // Order matters: the timing pattern and the format reservation both write on
  // row/column 6 and 8, which overlap the finders' separator ring. Drawing the
  // finders last means nothing can overwrite them, and the separator stays
  // light as the spec requires.
  drawTiming(m)
  drawDarkModule(m)
  reserveFormatAreas(m)
  drawFinder(m, 0, 0)
  drawFinder(m, 0, size - 7)
  drawFinder(m, size - 7, 0)
  // Alignment patterns, skipping the three finder corners.
  const positions = alignmentPositions(version, spec.align)
  for (const r of positions) {
    for (const c of positions) {
      const nearFinder =
        (r <= 8 && c <= 8) || (r <= 8 && c >= size - 9) || (r >= size - 9 && c <= 8)
      if (!nearFinder) drawAlignment(m, r, c)
    }
  }
  // Data placement: the upward/downward zigzag over two-module-wide columns.
  let bitIndex = 0
  const totalBits = codewords.length * 8
  for (let col = size - 1; col >= 1; col -= 2) {
    if (col === 6) col = 5 // skip the vertical timing column
    for (let i = 0; i < size; i++) {
      const upward = ((col + 1) & 2) === 0
      const row = upward ? size - 1 - i : i
      for (const c of [col, col - 1]) {
        if (m.reserved[row][c]) continue
        let bit = 0
        if (bitIndex < totalBits) {
          bit = (codewords[bitIndex >>> 3] >>> (7 - (bitIndex & 7))) & 1
          bitIndex++
        }
        m.modules[row][c] = bit === 1
      }
    }
  }
  applyBestMask(m)
  return m
}

function alignmentPositions(version: number, first: number): number[] {
  if (first < 0) return []
  const count = Math.floor(version / 7) + 2
  const size = version * 4 + 17
  const last = size - 7
  const step = count === 2 ? 0 : Math.ceil((last - first) / (count - 1))
  if (count === 2) return [first, last]
  const out: number[] = []
  for (let i = 0; i < count; i++) out.push(last - i * step)
  return out.reverse()
}

// ---------------------------------------------------------------------------
// Masking
// ---------------------------------------------------------------------------

const MASKS: Array<(r: number, c: number) => boolean> = [
  (r, c) => (r + c) % 2 === 0,
  (r) => r % 2 === 0,
  (_r, c) => c % 3 === 0,
  (r, c) => (r + c) % 3 === 0,
  (r, c) => (Math.floor(r / 2) + Math.floor(c / 3)) % 2 === 0,
  (r, c) => ((r * c) % 2) + ((r * c) % 3) === 0,
  (r, c) => (((r * c) % 2) + ((r * c) % 3)) % 2 === 0,
  (r, c) => (((r + c) % 2) + ((r * c) % 3)) % 2 === 0,
]

/** Format-information bit strings for the 8 masks at ECC level M. */
const FORMAT_M = [0x5412, 0x5125, 0x5e7c, 0x5b4b, 0x45f9, 0x40ce, 0x4f97, 0x4aa5]

function applyBestMask(m: Matrix) {
  let best: Matrix | null = null
  let bestScore = -1
  for (let mask = 0; mask < 8; mask++) {
    const candidate = cloneMatrix(m)
    applyMask(candidate, mask)
    writeFormatInfo(candidate, mask)
    const score = penalty(candidate)
    if (score < bestScore || bestScore < 0) {
      bestScore = score
      best = candidate
    }
  }
  if (!best) return
  m.modules = best.modules
}

function cloneMatrix(m: Matrix): Matrix {
  return {
    size: m.size,
    modules: m.modules.map((row) => [...row]),
    reserved: m.reserved,
  }
}

function applyMask(m: Matrix, mask: number) {
  const fn = MASKS[mask]
  for (let r = 0; r < m.size; r++) {
    for (let c = 0; c < m.size; c++) {
      if (m.reserved[r][c]) continue
      if (fn(r, c)) m.modules[r][c] = !m.modules[r][c]
    }
  }
}

function writeFormatInfo(m: Matrix, mask: number) {
  const bits = FORMAT_M[mask]
  for (let i = 0; i < 15; i++) {
    const dark = ((bits >>> i) & 1) === 1
    // Around the top-left finder.
    if (i < 6) m.modules[8][i] = dark
    else if (i < 8) m.modules[8][i + 1] = dark
    else if (i === 8) m.modules[7][8] = dark
    else m.modules[14 - i][8] = dark
    // The duplicate copy near the other two finders.
    if (i < 8) m.modules[m.size - 1 - i][8] = dark
    else m.modules[8][m.size - 15 + i] = dark
  }
}

/** The four penalty rules from the spec, summed. */
function penalty(m: Matrix): number {
  let score = 0
  const g = m.modules
  // Rule 1: runs of five or more same-colour modules in a line.
  for (let r = 0; r < m.size; r++) {
    for (let horizontal of [true, false]) {
      let run = 1
      for (let c = 1; c < m.size; c++) {
        const a = horizontal ? g[r][c - 1] : g[c - 1][r]
        const b = horizontal ? g[r][c] : g[c][r]
        if (a === b) {
          run++
          if (run === 5) score += 3
          else if (run > 5) score += 1
        } else {
          run = 1
        }
      }
    }
  }
  // Rule 2: 2×2 blocks of one colour.
  for (let r = 0; r < m.size - 1; r++) {
    for (let c = 0; c < m.size - 1; c++) {
      const v = g[r][c]
      if (v === g[r][c + 1] && v === g[r + 1][c] && v === g[r + 1][c + 1]) score += 3
    }
  }
  // Rule 3: the 1:1:3:1:1 finder-like pattern with four light modules either side.
  const pattern = [true, false, true, true, true, false, true]
  for (let r = 0; r < m.size; r++) {
    for (let c = 0; c <= m.size - 7; c++) {
      let match = true
      for (let i = 0; i < 7; i++) {
        if (g[r][c + i] !== pattern[i]) { match = false; break }
      }
      if (!match) continue
      // The pattern counts only with four light modules either side; the guards
      // keep the look-behind/ahead inside the matrix.
      const lightBefore = c >= 4 && g[r][c - 1] === false && g[r][c - 2] === false && g[r][c - 3] === false && g[r][c - 4] === false
      const lightAfter = c <= m.size - 11 && g[r][c + 7] === false && g[r][c + 8] === false && g[r][c + 9] === false && g[r][c + 10] === false
      if (lightBefore || lightAfter) score += 40
    }
  }
  // Rule 4: dark-module proportion away from 50%.
  let dark = 0
  for (let r = 0; r < m.size; r++) for (let c = 0; c < m.size; c++) if (g[r][c]) dark++
  const percent = (dark * 100) / (m.size * m.size)
  score += Math.floor(Math.abs(percent - 50) / 5) * 10
  return score
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/** Encodes `text` as a QR matrix of booleans (true = dark). */
export function encodeQR(text: string): boolean[][] {
  const bytes = new TextEncoder().encode(text)
  const version = chooseVersion(bytes.length)
  const spec = VERSION_M[version - 1]

  const buffer = new BitBuffer()
  buffer.version = version
  buffer.push(4, 4) // byte mode
  buffer.push(bytes.length, version >= 10 ? 16 : 8)
  for (const b of bytes) buffer.push(b, 8)
  buffer.push(0, 4) // terminator

  const dataCount = spec.total - spec.eccPerBlock * spec.blocks
  const dataCodewords = buffer.toCodewords().slice(0, dataCount)

  // Split into blocks, ECC each, then interleave: all first codewords, then all
  // second codewords, and so on — the same for data and for ECC. A flat
  // concatenation is only correct for the single-block versions (1–3); from
  // version 4 the reader expects the interleaved order, and a symbol built any
  // other way decodes to garbage.
  const perBlock = Math.floor(dataCodewords.length / spec.blocks)
  const dataBlocks: number[][] = []
  for (let b = 0; b < spec.blocks; b++) {
    const start = b * perBlock
    dataBlocks.push(dataCodewords.slice(start, start + perBlock))
  }
  const eccBlocks = dataBlocks.map((block) => Array.from(rsEncode(Uint8Array.from(block), spec.eccPerBlock)))

  const interleaved: number[] = []
  const maxData = Math.max(...dataBlocks.map((b) => b.length))
  for (let i = 0; i < maxData; i++) for (const block of dataBlocks) if (i < block.length) interleaved.push(block[i])
  for (let i = 0; i < spec.eccPerBlock; i++) for (const block of eccBlocks) interleaved.push(block[i])
  const codewords = interleaved

  const m = buildMatrix(version, codewords)
  return m.modules.map((row) => row.map((v) => v === true))
}

/** Encodes `text` and renders it as a PNG data URL, with a quiet zone. */
export async function renderQRToDataURL(text: string, scale = 4): Promise<string> {
  const matrix = encodeQR(text)
  const quiet = 4
  const size = matrix.length + quiet * 2
  const canvas = document.createElement('canvas')
  canvas.width = size * scale
  canvas.height = size * scale
  const ctx = canvas.getContext('2d')
  if (!ctx) throw new Error('QR: no 2D canvas context')
  // Light background first: the quiet zone must be light for the code to scan.
  ctx.fillStyle = '#ffffff'
  ctx.fillRect(0, 0, canvas.width, canvas.height)
  ctx.fillStyle = '#000000'
  for (let r = 0; r < matrix.length; r++) {
    for (let c = 0; c < matrix.length; c++) {
      if (matrix[r][c]) ctx.fillRect((c + quiet) * scale, (r + quiet) * scale, scale, scale)
    }
  }
  return canvas.toDataURL('image/png')
}
