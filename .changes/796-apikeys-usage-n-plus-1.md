### Dashboard: the API-key list stops paying one ledger scan per key

- **`GET /api/keys` reads every key's token spend in one statement.** The
  listing filled `usedTokens` with a read per key row, and each read paid
  three queries plus one ledger pass: the key row again, its reset mark,
  and the three-spelling history sum with its carryover lookup. On a
  108k-row ledger of 5 keys that measured ~860 ms server-side — the
  request the dashboard opens on. `GetApiKeyTokenUsageForKeys`
  (`internal/db/quota_reset.go`) now answers the whole page from one
  `VALUES`-CTE statement: the reset marks are read once per page and ride
  the statement as a column, and every key's spend is computed by the same
  terms the per-key reader uses (the three recorded spellings, the key's
  reset mark, the carryover row), so a key reset between two listings
  starts at zero in the list exactly as the limiter sees it. A database
  created before the resettable token budget (#365) cannot compile the
  mark read and falls back to the per-key reader, which already degrades
  the missing column to a lifetime sum — the fast path is a fast path and
  never changes the numbers the dashboard shows. The handler keeps its
  fail-open contract: a failed usage read logs a warning and reports zero
  spend rather than failing the listing.
- **The ledger indexes the sum reads are covering composites now.** The
  single-column `idx_uh_apikey` / `idx_uh_clientkey` pair answered the
  multi-index OR but still fetched one table row per ledger entry to reach
  the token columns. They are replaced by
  `idx_uh_apikey_tok` / `idx_uh_clientkey_tok` in the `idx_uh_prov_tok`
  shape — `(column, id, promptTokens, completionTokens)` — so the reset
  mark is a range inside the index and the sum is answered from the index
  alone; the narrow pair joins `supersededIndexes`, so an install upgraded
  in place stops maintaining indexes nothing reads. Every other reader of
  the same sum rides this too: the reset-quota endpoint and the
  request-path limiter.
- **Two plan facts measured on the way, both pinned in code.** A
  correlated scalar subquery inside the sum's WHERE is re-evaluated per
  candidate ledger row — the mark as a nested subquery cost 2.5× the whole
  per-key path, so it travels as a CTE column. And a single-pass
  `GROUP BY (clientKey, apiKey)` over `IN`-lists measured 4.7× worse,
  because SQLite does not answer that OR-over-IN through the composites;
  the correlated-CTE shape is the one kept.

Measured with `BenchmarkApiKeyTokenUsage*` on a ledger seeded from the
production request counts: the per-key loop the endpoint used to run fell
53.7 → 33.8 ms/op and 4,924 → 916 allocs from the composites alone, and
the batched read the endpoint pays now is 37.7 ms/op with 311 allocs —
two statements for any key count, where the old path grew 1 + 3N.
Verified with `TestGetApiKeyTokenUsageForKeysMatchesPerKeyRead` (batched
equals the per-key reader for every key across all spellings, a mid-page
reset mark, a carryover row and an unrelated key's rows),
`TestGetApiKeyTokenUsageForKeysQueryPlan` (the exact statement plans
through both composites, no `SCAN usageHistory`),
`TestGetApiKeyTokenUsageForKeysLegacySchema` (a pre-#365 database still
gets correct numbers through the fallback), the updated legacy-migration
tests, and `go test ./internal/db/ ./internal/handlers/...` green.
`AGENTS.md` §4.A pins the lesson as a house rule: SQLite access reads one
statement per page, never N+1.
