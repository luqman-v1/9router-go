### Changed

- Lower per-request CPU on the SQLite path: prepared statements are now cached per connection (256 entries max, pool of 4) instead of being re-parsed by `sqlite3_prepare_v2` on every `db.Query`/`db.Exec`. pprof of the request path had put the SQLite parser at ~15% of CPU (~10 statements parsed per request); a repeated point query benchmarks at ~10.7µs vs ~15.2µs before (~30% off). No wire, schema, or endpoint change; the PostgreSQL path is untouched. Cached handles survive `ResetSession`, are finalized on connection close, and SQLite re-prepares them itself on schema change, so migrations keep working.

Verified with `go test ./internal/db/ -count=1` (incl. a counting driver asserting exactly one engine-level prepare per distinct query across Query/Exec/`db.Prepare`/transaction paths), `go vet ./...`, `gofmt`, and `-race` on the new tests; benchmark output in PR #1584.
