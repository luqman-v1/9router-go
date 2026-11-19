### RTK: fold ping echoes to the statistics a reader needs

An agent's `ping` run is one summary paragraph buried under a wall of identical
echo replies. The RTK (Reduce Token Kit) compressor now owns that shape instead
of collapsing it with the generic fallback, so a 200-reply run reaches the model
as its header, the round-trip statistics, and the packet counts.

- **New `ping` filter** (`internal/tokensaver/filters/ping.json`), added to the
  catalog of command-scoped filters the compressor already loads. It follows the
  same data-driven shape as its neighbours — `match`, `rules`, and `preserve`
  blocks, no new Go code — so the compression stays one mechanism rather than a
  second hand-rolled compressor beside the pipeline.
- **Command-scoped, like every filter that names a command.** A `ping` filter
  claiming bodies on content alone would rewrite a file that merely mentions a
  transcript; the catalog deliberately only matches content for filters that do
  not name commands, and this one names both `ping` and `ping6`.
- **The statistics survive; the echoes go.** The header line, the
  `--- host ping statistics ---` block, the transmitted/received/loss counts, and
  the `round-trip min/avg/max/mdev` line are all kept. Only the per-sequence echo
  replies and blank padding are dropped, and the fold reports how many were.
- **A failure is the one result never folded away.** `100% packet loss`,
  `Network is unreachable`, and `Name or service not known` are declared as
  preserved error patterns, so a dead host still reads as dead rather than as a
  short-but-clean run.
- **Reaches the request path with no command to key on.** Tool output rarely
  carries its originating command, so the filter also declares content patterns
  (`^PING `, the statistics line, the transmitted/received counts) so the shape
  is recognised from the body itself.

Verified: unit tests in `internal/tokensaver` — the filter resolves from its
command, resolves from content alone while *not* claiming prose that merely
mentions a ping, folds a 200-echo run down to its statistics while keeping the
failure detail of an unreachable host, and keeps the existing command-scoped
guard tests green. `go build ./...`, `go vet`, and `gofmt` clean;
`golangci-lint --new-from-rev` on the base reports 0 issues.