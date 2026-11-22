### chore(gitignore): keep a local gateway database out of the tree
- A gateway started from the repo root leaves its SQLite database beside the source, and `providerConnections` holds live provider tokens and OAuth data — the `-wal`/`-shm` sidecars carry the same rows. An accidental `git add -A` published every connected account.
- `*.db*` and `*.sqlite*` are ignored, along with `/bin/` build output and `/docs/reports/`, which holds per-session run output rather than product.
- No fixture in the repository uses those extensions, so nothing tracked becomes hidden.
