### Wiki: clicking the search box opens a modal with the results

Clicking or focusing the sidebar search box now opens a `<dialog>` containing the query field and its results, instead of typing in place.

**Why it changed.** The old behaviour rendered results into a `<ul>` inside the sidebar's `.wiki-search` block — a 15rem column. Every heading hit was truncated to an unreadable width, and on a phone the same list lived inside the full-screen nav sheet, where it competed with the nav itself and scrolled the article behind it. The query now belongs to a surface sized for it: `min(38rem, 100vw - 2rem)` wide, `min(32rem, 100vh - 4rem)` tall, with the results list as the only scrolling region (`max-height: 20rem; overflow-y: auto; overscroll-behavior: contain`).

**The trigger is read-only on purpose.** The sidebar box keeps its placeholder and gains `readonly`, `aria-haspopup="dialog"` and `aria-expanded`, plus a `Click to search` hint — so it still reads as a control and a screen reader announces what it does, while typing belongs to the dialog. Click, Enter and Space all open it; the dialog's input takes focus and selects its text so a fresh query starts from empty.

**`<dialog>` supplies the semantics.** `showModal()` gives the top layer, the focus trap and Escape; `::backdrop` paints the scrim (`rgb(15 23 42 / 0.45)`). No JS reimplements any of it, and the close paths are the three a reader expects: the ✕ button, Escape (via the `cancel` event, which also resets `aria-expanded` on the trigger), and a click on the backdrop — measured by the dialog's own bounding rect, because the box fills the dialog's content area.

**The dialog is a direct child of `<body>`, not of the sidebar.** Below 60rem that `<aside>` is `position: fixed` with transform and visibility transitions. A dialog inside it inherits the sheet's transform, is unreachable while the sheet is closed, and covers a sheet that is itself full-screen. `TestSearchDialogIsOutsideTheSidebar` fails if the dialog ever moves back inside.

**Result details that came with the move.** While the index is in flight the dialog says `Loading…` rather than showing an empty list that reads as "no results"; a query that matches more than the 12-row cap now ends with `N more matches — refine the query to narrow them down.`, so a broad search does not look like it found everything; and a shared link carrying `?q=` opens the dialog already showing its results instead of asking the reader to retype. Picking a result navigates and closes the dialog behind it.

**The sidebar's old result list is gone.** `#results` had no code touching it after the move, so it was a dead element that could only confuse — removed rather than left hidden.

Verified in a real browser on desktop (1280×900) and a phone viewport (390×844): the trigger is read-only and the modal starts closed; clicking opens it with focus in the dialog's input; typing `combo` renders 13 rows with page/heading provenance and a `162 more matches` footer; `zzzzqqq` shows `No match for "zzzzqqq"`; Escape, the ✕ button, a backdrop click and picking a result each close it; Enter on the focused trigger opens it; a result navigates to its page and anchor with the dialog closed. No 4xx, no console errors. Desktop modal measures 608×468, phone 352×468 and fully inside the viewport.
