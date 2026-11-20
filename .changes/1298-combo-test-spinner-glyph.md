### Fixed

- The combo card's action trigger rendered the literal text `progress_activity animate-spin` instead of a spinning icon while a combo test was running. The Material Symbols glyph name and the Tailwind `animate-spin` utility were both written into the span's **text**, so the font had no single ligature to draw and the browser showed the words. The utility now lives in the `class` attribute and the glyph text carries the icon name alone.

### Verification

- New test `web/src/lib/iconGlyphText.test.ts` fails on base (the offending literal is reported with file and line) and passes with the fix. It scans every `.svelte` file for a Material Symbols span whose quoted text contains whitespace — a glyph name never does.
- `bun test src scripts` → 880 pass / 0 fail. `bun run build` green. `bun run ratchet:svelte` → 0 unresolved identifiers, 74 errors (baseline 74).
- Live check on a scratch gateway: base trigger span text = `progress_activity animate-spin` (272 px wide, no icon drawn); head = `progress_activity` with `animate-spin` on the class (28 px, spinner drawn).
- Screenshots: `docs/screenshots/combo-test-spinner-before.png` (base) and `docs/screenshots/combo-test-spinner-after.png` (head).
