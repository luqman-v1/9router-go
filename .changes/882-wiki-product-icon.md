### Wiki: the product's own icon, not a stand-in

The wiki and the project page each drew their own logo: a terracotta plate with the letter **9** in it, in `#e56a4a`. The product's actual icon — the one the dashboard serves from `web/public/favicon.svg` and the one a reader sees in the repository — is a dark plate with an orange core and four coloured satellites. The wiki was showing a different logo in a different colour, on every page.

- **The real icon is now the mark.** The masthead wordmark, the phone sheet head, and both favicons draw the same SVG: plate `#181D27`, core `#FF5C35`, satellites `#FF8469` / `#38BDF8` / `#34D399` / `#A78BFA`. Inline SVG rather than a letter in a coloured box, so it is the actual glyph at any size.
- **No second plate.** `.wordmark-mark` and `.wiki-sheet-mark` were painting `--mark-bg` behind the glyph. The real icon carries its own dark plate, so a background here was a plate behind a plate — and the CSS radius and the SVG's own 12-unit corner radius were two radii on one 28px square. Both rules are now transparent containers sized to hold the SVG.
- **`--mark-bg` and `--mark-fg` are gone.** They existed only to colour that stand-in plate. Four blocks across light, dark, `prefers-color-scheme` and the class overrides — all dead once the mark became the real icon.
- **Pinned.** Both test files now fail if the icon's core circle disappears, if the sheet mark paints a plate again, or if the wordmark goes back to `>9</span>`.

Verified in a browser: the masthead mark is 28×28 with `background-color: rgba(0,0,0,0)`, five circles, core `#FF5C35`, plate `#181D27`, and no text. The phone sheet head's mark is 20×20 with the same glyph.
