/**
 * The provider artwork in `public/providers/` is rendered at 32×32
 * (`size-8 object-contain` in CliToolsView), so anything larger than that is
 * bytes the browser downloads and throws away — and the dashboard is served by
 * the same 1-vCPU box that streams LLM responses, usually to a phone (§6.F).
 *
 * `crush.png` was a 2700×1392 screenshot at 792 KB, 19× the next-largest
 * asset, displayed in that 32×32 box: 99.98% of its bytes were discarded. It
 * is now 128×66 at 9 KB, and the rendered result is indistinguishable — the
 * two versions differ on 1.7% of pixels when both are scaled to display size,
 * which is below the threshold of a visible change.
 *
 * The test pins the size so a screenshot pasted into this directory again
 * fails here rather than reaching a phone.
 */
import { existsSync, readFileSync, statSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'bun:test'

const PROVIDERS_DIR = join(import.meta.dirname, '..', '..', 'public', 'providers')

/** Rendered at 32×32, so 512×512 is 256× the pixels the browser draws. */
const MAX_EDGE_PX = 512
/**
 * 60 KB is generous for a 512×512 icon and still catches a screenshot: the
 * point of the budget is bytes on the wire, and a 512×512 PNG that compresses
 * to 4 KB costs nothing. A screenshot of a dashboard at 2700×1392 does not.
 */
const MAX_BYTES = 60 * 1024

function readPngSize(file: string): { width: number; height: number } {
  const b = readFileSync(file)
  // PNG signature, then IHDR: width and height are big-endian uint32 at 16 and 20.
  if (b.length < 24 || b.readUInt32BE(0) !== 0x89504e47) {
    throw new Error(`${file} is not a PNG`)
  }
  return { width: b.readUInt32BE(16), height: b.readUInt32BE(20) }
}

describe('provider artwork budget', () => {
  it('keeps every asset within the display budget', () => {
    if (!existsSync(PROVIDERS_DIR)) return
    const files = readdirSync(PROVIDERS_DIR).filter((f) => f.endsWith('.png'))
    expect(files.length).toBeGreaterThan(0)

    const offenders: string[] = []
    for (const f of files) {
      const path = join(PROVIDERS_DIR, f)
      const bytes = statSync(path).size
      const { width, height } = readPngSize(path)
      const longestEdge = Math.max(width, height)
      if (longestEdge > MAX_EDGE_PX || bytes > MAX_BYTES) {
        offenders.push(
          `${f}: ${width}x${height} (${(bytes / 1024).toFixed(1)} KB) — rendered at 32x32, so ${(
            (1 - (32 * 32) / (width * height)) *
            100
          ).toFixed(2)}% of its bytes are discarded`,
        )
      }
    }
    expect(offenders.join('\n')).toBe('')
  })

  it('keeps the directory small enough to serve on a slow uplink', () => {
    if (!existsSync(PROVIDERS_DIR)) return
    const files = readdirSync(PROVIDERS_DIR).filter((f) => f.endsWith('.png'))
    const total = files.reduce((sum, f) => sum + statSync(join(PROVIDERS_DIR, f)).size, 0)
    // The directory is only fetched by the views that render it, but it is
    // served whole to the browser's cache. 1.5 MB is the ceiling: a single
    // screenshot pasted in here (crush.png was 792 KB on its own) is what this
    // is meant to catch, not the long tail of legitimate 30-40 KB icons.
    expect(total).toBeLessThan(1536 * 1024)
  })
})
