/**
 * Staleness guard for `make web-build`.
 *
 * Why this exists: the guard used to be "rebuild only when web/dist/index.html
 * is missing". That holds on a fresh clone, but it is wrong on every machine
 * that already has a dist — pulling or fast-forwarding `main` changes
 * `web/src` while `web/dist` stays exactly where it was, so the SPA was never
 * rebuilt and `go build` embedded the previous bundle. The symptom is a
 * dashboard that looks like the old release while the Go side is current, and
 * the only cure is `FORCE=1`, which nobody remembers. It cost a real debugging
 * session on 2026-10-04.
 *
 * The fix is to fingerprint the actual build inputs and rebuild when that
 * fingerprint changes. `bun.lock` and `package.json` are inputs too: a
 * dependency bump changes the emitted bundle without touching `web/src`.
 *
 * The stamp lives at web/.dist-stamp, deliberately outside web/dist. A file
 * named `.*` matches `//go:embed dist/*` when it sits directly in dist, which
 * would ship the build's own fingerprint inside the binary and expose it as a
 * downloadable route.
 *
 * Shell-agnostic on purpose: this runs under /bin/sh (Git Bash, CI) or cmd.exe
 * (a plain Windows prompt). A POSIX `[ -nt x ]` comparison is unavailable
 * under cmd and unreliable across filesystems anyway, so the decision lives in
 * one place, in code.
 *
 * Env:
 *   FORCE=1   rebuild even when the fingerprint is unchanged
 */

import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { precompress } from './precompress'

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const stampPath = join(webRoot, '.dist-stamp')
const distIndex = join(webRoot, 'dist', 'index.html')

/** Directories walked wholesale: everything in them reaches the bundle. */
const INPUT_DIRS = ['src', 'public']

/** Individual files vite/tsc read. bun.lock pins the toolchain versions. */
const INPUT_FILES = [
  'index.html',
  'package.json',
  'bun.lock',
  'vite.config.ts',
  'tsconfig.json',
  'tsconfig.app.json',
  'tsconfig.node.json',
]

/** Never hashed: generated output, dependencies, and VCS metadata. */
const SKIP_DIRS: Record<string, true> = {
  node_modules: true,
  dist: true,
  '.git': true,
  '.svelte-kit': true,
}

function collectFiles(dir: string, out: string[]): void {
  const entries = readdirSync(dir, { withFileTypes: true })
  // Sort so the digest depends on content and paths, never on readdir order.
  entries.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  for (const entry of entries) {
    if (SKIP_DIRS[entry.name]) continue
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      collectFiles(full, out)
    } else if (entry.isFile()) {
      out.push(full)
    }
  }
}

/**
 * Digest of every build input under `root`, including paths. A rename that
 * leaves the bytes identical still changes the digest, because the module
 * graph it belongs to changed.
 */
export function fingerprint(root: string = webRoot): string {
  const files: string[] = []
  for (const dir of INPUT_DIRS) {
    const full = join(root, dir)
    if (existsSync(full)) collectFiles(full, files)
  }
  for (const file of INPUT_FILES) {
    const full = join(root, file)
    if (existsSync(full)) files.push(full)
  }
  files.sort()

  const hash = createHash('sha256')
  for (const file of files) {
    hash.update(relative(root, file).split(sep).join('/'))
    hash.update('\0')
    hash.update(readFileSync(file))
    hash.update('\0')
  }
  return hash.digest('hex')
}

function buildSpa(): number {
  console.log('Building web SPA assets...')
  if (!existsSync(join(webRoot, 'node_modules'))) {
    const install = spawnSync('bun', ['install'], { cwd: webRoot, stdio: 'inherit' })
    if (install.status !== 0) return install.status ?? 1
  }
  // `bun run build` runs the pre-compression itself (web/package.json), so
  // the variants exist in dist before the Go build embeds it. A failure
  // there fails the build: a gateway that embeds an uncompressed bundle
  // serves a dashboard that takes seconds to open, which is the regression
  // the pre-compression exists to prevent — and it is deterministic, so it
  // fails in CI rather than on a user's phone.
  return spawnSync('bun', ['run', 'build'], { cwd: webRoot, stdio: 'inherit' }).status ?? 1
}

function main(): void {
  if (process.env.FORCE !== '1') {
    if (!existsSync(distIndex)) {
      console.log('web/dist is missing — building the SPA.')
    } else if (!existsSync(stampPath)) {
      console.log('web/dist has no build stamp — building the SPA once to record it.')
    } else {
      const current = fingerprint()
      const recorded = readFileSync(stampPath, 'utf8').trim()
      if (recorded === current) {
        console.log('web/dist is up to date (inputs unchanged).')
        // The SPA itself does not need rebuilding, but the compressed
        // variants do have to exist: `vite build` clears dist, so a build run
        // directly (`bun run build`, which is what CI does) leaves the
        // gateway embedding an uncompressed bundle. The gateway falls back
        // to the original file, so nothing breaks — the dashboard is just
        // served without compression, which is the exact regression the
        // pre-compression exists to prevent.
        precompress()
        process.exit(0)
      }
      console.log('Frontend sources changed since the last build — rebuilding the SPA.')
    }
  }

  const status = buildSpa()
  if (status !== 0) return process.exit(status)

  // Record the fingerprint only after a successful build, so a failed build
  // leaves the previous stamp and the next run retries instead of skipping.
  mkdirSync(dirname(stampPath), { recursive: true })
  writeFileSync(stampPath, `${fingerprint()}\n`)
  console.log(`Recorded web build stamp ${fingerprint().slice(0, 12)}`)
}

if (import.meta.main) main()