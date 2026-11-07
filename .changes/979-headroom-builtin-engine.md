### In-process headroom compression, no external proxy required

The token saver's in-flight compression stage now works without the external
Python headroom proxy. Turning the setting on used to mean "install a second
service and run it at a URL"; it now means "compress this request", because the
compression runs inside the gateway process.

- **New in-process engine** (`internal/headroom/builtin.go`). It reuses the
  existing `tokensaver` pipeline, so compression behaviour is the one already
  covered by that package's tests rather than a second implementation that can
  drift. It implements the same response contract as the proxy, including
  `tokens_before` / `tokens_after`, `transforms_summary`, and the structural
  invariants the client validates before committing a result.
- **Engine selection is explicit.** `headroomEngine` selects `builtin` (default)
  or `external`; an empty or unrecognized value resolves to `builtin`. Inference
  from the URL was deliberately not used: the default URL is present in every
  settings row, so a URL-configured test would send every request to a proxy
  nobody installed.
- **The engine defaults to built-in**, so an existing settings row written
  before this field existed gets the in-process engine rather than a dead
  network path. A fresh install compresses with nothing installed.
- **The protected-tool guard still applies.** A tool the compressor was never
  cleared for reaches the provider byte for byte, so the switch cannot cost
  correctness for a saving.
- **`GET /api/headroom/status` reports the active engine**, read from settings
  rather than inferred from install state: `installed: false` no longer reads
  as "compression is off" when the in-process engine is doing the work.
- **Dashboard** gains a two-button engine selector in the Headroom configure
  panel. `headroomEngine` is written alongside `headroomEnabled` so the panel
  and the backend can never disagree about which engine is active.
- **The compression stage reports the tokens it saved.** The stage rewrote the
  outbound body and dropped the token counts, so a request compressed by
  headroom alone reached the usage ledger with `SavedTokens: 0`. The ledger only
  writes a compression row when `SavedTokens > 0`, which made that request
  invisible on the Usage page: compression was demonstrably working and the
  dashboard reported none. `applyTokenSavers` now returns the original and saved
  token counts for the headroom stage too, and the totals stack correctly when
  RTK and headroom both act on the same request.
- **`resolveCompressionMode` names the engine from the switches, not from the
  saving.** It used to seed `hasRTK` from `savedTokens > 0`, which attributed a
  saving from any stage to RTK — headroom-only compression was filed as `rtk`,
  and the mode breakdown told an operator the wrong engine did the work. The
  parameter is gone rather than reinterpreted; the mode now comes from the
  saver switches alone.
- **The dashboard status describes the engine, not a process.** The built-in
  engine has no process, so a status derived from the proxy's process state read
  "Not installed" or "Stopped" on an instance that was compressing every
  request. The label is now `Active` whenever the selected engine is running
  compression, and the external engine's process state and dashboard link only
  apply when `external` is selected.
- **A global flag typed after the sub-command is answered with guidance.**
  `9router-go start --rtk=false` used to print `flag provided but not defined:
  -rtk` about a flag the binary does define, which reads as a missing feature
  and sends an operator to the release notes instead of to their own command
  line. The message now names the flag, says it belongs before the sub-command,
  and shows the corrected form. A genuine typo (`--rtkk`) and a real
  sub-command typo (`--lins`) keep urfave's original message and its
  suggestions, so nothing is masked. `App.OnUsageError` alone did not reach
  this: urfave/cli copies it onto the root command only, while the failing
  parse happens inside the sub-command, so the hook is installed on every
  command in the tree.

- **A persona-only request is now visible in the usage analytics.** Running
  Caveman, Ponytail or ADHD without RTK or headroom left both
  `OriginalInputTokens` and `SavedTokens` at 0, and the analytics row is only
  written when either is non-zero — so an instance with only a style prompt
  switched on recorded no compression activity at all, on a request where a
  saver demonstrably ran. `recordPersonaInjection` books the prompt's own size
  into the totals (OriginalInputTokens up, SavedTokens down, clamped at 0), so
  the row is written and the dashboard shows the mode with
  `skipReason=no_savings` — a style prompt compresses nothing, and saying so is
  the accurate report.
- **`resolveCompressionMode` no longer invents a mode.** Its fallback answered
  `rtk` when no saver was switched on, naming a stage that had not run on the
  one instance where an operator would never question the claim; and the branch
  order let a single persona fall through to a stale row's mode. Nothing enabled
  now reports `none`.

Verified: unit tests in `internal/headroom` (including the protected-tool,
cancellation, and no-gain paths), new table cases in
`internal/handlers/chat` for headroom-only and headroom-stacked accounting, and
the integration suite in `internal/integration`, where a real gateway with the
setting on forwarded a 10490-byte client body as a 391-byte upstream body
(96% smaller) while preserving the tool call id, model, and arguments — and
where that request now also leaves a compression analytics row with a named mode
and the saving it made. The CLI guidance is covered by a table-driven test in
`cmd/9router-go` that pins both the rewritten message and the cases that must
stay untouched, plus a test that the hook reaches nested sub-commands. All 32
on/off combinations of the five saver switches were run against a real gateway
with a fake upstream: every combination that ran a saver writes a row with the
correct mode, the one combination with no saver writes none, and the
RTK/headroom/stacked savings are unchanged (RTK alone 2622->91, 96%); the
dashboard endpoint reports `adhd`, `caveman` and `ponytail` as their own modes
with `skipped: 1` instead of omitting them. Full suite:
`ok 9router/proxy/internal/integration 107.219s`.
