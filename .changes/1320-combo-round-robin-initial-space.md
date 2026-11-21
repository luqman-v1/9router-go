---
### Fixed
- **Combo round-robin rotation skew** — The unweighted round-robin pointer now advances across the combo's initial configured target space (`comboStickyState.Space`) rather than the dynamic post-filtered candidate list, eliminating rotation skew and seat pinning when models are filtered out by capability or affinity.
- **Regression test** — `TestApplyComboStrategy_RoundRobinInitialSpaceFilteredEveryTurn` discriminates and fails on base (`heads: got map[p/a:3 p/c:3], want map[p/a:2 p/c:4]`), passing on head with the expected 2/4 split.