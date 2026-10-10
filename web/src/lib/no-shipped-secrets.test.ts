/**
 * The dashboard SPA is served to whoever can reach the gateway, so any secret
 * literal in `web/src` becomes a public credential the moment `vite build`
 * minifies it into `web/dist/assets/*.js`.
 *
 * `CliToolsView.svelte` shipped one: the guide cards fell back to a real
 * machine-minted `sk-…` key when the instance had none, so the fallback itself
 * was a working key for the author's deployment and the cards handed it to
 * every visitor who opened CLI Tools on an instance with no key. The rendered
 * snippet looked correct, which is why nothing caught it — the e2e only ever
 * booted an instance that had a key.
 *
 * A source scan is the cheap half of that guard. `web/e2e/cliToolsPi.test.ts`
 * asserts the same invariant against the served bundle.
 */

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'

const SRC = resolve(import.meta.dirname, '..')

/** Extensions a browser bundle can be built from. */
const SOURCE_EXT = new Set(['.svelte', '.ts', '.js', '.jsx', '.tsx'])

/**
 * A minted client key. 9router-go generates `sk-` + 32 uuid hex chars, but
 * `POST /api/keys` accepts any caller-supplied secret, so the pattern is
 * deliberately loose about length and shape. Global, because matchAll
 * requires it.
 */
const SECRET = /sk-[A-Za-z0-9_-]{20,}/g

/**
 * Values that look like a key but are labels the user replaces before use.
 * `sk-9router-local-token` is the shell-profile placeholder the env cards have
 * always shown, so every member of that family is exempt.
 */
const PLACEHOLDER_PREFIX = 'sk-9router-'

function* walk(dir: string): Generator<string> {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      yield* walk(path)
    } else if (SOURCE_EXT.has(extname(path))) {
      yield path
    }
  }
}

describe('web sources ship no client API key', () => {
  it('finds no secret literal in any bundled source file', () => {
    const leaks: string[] = []
    for (const file of walk(SRC)) {
      const body = readFileSync(file, 'utf8')
      for (const match of body.matchAll(SECRET)) {
        const token = match[0]
        if (token.startsWith(PLACEHOLDER_PREFIX)) continue
        const line = body.slice(0, match.index).split('\n').length
        leaks.push(`${relative(SRC, file)}:${line}: ${token}`)
      }
    }
    expect(leaks).toEqual([])
  })
})