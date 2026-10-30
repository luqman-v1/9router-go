### Added

- **Combo-to-Combo fallback chain & nested combo expansion** (upstream #5307):
  - Support `fallbackCombos` in `ComboStrategy` to chain secondary combos when all members of a primary combo fail or time out on retryable errors.
  - Recursively expand nested combo members up to depth 5 with cycle detection.
  - Accurately report winning combo in `x-9router-combo` response headers.
  - Short-circuit on non-retryable 4xx client errors.
