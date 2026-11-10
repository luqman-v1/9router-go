### fix(tests): stop two time- and eviction-dependent tests from flaking CI

Both failures were seen on the Race detector and `test` jobs, on commits that
could not have caused them (one changed only a markdown file), which is what
showed they were pre-existing flakes rather than regressions.

**`TestOpencodeClosureError_PrefersUpstreamRetryAfter/http-date_is_honoured`**
asserted an exact `5400s` for a carrier built from
`time.Now().Add(90 * time.Minute)`. `http.TimeFormat` carries only second
granularity, so formatting a sub-second time truncates it, and the elapsed time
between building the header (in the table literal) and parsing it (inside the
subtest) can cost one more second. When the truncation and that elapsed time
together cross a second the resolved value is `5399s` and the case fails:

```
Retry-After = 5399s, want 5400s from the date carrier
```

The assertion is now a range (`5395..5400`) around the ~90 minutes the carrier
names. That is the property under test — the carrier beats the classifier's own
60s estimate — and it is what the exact-second form was trying to say. `5399`
is the honest answer for a date that was truncated, so no production code
changes.

**`TestFreePiResetCooldownIsBounded`** filled the cooldown map to its 256-entry
cap with a single shared timestamp, then asserted that a later-inserted account
was still present on the next call. Every entry carries the same `at`, so when
the prune at the cap has to evict the oldest it is choosing arbitrarily among
ties — and `acct-fresh` could be the one evicted by the prune inside the very
call that asserts on it. The lookup then misses, the attempt is allowed, and the
case fails at `free_pi_session_test.go:333`.

Measured at 2 in 500 (≈0.4%, matching the predicted 1-in-257), which is why 300
local runs missed it and CI hit it. The two halves of the test are independent
assertions, so the map is now emptied between them.

Covered by stressing both cases under `-race` (`TestFreePi` ×300, closure ×3).
No production code changes in either fix.
