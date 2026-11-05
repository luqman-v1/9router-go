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

The baseline is 68.8%. The number that matters is the one the CI runner
produces, not the one a developer's workstation produces: the same suite
measures 69.0% on Windows and 68.9% on the Linux runner, so the baseline is set
from the Linux figure with 0.1 of margin below it, which is what the gate was
designed to carry. Measured on `main` at `d1f046ed` — the base this PR was
rebased onto, not this branch, which carried the #901 changes #902 was stacked
on.

The baseline is validated as exactly one decimal number, not merely as
"contains digits": `tr -dc '0-9.'` also accepts a file holding two numbers
(`68.0\n69.0` collapses to `68.069.0`) or a malformed one (`8.8.1`), and awk's
`+0` then reads the leading prefix, so the gate fails against a number nobody
wrote. Anything not matching `^[0-9]+(\.[0-9]+)?$` is rejected with the file's
content named in the error, so a typo is diagnosable without reading the script.

### Verified

- `go test -count=1 -covermode=atomic -coverprofile=coverage.out ./...` — suite
  passes. The total is platform-dependent: `go tool cover -func` reports 69.0%
  on Windows and the CI runner reports 68.9% on Linux. The baseline is set from
  the Linux figure, since that is the environment the gate runs in.
- `scripts/coverage-gate.sh coverage.out` run against the real profile: 69.0%
  vs baseline 68.8% passes; a baseline above the measured total exits 1 with
  the regressed message. Confirmed in CI, where the Linux total of 68.9% also
  clears the baseline.
- Baseline validation negative controls: `68.0\n69.0` and `8.8.1` — both
  accepted by the previous `tr -dc '0-9.'` — are now rejected; `68` and `68.5`
  are still accepted.
- `go vet ./...` clean; `go build ./...` clean.
- `coverage.out` is covered by the existing `*.out` gitignore rule, so the
  profile never enters the tree.
- ROADMAP §4 and TECHNICAL_DEBT CI-01 updated to record what is now in place
  rather than leaving the register overstating open work.
