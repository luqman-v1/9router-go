/**
 * The pre-compression is what makes the dashboard open quickly, and the
 * handler prefers a compressed variant whenever the client accepts one. That
 * makes two properties load-bearing, and neither is visible in the build
 * output:
 *
 *   1. A variant must exist for every file worth compressing. Without it the
 *      dashboard is served uncompressed, which is correct but slow — and
 *      nothing fails, because the handler falls back to the original.
 *   2. A variant must never be *larger* than the original. The handler picks
 *      the compressed variant whenever the client accepts it, so a variant
 *      that grew would make the client download more bytes than the identity
 *      body, and the extra bytes would sit in the binary forever.
 *
 * The second one is the trap: a file that does not compress (already
 * compressed content behind a compressible extension) produces a variant that
 * is larger, and the build would ship it and the handler would prefer it.
 */
import { randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { afterEach, describe, expect, it } from 'bun:test'

import { precompress } from './precompress'

let tmp: string | undefined

afterEach(() => {
  if (tmp) rmSync(tmp, { recursive: true, force: true })
  tmp = undefined
})

/** A scratch dist tree. */
function scaffold(): string {
  tmp = mkdtempSync(join(tmpdir(), 'precompress-'))
  mkdirSync(join(tmp, 'assets'), { recursive: true })
  return tmp
}

/** Bytes that do not compress: random data has no pattern to exploit. */
function incompressible(size: number): Buffer {
  return randomBytes(size)
}

describe('precompress', () => {
  it('writes a brotli and a gzip variant for a compressible file', () => {
    const root = scaffold()
    writeFileSync(join(root, 'assets', 'app.js'), 'const x = 1;\n'.repeat(200))

    precompress(root)

    expect(existsSync(join(root, 'assets', 'app.js.br'))).toBe(true)
    expect(existsSync(join(root, 'assets', 'app.js.gz'))).toBe(true)
  })

  it('leaves a file that does not compress without a variant', () => {
    const root = scaffold()
    writeFileSync(join(root, 'assets', 'blob.json'), incompressible(4096))

    precompress(root)

    // The variant would be larger than the original, so it must not exist:
    // the handler prefers it whenever the client accepts the encoding, and a
    // larger variant means a larger download.
    expect(existsSync(join(root, 'assets', 'blob.json.br'))).toBe(false)
    expect(existsSync(join(root, 'assets', 'blob.json.gz'))).toBe(false)
  })

  it('never writes a variant that is larger than the original', () => {
    const root = scaffold()
    writeFileSync(join(root, 'assets', 'app.js'), 'const x = 1;\n'.repeat(200))
    writeFileSync(join(root, 'assets', 'noise.json'), incompressible(4096))

    precompress(root)

    for (const name of ['app.js', 'noise.json']) {
      const original = readFileSync(join(root, 'assets', name))
      for (const ext of ['.br', '.gz']) {
        const variantPath = join(root, 'assets', name + ext)
        if (!existsSync(variantPath)) continue
        const variant = readFileSync(variantPath)
        expect(variant.length).toBeLessThan(original.length)
      }
    }
  })

  it('never writes a variant that is larger, when only one encoder is larger', () => {
    const root = scaffold()
    // gzip's window is 32 KiB and brotli's is 4 MiB, so a file whose
    // redundancy sits farther apart than 32 KiB compresses under brotli and
    // NOT under gzip. The per-file test above cannot produce this case: its
    // fixture is either compressible by both or by neither, so the two
    // encoders always agree on the direction. Here they disagree, and the
    // gzip variant is the one that grew.
    const period = 100 * 1024
    const pat = randomBytes(period)
    const reps = Math.ceil((2 * 1024 * 1024) / period)
    writeFileSync(join(root, 'assets', 'blob.js'), Buffer.concat(Array.from({ length: reps }, () => pat)))

    precompress(root)

    const original = readFileSync(join(root, 'assets', 'blob.js')).length
    for (const ext of ['.br', '.gz']) {
      const variantPath = join(root, 'assets', 'blob.js' + ext)
      if (!existsSync(variantPath)) continue
      const variant = readFileSync(variantPath).length
      expect(variant).toBeLessThan(original)
    }
  })

  it('leaves a file that is already compressed alone', () => {
    const root = scaffold()
    // A woff2 is not in the compressible list: it is already deflate-based,
    // so compressing it again would cost CPU and save nothing.
    writeFileSync(join(root, 'assets', 'font.woff2'), incompressible(2048))

    precompress(root)

    expect(existsSync(join(root, 'assets', 'font.woff2.br'))).toBe(false)
    expect(existsSync(join(root, 'assets', 'font.woff2.gz'))).toBe(false)
  })

  it('removes the variants of a previous build', () => {
    const root = scaffold()
    writeFileSync(join(root, 'assets', 'app.js'), 'const x = 1;\n'.repeat(200))
    precompress(root)
    expect(existsSync(join(root, 'assets', 'app.js.br'))).toBe(true)

    // The file is gone in the next build; its variant must not survive, or
    // the gateway serves a variant for a path that has no original.
    rmSync(join(root, 'assets', 'app.js'))
    writeFileSync(join(root, 'assets', 'other.js'), 'const y = 2;\n'.repeat(200))
    precompress(root)

    expect(existsSync(join(root, 'assets', 'app.js.br'))).toBe(false)
    expect(existsSync(join(root, 'assets', 'app.js.gz'))).toBe(false)
  })
})
