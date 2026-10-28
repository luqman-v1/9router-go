/**
 * Pre-compresses the built SPA assets.
 *
 * Why this exists
 * ---------------
 * The dashboard ships inside the gateway binary and is served from the gateway
 * itself, so every byte of the bundle is transferred over the operator's own
 * link — often a phone on mobile data. The bundle was served uncompressed:
 * a 1.2 MB JavaScript file and a 114 kB stylesheet went over the wire as-is,
 * because the embedded file handler has no compression middleware (the
 * gateway's data plane streams LLM responses, and a buffering compressor on
 * that path would be the wrong trade).
 *
 * Compressing at request time would fix the transfer but pay the CPU cost on
 * every request, on a 1 vCPU box, for a file that never changes. So the
 * compression happens once, at build time, and the result is embedded next to
 * the original. The Go handler then picks the best variant the client's
 * `Accept-Encoding` allows and serves it with the matching `Content-Encoding`.
 *
 * Measured on the current bundle: 1 200 kB → 226 kB (brotli) / 305 kB
 * (gzip) for the script, 115 kB → 15 kB / 18 kB for the stylesheet.
 *
 * Fonts are already compressed (woff2 is deflate-based) and images are
 * already compressed (png), so both are left alone — compressing them again
 * would cost CPU and save nothing.
 */

import { readdirSync, readFileSync, unlinkSync, writeFileSync } from 'node:fs'
import { extname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { brotliCompressSync, constants, gzipSync } from 'node:zlib'

const webRoot = join(fileURLToPath(import.meta.url), '..', '..')
const distRoot = join(webRoot, 'dist')

/**
 * Extensions worth compressing: text that compresses well.
 *
 * Anything not listed is left alone. The list is intentionally short rather
 * than "everything but images": a file that does not compress well (png, woff2,
 * ico) would gain nothing and would still cost the CPU at build time.
 */
const COMPRESSIBLE: Record<string, true> = {
  '.css': true,
  '.html': true,
  '.js': true,
  '.json': true,
  '.map': true,
  '.svg': true,
  '.txt': true,
  '.webmanifest': true,
}

/** Brotli quality: 11 is the best ratio and the build pays for it once. */
const BROTLI_QUALITY = 11

/** Files smaller than this are not worth a variant: the headers cost more. */
const MIN_COMPRESS_BYTES = 512

/**
 * Removes the variants of a previous build.
 *
 * The build clears `dist` on its own, but a stale `.br` for a file that no
 * longer exists would be embedded and served for a path that has no original
 * any more — a 404 with a body that is not a 404.
 */
function clearVariants(files: string[]): void {
  for (const file of files) {
    if (file.endsWith('.br') || file.endsWith('.gz')) {
      try {
        unlinkSync(file)
      } catch {
        // Already gone: nothing to clear.
      }
    }
  }
}

/**
 * Every file under `dir`, recursively.
 */
function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) walk(full, out)
    else if (entry.isFile()) out.push(full)
  }
  return out
}

export function precompress(distRootPath: string = distRoot): void {
  const files = walk(distRootPath)
  clearVariants(files)

  let rawTotal = 0
  let brTotal = 0
  let gzTotal = 0

  for (const file of files) {
    if (!COMPRESSIBLE[extname(file).toLowerCase()]) continue

    const raw = readFileSync(file)
    // A file this small does not gain enough from compression to be worth
    // the variant: the headers it saves are worth more than the bytes it
    // saves, and every variant is another file in the binary.
    if (raw.length < MIN_COMPRESS_BYTES) continue

    const br = brotliCompressSync(raw, {
      params: {
        [constants.BROTLI_PARAM_QUALITY]: BROTLI_QUALITY,
        [constants.BROTLI_PARAM_SIZE_HINT]: raw.length,
      },
    })
    const gz = gzipSync(raw, { level: 9 })

    // A variant that is not smaller than the original is pure loss: the
    // handler prefers the compressed variant whenever the client accepts it,
    // so the client would download more bytes than the identity body, and
    // the extra bytes would live in the binary forever. A file that does not
    // compress — already-compressed content behind a compressible extension,
    // or a short random blob — hits this.
    //
    // The comparison is per variant, not per file. The two encoders do not
    // agree on which is smaller for every input: brotli's window is 4 MiB and
    // gzip's is 32 KiB, so a file whose redundancy sits farther apart than
    // 32 KiB compresses under brotli and not under gzip. A file-level
    // "both are too big" test would write that file's too-large gzip variant
    // anyway, and a client that accepts gzip would download more than the
    // identity body. So each variant is written only when it is smaller.
    const brSmaller = br.length < raw.length
    const gzSmaller = gz.length < raw.length
    if (!brSmaller && !gzSmaller) {
      console.log(`  skipped ${file.slice(distRootPath.length + 1)} — the compressed variant is not smaller`)
      continue
    }

    if (brSmaller) {
      writeFileSync(`${file}.br`, br)
      brTotal += br.length
    }
    if (gzSmaller) {
      writeFileSync(`${file}.gz`, gz)
      gzTotal += gz.length
    }

    rawTotal += raw.length
  }

  if (rawTotal === 0) return
  const kb = (n: number) => `${(n / 1024).toFixed(0)} KB`
  console.log(
    `Pre-compressed assets: ${kb(rawTotal)} → ${kb(brTotal)} (brotli) / ${kb(gzTotal)} (gzip)`
  )
}

if (import.meta.main) precompress()
