package main

// wikiData is what the wiki template renders.
type wikiData struct {
	Page  *page
	Pages []*page
	// Groups is the sidebar split by section heading. The first group has no
	// heading, which is how the dashboard's own top nav group renders.
	Groups []pageGroup
	Repo   string
	// StableVersion is the release the wiki documents, read from version.json by
	// the generator. It is rendered under the footer links so a reader can check
	// which release the contract on these pages belongs to — the docs for two
	// releases sit at the same URL and are otherwise indistinguishable. It is
	// deliberately not the build version: on an experimental line that is a
	// version nobody can download.
	StableVersion string
}

// pageGroup is one sidebar section: its heading (empty for the first group, which
// renders without one) and the pages under it, already sorted.
type pageGroup struct {
	Label string
	Pages []*page
}

// groupBySection splits the ordered pages into sidebar sections. A page with no
// section joins the first group — the one that renders without a heading — so a
// page that forgets the field still shows up, and no stray unnamed heading
// appears at the end of the list.
func groupBySection(pages []*page) []pageGroup {
	var groups []pageGroup
	index := map[string]int{}
	for _, p := range pages {
		label := p.Section
		i, ok := index[label]
		if !ok {
			// An unsectioned page belongs to whichever group came first. On an
			// empty list that is a new unlabelled group; otherwise it is group 0,
			// so the heading never changes because of a missing field.
			if label == "" && len(groups) > 0 {
				i = 0
			} else {
				i = len(groups)
				groups = append(groups, pageGroup{Label: label})
			}
			index[label] = i
		}
		groups[i].Pages = append(groups[i].Pages, p)
	}
	return groups
}

// wikiTemplate is the chrome around a rendered wiki page: the same masthead and
// stylesheet the project page uses, a sidebar built from every page, the article
// itself, and the client-side search box.
//
// Paths are relative because the site is served from the /9router-go/ project
// subpath, where a root-absolute /style.css would 404. The README is one level
// up at index.html, so the wordmark points there.
//
// The search box is deliberately plain: it reads search-index.json, which the
// generator writes next to these pages, and navigates to the matching page and
// heading. No framework, no third-party request — the site stays on the
// gateway's own origin, per the first-paint budget.
const wikiTemplate = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>{{ .Page.Title }} — 9router-go Wiki</title>
    <meta name="description" content="9router-go documentation: {{ .Page.Title }}" />
    <link rel="stylesheet" href="style.css" />
    <!-- The product's own icon, not a stand-in: the same SVG the dashboard
         serves from web/public/favicon.svg. The dark plate, the orange core and
         the four coloured satellites are the mark a reader already knows from
         the dashboard and the repository, so the wiki agrees with them instead
         of drawing a second "9" plate in a different colour. Inlined as a data
         URI rather than shipped as a file: the wiki embeds one stylesheet and
         would otherwise need a second asset for a 48px image. -->
    <link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' fill='none' viewBox='0 0 48 48'%3E%3Crect width='48' height='48' fill='%23181D27' rx='12'/%3E%3Crect width='47' height='47' x='.5' y='.5' stroke='%232B3245' rx='11.5'/%3E%3Ccircle cx='24' cy='24' r='7' fill='%23FF5C35'/%3E%3Ccircle cx='14' cy='16' r='3.5' fill='%23FF8469'/%3E%3Ccircle cx='34' cy='16' r='3.5' fill='%2338BDF8'/%3E%3Ccircle cx='14' cy='32' r='3.5' fill='%2334D399'/%3E%3Ccircle cx='34' cy='32' r='3.5' fill='%23A78BFA'/%3E%3Cpath stroke='%2394A3B8' stroke-linecap='round' stroke-width='1.75' d='M16.5 18L21 21.5M31.5 18L27 21.5M16.5 30L21 26.5M31.5 30L27 26.5'/%3E%3C/svg%3E" />
    <script>
      // Apply the stored theme before the first paint. Without this the page
      // renders in whatever the OS prefers and then repaints a moment later,
      // which is a white flash on a reader who chose dark — the exact symptom
      // the dashboard's own boot avoids. Reads "9router-theme", the same key
      // TopBar.svelte writes, so a choice made in either surface holds in both.
      // Falls back to the OS preference, which is what the media query in
      // style.css provides on its own.
      (function () {
        try {
          var stored = localStorage.getItem("9router-theme");
          if (stored !== "light" && stored !== "dark") return;
          var root = document.documentElement;
          root.classList.toggle("dark", stored === "dark");
          root.classList.toggle("light", stored === "light");
        } catch (e) {
          // Storage unavailable: the media query stands as the theme.
        }
      })();
    </script>
    <style>
      /* Layout for the wiki shell. Everything visible reuses the palette from
         site/style.css so the wiki and the dashboard read as one product; these
         rules only position the sidebar and the search box. */
      .wiki-shell {
        display: grid;
        grid-template-columns: 15rem minmax(0, 1fr);
        gap: 2rem;
        max-width: 76rem;
        margin: 0 auto;
        /* No bottom padding here. The shell is the grid area that the sticky
           sidebar is measured against, and a sticky box cannot travel further
           down than the bottom of its containing block — so this padding used
           to stop the sidebar 64px short of the window bottom and push the
           search box up over the masthead once the reader reached the end of
           the page. Measured at the bottom of the longest page: the sidebar's
           top was 36px against a 68px masthead, so the search box covered 32px
           of the bar. The bottom margin belongs on the article column, where it
           is breathing room for the text and cannot move the sidebar. */
        padding: 2rem 1.25rem 0;
        align-items: start;
      }
      /* The sidebar is a sticky column that runs the full height of the window
         minus the masthead, and scrolls its own contents. Before the fix it was
         sticky with no height bound: on a short window the 21 nav links ran
         past the bottom of the screen with no scrollbar, because the article
         scrolled and the sidebar did not. align-self stretch on the grid child
         plus a height in viewport units is what makes it full height instead of
         only as tall as its content. */
      .wiki-side {
        position: sticky;
        /* The sticky offset equals the measured distance from the top of the
           window to the top of the column: the 68px masthead plus the shell's
           32px of padding. It used to be 5.5rem, which left the box 12px lower
           than it needed to be. */
        top: 6.25rem;
        /* Full height means the box reaches the bottom of the window, not that
           it is as tall as its content. Measured: the masthead is 68px and the
           shell adds 32px of padding above the sidebar, so the column starts
           100px down and needs the window height minus that to reach the
           bottom edge — not 7.5rem, which stops 20px short. */
        height: calc(100vh - 6.25rem);
        align-self: stretch;
        border-right: 1px solid var(--border-subtle);
        padding-right: 1.5rem;
        display: flex;
        flex-direction: column;
        overflow: hidden;
        overscroll-behavior: contain;
      }
      /* The middle of the column: the nav groups. It is the only part that
         scrolls, so the header and search above it and the footer below are all
         pinned without a second scroll container. min-height 0 is what makes a
         flex child actually able to shrink and scroll — without it the child
         keeps its content height and pushes the footer off the bottom. */
      .wiki-side-scroll {
        flex: 1 1 auto;
        min-height: 0;
        overflow-y: auto;
        overscroll-behavior: contain;
        scrollbar-gutter: stable;
      }
      /* The footer is the last flex child of the column, so it sits at the
         bottom and never scrolls: the middle above it is the part that moves.
         It is not position:sticky — a sticky child inside a flex column whose
         sibling scrolls would still be pushed by that sibling's content. */
      .wiki-side-foot {
        flex-shrink: 0;
        margin-top: auto;
        /* Centre the footer's contents against the sidebar column, the way the
           nav rows above it are centred by their own padding. The footer is a
           block, so its two children — the icon row and the release line — both
           default to the left edge of the column. Centring here rather than on
           each child keeps the two rows on one axis, and it survives a fourth
           child being added later without a second rule to remember.
           The alignment is to the column, not the viewport: the sidebar has
           1.5rem of right padding and sits inside the shell's grid, so the
           visual centre of the column is the sidebar's own content box. */
        display: flex;
        flex-direction: column;
        align-items: center;
        text-align: center;
        /* The footer is chrome, and every pixel it costs is a pixel the nav
           loses. It measured 85px — 44 of which was one row of icons at a
           target size the nav rows above it do not need either (they are 33px).
           Padding is now even and smaller, and the icon row is 32px: still above
           the 24px minimum target floor, and 13px shorter. */
        padding: 0.5rem 0 0.375rem;
        background: var(--bg);
        border-top: 1px solid var(--border-subtle);
      }
      #project-links {
        display: flex;
        flex-direction: row;
        gap: 0.25rem;
      }
      /* The three project links. Icons only, in one row: these are three
         destinations a reader recognises by their mark, and a label beside each
         would spend three lines of the sidebar's height to say what the mark
         already says. Horizontal, so the row costs about a line and a half and
         leaves the release line beneath it room to be read.
         Icon-only, so the accessible name lives in aria-label and the SVG is
         aria-hidden — a screen reader announces the destination, not the
         picture of it. title is what a pointer reader gets on hover, which is
         the one thing aria-label does not cover. */
      #project-links a {
        display: flex;
        align-items: center;
        justify-content: center;
        /* 32px square, not the 44 the first icon-only version used. 44 was
           measured against the nav rows and found to be the same — but they are
           33px, so the footer was the one place demanding a target its own
           siblings do not get. 32px is still above the 24px floor and is what
           makes the footer 13px shorter, which the nav spends on links. */
        width: 2rem;
        height: 2rem;
        padding: 0;
        border-radius: var(--radius);
        color: var(--text-muted);
        text-decoration: none;
      }
      #project-links a:hover {
        background: var(--bg-alt);
        color: var(--text);
      }
      #project-links a:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: 1px;
      }
      /* The glyph is muted by default and lifts on hover, so the control lights
         up as one thing. 18px, not 20: at a 32px target a 20px glyph leaves 6px
         of breathing room and the hover tint behind it barely shows. 18 leaves
         7px per side and the tint reads as a plate under the icon. */
      .wiki-foot-icon {
        width: 1.125rem;
        height: 1.125rem;
        flex-shrink: 0;
        color: var(--text-subtle);
      }
      #project-links a:hover .wiki-foot-icon {
        color: var(--brand);
      }
      /* The release the wiki documents. It sits under the links because it is
         metadata about them, not a fourth destination: it is the one number a
         reader can check the update command against. Muted and small so it
         reads as build provenance rather than something to click — it is the
         only text in the footer, and every other thing here is a control. */
      .wiki-version {
        margin: 0.1875rem 0 0;
        font-size: 0.75rem;
        color: var(--text-subtle);
      }
      /* A section heading, in the dashboard's own idiom: small, uppercase, and
         tracked, so it reads as a label for the rows beneath it rather than as
         a page in its own right. */
      .wiki-side h2.wiki-group-title {
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        color: var(--text-muted);
        margin: 0 0 0.5rem;
        padding-left: 0.75rem;
      }
      /* Sections after the first are separated by space and a hairline, the way
         the dashboard separates its nav groups. The first group has no heading
         and needs no rule above it. */
      .wiki-group + .wiki-group {
        margin-top: 1.25rem;
        padding-top: 1.25rem;
        border-top: 1px solid var(--border-subtle);
      }
      .wiki-side ol {
        list-style: none;
        margin: 0;
        padding: 0;
      }
      .wiki-side li + li {
        margin-top: 0.125rem;
      }
      /* Marks a page that documents behaviour newer than the release this wiki
         describes. It sits after the link rather than inside it, so the badge is
         not part of the row's hit area and does not inherit its hover. The
         border carries the colour: at 0.6875rem a filled pill in --warning
         fails contrast against the sidebar background, and the failure is on a
         piece of text a reader relies on to know the page is ahead. */
      .wiki-since {
        margin-left: 0.375rem;
        padding: 0.0625rem 0.3125rem;
        border: 1px solid var(--warning, #d97706);
        border-radius: 999px;
        color: var(--text-subtle);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.02em;
        text-transform: uppercase;
        vertical-align: 0.05em;
      }
      .wiki-side a {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.375rem 0.75rem;
        border-radius: var(--radius);
        color: var(--text-muted);
        font-size: 0.8125rem;
        text-decoration: none;
        transition: background-color 0.15s ease, color 0.15s ease;
      }
      /* The same hover the dashboard sidebar uses: muted text lifts to the main
         colour over a flat tint, with the brand colour on the marker. A hover
         that only changes the background leaves the label looking disabled. */
      .wiki-side a:hover {
        background: var(--bg-alt);
        color: var(--text);
      }
      .wiki-side a:hover::before {
        background: var(--brand);
      }
      /* A 2px rail marks the row under the pointer, the way the dashboard marks
         its active row. It is the affordance that says the whole row is a
         target, not just the words. */
      .wiki-side a::before {
        content: "";
        width: 2px;
        height: 0.875rem;
        border-radius: 1px;
        background: transparent;
        transition: background-color 0.15s ease;
      }
      .wiki-side a[aria-current="page"] {
        background: var(--bg-alt);
        color: var(--text);
        font-weight: 600;
      }
      .wiki-side a[aria-current="page"]::before {
        background: var(--brand);
      }
      .wiki-side a[aria-current="page"]:hover {
        background: var(--bg-alt);
        color: var(--text);
      }
      .wiki-side a:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: -2px;
      }
      .wiki-results {
        list-style: none;
        margin: 0.5rem 0 0;
        padding: 0;
        font-size: 0.875rem;
        max-height: 18rem;
        overflow-y: auto;
      }
      .wiki-results li + li {
        margin-top: 0.125rem;
      }
      .wiki-results a {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.375rem 0.75rem;
        border-radius: var(--radius);
        color: var(--text-muted);
        font-size: 0.8125rem;
        text-decoration: none;
        transition: background-color 0.15s ease, color 0.15s ease;
      }
      /* Same hover as the sidebar rows, so a search result and a nav entry feel
         like the same kind of target. */
      .wiki-results a:hover,
      .wiki-results a:focus-visible {
        background: var(--bg-alt);
        color: var(--text);
      }
      .wiki-results .where {
        color: var(--text-muted);
        font-size: 0.75rem;
      }
      /* The search modal. It is a <dialog>, so the browser supplies the layer,
         the top layer placement and the Escape key; this only gives it the same
         surface tokens as the rest of the shell. The sidebar box that opens it
         is read-only — typing happened there before and produced a list
         squeezed into a 15rem column. */
      .wiki-search-modal {
        width: min(38rem, calc(100vw - 2rem));
        max-height: min(32rem, calc(100vh - 4rem));
        padding: 0;
        border: 1px solid var(--border);
        border-radius: var(--radius-lg);
        background: var(--surface);
        color: var(--text);
        box-shadow: 0 24px 60px rgb(0 0 0 / 0.22);
        flex-direction: column;
        overflow: hidden;
      }
      /* A dialog that is not open must not reserve layout, and must not be
         reachable by keyboard. */
      .wiki-search-modal:not([open]) {
        display: none;
      }
      .wiki-search-modal[open] {
        display: flex;
      }
      .wiki-search-modal::backdrop {
        background: rgb(15 23 42 / 0.45);
        backdrop-filter: blur(2px);
      }
      .wiki-search-modal-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 1rem;
        padding: 1rem 1.25rem 0.75rem;
        border-bottom: 1px solid var(--border-subtle);
      }
      .wiki-search-modal-title {
        margin: 0;
        font-size: 0.9375rem;
        font-weight: 600;
        color: var(--text-muted);
        text-transform: uppercase;
        letter-spacing: 0.04em;
      }
      .wiki-search-modal-close {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 2rem;
        height: 2rem;
        padding: 0;
        border: 1px solid var(--border);
        border-radius: var(--radius);
        background: var(--surface);
        color: var(--text-muted);
        font-size: 0.875rem;
        line-height: 1;
        cursor: pointer;
      }
      .wiki-search-modal-close:hover {
        background: var(--bg-alt);
        color: var(--text);
      }
      .wiki-search-modal-close:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: 1px;
      }
      .wiki-search-modal-field {
        padding: 0.875rem 1.25rem 0.75rem;
      }
      .wiki-search-modal-field input {
        width: 100%;
        padding: 0.625rem 0.875rem;
        border: 1px solid var(--border);
        border-radius: var(--radius);
        background: var(--bg);
        color: var(--text);
        font: inherit;
        font-size: 1rem;
      }
      .wiki-search-modal-field input:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: 1px;
      }
      /* The result list is the only part that scrolls, so the head and the
         field stay put while a long list moves under them. */
      .wiki-search-modal-results {
        margin: 0;
        padding: 0 1.25rem 1.25rem;
        font-size: 0.9375rem;
        max-height: 20rem;
        overflow-y: auto;
        overscroll-behavior: contain;
      }
      .wiki-search-modal-results a {
        padding: 0.5rem 0.75rem;
        font-size: 0.9375rem;
      }
      /* The trigger in the sidebar reads as a control, not as a dead text box:
         a pointer cursor and a focus ring, because a read-only input that
         behaves like a button is otherwise indistinguishable from a broken
         one. The hint names the gesture once; after the first open the reader
         already knows. */
      .wiki-main {
        /* The article column. min-width: 0 is what lets a wide table or a long
           URL shrink the column instead of pushing the shell open. */
        min-width: 0;
        max-width: 100%;
        /* The bottom breathing room the shell used to carry. It moved here
           because on the shell it also bounded the sticky sidebar: the shell is
           the sidebar's containing block, so padding there ended the sidebar
           64px above the window bottom and pushed the search box over the
           masthead at the end of the page. 4rem, the value the shell had.
           Only from the tablet up: below 60rem the shell is a single column
           with its own 3rem bottom padding, and this column would double it. */
        margin-bottom: 4rem;
        overflow-wrap: break-word;
      }
      @media (max-width: 60rem) {
        .wiki-main {
          margin-bottom: 0;
        }
      }

      /* Search lives in the masthead, at the bar's far right after the theme
         switcher. It is hidden below 60rem: the phone bar is a flex row of
         three items that already fits a 320px screen, and a fourth control
         pushed the wordmark off it. On a phone the same control appears in the
         sidebar's head row instead — see .wiki-search-btn-side below.
         margin-left:auto is what puts it at the bar's far right, after the
         theme switcher: the bar is a flex row with justify-content:
         space-between, so without it the button would sit beside the theme
         button instead of at the edge. */
      /* Shared geometry for both copies of the button. The two classes are
         listed together rather than one inheriting the other, because each copy
         also has its own display rule below and the selectors must stay
         independent: sharing .wiki-search-btn made the desktop media block
         render the phone copy too. */
      .wiki-search-btn,
      .wiki-search-btn-side {
        align-items: center;
        justify-content: center;
        width: 2.5rem;
        height: 2.5rem;
        padding: 0;
        border: 1px solid var(--border);
        border-radius: var(--radius);
        background: var(--surface);
        color: var(--text-muted);
        font: inherit;
        line-height: 1;
        cursor: pointer;
      }
      /* The masthead copy. Hidden at the base level and restored above the
         phone breakpoint below. margin-left:auto is what puts it at the bar's
         far right, after the theme switcher: the bar is a flex row with
         justify-content:space-between, and the button is its last child, so
         without the auto margin it would sit beside the theme button instead of
         at the edge. */
      .wiki-search-btn {
        display: none;
      }
      /* The phone copy. Hidden at the base level and shown below 60rem, where
         the bar has no room for a fourth control. margin-left:auto is what puts
         it at the far right of the sheet's head row, beside the mark and the
         name. */
      .wiki-search-btn-side {
        display: none;
        margin-left: auto;
      }
      /* Above the phone breakpoint the masthead copy is the one that renders.
         The bar is a flex row with justify-content:space-between, and the
         button is its last child, so margin-left:auto pushes it to the far
         right - after the theme switcher, which is the control it sits beside
         because search acts on the wiki's contents rather than on this page's
         appearance. */
      @media (min-width: 60.0625rem) {
        .wiki-search-btn {
          display: inline-flex;
        }
      }
      .wiki-search-btn:hover,
      .wiki-search-btn-side:hover {
        background: var(--bg-alt);
        color: var(--text);
      }
      .wiki-search-btn:focus-visible,
      .wiki-search-btn-side:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: 1px;
      }

      /* The magnifier. currentColor so it lifts with the button on hover, the
         way the theme glyph beside it does. 1.25rem matches the menu icon. */
      .wiki-search-icon {
        width: 1.25rem;
        height: 1.25rem;
        flex-shrink: 0;
      }
      /* The 68-character reading measure is a comfort rule, not a desktop
         decoration. The cap applies from the tablet up — and to a tablet it
         matters most, because below the 60rem breakpoint the sidebar folds away
         and the article column becomes as wide as a desktop's: measured 696px,
         about 87 characters per line, past the 45-75 range. On a phone the
         column is already narrow, so the cap is not needed there.
         It is set on .wiki-main, the grid child, and NOT on .prose, its inner
         child: capping .prose leaves the grid column sized to its content, so the
         cap changes where the text wraps but never how wide the page is. Measured
         272px of sideways overflow at 430px when the cap was applied everywhere
         and to .prose, because the article then fought the column for width. */
      @media (min-width: 40rem) {
        .wiki-main {
          max-width: 68ch;
        }
      }
      /* The wiki header is wider than the project page's, and carries one more
         control: the nav trigger. Both bars share the 76rem shell so a control
         in the header sits directly above what it opens, instead of floating
         over the article. */
      .masthead-inner {
        max-width: 76rem;
      }
      /* Trigger sits at the far left of the bar, before the wordmark, so it is
         the first thing a phone reader reaches. Hidden on desktop, where the
         sidebar is already open. Icon-only: the glyph carries the meaning, and
         the accessible name lives in aria-label. */
      /* The theme switcher is the one bar control that is always visible on a
         screen with room for it: it is useful at every width, and hiding it
         behind a sheet would make the theme harder to reach than the links it
         sits beside. It shares the menu buttons' geometry so the two read as one
         set. Below 60rem the bar gives it up — three controls plus a wordmark do
         not fit a phone bar — and the copy inside the links sheet takes over. */
      /* margin-left:auto is what groups this control with the search button at
         the bar's far right. The bar is justify-content:space-between, so
         without it the theme button sat beside the wordmark and the search
         button was left at the opposite edge, with the whole article width
         between two controls that belong together. */
      .wiki-theme-btn-bar {
        display: inline-flex;
        margin-left: auto;
        align-items: center;
        justify-content: center;
        width: 2.5rem;
        height: 2.5rem;
        padding: 0;
        border: 1px solid var(--border);
        border-radius: var(--radius);
        background: var(--surface);
        color: var(--text-muted);
        font: inherit;
        font-size: 1.25rem;
        line-height: 1;
        cursor: pointer;
      }
      .wiki-theme-btn-bar:hover {
        background: var(--bg-alt);
        color: var(--text);
      }
      .wiki-theme-btn-bar:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: 1px;
      }
      /* On a phone the bar keeps the theme button: it is the one control a
         reader may want at any width, and it was moved into the links sheet
         below 60rem because three controls plus a wordmark did not fit. The bar
         is a flex row with the trigger, the wordmark and this button, so it
         fits at 320px as long as the button keeps its square geometry. */
      .wiki-menu-btn {
        display: none;
        align-items: center;
        justify-content: center;
        /* 40px square, comfortably above the 24px minimum target, without a
           label widening the bar. */
        width: 2.5rem;
        height: 2.5rem;
        padding: 0;
        border: 1px solid var(--border);
        border-radius: var(--radius);
        background: var(--surface);
        color: var(--text);
        font: inherit;
        font-size: 1.125rem;
        line-height: 1;
        cursor: pointer;
      }
      .wiki-menu-btn:hover {
        background: var(--bg-alt);
      }
      .wiki-menu-btn:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: 1px;
      }
      .wiki-menu-icon {
        font-size: 1.25rem;
        line-height: 1;
      }
      /* The row that names the product. It was phone-only - the masthead above
         carried the wordmark on desktop, so a second copy put two logos in one
         view. It is now shown at every width because it is where the search
         button lives: the search field moved into a dialog, and the control
         that opens it belongs beside the product name rather than in a box that
         spends a whole row of the column to say "click me". The wordmark itself
         stays masthead-only; only this row and its two controls are here. */
      .wiki-sheet-head {
        display: flex;
        align-items: center;
        gap: 0.75rem;
        flex-shrink: 0;
        margin-bottom: 0.75rem;
      }
      /* The logo and the product name are phone-only, for the same reason the
         close control is: they belong to the sheet. On desktop the masthead
         above already carries the mark and the name on every screen, so a
         second copy in the sidebar put two logos in one view - which is what it
         did when this row was first shown at every width to hold the search
         button. The row itself stays: it carries the search button, and hiding
         only its first child leaves the button at the top of the column where a
         reader looks for it. */
      .wiki-sheet-head strong {
        display: none;
      }
      /* The close control is a phone-only affordance: it dismisses the sheet,
         and a sheet is a phone thing. On desktop the sidebar is always open, so
         the control would be a button that does nothing. The head row it sits
         in is now shown at every width, so this has to be said here rather than
         assumed from the row's old display:none. */
      .wiki-sheet-close {
        display: none;
      }
      @media (max-width: 60rem) {
        .wiki-shell {
          grid-template-columns: minmax(0, 1fr);
          padding: 1.25rem 1rem 3rem;
        }
        .wiki-menu-btn {
          display: inline-flex;
        }
        /* The sidebar becomes a full-screen sheet. It is taken out of flow
           rather than merely hidden, so the article starts at the top of the
           page instead of below 20 nav links. */
        .wiki-side {
          position: fixed;
          inset: 0;
          /* inset:0 alone does not fill the screen when an earlier rule set a
             height: the fixed box would keep that height and its bottom edge
             would stop short of the window. Measured: 744px against an 844px
             viewport, leaving the footer 100px above the bottom. height auto
             lets the inset win. */
          height: auto;
          z-index: 50;
          display: flex;
          flex-direction: column;
          overflow: hidden;
          padding: 1.25rem 1.25rem 3rem;
          background: var(--bg);
          border-right: 0;
          border-bottom: 0;
          transform: translateY(-100%);
          visibility: hidden;
          transition: transform 0.18s ease, visibility 0.18s;
        }
        body.nav-open .wiki-side {
          transform: translateY(0);
          visibility: visible;
        }
        /* The page behind an open sheet must not scroll: a swipe on the sheet
           would otherwise scroll the article underneath it. */
        body.nav-open {
          overflow: hidden;
        }
        /* The head row is defined once, above the media query, and shown at
           every width. The only thing the sheet needs here is more space under
           it before the nav starts, because the sheet's own padding is larger
           than the desktop column's. */
        .wiki-sheet-head {
          margin-bottom: 1.25rem;
        }
        /* The mark inside the sheet head, the same square the masthead's
           wordmark plate draws. Aligned to the text by the same flex row, so the
           sheet and the masthead do not disagree about the logo's size. The
           sidebar's own header row was removed — the masthead above already
           carries this mark on every screen, so a second copy put two logos in
           one view — which leaves the sheet head as the only other place the
           product is named, and it is where a phone reader looks first. */
        .wiki-sheet-head strong {
          display: inline-flex;
          align-items: center;
          gap: 0.5rem;
          font-size: 0.9375rem;
          font-weight: 650;
          letter-spacing: -0.01em;
        }
        /* The same icon the masthead draws, at the sheet head's own size: a
           container for the SVG, not a coloured plate. The glyph carries its own
           dark plate and its own colours, so a background here would put a
           second plate behind the real one. 1.25rem is the masthead's mark at
           0.71 scale — the sheet head's text is 0.9375rem, so the mark tracks it. */
        .wiki-sheet-mark {
          display: inline-grid;
          place-items: center;
          width: 1.25rem;
          height: 1.25rem;
          flex-shrink: 0;
        }
        .wiki-sheet-mark svg {
          display: block;
          width: 100%;
          height: 100%;
        }
        /* Icon-only, square, and at the left edge of the sheet — the same
           treatment the header trigger gets, so the two controls read as a
           pair. The flex default (flex-start) puts the icon first; the old
           rule pushed the label and button apart. */
        .wiki-sheet-close {
          display: inline-flex;
          align-items: center;
          justify-content: center;
          flex-shrink: 0;
          width: 2.5rem;
          height: 2.5rem;
          padding: 0;
          border: 1px solid var(--border);
          border-radius: var(--radius);
          background: var(--surface);
          color: var(--text);
          font: inherit;
          line-height: 1;
          cursor: pointer;
        }
        /* The close control is a phone-only affordance: it dismisses the sheet,
      }
      @media (max-width: 60rem) {
        /* The sheet's own hover and focus states. They were defined in this
           block all along; a rule moved out of it took them with it. */
        .wiki-sheet-close:hover {
          background: var(--bg-alt);
        }
        .wiki-sheet-close:focus-visible {
          outline: 2px solid var(--brand);
          outline-offset: 1px;
        }
        .wiki-sheet-close-icon {
          font-size: 1.125rem;
          line-height: 1;
        }

        /* Search, in the sheet head's own row: the bar above has no room for a
           fourth control below 60rem, so this is where the control lives on a
           phone. 2.5rem square, the same size as the close control beside it,
           so the two read as one set. */
        .wiki-search-btn-side {
          display: inline-flex;
        }        /* Larger targets: a phone reader is tapping, not clicking. The hover
           rule is inherited from the desktop row, so only the size changes
           here — a second hover definition is how the two drift apart. */
        .wiki-side a {
          padding: 0.55rem 0.75rem;
          font-size: 0.875rem;
        }
      }
      @media (max-width: 40rem) {
        .masthead-inner {
          gap: 0.5rem;
        }
      }
      /* Contributor cards. One grid, not 24 stacked blocks: the section is a
         roster, and a reader scans it rather than reading it top to bottom.
         auto-fill picks the column count from the width it is given.
         The floor is measured, not guessed: the grid is 646px wide inside the
         68ch measure and the root font-size is 16px, so 4 columns need
         (646 - 3*16) / 4 = 149.5px. 9rem (144px) fits 4 across with room to
         spare; 9.5rem (152px) does not and silently drops to 3. */
      .contrib-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(9rem, 1fr));
        gap: 1rem;
        margin: 1.5rem 0;
      }
      .contrib-card {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 0.5rem;
        padding: 1rem 0.75rem;
        border: 1px solid var(--border);
        border-radius: var(--radius-lg);
        background: var(--surface);
        text-align: center;
      }
      .contrib-card img {
        width: 4rem;
        height: 4rem;
        border-radius: 50%;
        /* The avatar is the person's own picture, not a screenshot: a border
           sized for a UI capture would box it like a figure. */
        border: 0;
        background: var(--bg-alt);
        /* Reserve the box before the remote bytes arrive, so the card does not
           grow and shove its neighbours while 24 avatars load. */
        aspect-ratio: 1;
        object-fit: cover;
        margin-inline: auto;
      }
      .contrib-name {
        font-weight: 600;
        font-size: 0.9375rem;
        line-height: 1.3;
      }
      .contrib-name a {
        color: var(--text);
        text-decoration: none;
      }
      .contrib-name a:hover {
        color: var(--link);
        text-decoration: underline;
      }
      .contrib-login {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-muted);
        word-break: break-all;
      }
      .contrib-count {
        font-size: 0.75rem;
        color: var(--text-muted);
      }
      /* The owner card spans the grid's full first row: 585 commits is 5.3x the
         next contributor, and a card the same size as the rest would say the
         opposite. */
      .contrib-card.owner {
        grid-column: 1 / -1;
        flex-direction: row;
        text-align: left;
        gap: 1.25rem;
        padding: 1.25rem;
      }
      .contrib-card.owner img {
        width: 5.5rem;
        height: 5.5rem;
        flex-shrink: 0;
      }
      .contrib-card.owner .contrib-name {
        font-size: 1.125rem;
      }
      @media (max-width: 40rem) {
        .contrib-grid {
          grid-template-columns: repeat(auto-fill, minmax(8rem, 1fr));
          gap: 0.75rem;
        }
        .contrib-card.owner {
          flex-direction: column;
          text-align: center;
        }
      }
      /* A name that still does not fit its card wraps onto two lines rather than
         being cut off — the card is the thing that grows. */
      .contrib-name {
        overflow-wrap: break-word;
      }
      @media (prefers-reduced-motion: reduce) {
        .wiki-side {
          transition: none;
        }
      }
      /* A wide table scrolls inside its own box. Without this the table sets the
         page's width and the whole layout scrolls sideways, which is the one
         responsive failure a reader cannot recover from by scrolling back. */
      [data-table-scroll] {
        overflow-x: auto;
        -webkit-overflow-scrolling: touch;
        max-width: 100%;
      }
      [data-table-scroll] table {
        margin: 0;
      }
    </style>
  </head>
  <body>
    <a class="skip-link" href="#wiki">Skip to content</a>

    <header class="masthead">
      <div class="masthead-inner">
        <!-- The nav trigger leads the bar, before the wordmark: on a phone it is
             the control this whole page depends on, and it opens the sidebar
             that sits directly beneath it. Icon only — the bar is 320px wide at
             its smallest, and a word beside each glyph costs more than it
             explains. The accessible name comes from aria-label, so a screen
             reader still announces "Open wiki navigation". -->
        <button
          type="button"
          class="wiki-menu-btn"
          id="nav-open"
          aria-label="Open wiki navigation"
          aria-expanded="false"
          aria-controls="wiki-side"
        >
          <span class="wiki-menu-icon" aria-hidden="true">☰</span>
        </button>
        <a class="wordmark" href="index.html">
          <!-- The product's own icon: the same mark the dashboard and the
               repository use. A 1.75rem square holding the 48-unit glyph, drawn
               as inline SVG rather than a span of text so it is the real mark at
               any size and not a letter in a coloured box. aria-hidden, because
               the word beside it is the accessible name. -->
          <span class="wordmark-mark" aria-hidden="true">
            <svg viewBox="0 0 48 48" focusable="false">
              <rect width="48" height="48" fill="#181D27" rx="12"/>
              <rect width="47" height="47" x=".5" y=".5" stroke="#2B3245" rx="11.5"/>
              <circle cx="24" cy="24" r="7" fill="#FF5C35"/>
              <circle cx="14" cy="16" r="3.5" fill="#FF8469"/>
              <circle cx="34" cy="16" r="3.5" fill="#38BDF8"/>
              <circle cx="14" cy="32" r="3.5" fill="#34D399"/>
              <circle cx="34" cy="32" r="3.5" fill="#A78BFA"/>
              <path stroke="#94A3B8" stroke-linecap="round" stroke-width="1.75" d="M16.5 18L21 21.5M31.5 18L27 21.5M16.5 30L21 26.5M31.5 30L27 26.5"/>
            </svg>
          </span>
          <span>9router-go</span>
        </a>
        <!-- The theme switcher, in the bar's far right. It sits beside the
             wordmark because it acts on this page, not on the wiki's contents,
             and it stays at every width: a reader who wants the other theme
             should not have to open a sheet to get it. The bar is a flex row of
             three items — trigger, wordmark, this — so it fits a 320px phone
             without one of them leaving. The project links are rows in the
             sidebar footer, where a reader who wants the repo looks for them. -->
        <button
          type="button"
          class="wiki-theme-btn wiki-theme-btn-bar"
          id="theme-toggle-bar"
          aria-label="Switch to dark theme"
        >
          <span class="wiki-theme-icon" aria-hidden="true">☾</span>
        </button>
        <!-- Search, at the bar's far right, after the theme switcher: it acts
             on the wiki's contents rather than on this page's appearance, so it
             belongs beside the control that changes appearance rather than in
             the sidebar. Desktop only — below 60rem the bar is a flex row of
             three items that already fits a 320px phone, and a fourth control
             pushed the wordmark off it. On a phone the search button lives in
             the sidebar's head row instead, where the sheet has room.
             The glyph carries the action and aria-label carries the name, the
             same contract the theme button beside it uses. -->
        <button
          type="button"
          class="wiki-search-btn"
          id="search-open"
          aria-label="Search the wiki"
          aria-haspopup="dialog"
          aria-expanded="false"
        >
          <svg class="wiki-search-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
            <circle cx="10.5" cy="10.5" r="6.25" fill="none" stroke="currentColor" stroke-width="1.8"/>
            <path d="m15.4 15.4 4.1 4.1" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>
          </svg>
        </button>
      </div>
    </header>

    <div class="wiki-shell">
      <aside class="wiki-side" id="wiki-side">
        <div class="wiki-sheet-head">
          <!-- The close control leads the sheet, at its top left, so it sits
               where the eye already is after the trigger in the header. Icon
               only: the sheet is full-screen and a word costs width the list
               could use. The accessible name carries the meaning. -->
          <button
            type="button"
            class="wiki-sheet-close"
            id="nav-close"
            aria-label="Close wiki navigation"
          >
            <span class="wiki-sheet-close-icon" aria-hidden="true">✕</span>
          </button>
          <strong>
            <!-- The same icon the masthead draws, at the sheet head's own size.
               The sheet is where a phone reader lands, so it is the one place
               besides the bar that names the product — and it should be the
               real mark, not a second drawing of it. -->
            <span class="wiki-sheet-mark" aria-hidden="true">
              <svg viewBox="0 0 48 48" focusable="false">
                <rect width="48" height="48" fill="#181D27" rx="12"/>
                <rect width="47" height="47" x=".5" y=".5" stroke="#2B3245" rx="11.5"/>
                <circle cx="24" cy="24" r="7" fill="#FF5C35"/>
                <circle cx="14" cy="16" r="3.5" fill="#FF8469"/>
                <circle cx="34" cy="16" r="3.5" fill="#38BDF8"/>
                <circle cx="14" cy="32" r="3.5" fill="#34D399"/>
                <circle cx="34" cy="32" r="3.5" fill="#A78BFA"/>
                <path stroke="#94A3B8" stroke-linecap="round" stroke-width="1.75" d="M16.5 18L21 21.5M31.5 18L27 21.5M16.5 30L21 26.5M31.5 30L27 26.5"/>
              </svg>
            </span>
            9router-go
          </strong>
          <!-- The phone copy of search. Desktop has its own in the masthead,
               after the theme switcher; below 60rem the bar has no room for a
               fourth control, so the button moves here, into the sheet's head
               row, where the mark and the name already live. Same glyph, same
               accessible name, same dialog - a reader who switches screens
               finds the same control in the place that screen can hold it. -->
          <button
            type="button"
            class="wiki-search-btn-side"
            id="search-open-side"
            aria-label="Search the wiki"
            aria-haspopup="dialog"
            aria-expanded="false"
          >
            <svg class="wiki-search-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
              <circle cx="10.5" cy="10.5" r="6.25" fill="none" stroke="currentColor" stroke-width="1.8"/>
              <path d="m15.4 15.4 4.1 4.1" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>
            </svg>
          </button>
        </div>
        <div class="wiki-side-scroll">
          {{ range .Groups }}
          <div class="wiki-group">
            {{ if .Label }}
            <h2 class="wiki-group-title">{{ .Label }}</h2>
            {{ end }}
            <ol>
              {{ range .Pages }}
              <li><a href="{{ .Slug }}.html"{{ if eq .Slug $.Page.Slug }} aria-current="page"{{ end }}>{{ .Title }}</a>{{ if .Unreleased }}<span class="wiki-since" title="Documents {{ .Since }}, which is newer than the release this wiki describes">new</span>{{ end }}</li>
              {{ end }}
            </ol>
          </div>
          {{ end }}
        </div>
        <!-- The footer. These three links left the masthead: a bar that carried
             the nav trigger, the wordmark, three links and a theme control had
             more chrome than content on a narrow desktop, and none of the three
             links acts on the page the reader is on. Here they are a single row
             of icons: three destinations a reader recognises by their mark, so
             the row costs three glyphs of height instead of three rows, and the
             release line beneath it has room to be read.
             Icon-only, so every glyph is hidden from assistive tech and the
             accessible name lives in aria-label — a screen reader announces the
             destination, not the picture of it. The theme control is in the bar,
             where it is reachable at any width. -->
        <div class="wiki-side-foot">
          <nav aria-label="Project" id="project-links">
            <a href="index.html" aria-label="Project" title="Project">
              <svg class="wiki-foot-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
                <path d="M3 10.5 12 3l9 7.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
                <path d="M5.5 9.5V20h13V9.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
              </svg>
            </a>
            <a href="https://github.com/{{ .Repo }}" aria-label="Repository" title="Repository">
              <!-- The GitHub mark. An inline SVG rather than a glyph: the ⌥
                   character rendered as an odd box on some systems, and a
                   misdrawn icon on a link that leaves the site is worse than no
                   icon. currentColor keeps it in step with the label. -->
              <svg class="wiki-foot-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
                <path fill="currentColor" d="M12 2C6.48 2 2 6.58 2 12.26c0 4.5 2.87 8.32 6.84 9.67.5.1.68-.22.68-.49 0-.24-.01-1.05-.01-1.9-2.78.62-3.37-1.21-3.37-1.21-.45-1.18-1.1-1.5-1.1-1.5-.91-.63.07-.62.07-.62 1 .07 1.53 1.05 1.53 1.05.89 1.56 2.34 1.11 2.91.85.09-.66.35-1.11.63-1.37-2.22-.26-4.56-1.14-4.56-5.07 0-1.12.39-2.03 1.03-2.75-.1-.26-.45-1.3.1-2.71 0 0 .84-.28 2.75 1.05a9.4 9.4 0 0 1 5 0c1.91-1.33 2.75-1.05 2.75-1.05.55 1.41.2 2.45.1 2.71.64.72 1.03 1.63 1.03 2.75 0 3.94-2.34 4.81-4.57 5.06.36.32.68.94.68 1.9 0 1.37-.01 2.48-.01 2.82 0 .27.18.6.69.49A10.06 10.06 0 0 0 22 12.26C22 6.58 17.52 2 12 2Z"/>
              </svg>
            </a>
            <a href="tg" aria-label="Telegram group" title="Telegram group">
              <svg class="wiki-foot-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
                <path d="M21.5 4.3 2.9 11.2c-.9.3-.9 1.6.1 1.9l4.3 1.3 1.6 4.9c.2.7 1.2.8 1.7.3l2.3-2.6 4.3 3.2c.6.5 1.5.1 1.7-.6l3.6-14.2c.2-.8-.6-1.5-1.4-1.2Z" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>
                <path d="M7.3 14.4 16.9 7.6l-6.9 6.8" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>
              </svg>
            </a>
          </nav>
          {{ if .StableVersion }}
          <!-- The release the wiki documents, from version.json — not the build
               the binary was compiled from, which on an experimental line is a
               version nobody can download. This is the one number a reader can
               check the update command against, so it sits under the links as
               metadata rather than inside them, and it is a plain sentence of
               text so a screen reader reads it as one. -->
          <p class="wiki-version">Documents release {{ .StableVersion }}</p>
          {{ end }}
        </div>
      </aside>

      <!-- No "back to project" link here: the wordmark in the masthead already
           goes to the project page, on every screen, without scrolling. A second
           link to the same destination in the same view is the duplicate-action
           pattern — it makes a reader check whether the two differ. -->
      <main id="wiki" class="prose wiki-main">
        {{ .Page.Body }}
      </main>
    </div>
    <!-- The search modal, a direct child of <body>. It cannot live inside the
         sidebar: below 60rem that aside is position:fixed with
         transform/visibility transitions, and a <dialog> inside it would be
         trapped by the sheet's transform and its hidden state — it would be
         reachable only when the nav sheet is open, and would cover a sheet that
         is itself full-screen. At the body level the dialog is its own layer
         over the whole page on every screen.
         The sidebar input is a read-only trigger that opens this: typing there
         showed results in a list squeezed into a 15rem column, and a phone
         reader got that same list inside a full-screen sheet. A dialog gives
         the query its own surface — the input, the results, and the keyboard
         all get the room the sidebar never had. Escape and the backdrop close
         it, and picking a result navigates. <dialog> carries the semantics
         (role, focus trap, Escape) and ::backdrop paints the scrim, so no JS
         reimplements them. -->
    <dialog class="wiki-search-modal" id="search-modal" aria-labelledby="search-modal-title">
      <div class="wiki-search-modal-head">
        <h2 id="search-modal-title" class="wiki-search-modal-title">Search</h2>
        <button type="button" class="wiki-search-modal-close" id="search-modal-close" aria-label="Close search">
          <span aria-hidden="true">&#10005;</span>
        </button>
      </div>
      <div class="wiki-search-modal-field">
        <input id="search-modal-input" type="search" placeholder="Search features…" autocomplete="off" />
      </div>
      <ul class="wiki-results wiki-search-modal-results" id="results-modal" hidden></ul>
    </dialog>

    <script>
      // Search reads the index the generator wrote beside these pages. The URL
      // carries the query (?q=) and the target section (#), so a result can be
      // shared as a link and reopened exactly where it was found.
      //
      // The sidebar box is a read-only trigger that opens a <dialog>. It is
      // read-only on purpose: the search that used to run there rendered into a
      // 15rem column, which truncated every heading to an unreadable width, and
      // on a phone that same list lived inside the full-screen nav sheet. The
      // query now belongs to the dialog, where the input and the results share
      // a surface wide enough for both.
      (function () {
        // Two triggers: one in the masthead for desktop, one in the sidebar's
        // head row for a phone. Only one is rendered at a time - CSS hides the
        // other - so both are bound and whichever is visible answers.
        var trigger = document.getElementById("search-open");
        var triggerSide = document.getElementById("search-open-side");
        var modal = document.getElementById("search-modal");
        var modalInput = document.getElementById("search-modal-input");
        var modalResults = document.getElementById("results-modal");
        var closeBtn = document.getElementById("search-modal-close");
        if ((!trigger && !triggerSide) || !modal || !modalInput || !modalResults) return;
        // A plain array of the two, with the absent one dropped: a build that
        // carries only one copy still works, and the loop below does not have to
        // nil-check each one.
        var triggers = [trigger, triggerSide].filter(Boolean);
        function paintTriggers(open) {
          triggers.forEach(function (btn) {
            btn.setAttribute("aria-expanded", open ? "true" : "false");
          });
        }
        // The index is fetched once and kept in the closure, so every keystroke
        // reads it without a second request. Declaring it here rather than
        // relying on a parameter name is what makes the listener work at all:
        // index was previously only a parameter of render(), so the input
        // handler threw a ReferenceError and search returned nothing.
        var index = [];
        var indexLoaded = false;
        var params = new URLSearchParams(location.search);
        var query = params.get("q") || "";
        // A shared link carries the query, so opening the page with ?q= shows
        // the dialog rather than a search the reader has to repeat.
        fetch("search-index.json")
          .then(function (r) { return r.json(); })
          .then(function (data) {
            index = data || [];
            indexLoaded = true;
            // Only render if the dialog is already open — a ?q= on load opens
            // it below, and a query typed before the fetch resolved renders
            // once the index arrives.
            if (modal.open) render(modalInput.value);
          })
          .catch(function () { modalResults.hidden = true; });
        // The dialog gives the query a surface wide enough for the results, so
        // the sidebar box that used to render them is only a trigger now.
        function openSearch() {
          // A browser without <dialog>.showModal would throw; the fallback
          // opens the element as a plain overlay so the search still works.
          if (typeof modal.showModal === "function") modal.showModal();
          else modal.setAttribute("open", "");
          paintTriggers(true);
          // A shared ?q= seeds the field; anything else starts empty, because a
          // button carries no text of its own to copy across.
          modalInput.value = query || "";
          // The index may still be in flight. An empty list here reads as "no
          // results", so the previous query is replayed when it lands.
          render(modalInput.value);
          modalInput.focus();
          modalInput.select();
        }
        function closeSearch() {
          if (typeof modal.close === "function") modal.close();
          else modal.removeAttribute("open");
          paintTriggers(false);
        }
        // Both copies of the button answer, whichever one this screen shows.
        // Only one is rendered at a time - CSS hides the other - but a reader
        // who resizes the window does not get a dead control on the screen they
        // just switched to.
        triggers.forEach(function (btn) {
          btn.addEventListener("click", openSearch);
          // A <button> fires click on Enter and Space on its own, so the click
          // listener above already covers the keyboard. Only Space needs a
          // guard: the browser scrolls the page on Space, and that scroll would
          // move the sheet the button sits in while the dialog opened over it.
          btn.addEventListener("keydown", function (e) {
            if (e.key === " ") e.preventDefault();
          });
        });
        closeBtn.addEventListener("click", closeSearch);
        // Clicking the backdrop closes. The dialog element is the target only
        // when the click lands outside its box, and because the box fills the
        // dialog's content area the check is the dialog's own bounding rect.
        modal.addEventListener("click", function (e) {
          var r = modal.getBoundingClientRect();
          var outside =
            e.clientX < r.left || e.clientX > r.right ||
            e.clientY < r.top || e.clientY > r.bottom;
          if (outside) closeSearch();
        });
        // The dialog is dismissed with Escape by the browser, and that fires a
        // cancel event — the only signal that closes it from the keyboard.
        modal.addEventListener("cancel", function () {
          paintTriggers(false);
        });
        // Picking a result navigates, so the dialog closes behind it.
        modalResults.addEventListener("click", function (e) {
          if (e.target.closest("a")) closeSearch();
        });

        function render(q) {
          q = (q || "").trim().toLowerCase();
          // The index is fetched once on load. While it is in flight an empty
          // list would read as "no results", which is a lie - say what is
          // actually happening instead.
          if (!indexLoaded) {
            modalResults.hidden = false;
            modalResults.innerHTML = '<li><span class="where">Loading\u2026</span></li>';
            return;
          }
          if (!q) {
            modalResults.hidden = true;
            modalResults.innerHTML = "";
            return;
          }
          var hits = [];
          var seen = {};
          for (var i = 0; i < index.length; i++) {
            var p = index[i];
            if (p.title.toLowerCase().indexOf(q) !== -1) {
              hits.push({ slug: p.slug, title: p.title, where: "Page" });
              seen[p.slug] = true;
            }
            for (var j = 0; j < p.headings.length; j++) {
              var h = p.headings[j];
              if (h.text.toLowerCase().indexOf(q) !== -1) {
                hits.push({ slug: p.slug, title: h.text, where: p.title });
              }
            }
            // A term that lives only in a paragraph still deserves a hit: it
            // links to the page rather than to a section, because no heading
            // owns it. Deduplicated so a page matched three ways shows once.
            if (!seen[p.slug] && p.text && p.text.indexOf(q) !== -1) {
              hits.push({ slug: p.slug, title: p.title, where: "Mentioned in this page" });
              seen[p.slug] = true;
            }
          }
          if (!hits.length) {
            modalResults.hidden = false;
            modalResults.innerHTML = '<li><span class="where">No match for "' + escapeHTML(q) + '"</span></li>';
            return;
          }
          // The cap is part of the answer: a reader who typed a broad term
          // needs to know the list was cut, not that nothing else matched.
          var shown = hits.slice(0, 12);
          var html = shown
            .map(function (h) {
              var anchor = h.where === "Page" ? "" : "#" + anchorOf(h.title);
              return (
                '<li><a href="' + h.slug + '.html' + anchor + '">' +
                escapeHTML(h.title) +
                '<span class="where">' + escapeHTML(h.where) + "</span></a></li>"
              );
            })
            .join("");
          var rest = hits.length - shown.length;
          if (rest > 0) {
            html += '<li><span class="where">' + rest + " more match" +
              (rest === 1 ? "" : "es") + " \u2014 refine the query to narrow them down.</span></li>";
          }
          modalResults.innerHTML = html;
          modalResults.hidden = false;
        }

        function anchorOf(text) {
          return text
            .toLowerCase()
            .replace(/[^a-z0-9]+/g, "-")
            .replace(/^-+|-+$/g, "");
        }

        function escapeHTML(s) {
          return s.replace(/[&<>"']/g, function (c) {
            return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
          });
        }

        var debounce;
        modalInput.addEventListener("input", function () {
          clearTimeout(debounce);
          debounce = setTimeout(function () {
            render(modalInput.value);
          }, 120);
        });
        // A shared link with ?q= opens the search already showing its results.
        if (query) openSearch();
      })();

      // Nav disclosure. On a phone or tablet the sidebar is a full-screen sheet,
      // so the page behind it must not scroll and Escape must close it. Picking
      // a page also closes it — a sheet left open after navigation would cover
      // the article the reader just asked for.
      (function () {
        var open = document.getElementById("nav-open");
        var close = document.getElementById("nav-close");
        var side = document.getElementById("wiki-side");
        if (!open || !close || !side) return;
        function setOpen(isOpen) {
          document.body.classList.toggle("nav-open", isOpen);
          open.setAttribute("aria-expanded", isOpen ? "true" : "false");
          if (isOpen) close.focus();
          else open.focus();
        }
        open.addEventListener("click", function () { setOpen(true); });
        close.addEventListener("click", function () { setOpen(false); });
        document.addEventListener("keydown", function (e) {
          if (e.key === "Escape" && document.body.classList.contains("nav-open")) setOpen(false);
        });
        side.addEventListener("click", function (e) {
          if (e.target.tagName === "A") setOpen(false);
        });
      })();

      // Project links. On a narrow bar the three links fold behind one control
      // so the nav trigger keeps its place; this is a plain disclosure, and the
      // links themselves are real anchors either way — the markup is the same
      // list, only its container is shown or hidden by CSS.
      (function () {
        var toggle = document.getElementById("links-open");
        var links = document.getElementById("project-links");
        if (!toggle || !links) return;
        function setOpen(isOpen) {
          links.classList.toggle("is-open", isOpen);
          toggle.setAttribute("aria-expanded", isOpen ? "true" : "false");
        }
        toggle.addEventListener("click", function (e) {
          e.stopPropagation();
          setOpen(!links.classList.contains("is-open"));
        });
        // A click outside dismisses it, the way a menu does. Clicking a link
        // navigates, which also removes the class with the next document.
        document.addEventListener("click", function (e) {
          if (links.classList.contains("is-open") && !links.contains(e.target)) setOpen(false);
        });
        document.addEventListener("keydown", function (e) {
          if (e.key === "Escape" && links.classList.contains("is-open")) {
            setOpen(false);
            toggle.focus();
          }
        });
      })();

      // Theme switcher. Same contract as the dashboard's toggle: a "light" or
      // "dark" class on <html>, stored under "9router-theme". The wiki is a
      // hand-written static page with no framework, so it cannot import the
      // Svelte component — it reuses the storage key and the class names, so a
      // reader's choice carries between the two surfaces instead of being asked
      // for twice.
      //
      // One button, in the bar, at every width. It used to be two — a bar copy
      // for a desktop and a row inside the links sheet for a phone — because the
      // bar had no room below 60rem. The sheet copy was removed with the footer
      // caption it sat beside; the bar is a flex row of three items, so it fits
      // at 320px without one.
      //
      // The stored value is read before the body renders, by the inline script
      // in <head>. This listener only handles the click; it must not re-read
      // localStorage on load, because that would race the head script and could
      // repaint the page from the OS preference after a choice was applied.
      (function () {
        var btn = document.getElementById("theme-toggle-bar");
        if (!btn) return;

        function current() {
          return document.documentElement.classList.contains("dark") ? "dark" : "light";
        }
        function paint() {
          var dark = current() === "dark";
          // The icon shows the theme a click would switch TO, so it reads as an
          // affordance rather than as a status badge.
          var icon = btn.querySelector(".wiki-theme-icon");
          if (icon) icon.textContent = dark ? "☀" : "☾";
          btn.setAttribute("aria-label", dark ? "Switch to light theme" : "Switch to dark theme");
          btn.setAttribute("aria-pressed", dark ? "true" : "false");
        }
        function apply(next) {
          // Both classes are always set: leaving a stale one behind would make
          // the stylesheet's .light/.dark rules disagree about which applies.
          document.documentElement.classList.toggle("dark", next === "dark");
          document.documentElement.classList.toggle("light", next === "light");
          try {
            localStorage.setItem("9router-theme", next);
          } catch (e) {
            // Private browsing and some embedded contexts refuse storage. The
            // theme still applies for this visit; it just will not persist.
          }
          paint();
        }
        btn.addEventListener("click", function () {
          apply(current() === "dark" ? "light" : "dark");
        });
        // Another tab changed the theme: follow it, so the two stay in step.
        window.addEventListener("storage", function (e) {
          if (e.key !== "9router-theme") return;
          if (e.newValue !== "light" && e.newValue !== "dark") return;
          document.documentElement.classList.toggle("dark", e.newValue === "dark");
          document.documentElement.classList.toggle("light", e.newValue === "light");
          paint();
        });
        paint();
      })();
    </script>
  </body>
</html>
`
