### feat(persona): preset auto-sync from a GitHub collection, with a default source seeded

`POST /api/jailbreak/import` answers "give me these prompts now": a repository
is read, one preset per file is written, and the story ends there. That is right
for a one-shot fill and wrong for a collection whose prompts change — a new
model file appears, a prompt is rewritten, and the library has no way to hear
about it. The Persona page's preset library now follows repositories the way a
subscription follows a feed.

- **Sources, not imports.** `GET/PUT /api/jailbreak/sync/sources` list and
  replace the operator's GitHub collections; `POST /api/jailbreak/sync/run` runs
  one pass immediately. Each source carries a target (anything the GitHub import
  already accepts — `owner/repo`, a GitHub URL, or a raw file URL), an optional
  branch or tag pin, and an enabled switch. Adding one is a single field: paste
  the repository and press **Follow**.
- **Auto.** One pass runs 45 s after boot, then hourly, on the same ticker shape
  `StartBackgroundCatalogSync` uses for the model catalog. A pass that fails
  records the reason on the source and retries on the next tick; the periodic
  loop skips when a manual pass ran recently, so the two never re-read the same
  bytes.
- **Default data.** A fresh install seeds
  `togg53192-cmd/jailbreaks` (54 prompt files, one per model family) as an
  enabled source, so the library has something worth looking at before the
  operator adds anything. A fresh database writes the seed on first read of the
  source list — the same "seed on first read" shape the jailbreak keyword store
  already uses.
- **Id naming follows the operator's convention.** A preset id is
  `owner_repo-model`: `togg53192-cmd/jailbreaks` with `DeepSeek/deepseek v4.md`
  becomes `togg53192-cmd_jailbreaks-deepseek-v4`, so two collections that both
  carry a deepseek file do not collide. A file that already follows the
  convention keeps its own name — prefixing again would produce
  `me_repo-me_repo-model-v1`.
- **Three rules keep it safe to run unattended.**
  - **Only its own presets are rewritten.** A synced preset carries its origin
    (`JailbreakDoc.SyncOrigin`, "owner/repo@ref") and the file it came from. A
    pass touches a preset only when the origin names the source it came from,
    so a preset typed, uploaded or imported by hand is invisible to every pass —
    which is what stops a background write from reverting an operator's edit.
    An operator's title, order, creation date, position, mode, excludes and
    hand-added keyword rules survive an update; the prompt is the only field
    upstream owns, and an unchanged body is not rewritten at all.
  - **Nothing upstream is ever deleted.** A file the repository dropped leaves
    its preset in place and is reported as `orphaned` in the pass log. The
    operator removes it by hand; a subscription that silently deleted rows could
    not be undone by reading a log.
  - **Every source is removable**, and removing one changes nothing about the
    presets it already wrote — sync only ever adds and rewrites, it never clears.
- **A target may point at a subfolder.** `github.com/owner/repo/tree/main/jailbreaks`,
  `github.com/owner/repo/blob/main/prompts/jailbreaks` and the `owner/repo`
  shorthand all work; a folder contributes everything inside it and a file path
  imports exactly that file. Without this the collections that keep their
  prompts in a subfolder contributed nothing at all, and the 404 the operator
  saw read as "this repository has no prompts in it".
- **`.md`, `.markdown`, `.mdown`, `.mkd` and `.txt` are prompt files.**
  `.mkd` in particular is what several collections ship, and a filter that did
  not know it imported 1 file out of a 43-prompt library.
- The worker reuses the importer's own machinery — `collectFiles`, the GitHub
  allowlist, the redirect re-check, the per-file and total size caps, and the
  claimed-stem keyword derivation — so a synced preset carries the same keyword
  chain a manual import produces, and a short file name cannot steal a sibling's
  model. A collection that outgrows the caps reports what it dropped in the pass
  result rather than reading as a clean success.

Verified: 17 new Go tests over a fake store (an in-memory store, so no
SQLite and no GitHub in the unit path) covering the write path with its origin
and id, the rewrite of only its own preset, the kept operator fields, an
unchanged body not rewritten, the fetch failure reported rather than written,
the orphan reported not deleted, the busy answer, the id and origin naming, the
three endpoints against a real database — the seeded default, the operator's own
repository saved enabled, and a target the importer cannot parse refused with 400
naming the target — plus three more for the two import fixes below: a pointed
subfolder importing its own files and not the repository's README, a single
file path still importing one file, and the `treeHasSubtree`/`restrictToSubtree`
helpers. 11 new frontend tests over `web/src/lib/presetSync.ts`
(the summary, the relative time, the label and the pass totals) pin what the card
shows before anything is pressed. `go test ./internal/handlers/... ./internal/db/...`
green, `bun test src scripts` 943 pass / 0 fail, `bun run ratchet:svelte` 0
unresolved identifiers at baseline 74, `bunx oxlint --deny-warnings` clean,
`go build ./...` clean.

Seven collections were followed live against the gateway
(`e2sy/jailbreak-archives`, `elder-plinius/L1B3RT4S`,
`7API/glm-5.3-abliterated`, `routerhubSI/glm-5.3-abliterated/jailbreaks`,
`4ndr0666/gpt/prompts/jailbreaks`, `2knoah-glitch/ABYSS-AI-REWORK` and
`RJKaler/Grok-Jailbreak`), which is where the last two fixes in this entry came
from. The library reached 512 presets, 335 of them sync-owned.

### fix(import): a GitHub path naming a directory is a subtree, not a file

The seven collections above exposed a gap the one-shot import had never met.

- **A GitHub path naming a directory was read as a file.** `collectFiles`
  treated a non-empty `src.Path` as one file, so
  `github.com/owner/repo/tree/main/jailbreaks` and
  `github.com/owner/repo/blob/main/prompts/jailbreaks` fetched the directory
  itself — a 404 the operator reads as "this repository has no prompts in it".
  The tree listing now keeps `tree` entries so the code can tell a pointed
  subfolder from a pointed file, collects everything under a folder, and keeps a
  file path importing exactly that one file. Live: `routerhubSI/…/jailbreaks`
  went from 0 imported to 34, `4ndr0666/gpt/prompts/jailbreaks` to 195.

### fix(sync): a capped collection reports what it dropped

A 337-file collection contributes only what fits under the import caps, and the
sync pass was discarding the importer's skip list — so 195 presets were written
and the pass reported a clean success, leaving the operator believing two thirds
of a library was synced. `FetchFiles` now returns the skip list as part of its
contract and each dropped file is reported in the pass result with the reason
that dropped it, so a trimmed collection reads "195 written, 142 failed — import
is capped at 200 files" rather than a clean pass.

### feat(persona): a GitHub token and a mirror, because 60 requests an hour is not enough

`api.github.com` allows **60 anonymous requests an hour** and **5,000 with a
token**. A library following seven collections — each pass being one repo lookup,
one tree listing and one fetch per file — exhausts the anonymous quota on the
third pass, after which every source reports `403` and none of them says why.
The persona page now has a place to put a credential, and both endpoints can be
pointed at a mirror.

- **Token**, typed into the Auto-sync card and stored beside the sync sources.
  `GET /api/jailbreak/github` reports whether one is in force and where it came
  from (`dashboard`, `environment` or `none`) but **never the token itself** — a
  response carrying it would put the credential in browser storage, in
  screenshots and in the request log. `PUT /api/jailbreak/github/token` writes
  it, and an empty string clears it, so a revoked credential is removable. It
  takes effect on the next request without a restart, and it is loaded again at
  boot so a restart does not lose it and fail the first pass with a 403 the
  operator already fixed.
- **Mirror**: `GITHUB_API_MIRROR` and `GITHUB_RAW_MIRROR` point the two bases at
  a proxy, for a network that cannot reach GitHub directly or that wants a
  self-hosted quota. The paths are unchanged, so every existing target form
  still parses, and the card shows the base in force.
- The token rides **only** the request to the configured API base, host *and*
  port — never the file host. Proved by a test that asserts on what each fake
  server actually received: the API server sees `Bearer …`, and the raw server
  sees nothing. That port comparison is load-bearing, not defensive: two
  stand-in servers on one loopback address share a hostname and differ only by
  port, and a hostname-only check would have sent the credential to the file
  server too.
- `GITHUB_TOKEN` still works and still wins, for a process started with one.

### feat(persona): a conditional re-fetch, so an unchanged collection costs nothing

A mirror and a token both raise the ceiling, but neither reduces what is fetched:
without one, **every pass of every collection re-downloads the whole tree and
every file**, and the quota a token buys is spent on bytes that have not changed.
The GitHub API answers `If-None-Match` with a `304` that costs nothing.

- Every body the server tags with an `ETag` is held in a bounded in-memory cache
  (`githubETagCacheMax` entries, oldest written evicted), and the next fetch of
  the same URL sends it back. A `304` answers from the cache — no body crosses
  the wire and no quota is spent — and a `200` replaces the entry. The cache is a
  cache, not a store: the authoritative copy of every preset is the database, so
  a restart re-fetches and the worst case is one pass that spends what the cache
  would have saved. It is bounded because it is keyed by a URL the operator
  controls, and an unbounded map keyed by user input is a memory leak with a
  schedule.
- The panel reports what the cache holds (`cache: 512/4096`), so an operator can
  see that a second pass with no upstream change is not costing anything rather
  than taking the saving on faith.
- The tree listing is cached the same way as a file, so the one call that decides
  whether a pass has anything to do at all is also conditional.

Verified by a test that runs two passes against a fake that answers `304` to a
conditional request: the first fills the cache, the second spends no body bytes
on the file, and both write the same preset from the same prompt. Without the
cache the second pass would re-fetch, and the preset would be identical anyway —
which is exactly the waste the quota was being spent on. `go test ./internal/
handlers/dashboard/` green, `go build ./...` clean, `bun run ratchet:svelte` 0
unresolved at baseline 74, `oxlint` clean.

### fix(persona): sync read files at "HEAD" instead of the ref it listed

`FetchText` hardcoded `"HEAD"` while the pass had already resolved a default
branch — so a mirror or a fork that refuses a branch name in the path returned
404 for every file, and the pass reported `written=0, failed=1` with a 404 the
operator reads as "this file has no content". It now sends the resolved ref, and
retries unqualified when a proxy refuses it, which is how several proxies serve
the same file. Live against a local mirror: `written=0` before the fix,
`written=1` after, with the preset's origin recording `owner/repo@main`.