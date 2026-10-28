### fix(rtk): truncate a line-numbered read result the way upstream truncates it

A `read` or `grep` result that is a long line-numbered file dump — the shape
Cursor's and Codex's `read_file` produce — now reaches the model truncated the
way upstream truncates it: the first 120 lines and the last 60 are kept, and
the cut is marked. The head and the tail are byte-for-byte what the tool
returned, so the content stays recognisable.

Upstream selects its filter by the *shape* of the content, not by the tool
name (`open-sse/rtk/autodetect.js`), and has no tool-name protection at all.
A numbered dump is the one shape it truncates safely
(`open-sse/rtk/filters/readNumbered.js`). This port keeps the tool-name
protected list from #454 — a protected tool's result is not rewritten, because
the agent asked for that content and a rewritten copy is a different thing —
so a numbered dump was left intact even where upstream compresses it. On a
read-heavy session that is roughly 1.7x the bytes upstream would send for the
same results.

The thresholds are upstream's, unchanged: at least 250 lines
(`SMART_TRUNCATE_MIN_LINES`), a hit ratio of 0.7 over the first 100 sampled
lines (`READ_NUMBERED_MIN_HIT_RATIO`), and the line shape `/^\s*\d+\|/`. A
result that is not that shape is left exactly as it was, byte for byte.

Measured on the same 300-line dump, the port now gives the same numbers as
upstream's `readNumbered` run under `bun`: 10,691 bytes in, 6,413 bytes out,
and the marker `... +120 lines truncated (file continues)` in both — the two
outputs are byte-for-byte identical. On the pre-fix tree the same result came
back unchanged at 10,691 bytes.

The 250-line threshold is now defined once in the package and compared the
way upstream compares it. Upstream has one constant
(`SMART_TRUNCATE_MIN_LINES`) and two filters that read it, both with a `<`:
a payload of exactly 250 lines IS truncated, a shorter one is not. This port
had the number twice, with two different boundaries — the read-numbered path
used `<` (upstream's) and `smartTruncate` used `<=`, so a 250-line payload
was truncated by one and left intact by the other. The constant now lives
only in `SmartTruncMin` (`ReadNumberedMinLines` is an alias of it) and
`smartTruncate` compares with `<` as upstream does. The marker text of
`smartTruncate` is unchanged and still differs from upstream's; that
difference is pre-existing and out of scope for this change.

Deliberate divergences from upstream, recorded here as AGENTS.md §7.6 asks:

- The port keeps a tool-name protected list (`read`, `grep`, `edit`, `write`,
  `patch`, `anchor`) that upstream does not have. `patch` and `anchor` have no
  counterpart in either repository — no tool by either name exists, and
  upstream's filter set has no patch filter — so their provenance is
  unrecorded. They are left in place here; `patch` is a generic enough name
  that an unrelated third-party tool called `patch` would silently lose its
  compression.
- The port still skips compression for a protected tool whose content is *not*
  a numbered dump, where upstream would fall through to the rest of its
  shape-based selection (grep, find, ls, tree, ...). Upstream's whole
  autodetect ladder is not ported.
- The port's *generic* path does not reproduce upstream's shape-based
  selection at all. Upstream picks `readNumbered` by the content shape for
  **any** tool (`open-sse/rtk/autodetect.js`) — there is no tool-name
  protection upstream — so upstream truncates a numbered dump no matter which
  tool produced it. The port applies the numbered-dump truncation only to a
  **protected** tool; an unprotected tool's numbered dump is left to the
  port's own generic machinery, which applies a different filter set and
  produces a different result. This is pre-existing on `main` for the
  unprotected case and is not introduced by this change; it is recorded here
  because the protected/unprotected split is the shape of the whole
  difference.
- The protected path applies upstream's **fixed** constants and deliberately
  does **not** consult the operator's RTK config. The generic path in
  `CompressTextDetailedWithCommand` applies `cfg.Deduplicate`, the filter
  set (`MatchFilter` plus `cfg.Filters`), the category switch, and the
  limits from `resolveLimits` — where `cfg.MaxLines` and `cfg.Intensity`
  become a budget. The protected path applies none of them. Two reasons:
  upstream's `readNumbered` filter takes no config at all (it reads the
  constants and nothing else), so upstream's answer for a numbered dump is
  always 120/60 and threading `cfg` through would make the port's bytes
  differ from upstream's; and `cfg.Deduplicate` and the filter set rewrite
  content, which a protected tool's result must not have. The visible
  consequence: on the same 300-line dump the protected path yields 181
  lines (120 head + marker + 60 tail), the generic path with the default
  config yields 81 lines, and the generic path with `intensity: aggressive`
  yields 51. An operator who tightens the RTK config gets a tighter budget
  for unprotected tools and upstream's fixed constants for a protected
  `read`/`grep` result.

Verified with `go build ./...`, `go vet ./...`, `go test -count=1
./internal/tokensaver/...`, `go test -count=1 -race ./internal/tokensaver/...`
and `gofmt -l internal/tokensaver/`. The fix-test fails on the pre-fix parent
("a line-numbered dump under a protected tool was not compressed") and passes
here; the pre-fix parent returns the same dump unchanged, 10,691 bytes in and
10,691 bytes out. The control cases — a result that is not a numbered dump, a
dump below the 250-line threshold, and a dump below the 0.7 hit ratio — are
left untouched on both trees. The boundary is pinned from both sides in
`internal/tokensaver/truncate_boundary_test.go`: a 250-line payload IS
truncated and a 249-line one is NOT, for the read-numbered shape, for
`smartTruncate`, and end to end through a protected `read` result.
