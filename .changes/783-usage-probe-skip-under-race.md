### fix(ci): skip the usage-write latency probe under the race detector

`TestUsageWriteLatencyProbe` measures wall-clock cost per usage write, and the
race detector slows every memory access by roughly an order of magnitude: the
same daily-blob read-modify-write measured **18.6 ms** per request plain and
**152 ms** under `-race` on this tree. The probe ran in both modes anyway, so
on `main` it consumed most of the `Race detector` job's 10-minute budget and
the package timed out at `600.099s` (`758c5af`, run 40840542489, fixed in #783) — the whole
job failed on a measurement whose numbers were instrumentation, not the write
path.

The probe now skips itself when the binary was built with `-race`, and keeps
running in the plain `test` job, where its numbers mean what they claim. The
`-race` build's coverage of the write path is unaffected: the async writer's
concurrency contract (FIFO landing, drop-on-full, flush, synchronous mode)
lives in `usage_writer_test.go` and still runs under the detector.

Verified by mutation, not by reading the guard: with `raceDetectorEnabled`
forced to `false` in the `//go:build race` file,
`go test -race -run TestUsageWriteLatencyProbe/daily_blob_grown` times out at
100 s; restored, the same command skips in 1.1 s and the full package passes
`-race` in 482 s. Without `-race` the probe still runs end to end (96 s, all
five scenarios PASS).
