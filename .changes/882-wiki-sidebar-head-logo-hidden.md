### Wiki: the sidebar's logo and label are hidden on desktop

The sidebar's head row no longer shows the product mark or the `9router-go` name above 60rem. On a phone the sheet still shows both.

**Why.** The row was phone-only, because the masthead already carried the mark and the name on desktop and a second copy put two logos in one view. It then became visible at every width to hold the search button — which reintroduced exactly that duplication: a logo and a label in the sidebar, and the same logo and label in the masthead above it, in the same view.

**What stays and what goes.** `.wiki-sheet-head strong` is `display: none` at the base level and restored inside `@media (max-width: 60rem)`, the same contract the close control uses. The `<strong>` element itself stays in the markup: it is what makes the row the product's row, and on a phone it is the only chrome that is always visible and names the product. Only its rendering is scoped.

**The row itself does not go.** It carries the search button, so hiding the row would hide search with it. Hiding only its first child leaves the button at the top of the column where a reader looks for it — measured at the column's top edge, 25px from the sidebar's right edge, which is the column's own 1.5rem padding. On desktop the row is 40px tall, which is exactly the button: nothing of the logo's 1.25rem mark or its label is left to space.

**The article gains that space back.** The sidebar's first link used to start below a 40px row that held a 20px mark and a label. It now starts below the button alone, so the nav's first row sits higher and the column reads as a list of destinations rather than as a page with a header.

Verified in a real browser. Desktop 1280×900: the `<strong>` is `display: none` and has zero width, the masthead still carries the mark and the name, the head row is 40px tall and holds only the search button, and clicking the button still opens the dialog. Phone 390×844 with the sheet open: the `<strong>` is `display: flex`, visible, and reads `9router-go`; the close control is back at the row's left and the search button at its right. No 4xx, no console errors.
