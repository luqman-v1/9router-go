### fix(tests): stop the `Retry-After` http-date case from flaking on a second boundary

`TestOpencodeClosureError_PrefersUpstreamRetryAfter/http-date_is_honoured`
asserted an exact `5400s` for a carrier built from
`time.Now().Add(90 * time.Minute)`. `http.TimeFormat` carries only second
granularity, so formatting a time with sub-second precision truncates it, and
the elapsed time between building the header (in the table literal) and parsing
it (inside the subtest) can cost one more second. When the truncation and that
elapsed time together cross a second the resolved value is `5399s` and the case
fails.

Measured: the Race detector job failed on exactly this —
`Retry-After = 5399s, want 5400s from the date carrier` — on a commit whose only
change was a markdown file, which is what showed the failure was neither a race
nor related to that diff.

The assertion is now a range (`5395..5400`) around the ~90 minutes the carrier
names. That is the property under test — the carrier beats the classifier's own
60s estimate — and it is what the exact-second form was trying to say. The
hardcoded value was never the thing being pinned.

No production code changes: `parseRetryAfterHeader` and `ceilSeconds` behave
correctly, and `5399` is the honest answer for a date that was truncated.
