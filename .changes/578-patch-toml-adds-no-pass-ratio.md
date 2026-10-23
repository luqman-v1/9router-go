### Fixed

- **Stabilize `TestPatchTOMLAddsNoPass` timing ratio budget on CI runner.**
  `TestPatchTOMLAddsNoPass` intermittently failed on `main` (run 39935002391)
  under concurrent runner load when the ratio reached 1.36x–1.60x against a
  tight 1.35x budget. The test now warms up allocator state before timing and
  raises `maxRatio` to 1.80x, providing headroom for runner scheduling and GC
  jitter while deterministically catching any regression back to the two-pass
  quadratic parse (>= 2.05x).
