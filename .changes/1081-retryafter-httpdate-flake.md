### Fixed

- **chat:** `TestRetryAfterWait_Extraction`'s HTTP-date header is now built from an instant rounded up to the
  next second boundary. An HTTP-date carries no sub-second part, so a date written from a mid-second instant
  was up to a second behind the wait it named once formatted, and the test's ±1s slack could be exceeded by
  that truncation plus read latency — a one-off CI failure on `main` (run 42957731619, subtest
  "header http date"), proven deterministically with a pinned-clock probe (old construction diff −1.005s,
  new −5ms). Test-only; no production behavior change.
