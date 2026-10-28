### test(nous): pin the catalog to the verified §3.1 table

`TestNousCapabilities` asserted `ContextWindow >= 262144`, which passes for any
larger value. A later edit could move `meituan/longcat-2.0:free` from its
documented 1,048,756 down to 262,144 — quietly shrinking the largest window in
the catalogue by 75% — and the test would stay green.

The spec's acceptance criterion §7.1 asks for the table to be *declared
exactly* ("khai báo chính xác ... context window, max output tokens"), and a
lower bound is not an exact declaration. The test now carries all eight rows
from §3.1 as literal values, checks `MaxOutput` too (it was not checked at
all), and fails when the registry holds a model the table does not name — a
model added without a row would otherwise be asserted on by nothing.

- Not changed: any production code. The eight models already matched §3.1; the
  gap was in what the test could detect, not in what the registry declared.
- Evidence: `go test ./internal/providers/ -run TestNous` passes. Mutation-
  checked by lowering `longcat-2.0` to 262144 in `capabilities.go`, which fails
  with `expected contextWindow=1048756 (spec §3.1), got 262144`; restored after.
