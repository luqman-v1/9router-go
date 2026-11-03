### Wiki: footer links are icon-only, and the release the docs describe is named

The sidebar footer spent three rows of the sidebar's height on three links whose labels repeat what their marks already say, and a reader who wants to know which release the pages describe had no number to check against.

- **Icon-only, in a row.** The three links — project page, repository, Telegram — are now glyphs at 44×44, one line tall instead of three. The glyph grew to 20px: an icon carrying the label's whole job has to be readable at a glance, and 18px inside a 44px target reads as a speck.
- **The accessible name moved to `aria-label`.** Removing the visible label removes the only text a screen reader had, so each link now carries `aria-label` with the SVG `aria-hidden` — the destination is announced, not the picture of it. `title` covers the pointer reader on hover, which is the one thing `aria-label` does not.
- **The release line is back, and it is the right one.** `Documents release 1.9.14` sits under the row, rendered from `version.json` rather than the build version — on an experimental line those differ, and naming the build told readers the docs described a release nobody could download. It is omitted when the generator is given no version, so a local run does not print a bare label.

Verified in a browser at 1280px and 390px: the row is `flex-direction: row`, three links, no visible text on any of them, each a 44×44 target, and the release line reads `Documents release 1.9.14` 61px below the top of the footer at both widths. On the phone the links are inside the sheet, which is closed on load — with it open, `elementFromPoint` over the first icon hits the link, so the row is genuinely clickable and not covered by the article behind it.
