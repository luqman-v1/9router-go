### fix(search): reset regex state between model matches

A `/.../g` or `/.../y` model-picker query carried regex match state
across candidates and across repeated searches with a shared parsed
query, so matches were skipped non-deterministically. `lastIndex` is now
reset before every `test()` call, making each candidate an independent
search.

Mirrors upstream decolua/9router 5853a88a. Verified with the new
regression tests in `modelSearchFilter.test.ts`
(`bun test src/lib/modelSearchFilter.test.ts`).
