package main

// wikiData is what the wiki template renders.
type wikiData struct {
	Page  *page
	Pages []*page
	// Groups is the sidebar split by section heading. The first group has no
	// heading, which is how the dashboard's own top nav group renders.
	Groups []pageGroup
	Repo   string
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
    <link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='7' fill='%23e56a4a'/%3E%3Ctext x='16' y='23' font-family='system-ui,sans-serif' font-size='19' font-weight='700' fill='%23fff' text-anchor='middle'%3E9%3C/text%3E%3C/svg%3E" />
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
        padding: 2rem 1.25rem 4rem;
        align-items: start;
      }
      .wiki-side {
        position: sticky;
        top: 5.5rem;
        border-right: 1px solid var(--border-subtle);
        padding-right: 1.5rem;
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
      .wiki-search {
        margin-bottom: 1.25rem;
      }
      .wiki-search input {
        width: 100%;
        padding: 0.5rem 0.75rem;
        border: 1px solid var(--border);
        border-radius: var(--radius);
        background: var(--surface);
        color: var(--text);
        font: inherit;
        font-size: 0.9375rem;
      }
      .wiki-search input:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: 1px;
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
      .wiki-main {
        /* The article column. min-width: 0 is what lets a wide table or a long
           URL shrink the column instead of pushing the shell open. */
        min-width: 0;
        max-width: 100%;
        overflow-wrap: break-word;
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
      .wiki-theme-btn-bar {
        display: inline-flex;
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
      /* The copy inside the sheet. It is a link-styled row, not a chip: it sits
         in a list of links and has to read as one of them, with a word beside
         the glyph because a bare icon in a list of text rows looks broken. */
      #project-links .wiki-theme-btn {
        display: flex;
        align-items: center;
        gap: 0.6rem;
        width: 100%;
        padding: 0.55rem 0.7rem;
        border: 0;
        border-radius: var(--radius);
        background: none;
        color: inherit;
        font: inherit;
        font-size: 0.9375rem;
        text-align: left;
        cursor: pointer;
      }
      #project-links .wiki-theme-btn:hover {
        background: var(--bg-alt);
      }
      #project-links .wiki-theme-btn:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: 1px;
      }
      #project-links .wiki-theme-icon {
        font-size: 1.0625rem;
        line-height: 1;
      }
      /* On a phone the sheet is the only way to reach the theme, and on a
         desktop the sheet is hidden — so each copy exists only where it is the
         one a reader can use. Both are in the DOM: moving one by script would
         paint the bar after load and make it jump. */
      @media (max-width: 60rem) {
        .wiki-theme-btn-bar {
          display: none;
        }
      }
      @media (min-width: 60.0625rem) {
        #project-links .wiki-theme-btn {
          display: none;
        }
      }
      .wiki-menu-btn,
      .wiki-links-toggle {
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
      .wiki-menu-btn:hover,
      .wiki-links-toggle:hover {
        background: var(--bg-alt);
      }
      .wiki-menu-btn:focus-visible,
      .wiki-links-toggle:focus-visible {
        outline: 2px solid var(--brand);
        outline-offset: 1px;
      }
      .wiki-menu-icon,
      .wiki-links-icon {
        font-size: 1.25rem;
        line-height: 1;
      }
      .wiki-sheet-head {
        display: none;
      }
      @media (max-width: 60rem) {
        .wiki-shell {
          grid-template-columns: minmax(0, 1fr);
          padding: 1.25rem 1rem 3rem;
        }
        .wiki-menu-btn,
        .wiki-links-toggle {
          display: inline-flex;
        }
        /* The project links fold into a sheet of their own. They are the
           secondary job — the reader came for the wiki — so they give up their
           row in the bar rather than crowding the nav trigger. */
        .masthead nav {
          display: none;
        }
        .masthead-inner {
          justify-content: space-between;
        }
        .masthead nav.is-open {
          display: flex;
          flex-direction: column;
          position: absolute;
          top: 100%;
          right: 0;
          left: auto;
          z-index: 40;
          min-width: 12rem;
          padding: 0.5rem;
          border: 1px solid var(--border);
          border-radius: var(--radius);
          background: var(--surface);
          box-shadow: 0 8px 24px rgb(0 0 0 / 0.12);
        }
        .masthead nav.is-open a {
          padding: 0.55rem 0.7rem;
        }
        /* The sidebar becomes a full-screen sheet. It is taken out of flow
           rather than merely hidden, so the article starts at the top of the
           page instead of below 20 nav links. */
        .wiki-side {
          position: fixed;
          inset: 0;
          z-index: 50;
          overflow-y: auto;
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
        .wiki-sheet-head {
          display: flex;
          align-items: center;
          gap: 0.75rem;
          margin-bottom: 1.25rem;
        }
        .wiki-sheet-head strong {
          font-size: 0.9375rem;
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
        /* Larger targets: a phone reader is tapping, not clicking. The hover
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
        .masthead nav {
          gap: 0.65rem;
          font-size: 0.875rem;
        }
        .wiki-search input {
          font-size: 1rem;
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
          <span class="wordmark-mark" aria-hidden="true">9</span>
          <span>9router-go</span>
        </a>
        <nav aria-label="Project" id="project-links">
          <a href="index.html">Project</a>
          <a href="https://github.com/{{ .Repo }}">Repository</a>
          <a href="tg">Telegram group</a>
          <!-- The theme switcher lives in this sheet on a phone, where the bar
               already carries the nav trigger and this toggle beside the
               wordmark — three controls in a row too narrow for them. It is the
               same button, moved: on a desktop the sheet is hidden and the
               button is placed in the bar directly. -->
          <button
            type="button"
            class="wiki-theme-btn"
            id="theme-toggle"
            aria-label="Switch to dark theme"
          >
            <span class="wiki-theme-icon" aria-hidden="true">☾</span>
            <span class="wiki-theme-label">Theme</span>
          </button>
        </nav>
        <!-- The same button, for a screen wide enough to show it in the bar.
             Duplicated in the markup rather than moved by script, because the
             page is static: a script that relocated it would run after the
             first paint, and the bar would jump as the button arrived. -->
        <button
          type="button"
          class="wiki-theme-btn wiki-theme-btn-bar"
          id="theme-toggle-bar"
          aria-label="Switch to dark theme"
        >
          <span class="wiki-theme-icon" aria-hidden="true">☾</span>
        </button>
        <!-- The same three links, behind one control, once the bar is too narrow
             to show them beside the nav trigger. -->
        <button
          type="button"
          class="wiki-links-toggle"
          id="links-open"
          aria-label="Show project links"
          aria-expanded="false"
          aria-controls="project-links"
        >
          <span class="wiki-links-icon" aria-hidden="true">⋯</span>
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
          <strong>Wiki</strong>
        </div>
        <div class="wiki-search">
          <label class="skip-link" for="q">Search the wiki</label>
          <input id="q" type="search" placeholder="Search features…" autocomplete="off" />
          <ul class="wiki-results" id="results" hidden></ul>
        </div>
        {{ range .Groups }}
        <div class="wiki-group">
          {{ if .Label }}
          <h2 class="wiki-group-title">{{ .Label }}</h2>
          {{ end }}
          <ol>
            {{ range .Pages }}
            <li><a href="{{ .Slug }}.html"{{ if eq .Slug $.Page.Slug }} aria-current="page"{{ end }}>{{ .Title }}</a></li>
            {{ end }}
          </ol>
        </div>
        {{ end }}
      </aside>

      <!-- No "back to project" link here: the wordmark in the masthead already
           goes to the project page, on every screen, without scrolling. A second
           link to the same destination in the same view is the duplicate-action
           pattern — it makes a reader check whether the two differ. -->
      <main id="wiki" class="prose wiki-main">
        {{ .Page.Body }}
      </main>
    </div>

    <script>
      // Search reads the index the generator wrote beside these pages. The URL
      // carries the query (?q=) and the target section (#), so a result can be
      // shared as a link and reopened exactly where it was found.
      (function () {
        var input = document.getElementById("q");
        var results = document.getElementById("results");
        if (!input || !results) return;
        // The index is fetched once and kept in the closure, so every keystroke
        // reads it without a second request. Declaring it here rather than
        // relying on a parameter name is what makes the listener work at all:
        // index was previously only a parameter of render(), so the input
        // handler threw a ReferenceError and search returned nothing.
        var index = [];

        var params = new URLSearchParams(location.search);
        var query = params.get("q") || "";
        if (query) input.value = query;

        fetch("search-index.json")
          .then(function (r) { return r.json(); })
          .then(function (data) {
            index = data || [];
            render(query);
          })
          .catch(function () { results.hidden = true; });

        function render(q) {
          q = q.trim().toLowerCase();
          if (!q) {
            results.hidden = true;
            results.innerHTML = "";
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
            results.hidden = false;
            results.innerHTML = '<li><span class="where">No match for "' + escapeHTML(q) + '"</span></li>';
            return;
          }
          results.hidden = false;
          results.innerHTML = hits
            .slice(0, 12)
            .map(function (h) {
              var anchor = h.where === "Page" ? "" : "#" + anchorOf(h.title);
              return (
                '<li><a href="' + h.slug + '.html' + anchor + '">' +
                escapeHTML(h.title) +
                '<span class="where">' + escapeHTML(h.where) + "</span></a></li>"
              );
            })
            .join("");
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
        input.addEventListener("input", function () {
          clearTimeout(debounce);
          debounce = setTimeout(function () {
            var q = input.value;
            var url = new URL(location.href);
            if (q) url.searchParams.set("q", q);
            else url.searchParams.delete("q");
            history.replaceState(null, "", url);
            render(q);
          }, 120);
        });
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
      // There are two buttons, because the page is static: one in the bar for a
      // desktop, one inside the links sheet for a phone, where the bar has no
      // room for it. CSS shows exactly one of them, but both are wired here and
      // painted together — a reader who switches in the sheet and then rotates
      // to landscape must see the bar button already showing the new theme.
      //
      // The stored value is read before the body renders, by the inline script
      // in <head>. This listener only handles the click; it must not re-read
      // localStorage on load, because that would race the head script and could
      // repaint the page from the OS preference after a choice was applied.
      (function () {
        var barBtn = document.getElementById("theme-toggle-bar");
        var sheetBtn = document.getElementById("theme-toggle");
        if (!barBtn && !sheetBtn) return;

        function current() {
          return document.documentElement.classList.contains("dark") ? "dark" : "light";
        }
        function paint() {
          var dark = current() === "dark";
          // The icon shows the theme a click would switch TO, so it reads as an
          // affordance rather than as a status badge.
          [barBtn, sheetBtn].forEach(function (btn) {
            if (!btn) return;
            var icon = btn.querySelector(".wiki-theme-icon");
            if (icon) icon.textContent = dark ? "☀" : "☾";
            btn.setAttribute("aria-label", dark ? "Switch to light theme" : "Switch to dark theme");
            btn.setAttribute("aria-pressed", dark ? "true" : "false");
          });
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
        [barBtn, sheetBtn].forEach(function (btn) {
          if (!btn) return;
          btn.addEventListener("click", function () {
            apply(current() === "dark" ? "light" : "dark");
          });
        });
        // Switching from inside the sheet closes it, so the reader sees the new
        // theme rather than a menu still covering the page.
        if (sheetBtn) {
          sheetBtn.addEventListener("click", function () {
            var links = document.getElementById("project-links");
            var linksToggle = document.getElementById("links-open");
            if (links && links.classList.contains("is-open")) {
              links.classList.remove("is-open");
              if (linksToggle) linksToggle.setAttribute("aria-expanded", "false");
            }
          });
        }
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
