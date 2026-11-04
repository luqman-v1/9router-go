### Wiki: the sticky search box no longer covers the masthead at the end of a page

Reported: the search box is sticky, but once a reader reached the end of a page it rose and covered the top bar.

**Cause.** `.wiki-side` is `position: sticky`, and a sticky box cannot travel further down than the bottom of its *containing block* — here the `.wiki-shell` grid area, which ended 64px above the bottom of the document because of the shell's own `padding-bottom: 4rem`. So on every page, at the end of the scroll, the sidebar was pushed up by exactly that 64px. Measured at the bottom of the longest page: the sidebar's top was 36px against a 68px masthead, so the search box covered **32px of the bar**.

**Fix.** The bottom padding moved off the shell and onto `.wiki-main`, the article column. It is the same 4rem of breathing room for the text, and on the article column it cannot bound the sidebar. The phone override keeps the shell's own 3rem and zeroes the article's margin, so the two do not add up to 7rem of dead space below 60rem.

**Pinned.** `TestShellDoesNotBoundTheStickySidebar` fails if `padding-bottom: 4rem` returns to the shell, if the article column loses the margin that replaced it, or if the phone override stops zeroing it. Verified with a negative control: reintroducing the shell padding fails the test, and restoring it passes.

Verified in a browser at 1280×800: at the top, middle and bottom of the page — and at the bottom of the longest page, `providers.html` — the search box holds `top: 100px` and overlaps the masthead by **0px**, where before it sat at 36px and covered 32px.
