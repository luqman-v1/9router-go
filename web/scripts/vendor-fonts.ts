/**
 * Vendors the fonts the dashboard ships, and instances the Material Symbols
 * icon font down to the axes the UI actually uses.
 *
 * Why this exists
 * ---------------
 * The dashboard used to pull Inter, JetBrains Mono and Material Symbols from
 * fonts.googleapis.com and fonts.gstatic.com. That cost the operator three
 * things on the very first dashboard open, which is the moment the SPA is
 * slowest and most visible:
 *
 *   1. Two render-blocking stylesheets on a third-party origin. A blocked or
 *      slow CDN stalls the first paint entirely — measured here: with the
 *      stylesheet requests left hanging, the app did not mount for 21 s.
 *   2. A DNS lookup, a TLS handshake and two round trips to a host that has
 *      nothing to do with the gateway the operator is running.
 *   3. The 4 MB Material Symbols variable font, whose every byte is fetched
 *      before the icons can render.
 *
 * The icons are the expensive part: the font carries every glyph Material
 * Symbols defines, on four variable axes, and the UI only ever draws a few
 * hundred of them. Instancing the axes the stylesheet pins anyway (wght 400,
 * GRAD 0, opsz 24, FILL 0..1) drops 4.0 MB to 349 KB without removing a
 * single glyph — every icon name Material Symbols defines keeps working, so a
 * newly added icon can never render as tofu.
 *
 * Everything lands in `web/src/assets/` as a plain file the Vite build copies
 * like any other asset. Nothing here runs at request time.
 *
 * Regenerating after a dependency bump:
 *
 *   bun run vendor:fonts
 *
 * The output is deterministic (same input, byte-identical file), so the
 * generated assets are committed and CI never has to run this.
 */

import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import subsetFont from 'subset-font'

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const assetRoot = join(webRoot, 'src', 'assets')
const fontRoot = join(assetRoot, 'fonts')

/**
 * The Material Symbols axes the stylesheet pins. Pinning them is lossless for
 * this UI: `web/src/index.css` sets exactly these values on every
 * `.material-symbols-outlined` span, and the only axis the UI varies is FILL,
 * which the sidebar's active-item state flips between 0 and 1 — so FILL keeps
 * its full range and nothing else is reachable.
 */
const SYMBOL_AXES = {
  wght: 400,
  GRAD: 0,
  opsz: 24,
  FILL: { min: 0, max: 1, default: 0 },
} as const

/** Fontsource files copied verbatim into `src/assets/fonts`. */
const VENDORED = [
  {
    from: '@fontsource-variable/inter/files/inter-latin-wght-normal.woff2',
    to: 'inter-latin-wght-normal.woff2',
  },
  {
    from: '@fontsource-variable/inter/files/inter-latin-ext-wght-normal.woff2',
    to: 'inter-latin-ext-wght-normal.woff2',
  },
  {
    from: '@fontsource-variable/inter/LICENSE',
    to: 'LICENSE-Inter.txt',
  },
  {
    from: '@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2',
    to: 'jetbrains-mono-latin-wght-normal.woff2',
  },
  {
    from: '@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-ext-wght-normal.woff2',
    to: 'jetbrains-mono-latin-ext-wght-normal.woff2',
  },
  {
    from: '@fontsource-variable/jetbrains-mono/LICENSE',
    to: 'LICENSE-JetBrainsMono.txt',
  },
]

async function vendorTextFonts(): Promise<void> {
  mkdirSync(fontRoot, { recursive: true })
  for (const file of VENDORED) {
    const from = join(webRoot, 'node_modules', file.from)
    copyFileSync(from, join(fontRoot, file.to))
    const size = readFileSync(join(fontRoot, file.to)).length
    console.log(`  ${file.to} — ${(size / 1024).toFixed(0)} KB`)
  }
}

async function instanceSymbolFont(): Promise<void> {
  const source = join(
    webRoot,
    'node_modules',
    'material-symbols',
    'material-symbols-outlined.woff2'
  )
  const original = readFileSync(source)
  const subset = await subsetFont(original, undefined, {
    targetFormat: 'woff2',
    keepAllGlyphs: true,
    variationAxes: SYMBOL_AXES,
  })
  const target = join(assetRoot, 'material-symbols-outlined.woff2')
  writeFileSync(target, subset)
  console.log(
    `  material-symbols-outlined.woff2 — ${(original.length / 1024).toFixed(0)} KB → ${(
      subset.length / 1024
    ).toFixed(0)} KB`
  )
}

async function main(): Promise<void> {
  console.log('Vendoring dashboard fonts...')
  mkdirSync(assetRoot, { recursive: true })
  await vendorTextFonts()
  await instanceSymbolFont()
}

if (import.meta.main) await main()
