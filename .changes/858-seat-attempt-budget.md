### feat(executors): bound per-seat retry budget before combo fallback

- In combo execution and retry handling, bound wall-clock retry time per member seat via `SEAT_ATTEMPT_BUDGET_MS` and `providerOverrides.seatBudgetMs` (porting upstream `decolua/9router` PR #5341).
- Skips retry delay and advances to the next combo member immediately when seat elapsed time exceeds the allocated budget, preventing mute seats from exhausting downstream client deadlines (e.g. Cloudflare 524).
- Added unit tests in `internal/handlers/chat/combo_seat_budget_test.go`.
