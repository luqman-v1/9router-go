### Wiki: the sidebar footer is links only, and the theme switcher lives in the header

The sidebar footer carried two things that were not links: a `Version 1.9.14` caption and the phone's theme control. A reader scanning a list of rows could not tell at a glance that the version line does not go anywhere, and the theme row below it was one more thing to work out before reaching the links.

- **The version caption is gone.** It was the only text in that column that was not a control, and it sat under three rows that all were. Nothing else depends on it: the release the wiki describes is still in the page source and the search index.
- **The theme switcher is in the header bar, at its right edge, at every width.** It used to be two buttons — the bar's copy for a desktop, and a row in the footer for a phone, because the bar had no room below 60rem. The bar is a flex row of three items — trigger, wordmark, this — so it fits a 320px phone without one of them leaving. The sheet copy is deleted, and the media queries that swapped the two are deleted with it: if the query that hides the bar copy came back, a phone reader would have no theme control at all, which is what the new test pins.
- **`wikiData.Version` is removed.** The footer was the only consumer, so the field was set on every page and read by nothing.

Verified in a browser at 1280px and 390px: the footer reads "Project Repository Telegram group" — three links, zero buttons, no caption — and the bar button is 20px from the right edge and 14px from the top at both widths. The toggle was clicked on the phone: the `dark` class lands on `<html>`, the body repaints, and the accessible name flips to "Switch to light theme".
