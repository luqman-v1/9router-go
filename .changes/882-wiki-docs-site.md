### docs(wiki): publish the wiki next to the project page, with a dark theme

The site generator already turned `README.md` into a hosted page, so the
documentation for everything the README points at lived nowhere: the repo's
guides (`ARCHITECTURE.md`, `DATABASE.md`, `docs/`) were separate files with
separate styling, and the dashboard's own docs were a screenshot.

- **`tools/sitegen/wiki.go` + `wiki_template.go`** render the `wiki/`
  directory into the same site the README page already uses, so a visitor
  reads one thing and not two. Each page gets the sidebar, the search box and
  the deep-link treatment, and repository-relative links resolve against
  GitHub the same way the README's do.
- **`site/style.css`** gains `.light` / `.dark` class rules beside the
  existing `prefers-color-scheme` query, reusing the dashboard's storage key
  (`9router-theme`) and its `.dark` class contract. A reader who picks a theme
  in the dashboard does not pick it again in the docs. The class rules come
  after the media query, so a stored choice beats the OS preference.
- **The theme switcher sits in the ⋯ sheet on a phone**, where the bar already
  carries the nav trigger and the wordmark. It is the same button as the
  desktop's, wired and painted as one control, so a reader who switches in the
  sheet and then rotates to landscape sees the bar button already on the new
  theme.
- **`sitegen/main.go`** takes a `-wiki` flag, defaulting to `wiki`, and the
  directory is optional: a checkout without it still builds the project page
  it always did.
- **`.github/workflows/pages.yml`** triggers on `wiki/**` as well, so a docs
  change rebuilds and re-validates the site. The job now also asserts the
  search index exists, that it carries page bodies rather than headings only,
  that every anchor in it is an id a page actually publishes, that every
  sidebar link resolves, and that no intermediate screenshot is published.
- **`Makefile`** gains `wiki-build` / `wiki-serve`; `clean` removes `_site`.

The README points at the wiki, and at the Owner & Contributors page, which
credits every committer with their GitHub profile photo, username and
contribution count.

Verified with `go build ./tools/sitegen/`, `go vet ./tools/sitegen/`,
`go test ./tools/sitegen/ -count=1` (28 pass), and a browser check at 390px
and 1440px covering the toggle, its persistence across pages, and the theme
carrying over after a rotation. Every assertion the Pages job makes was run
against the generated `_site` before the PR was opened.
