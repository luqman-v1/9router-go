/**
 * The dashboard used to load Inter, JetBrains Mono and Material Symbols from
 * fonts.googleapis.com and fonts.gstatic.com.
 *
 * That cost the first dashboard open two render-blocking stylesheets on a
 * third-party origin. A blocked CDN stalls the first paint entirely — measured
 * with the stylesheet requests left hanging, the app did not mount for 21 s —
 * and a working one still costs a DNS lookup, a TLS handshake and two round
 * trips to a host that has nothing to do with the gateway the operator is
 * running.
 *
 * The fonts now ship from the gateway's own origin. This test pins that: a
 * build that reintroduces a CDN font link fails here rather than in a user's
 * browser.
 */

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'bun:test'

const WEB_ROOT = join(import.meta.dirname, '..', '..')
const SRC = join(WEB_ROOT, 'src')

/**
 * A reference to a font CDN, spelled as a URL.
 *
 * The pattern matches the scheme and the host, so a prose mention of the CDN
 * in a comment does not trip it — only a file that actually points a browser
 * at one does.
 */
const CDN_FONT_URL = /https?:\/\/fonts\.(googleapis|gstatic)\.com/

describe('dashboard fonts are self-hosted', () => {
  it('the entry document names no third-party font origin', () => {
    const html = readFileSync(join(WEB_ROOT, 'index.html'), 'utf8')
    expect(html).not.toMatch(CDN_FONT_URL)
    expect(html).not.toMatch(/@import\s+url\(['"]?https?:/)
  })

  it('the stylesheet declares the fonts locally', () => {
    const css = readFileSync(join(SRC, 'index.css'), 'utf8')
    expect(css).toContain("font-family: 'Inter'")
    expect(css).toContain("font-family: 'JetBrains Mono'")
    expect(css).toContain("font-family: 'Material Symbols Outlined'")
    expect(css).not.toMatch(CDN_FONT_URL)
  })

  it('no source file reaches a font CDN', () => {
    const offenders: string[] = []
    const sources: Array<[string, string]> = [
      ['index.html', join(WEB_ROOT, 'index.html')],
      ['index.css', join(SRC, 'index.css')],
      ['main.ts', join(SRC, 'main.ts')],
    ]
    for (const [name, file] of sources) {
      if (CDN_FONT_URL.test(readFileSync(file, 'utf8'))) {
        offenders.push(name)
      }
    }
    expect(offenders).toEqual([])
  })
})
