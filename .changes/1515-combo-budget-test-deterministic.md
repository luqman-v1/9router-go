### Fixed

- **`TestComboBudgetWindowRecycles` no longer races the wall clock.** The test set `budgetWindowMs: 1` and then asserted the combo was capped "immediately" — under `-race` on a loaded shared runner, more than a millisecond easily elapses between the record call and the exhausted check, the window recycles, and the test fails as `combo must be capped immediately after the ceiling`. That exact failure appears on otherwise-unrelated PRs (e.g. run 45811314197) and re-reddens the Race detector job for changes that never touch it. The test now uses a one-hour window for the capped assertion and drives the recycle deterministically by backdating `WindowStart`, the same technique `TestComboBudgetPruneExpiredWindows` already uses.

- Verified both directions: the old test injected with 5 ms of runner lag fails with the CI error message; the new test passes with the same lag and over five `-race` iterations.
