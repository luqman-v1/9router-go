/**
 * A Material Symbols glyph is named by its ligature text, so the text inside
 * the span must be exactly one glyph name. A Tailwind utility that belongs in
 * the class attribute but was written into the text instead renders as
 * visible words next to the icon: the combo test spinner showed
 * "progress_activity animate-spin" as literal text because the class was
 * pasted into the glyph slot.
 *
 * The rule is mechanical: a quoted literal inside a material-symbols span may
 * not contain whitespace. Glyph names never do.
 */

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'bun:test'

const SRC = join(import.meta.dirname, '..')

function svelteFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      out.push(...svelteFiles(path))
    } else if (entry.endsWith('.svelte')) {
      out.push(path)
    }
  }
  return out
}

// A span whose class attribute names the Material Symbols family. The body is
// captured lazily; spans that hold nested markup are skipped below.
const SPAN = /<span\b([^>]*)class="([^"]*material-symbols-outlined[^"]*)"([^>]*)>([\s\S]*?)<\/span>/g

// A quoted literal inside the span's text.
const LITERAL = /'([^']*)'|"([^"]*)"/g

describe('material symbols glyph text', () => {
  it('keeps utility classes out of the glyph text', () => {
    const offenders: string[] = []
    for (const file of svelteFiles(SRC)) {
      const source = readFileSync(file, 'utf8')
      for (const match of source.matchAll(SPAN)) {
        const body = match[4] ?? ''
        // Nested markup means the span is a wrapper, not a glyph slot.
        if (body.includes('<')) continue
        for (const literal of body.matchAll(LITERAL)) {
          const value = literal[1] ?? literal[2] ?? ''
          if (/\s/.test(value)) {
            const line = source.slice(0, match.index).split('\n').length
            offenders.push(`${file}:${line} — "${value}"`)
          }
        }
      }
    }
    expect(offenders).toEqual([])
  })
})
