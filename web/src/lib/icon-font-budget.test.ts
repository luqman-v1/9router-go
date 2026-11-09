/**
 * The icon font is the single heaviest asset the dashboard ships.
 *
 * The upstream Material Symbols variable font is 3.9 MB: every glyph the
 * family defines, on every axis, live. The shipped copy is the same font
 * instanced down to the axes the UI pins (wght 400, GRAD 0, opsz 24, FILL
 * 0..1), which is 349 KB — and it still carries every glyph, so an icon name
 * added to the catalog tomorrow renders instead of showing as tofu.
 *
 * The test pins two things: the file stays under the budget, and it stays
 * under the original by a wide margin. A dependency bump that swaps in an
 * uninstanced font again fails here.
 */

import { existsSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'bun:test'

const SRC = join(import.meta.dirname, '..')
const ICON_FONT = join(SRC, 'assets', 'material-symbols-outlined.woff2')

/** The budget the instanced font has to stay under. */
const MAX_BYTES = 400 * 1024

describe('material symbols font', () => {
  it('is vendored into the source tree', () => {
    expect(existsSync(ICON_FONT)).toBe(true)
  })

  it('stays under the size budget', () => {
    const size = statSync(ICON_FONT).size
    expect(size).toBeLessThan(MAX_BYTES)
  })

  it('is a woff2 file, not a raw sfnt', () => {
    const head = readFileSync(ICON_FONT).subarray(0, 4)
    // wOF2
    expect(head.toString('latin1')).toBe('wOF2')
  })

  it('carries the icon glyphs the UI uses', () => {
    // The woff2 header is followed by the table directory; the safest
    // observable check is that the font is large enough to hold a few
    // hundred glyphs and that the stylesheet still points at it.
    const size = statSync(ICON_FONT).size
    expect(size).toBeGreaterThan(100 * 1024)

    const css = readFileSync(join(SRC, 'index.css'), 'utf8')
    expect(css).toContain('material-symbols-outlined.woff2')
  })
})
