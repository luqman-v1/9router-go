### feat(cli-tools): write the nine CLI clients' own config files, and serve them over HTTP

The dashboard's per-tool pages had nothing behind them: a client's settings were
read from and written to its own file on disk by nine separate Next routes
upstream, and the Go port had none of them.

**The adapters** (`internal/clientconfig`) know, for each of claude, codex,
opencode, droid, crush, kilo, cline, pi and omp, which files the client owns,
what format each is in (JSONC, TOML, YAML), and which keys the gateway needs
written. They are deliberately not a network layer: `Plan` resolves the edits
without touching the disk so `--dry-run` has a path that cannot write, and
`Apply`/`Reset` are idempotent — a second run reports `Changed: false` rather
than rewriting the file.

**The HTTP surface** (`internal/handlers/media/cli_tool_settings.go`) resolves
the client out of the path through `clientconfig.Resolve`, so the adapter table
stays the single description of a client and the routes cannot drift from it:

- `GET /api/cli-tools/<tool>-settings` — files owned, format, whether the client
  is installed, and the plan it would apply, with key paths only and never the
  credential.
- `POST` — applies the adapter, writing the client's real file and reporting the
  backups it took.
- `DELETE` — resets what `POST` wrote.

An unknown name answers 404 with the accepted spellings: a connect that silently
ignored a typo would configure nothing and report success. `pi` keeps its own
handler, which merges over a previously saved block, and the generic route skips
it so the two cannot disagree about the same file.

Verified: `go build ./...` clean, `go test ./internal/clientconfig` and
`./internal/handlers/media` clean, and all eight generic routes answer 200 on the
production router with the correct per-client path (`~/.claude/settings.json`
jsonc, `~/.codex/config.toml` toml, `~/.omp/agent/models.yml` yaml, and the rest).
