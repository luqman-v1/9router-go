### Wiki: the sidebar footer is centred against the column

The footer's contents — the three project icons and the release line beneath them — are now centred against the sidebar column instead of sitting at its left edge.

**Why.** `.wiki-side-foot` is a block, so both of its children took the column's left edge. The nav rows above it are inset by their own padding, so the footer read as a stray element hanging off the side rather than as the column's footer.

**How.** Centred on `.wiki-side-foot` itself — `display: flex; flex-direction: column; align-items: center; text-align: center` — rather than on each child. That keeps the two rows on one axis and survives a fourth child being added later without a second rule to remember. `text-align` is the companion: without it only the release line's *box* is centred, and its text still hugs the left edge inside it.

**The axis is the column, not the viewport.** The sidebar has 1.5rem of right padding and sits inside the shell's grid, so the visual centre is the sidebar's own content box — which is what `align-items` resolves against. Measured in the browser: the icon row and the release line both land 0px from the column's centre, against -8px for a nav row (which carries its own padding and is not the target here).

**Height unchanged.** `flex-shrink: 0` and `margin-top: auto` are untouched: the footer is the column's last flex child, and every pixel it costs is a pixel the nav loses — the measurement that took it from 85px to 59px earlier is not given back.

Verified in a browser at 1280×900 and at 390×844 (inside the nav sheet): footer, icon row and release line each 0px from the column centre on both, and no request or console error. `TestWikiSideFooterIsCentred` pins the four declarations — `align-items: center`, `text-align: center`, `flex-direction: column` (centring must not collapse the two rows into one) and `flex-shrink: 0` — and was checked against a negative control that removes the centring.
