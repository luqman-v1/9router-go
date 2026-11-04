### Wiki: one logo, in the masthead

The sidebar carried its own header row — the same square mark and the same product name as the masthead above it. In one view a reader saw two logos and two `9router-go` wordmarks, and had to work out whether the two differed. They did not: both linked to `index.html`.

- **The sidebar header row is gone.** The sidebar now begins at the search box, which is the first thing it does that the masthead does not. The wordmark stays where it was, in the bar, on every screen — so nothing that used to be reachable from the sidebar's copy became unreachable.
- **The phone sheet head keeps its mark, under its own class.** There the bar is the only chrome that is always visible, and the sheet is where a phone reader lands, so the mark is doing a job the masthead cannot do from behind it. It was sharing `.wiki-side-mark` with the desktop row, which meant the class was deleted along with the row's CSS and the phone sheet head lost its styling — it now has `.wiki-sheet-mark` and its own rule, drawing from the same `--mark-bg`/`--mark-fg` pair so the two marks a phone reader sees are the same colour.
- **Pinned by a test.** `TestNavTriggerLivesInTheHeaderBar` now fails if `wiki-side-head` or `wiki-side-title` comes back, if the sheet mark loses its class or its colour pair, or if a second wordmark appears anywhere.

Verified in a browser: at 1280px exactly one logo is visible (`wordmark-mark`), the sidebar's first element is the search box, and the nav still lists 21 links. At 390px with the sheet closed one logo is visible; with it open there are two — the masthead's and the sheet head's — and the sheet mark renders at `rgb(176, 62, 31)`, the same terracotta as the masthead plate.
