/**
 * Ratcheting gate for svelte-check.
 *
 * Why this exists: `tsc -b` cannot read `.svelte` files, so every Svelte script
 * block in this repo has never been type-checked. `vite build` and `oxlint`
 * pass a file that calls a function it never imported — the name is only
 * resolved when the code runs, so the bug ships and only a click in a browser
 * finds it (issue #130).
 *
 * svelte-check closes that hole but reports ~92 pre-existing type errors, so it
 * cannot be a hard gate today. This script gates on the fatal class only
 * (`Cannot find name` / unresolved identifiers — the exact shape that produces a
 * runtime ReferenceError) and pins the total error count so the remaining debt
 * can only shrink, never grow.
 *
 * Exit codes:
 *   0  clean, or no worse than the recorded baseline
 *   1  a new undefined identifier, or the total error count grew
 */

import { spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const baselinePath = join(webRoot, 'scripts', 'svelte-check-baseline.json')

/**
 * Fatal class: an identifier the checker cannot resolve. These become a
 * `ReferenceError` the first time the code runs, so unlike a stylistic type
 * mismatch they take a whole feature down before the request leaves the browser.
 */
const FATAL = /Cannot find name|Cannot find module|has no exported member|is not exported by/

const run = spawnSync('bunx', ['svelte-check', '--tsconfig', 'tsconfig.app.json', '--output', 'machine'], {
  cwd: webRoot,
  encoding: 'utf8',
})

const output = `${run.stdout ?? ''}${run.stderr ?? ''}`
if (!output.trim()) {
  console.error('svelte-check produced no output; failing closed rather than passing blind.')
  console.error(output.slice(0, 2000))
  process.exit(1)
}

const errors = output
  .split(/\r?\n/)
  .filter((line) => line.includes(' ERROR '))
  .map((line) => {
    const match = line.match(/^(\d+) ERROR "([^"]+)" (\d+):(\d+) (.*)$/)
    if (!match) return null
    return {
      file: match[2].replace(/\\/g, '/'),
      line: Number(match[3]),
      column: Number(match[4]),
      message: match[5],
    }
  })
  .filter((e): e is { file: string; line: number; column: number; message: string } => e !== null)

// svelte-check exits non-zero whenever it reports anything; the findings
// themselves are what this gate judges, so its exit code is not the verdict.
const fatal = errors.filter((e) => FATAL.test(e.message))

const baseline = existsSync(baselinePath)
  ? (JSON.parse(readFileSync(baselinePath, 'utf8')) as { totalErrors: number })
  : { totalErrors: Number.POSITIVE_INFINITY }

let failed = false

if (fatal.length > 0) {
  failed = true
  console.error(`\n✗ ${fatal.length} unresolved identifier(s) — these throw a ReferenceError at runtime:`)
  for (const e of fatal) {
    console.error(`  ${e.file}:${e.line}:${e.column}  ${e.message}`)
  }
  console.error('\n  Fix these before pushing. This class of bug is invisible to tsc -b,')
  console.error('  oxlint and vite build; it only surfaces when a user clicks the button.\n')
}

if (errors.length > baseline.totalErrors) {
  failed = true
  console.error(`✗ svelte-check errors grew: ${errors.length} > baseline ${baseline.totalErrors}`)
  console.error('  Fix the new ones, or run `bun run ratchet:svelte --update` when the drop is intended.')
}

if (!failed) {
  console.log(`✓ svelte-check: 0 unresolved identifiers, ${errors.length} errors (baseline ${baseline.totalErrors})`)
  if (errors.length < baseline.totalErrors) {
    console.log(`  ↓ ${baseline.totalErrors - errors.length} below baseline — run \`bun run ratchet:svelte -- --update\` to tighten it.`)
  }
}

// Only rewrite the baseline when asked, so a plain gate run cannot silently
// bless whatever debt the current branch happens to carry.
if (process.argv.includes('--update') && !fatal.length) {
  writeFileSync(baselinePath, `${JSON.stringify({ totalErrors: errors.length }, null, 2)}\n`)
  console.log(`  baseline updated to ${errors.length}`)
}

process.exit(failed ? 1 : 0)