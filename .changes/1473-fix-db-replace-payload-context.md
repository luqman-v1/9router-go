### fix(db): pass request context through ReplaceProviderConnectionPayload

`ReplaceProviderConnectionPayload` used `r.db.Exec` without a context, so the
UPDATE could not be cancelled or timed out — it would hold a write lock for the
full statement duration even if the caller had already gone away.

- `ReplaceProviderConnectionPayload` now takes `ctx context.Context` as its
  first parameter and uses `ExecContext`.
- Both dashboard callers pass `r.Context()`.
