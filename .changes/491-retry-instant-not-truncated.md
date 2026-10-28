### test(chat): keep the retry instant the fixtures encode from being truncated away

The `Race detector` job failed on `TestHandleMessagesComboFallback_RetriesOnceOnBoundedRetryAfter`, and the cause is not runner timing.

The test's upstream handler built the instant it asked the router to wait for with `time.Now().Add(time.Second).Format(time.RFC3339)`. RFC3339 has second precision, so the instant was truncated: built at `12:00:00.900` it named `12:00:01.000`, which is 100 ms away rather than a second. The router reads that instant back and asks how long is left; when the answer is not positive there is no usable wait, the bounded second pass is skipped, and the upstream is hit once instead of twice.

A wait of one second became a wait of anywhere in `(0s, 1s]`, so the closer the request landed to a second boundary the less there was to lose. Measured by building the instant and reading it back after a delay:

| read latency | RFC3339 (before) | RFC3339Nano (after) |
|---|---|---|
| 200 µs | 4/3000 already elapsed | 0/3000 |
| 5 ms | 16/3000 already elapsed | 0/3000 |
| 20 ms | 61/3000 already elapsed | 0/3000 |

A CI runner under `-race` sits at the far end of that table, which is why the failure appears there and not on a quiet machine — 200 local runs reproduce nothing.

A shared helper now builds the instant once, with sub-second precision, and both fixtures use it. The router's reader keeps the fractional part, so the wait it reads back is the wait the test asked for.

A new test pins the property and fails **deterministically** on the old construction, with the base pinned late in a second because that is where a second-precision encoding loses the most:

```
the encoded wait lost its sub-second part: got 1ms, want 1s
```

No production file is touched. The behaviour under test is unchanged: a 429 whose wait is inside `brief429RetryTolerance` still earns the bounded second pass, and one whose wait is past it still does not.
