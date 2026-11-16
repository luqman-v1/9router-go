### fix(chat): skip quota-exhausted Codex accounts during 429 failover rotation

- The Codex 429 path already caches the reset instant for the account
  (`NoteCodexQuotaError` → `BlockCodexConnectionUntil`), and the connection
  pre-filter already refuses to *serve* a request on a parked row. The rotation
  still reached it first: `selectByRecency` stamps whichever row its strategy
  sweep lands on and persists that stamp, so an account parked until its 5h/7d
  window resets consumed the turn's rotation slot. The failover loop then
  re-resolved that pin, was told `quota cache`, and fell through to the next
  account — one wasted resolver pass and a mis-advanced rotation per request.
- `filterCodexQuotaExhausted` drops the parked Codex accounts from the candidate
  list before the strategy runs, in both places that rotate
  (`handleAccountFallback` and `getBestConnectionWithContext`). An all-parked
  list is returned unchanged so the existing `all excluded` / `all in cooldown`
  answers still apply — the filter must not turn "everything is parked" into a
  different error.
- Scope is Codex only, and it reuses the account-level cache
  `IsCodexConnectionExhausted` already exposed. No new state, no new query, no
  cross-provider aliasing (AGENTS.md §3.A).
- Verified: `TestHandleAccountFallback_CodexRotationSkipsQuotaExhausted` fails on
  `main` (parked account stamped, resolver logged `pinned connection
  ineligible ... quota cache`) and passes with the filter, with two live accounts
  alternating and the parked one never dialled nor stamped. Full
  `go test ./internal/handlers/chat/` passes.
