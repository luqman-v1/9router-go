#!/usr/bin/env bash
# Coverage gate: prove the total coverage of the Go suite has not regressed.
#
# ROADMAP §4 asks for two things: a coverage profile CI publishes, and a
# non-regression rule. This is the second half. The profile itself is uploaded
# as an artifact by the workflow; this script turns it into a pass/fail.
#
# The threshold is a single total, not a per-package floor. A per-package floor
# sounds stricter but is not: it pins every package's number, so any refactor
# that legitimately moves logic between two files fails the gate, and the only
# way to keep it green is to keep widening the baseline — which is exactly the
# "silently lowering or ignoring the result" the roadmap warns against. A total
# tracks the property the reader cares about (is the suite still covering the
# code?) while leaving the distribution free to move.
#
# Usage: scripts/coverage-gate.sh [profile-path]
# Exit 0 when the total meets or exceeds the baseline, 1 otherwise.
set -euo pipefail

cd "$(dirname "$0")/.."

BASELINE_FILE=".github/coverage-baseline.txt"
PROFILE="${1:-coverage.out}"

if [[ ! -f "$PROFILE" ]]; then
  echo "::error::coverage profile not found: $PROFILE"
  exit 1
fi

# `go tool cover -func` prints one row per function plus a final `total:` row
# whose percentage is statements-covered / statements-total. The row is padded
# with runs of tabs, so the percentage is the *last* field, not a fixed index:
# `$3` on the total row lands in the padding and reads empty. Take that row's
# percentage rather than re-deriving it: the tool already does the arithmetic,
# and re-deriving it from the profile's count fields would be a second
# implementation of the same sum.
TOTAL_PCT="$(go tool cover -func="$PROFILE" | awk -F'\t' '$1 == "total:" { gsub(/%|[[:space:]]/, "", $NF); print $NF }')"

if [[ -z "$TOTAL_PCT" ]]; then
  echo "::error::no total row in $PROFILE"
  exit 1
fi

if [[ ! -f "$BASELINE_FILE" ]]; then
  echo "::error::coverage baseline missing: $BASELINE_FILE"
  echo "Create it with the current total so the gate has something to compare against."
  exit 1
fi

BASELINE="$(tr -dc '0-9.' < "$BASELINE_FILE")"

if [[ -z "$BASELINE" ]]; then
  echo "::error::coverage baseline is not a number: $(cat "$BASELINE_FILE")"
  exit 1
fi

# awk, not bash arithmetic: both operands are decimals.
STATUS="$(awk -v c="$TOTAL_PCT" -v b="$BASELINE" 'BEGIN { print (c + 0 >= b + 0) ? "ok" : "regressed" }')"

echo "coverage total: ${TOTAL_PCT}% (baseline ${BASELINE}%)"

if [[ "$STATUS" == "ok" ]]; then
  exit 0
fi

echo "::error::Go coverage regressed: ${TOTAL_PCT}% < ${BASELINE}%"
echo "Either the change removed tests, or it added uncovered code. Add tests,"
echo "or — when the drop is a deliberate, reviewed reduction — update"
echo "$BASELINE_FILE and say why in the PR body."
exit 1
