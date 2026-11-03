### Publish the Go coverage profile, and gate against a baseline

ROADMAP §4 asked for two things the suite produced neither of: a coverage
profile CI publishes, and a non-regression rule. The `coverage` job now runs
`go test -count=1 -covermode=atomic -coverprofile=coverage.out ./...`, uploads
the profile as an artifact, and fails when `scripts/coverage-gate.sh` reports a
total below `.github/coverage-baseline.txt`.

Two decisions worth stating, because both are the kind that look right and are
not:

- **The gate is a single total, not a per-package floor.** A per-package floor
  sounds stricter but is the opposite: it pins every package's number, so any
  refactor that legitimately moves logic between two files fails the gate, and
  the only way to keep it green is to keep widening the baseline — which is
  exactly the "silently lowering or ignoring the result" the roadmap warns
  against. The total tracks the property a reviewer actually cares about (is
  the suite still covering the code?) while leaving the distribution free to
  move. A deliberate reduction is a one-line edit to the baseline file plus a
  reason in the PR body.
- **The gate runs on the un-raced suite.** `-race` instruments every allocation,
  which is orthogonal to which statements execute, so pairing the two would
  double the job's runtime to produce the same total. The profile uses
  `-covermode=atomic` regardless, because the default `set` mode is undefined
  under concurrent execution and several packages run tests in parallel — which
  would otherwise make the total vary between runs for no reason.

The baseline is 68.0%, measured 68.7% on `main`. The margin is deliberate: an
exact baseline would fail on ordinary run-to-run variation.

The gate was verified against the real profile, including a negative control —
a baseline above the measured total fails, and a baseline exactly equal to it
passes.

### Verified

- `go test -count=1 -coverprofile=coverage.out ./...` — suite passes, profile
  written, `go tool cover -func` reports `total: 68.7%`.
- `scripts/coverage-gate.sh coverage.out` logic verified against the real
  profile: 68.7% vs baseline 68.0% passes; vs 99% fails; vs 68.7% (equality)
  passes.
- `go vet ./...` clean; `go build ./...` clean.
- `coverage.out` is covered by the existing `*.out` gitignore rule, so the
  profile never enters the tree.
- ROADMAP §4 and TECHNICAL_DEBT CI-01 updated to record what is now in place
  rather than leaving the register overstating open work.
