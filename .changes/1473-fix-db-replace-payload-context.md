### fix(db): propagate request context through all DB write paths

`ReplaceProviderConnectionPayload`, `CreateProviderConnectionFull`,
`UpdateConnectionData`, `SetConnectionSealed`, `sealOnWrite`,
`NextConnectionPriority`, `ResolveDefaultPriority`, and
`RewrapConnectionDEKs` now accept `ctx context.Context` and use
`ExecContext`/`QueryRowContext`/`QueryContext` instead of context-less
`Exec`/`QueryRow`/`Query`. A context-less write holds a SQLite write lock
for the full statement duration even after the handler's context is
cancelled — client disconnect, timeout, or graceful shutdown cannot stop it.

All dashboard handlers, OAuth refresh paths, executor paths, and
integration tests now pass `r.Context()` or their existing context through
the full write path.
