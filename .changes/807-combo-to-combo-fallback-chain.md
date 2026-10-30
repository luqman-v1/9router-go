### Added

- **Combo-to-Combo fallback chain & nested combo expansion** (implements upstream issue decolua/9router#5307):
  - Support `fallbackCombos` in `ComboStrategy` to chain secondary combos in order when all members of a primary combo fail or time out on retryable errors.
  - Recursively expand nested combo members up to depth 5 with cycle detection.
  - Ensure multi-hop fallback chains (`A -> [B, C]`) continue to subsequent fallback combos when an intermediate fallback combo fails retryably without writing premature terminal errors.
  - Accurately report winning combo in `x-9router-combo` response headers.
  - Short-circuit on non-retryable 4xx client errors.
