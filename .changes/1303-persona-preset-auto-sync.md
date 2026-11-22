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
- **The worker reuses the importer's own machinery.** `collectFiles` and the
  GitHub allowlist, the redirect re-check, the per-file and total size caps and
  the claimed-stem keyword derivation are the ones `jailbreak_import.go` already
  applied, so a synced preset carries the same keyword chain a manual import
  produces and a short file name cannot steal a sibling's model.

**Verified:** 12 new Go tests over a fake store (an in-memory store, so no
SQLite and no GitHub in the unit path) covering the write path with its origin
and id, the rewrite of only its own preset, the kept operator fields, an
unchanged body not rewritten, the fetch failure reported rather than written,
the orphan reported not deleted, the busy answer, the id and origin naming, and
the three endpoints against a real database — the seeded default, the operator's
own repository saved enabled, and a target the importer cannot parse refused
with 400 naming the target. 11 new frontend tests over `web/src/lib/presetSync.ts`
(the summary, the relative time, the label and the pass totals) pin what the card
shows before anything is pressed. `go test ./internal/handlers/... ./internal/db/...`
green, `bun test src scripts` 943 pass / 0 fail, `bun run ratchet:svelte` 0
unresolved identifiers at baseline 74, `bunx oxlint --deny-warnings` clean,
`go build ./...` clean.