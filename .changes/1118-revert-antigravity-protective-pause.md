### fix(antigravity): revert global protective pause to restore account fallback in combos

- Revert PR #1093 / #1094: Remove the 6-hour pool-wide `ProtectivePause` on Antigravity `TOS_VIOLATION`. The speculative pause broke normal account rotation in combos, skipping all Antigravity accounts when any single account encountered a TOS refusal instead of falling back to healthy sibling accounts.
