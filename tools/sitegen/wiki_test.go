package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectCSS is the stylesheet shared with the project page. The tests read it
// to keep a wiki-only rule from leaking into a page that has no sidebar.
var projectCSS = func() string {
	b, err := os.ReadFile(filepath.Join("..", "..", "site", "style.css"))
	if err != nil {
		return ""
	}
	return string(b)
}()

// teamMarkdown is the contributors page, read by the test that checks the roster
// is complete and real.
var teamMarkdown = func() string {
	b, err := os.ReadFile(filepath.Join("..", "..", "wiki", "team.md"))
	if err != nil {
		return ""
	}
	return string(b)
}()

// The wiki pages are the published documentation, so a broken one is a bug a
// reader hits, not a build warning. These cases pin the three things the
// generator does to them: frontmatter parsing, inter-page links, and the search
// index that makes a section reachable by URL.
func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantTitle string
		wantOrder int
		wantBody  string
	}{
		{
			name:      "title and order are read",
			src:       "---\ntitle: Installation\norder: 2\n---\n\n# Body\n",
			wantTitle: "Installation",
			wantOrder: 2,
			wantBody:  "\n# Body\n",
		},
		{
			name:      "no frontmatter leaves the body untouched",
			src:       "# Heading\n\nBody\n",
			wantTitle: "",
			wantOrder: 0,
			wantBody:  "# Heading\n\nBody\n",
		},
		{
			name:      "unterminated frontmatter is treated as body",
			src:       "---\ntitle: Rusak\n",
			wantTitle: "",
			wantBody:  "---\ntitle: Rusak\n",
		},
		{
			name:      "unknown keys are ignored",
			src:       "---\ntitle: API\nlayout: page\n---\n\nBody\n",
			wantTitle: "API",
			wantBody:  "\nBody\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body := splitFrontmatter(tt.src)
			if fm.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", fm.Title, tt.wantTitle)
			}
			if fm.Order != tt.wantOrder {
				t.Errorf("order = %d, want %d", fm.Order, tt.wantOrder)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

// A wiki page links to another wiki page by file name. The published link must
// point at the .html that is actually written, keeping any fragment, or the
// navigation is a 404 the reader clicks.
func TestRenderWikiPageLinks(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "wiki", "index.md")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFixture(t, page, strings.Join([]string{
		"---",
		"title: Home",
		"order: 1",
		"---",
		"",
		"# Home",
		"",
		"Lihat [instalasi](installation.md) dan [API](api.md#api-surface).",
		"",
	}, "\n"))

	out := filepath.Join(root, "_site")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("MkdirAll(out): %v", err)
	}

	p, err := renderWikiPage(page, out)
	if err != nil {
		t.Fatalf("renderWikiPage: %v", err)
	}
	if p.Title != "Home" {
		t.Errorf("title = %q, want Home", p.Title)
	}
	if p.Order != 1 {
		t.Errorf("order = %d, want 1", p.Order)
	}

	body := string(p.Body)
	for _, want := range []string{
		`href="installation.html"`,
		`href="api.html#api-surface"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q", want)
		}
	}
}

// An image a wiki page references must exist, or the published page shows a
// broken picture. The README path already fails the build for this; the wiki
// path must not be the exception.
func TestRenderWikiPageFailsOnMissingImage(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "wiki", "galeri.md")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFixture(t, page, "![hilang](../docs/gone.png)\n")

	out := filepath.Join(root, "_site")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("MkdirAll(out): %v", err)
	}

	_, err := renderWikiPage(page, out)
	if err == nil {
		t.Fatal("renderWikiPage() succeeded with a missing image, want an error")
	}
	if !strings.Contains(err.Error(), "gone.png") {
		t.Errorf("error does not name the missing file: %v", err)
	}
}

// The search index drives ?q= navigation, and the anchor it emits must be the
// id goldmark actually published. A mismatch sends the reader to the top of the
// page instead of the section they searched for.
func TestHeadingsAnchorMatchesGoldmark(t *testing.T) {
	got := headings("# RTK Token Saver\n\nBody text.\n\n## A section\n")
	if len(got) != 2 {
		t.Fatalf("headings() returned %d entries, want 2", len(got))
	}
	if got[0].text != "RTK Token Saver" || got[0].anchor != "rtk-token-saver" {
		t.Errorf("first heading = %+v, want {RTK Token Saver rtk-token-saver}", got[0])
	}
	if got[1].text != "A section" || got[1].anchor != "a-section" {
		t.Errorf("second heading = %+v, want {A section a-section}", got[1])
	}
}

// generateWiki renders every page into the output directory and writes the
// index the client-side search reads. Both are part of the published contract.
func TestGenerateWiki(t *testing.T) {
	root := t.TempDir()
	wiki := filepath.Join(root, "wiki")
	if err := os.MkdirAll(wiki, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFixture(t, filepath.Join(wiki, "index.md"), "---\ntitle: Home\norder: 1\n---\n\n# Home\n")
	writeFixture(t, filepath.Join(wiki, "api.md"), "---\ntitle: API\norder: 4\n---\n\n# API\n\n## API surface\n")

	out := filepath.Join(root, "_site")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("MkdirAll(out): %v", err)
	}

	if err := generateWiki(wiki, out, "luqman-v1/9router-go", "9.9.9"); err != nil {
		t.Fatalf("generateWiki: %v", err)
	}

	for _, rel := range []string{"index.html", "api.html", "search-index.json"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("expected %s in the output: %v", rel, err)
		}
	}

	page := readFile(t, filepath.Join(out, "index.html"))
	// The sidebar is built from every page, ordered by the frontmatter value.
	for _, want := range []string{`href="api.html"`, `href="index.html"`, `aria-current="page"`} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html is missing %q", want)
		}
	}

	index := readFile(t, filepath.Join(out, "search-index.json"))
	for _, want := range []string{`"slug":"api"`, `"anchor":"api-surface"`, `"title":"Home"`} {
		if !strings.Contains(index, want) {
			t.Errorf("search index is missing %q", want)
		}
	}
}

// groupBySection decides what the sidebar looks like, so a page that forgets its
// section must not vanish from navigation — it joins the first group, the one
// that renders without a heading.
func TestGroupBySectionKeepsEveryPage(t *testing.T) {
	pages := []*page{
		{Slug: "home", Section: "Getting Started"},
		{Slug: "api", Section: "Getting Started"},
		{Slug: "arch", Section: "Under the Hood"},
		{Slug: "orphan", Section: ""}, // forgot the field
	}
	groups := groupBySection(pages)

	// Every page must appear exactly once across all groups.
	seen := map[string]int{}
	for _, g := range groups {
		for _, p := range g.Pages {
			seen[p.Slug]++
		}
	}
	for _, p := range pages {
		if seen[p.Slug] != 1 {
			t.Errorf("page %q appears %d times, want exactly 1", p.Slug, seen[p.Slug])
		}
	}
	// The orphan joins the first group (empty label), not a group of its own at
	// the end — a trailing "unnamed" section would show up as a stray heading.
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(groups), groups)
	}
	if groups[0].Label != "Getting Started" {
		t.Errorf("first group label = %q", groups[0].Label)
	}
	if groups[1].Label != "Under the Hood" {
		t.Errorf("second group label = %q", groups[1].Label)
	}
	orphanIn := false
	for _, p := range groups[0].Pages {
		if p.Slug == "orphan" {
			orphanIn = true
		}
	}
	if !orphanIn {
		t.Error("a page with no section did not join the first group")
	}
}

// A page with no section at all must still render, and must not gain a heading
// it did not ask for.
func TestGroupBySectionAllUnsectioned(t *testing.T) {
	groups := groupBySection([]*page{{Slug: "a"}, {Slug: "b"}})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].Label != "" {
		t.Errorf("label = %q, want empty", groups[0].Label)
	}
	if len(groups[0].Pages) != 2 {
		t.Errorf("got %d pages, want 2", len(groups[0].Pages))
	}
}

// The sidebar must render one list per group, with a heading only where the
// group has a label. A template that always emits the heading would print an
// empty "undefined" or a bare box above the first group.
func TestWikiTemplateRendersGroupedSidebar(t *testing.T) {
	if !strings.Contains(wikiTemplate, "{{ range .Groups }}") {
		t.Fatal("template does not iterate the sidebar groups")
	}
	if !strings.Contains(wikiTemplate, "{{ if .Label }}") {
		t.Error("group heading is unconditional: the first group would render an empty heading")
	}
	if !strings.Contains(wikiTemplate, `class="wiki-group-title"`) {
		t.Error("group heading has no class for the stylesheet to target")
	}
	if !strings.Contains(wikiTemplate, `class="wiki-group"`) {
		t.Error("group has no class: the separation rule cannot apply")
	}
}

// The contributors page is generated from real GitHub data, so its contract is
// about the shape of that data reaching the reader: one card per person, the
// owner distinguished, and every card carrying a clickable GitHub username. A
// page that renders the prose but drops the grid would still build and would
// quietly stop crediting the people who did the work.
func TestTeamPageCreditsEveryContributor(t *testing.T) {
	if teamMarkdown == "" {
		t.Skip("team.md not readable from this checkout")
	}
	body := teamMarkdown

	// 24 people: the owner plus 23 contributors the API reports. The bot account
	// is deliberately excluded — it is not a person.
	if got := strings.Count(body, `class="contrib-card`); got != 24 {
		t.Errorf("got %d contributor cards, want 24", got)
	}
	if got := strings.Count(body, "contrib-card owner"); got != 1 {
		t.Errorf("got %d owner cards, want exactly 1", got)
	}
	// Each card must link to the person's own profile, not a generic repo link.
	if got := strings.Count(body, `href="https://github.com/`); got != 24 {
		t.Errorf("got %d GitHub profile links, want 24", got)
	}
	// Every avatar must be lazy and carry a size, or 24 external requests block
	// the article before it renders.
	if got := strings.Count(body, `loading="lazy"`); got != 24 {
		t.Errorf("got %d lazy avatars, want 24", got)
	}
	if got := strings.Count(body, `decoding="async"`); got != 24 {
		t.Errorf("got %d async avatars, want 24", got)
	}
	if got := strings.Count(body, `width="64" height="64"`); got != 23 {
		t.Errorf("got %d sized contributor avatars, want 23", got)
	}
	// The owner's avatar is larger, and must be sized too.
	if !strings.Contains(body, `width="88" height="88"`) {
		t.Error("owner avatar is not sized")
	}
	// No fake people: every card must carry a real username, never a placeholder.
	if strings.Contains(body, "XXX") || strings.Contains(body, "[REAL DATA]") {
		t.Error("team page contains placeholder copy")
	}
	// The bot must be excluded and the exclusion must be explained, so a reader
	// does not think it was forgotten.
	if !strings.Contains(body, "imgbot[bot]") {
		t.Error("the excluded bot account is not mentioned: its absence looks like an oversight")
	}
	if !strings.Contains(body, "not a person") {
		t.Error("the reason for excluding the bot is not stated")
	}
}

// The theme switcher is the wiki's first hand-written control that persists a
// choice, so its contract is the same one the dashboard's toggle already keeps:
// a light/dark class on <html>, and the "9router-theme" key in localStorage.
// A second key or a second class name would mean a reader's choice applies in
// one surface and is silently ignored in the other.
func TestThemeSwitcherUsesTheDashboardContract(t *testing.T) {
	// The class rules must exist in the shared stylesheet, since the wiki pages
	// define no variables of their own — without these the toggle would change
	// classes and nothing on screen.
	if !strings.Contains(projectCSS, ".light {") {
		t.Error("the stylesheet has no .light rule: the toggle would change classes without changing colours")
	}
	if !strings.Contains(projectCSS, ".dark {") {
		t.Error("the stylesheet has no .dark rule")
	}
	for _, token := range []string{"--bg:", "--surface:", "--text:", "--link:"} {
		if !strings.Contains(projectCSS[strings.Index(projectCSS, ".dark {"):], token) {
			t.Errorf(".dark does not set %s, so the dark theme is incomplete", token)
		}
	}
	// The automatic preference must stay the default; the class rules override
	// it, not replace it.
	if !strings.Contains(projectCSS, "prefers-color-scheme: dark") {
		t.Error("the OS-preference media query is gone: readers who never click get no automatic theme")
	}

	// The stored value must be applied before the first paint, in <head>.
	headScript := wikiTemplate[:strings.Index(wikiTemplate, "<body")]
	if !strings.Contains(headScript, `localStorage.getItem("9router-theme")`) {
		t.Error("the head does not read the stored theme, so a dark reader gets a light flash on load")
	}
	if !strings.Contains(headScript, `classList.toggle("dark"`) {
		t.Error("the head does not apply the dark class")
	}
	// The click handler must use the same key, or the choice never comes back.
	body := wikiTemplate[strings.Index(wikiTemplate, "<body"):]
	if !strings.Contains(body, `localStorage.setItem("9router-theme", next)`) {
		t.Error("the toggle does not store its choice under the dashboard's key")
	}
	// One toggle, in the bar, at every width. It used to be two — a bar copy and
	// a row inside the links sheet — because the bar had no room below 60rem. A
	// second copy in the footer caption's place would be a control that appears
	// beside a caption that no longer exists, and a reader would have to work out
	// which one is live.
	if !strings.Contains(body, `id="theme-toggle-bar"`) {
		t.Error("the template has no theme toggle button in the bar")
	}
	if strings.Contains(body, `id="theme-toggle"`) {
		t.Error("the links sheet still carries its own theme button; the bar's copy must be the only one")
	}
	// The bar copy must never be hidden by width: the reason the sheet copy
	// existed was that the bar gave it up below 60rem. If that media query comes
	// back, a phone reader has no way to switch themes at all.
	if strings.Contains(wikiTemplate, ".wiki-theme-btn-bar {\n          display: none;") {
		t.Error("the bar copy is still hidden on a phone, which would leave no reachable theme control")
	}
	// The single button must still be painted on every switch, or its icon keeps
	// pointing at the theme that was just replaced.
	if !strings.Contains(body, "paint();") {
		t.Error("the toggle never repaints its own icon")
	}
	// Both classes are set on every switch; a stale one would leave the two rules
	// disagreeing about which theme applies.
	if !strings.Contains(body, `classList.toggle("dark", next === "dark")`) ||
		!strings.Contains(body, `classList.toggle("light", next === "light")`) {
		t.Error("the toggle does not set both classes, so a stale class can survive a switch")
	}
}

// The masthead wordmark already links to the project page, on every screen. A
// second link to the same destination inside the article is a duplicate action:
// a reader sees two ways to go somewhere and has to check whether they differ.
// The rule is pinned here because the link is easy to add back while styling the
// top of an article.
func TestNoDuplicateProjectLinkInTheArticle(t *testing.T) {
	if strings.Contains(wikiTemplate, "wiki-back") {
		t.Error("the article carries its own back-to-project link; the wordmark already does this")
	}
	if strings.Contains(wikiTemplate, "9router-go project") {
		t.Error("the article still names the project page in a link label")
	}
	// The wordmark must remain the way back, or removing the article link leaves
	// the wiki with no route to the project page at all.
	if !strings.Contains(wikiTemplate, `class="wordmark" href="index.html"`) {
		t.Error("the wordmark no longer links to the project page")
	}
}

// The nav disclosure is the whole responsive contract: above the breakpoint the
// sidebar is always open and the Menu button is not rendered; below it the
// sidebar is a hidden sheet and the button is the only way in. A template edit
// that drops either half produces a wiki with no navigation at all on a phone,
// which no other test would catch — every page would still render.
func TestWikiTemplateCarriesTheNavDisclosure(t *testing.T) {
	if !strings.Contains(wikiTemplate, `id="nav-open"`) {
		t.Error("template has no Menu button: a phone reader cannot open the nav")
	}
	// The sheet's close control is icon-only, so it needs its own accessible
	// name — the label "Close" used to supply it, and removing the label without
	// this would leave a screen reader announcing a bare button.
	if !strings.Contains(wikiTemplate, `aria-label="Close wiki navigation"`) {
		t.Error("sheet close button has no accessible name: it is icon-only")
	}
	if !strings.Contains(wikiTemplate, `class="wiki-sheet-close-icon" aria-hidden="true"`) {
		t.Error("the close glyph is not hidden: its name is announced twice")
	}
	if strings.Contains(wikiTemplate, `id="nav-close">Close</button>`) {
		t.Error("sheet close still shows a text label")
	}
	// The close control must come before the sheet title in the markup, since
	// flex row order puts it at the left edge where the reader looks first.
	head := wikiTemplate[strings.Index(wikiTemplate, `<div class="wiki-sheet-head">`):]
	head = head[:strings.Index(head, "</div>")]
	if strings.Index(head, `id="nav-close"`) > strings.Index(head, "<strong>") {
		t.Error("close button is not before the sheet title: it is not at the left edge")
	}
	if !strings.Contains(wikiTemplate, `id="wiki-side"`) {
		t.Error("template has no sidebar id: the button cannot reference it")
	}
	// aria-controls is what ties the two together for assistive tech; without it
	// the button announces nothing about what it opens.
	if !strings.Contains(wikiTemplate, `aria-controls="wiki-side"`) {
		t.Error("Menu button does not name the sheet it controls")
	}
	if !strings.Contains(wikiTemplate, `aria-expanded="false"`) {
		t.Error("Menu button has no aria-expanded: its state is unreadable")
	}
	// The sheet is hidden by transform, not display:none, so it can animate. The
	// visibility property is what actually removes it from the a11y tree and
	// from hit-testing while closed.
	if !strings.Contains(wikiTemplate, "visibility: hidden") {
		t.Error("closed sheet is not removed from the a11y tree")
	}
	// Escape must close it. A full-screen sheet with no keyboard exit traps a
	// keyboard user on every phone-width page.
	if !strings.Contains(wikiTemplate, `e.key === "Escape"`) {
		t.Error("Escape does not close the sheet")
	}
}

// The nav trigger lives in the header bar, not the article: a reader on a phone
// reaches for the bar first, and a trigger buried at the top of the content is
// found only after scrolling. The three project links are the secondary job, so
// they fold behind their own control once the bar is too narrow to hold both.
func TestNavTriggerLivesInTheHeaderBar(t *testing.T) {
	// The trigger must be inside the masthead, and it must be its first child —
	// before the wordmark — so it is the leftmost control on the bar.
	inner := wikiTemplate[strings.Index(wikiTemplate, `<div class="masthead-inner">`):]
	inner = inner[:strings.Index(inner, "</div>")]
	btnAt := strings.Index(inner, `id="nav-open"`)
	wordmarkAt := strings.Index(inner, `class="wordmark"`)
	if btnAt < 0 {
		t.Fatal("no Menu button in the masthead: a phone reader cannot open the nav")
	}
	if btnAt > wordmarkAt {
		t.Error("Menu button is not before the wordmark: it is not the leftmost control")
	}
	// The article must not carry a second copy. Two triggers for one sheet is
	// the duplicate-component failure, and the old one is easy to leave behind.
	if strings.Count(wikiTemplate, `id="nav-open"`) != 1 {
		t.Errorf("found %d Menu buttons, want exactly 1", strings.Count(wikiTemplate, `id="nav-open"`))
	}
	// The project links need an id, so the footer can be referenced and styled
	// as one group, and so a test can assert the links are still there.
	if !strings.Contains(wikiTemplate, `id="project-links"`) {
		t.Error("project links have no id")
	}
	// They must live in the sidebar footer, not the masthead: a bar carrying the
	// nav trigger, the wordmark, three links and a theme control has more chrome
	// than content on a narrow desktop, and none of the three acts on the page
	// the reader is on.
	if !strings.Contains(wikiTemplate, `class="wiki-side-foot"`) {
		t.Error("the sidebar has no footer: the project links have nowhere to live")
	}
	// The old toggle is gone: the links are always visible in the footer, so a
	// control that shows and hides them is dead weight a reader has to open.
	if strings.Contains(wikiTemplate, `id="links-open"`) {
		t.Error("the links toggle is still present: the footer links are always visible")
	}
	if strings.Contains(wikiTemplate, `wiki-links-toggle`) {
		t.Error("the links toggle CSS is still present: no toggle is rendered")
	}
	if strings.Contains(wikiTemplate, `aria-controls="project-links"`) {
		t.Error("a control still names the project links: nothing shows or hides them")
	}
	// Both header controls are icon-only, so each must carry an accessible name.
	// Without aria-label a screen reader announces "button" and nothing else, and
	// the whole navigation becomes unreachable for a blind reader on a phone.
	if !strings.Contains(wikiTemplate, `aria-label="Open wiki navigation"`) {
		t.Error("Menu button has no accessible name: it is icon-only")
	}
	// The glyph itself must be hidden from assistive tech, or the name is read
	// twice — once as the aria-label and once as the character.
	if !strings.Contains(wikiTemplate, `class="wiki-menu-icon" aria-hidden="true"`) {
		t.Error("the menu glyph is not hidden: its name is announced twice")
	}
	// Neither button may show a text label any more.
	for _, label := range []string{"> Menu</span>", "> Links</span>"} {
		if strings.Contains(wikiTemplate, label) {
			t.Errorf("header button still shows a text label %q", label)
		}
	}
	// The nav trigger must be hidden above the breakpoint, where the sidebar is
	// already open.
	if !strings.Contains(wikiTemplate, ".wiki-menu-btn {\n          display: inline-flex;") {
		t.Error("the header control is not revealed at the breakpoint")
	}
	// Each footer link carries an inline SVG icon, so the row reads as an
	// external action rather than another wiki page — and so it does not depend
	// on a glyph the reader's font may not have.
	if got := strings.Count(wikiTemplate, `class="wiki-foot-icon"`); got != 3 {
		t.Errorf("found %d footer icons, want 3 (project, repository, telegram)", got)
	}
	// The footer links are icons only, in a row. Each one lost its label, so the
	// accessible name has to come from somewhere: aria-label on the link, with
	// the SVG aria-hidden. Without it a screen reader announces "link" three
	// times and the reader cannot tell project from repository.
	for _, name := range []string{"Project", "Repository", "Telegram group"} {
		if !strings.Contains(wikiTemplate, `aria-label="`+name+`"`) {
			t.Errorf("footer icon %q has no aria-label: it is icon-only, so its name is unreachable", name)
		}
	}
	if !strings.Contains(wikiTemplate, `aria-label="Project" title="Project"`) {
		t.Error("the icon-only footer link carries no title: a pointer reader gets no name on hover")
	}
	// The row is horizontal, not a column: three labelled rows spent the sidebar's
	// height to say what the marks already say.
	if !strings.Contains(wikiTemplate, "#project-links {\n        display: flex;\n        flex-direction: row;") {
		t.Error("the footer links are not laid out as a row")
	}
	// The release line is back, but it is the release the wiki documents — read
	// from version.json — not the build the binary was compiled from. On an
	// experimental line those differ, and naming the build told readers the docs
	// described a release nobody could download.
	if !strings.Contains(wikiTemplate, `class="wiki-version">Documents release {{ .StableVersion }}`) {
		t.Error("the footer no longer names the release the wiki documents")
	}
	if strings.Contains(wikiTemplate, "{{ .Version }}") {
		t.Error("the template renders the build version, not the release the wiki documents")
	}
	// It must be omitted when the generator was given no version, so a local run
	// without the manifest does not print an empty label.
	if !strings.Contains(wikiTemplate, "{{ if .StableVersion }}") {
		t.Error("the release line has no guard: an empty version would render as bare text")
	}
	// One logo per view. The masthead carries the wordmark on every screen, so
	// the sidebar's own header row was a second copy of the same mark and name in
	// the same view — a reader sees two logos and has to work out whether they
	// differ. The sidebar now starts at the search box, which is the first thing
	// it does that the masthead does not.
	if strings.Contains(wikiTemplate, `class="wiki-side-head"`) {
		t.Error("the sidebar still carries its own header row: the masthead already names the product")
	}
	if strings.Contains(wikiTemplate, `class="wiki-side-title"`) {
		t.Error("the sidebar still has its own wordmark link: two links to index.html in one view")
	}
	// The phone sheet head keeps a mark, because there the bar is the only chrome
	// that is always visible and the sheet is where a phone reader lands. It has
	// its own class, so it cannot be deleted with the desktop row's CSS by
	// mistake — which is what happened the first time the row was cut.
	if !strings.Contains(wikiTemplate, `class="wiki-sheet-mark"`) {
		t.Error("the phone sheet head has no mark class: the sheet's logo lost its styling")
	}
	if !strings.Contains(wikiTemplate, ".wiki-sheet-mark {") {
		t.Error("the sheet mark has no rule, so the phone sheet head renders as bare text")
	}
	// Both marks must be the product's own icon — the same SVG the dashboard and
	// the repository serve — rather than two different drawings of it. The glyph
	// carries its own plate and its own colours, so neither mark paints a
	// background: a plate behind it would be a second plate, and the two radii
	// on one 28px square would disagree visibly.
	iconCore := `<circle cx="24" cy="24" r="7" fill="#FF5C35"/>`
	if !strings.Contains(wikiTemplate, iconCore) {
		t.Error("the masthead no longer draws the product's own icon")
	}
	if got := strings.Count(wikiTemplate, iconCore); got != 2 {
		t.Errorf("found %d copies of the icon core, want 2 (masthead + phone sheet head)", got)
	}
	if strings.Contains(wikiTemplate, ".wiki-sheet-mark {\n          display: grid;\n          place-items: center;\n          width: 1.25rem;\n          height: 1.25rem;\n          border-radius: 6px;") {
		t.Error("the sheet mark still paints its own plate behind the real icon")
	}
	// The masthead wordmark must remain the only route to the project page.
	if got := strings.Count(wikiTemplate, `class="wordmark"`); got != 1 {
		t.Errorf("found %d wordmarks, want exactly 1 (in the masthead)", got)
	}
	// The theme button must be the bar's last child, so it is the rightmost
	// control — the position the reader looks to for a view setting.
	bar := wikiTemplate[strings.Index(wikiTemplate, `<div class="masthead-inner">`):]
	bar = bar[:strings.Index(bar, "</div>")]
	themeAt := strings.Index(bar, `id="theme-toggle-bar"`)
	if themeAt < 0 {
		t.Fatal("no theme button in the bar")
	}
	if wordmarkAt := strings.Index(bar, `class="wordmark"`); themeAt < wordmarkAt {
		t.Error("theme button is not after the wordmark: it is not at the bar's right edge")
	}
}

// The search index must be reachable from every keystroke, which means the
// variable holding it has to outlive the fetch that fills it. It previously
// lived only as a parameter of render(), so the input handler threw a
// ReferenceError and search silently returned nothing on every page — a defect
// no render or CSS check can see, because the box still looks correct.
func TestSearchIndexIsInScopeForEveryKeystroke(t *testing.T) {
	// The closure variable that survives the fetch.
	if !strings.Contains(wikiTemplate, "var index = [];") {
		t.Error("no closure holds the search index: each keystroke has nothing to read")
	}
	// The fetch must fill that variable, not shadow it with a parameter of the
	// same name — a shadowed parameter is what broke it.
	if strings.Contains(wikiTemplate, ".then(function (index) {") {
		t.Error("fetch still shadows the index with a parameter of the same name")
	}
	if !strings.Contains(wikiTemplate, "index = data || [];") {
		t.Error("fetch does not assign the parsed index to the closure variable")
	}
	// render() takes the query only; a second argument is the old two-parameter
	// signature, and calling it that way passes the array where the string goes.
	if strings.Contains(wikiTemplate, "function render(index, q)") {
		t.Error("render() still declares the index as a parameter")
	}
	if strings.Contains(wikiTemplate, "render(index || [], q)") {
		t.Error("a caller still passes the index as render()'s first argument")
	}
	// Every call site must pass exactly one argument.
	for _, call := range []string{"render(query)", "render(q)"} {
		if !strings.Contains(wikiTemplate, call) {
			t.Errorf("no call site %q; search never renders", call)
		}
	}
}

// The wiki-main rule is the one that decides the article column's width, so the
// test looks for its declarations rather than a formatted block — a comment
// between the selector and min-width would otherwise read as a missing rule.
func wikiMainRule(t *testing.T) string {
	t.Helper()
	start := strings.Index(wikiTemplate, ".wiki-main {")
	if start < 0 {
		t.Fatal(".wiki-main has no CSS rule at all")
	}
	end := strings.Index(wikiTemplate[start:], "}")
	if end < 0 {
		t.Fatal(".wiki-main rule is never closed")
	}
	return wikiTemplate[start : start+end]
}

// The 68-character measure is a reading-comfort rule, so it applies wherever the
// column is wide enough to need it — including a tablet in portrait, which has no
// sidebar and therefore a column as wide as a desktop's (measured 696px, about 87
// characters per line, past the 45-75 range).
//
// Two properties of the rule are load-bearing and both were learned by breaking
// it:
//
//  1. It caps .wiki-main, the grid child — NOT .prose, its inner child. Capping
//     the inner child leaves the grid column sized to its content, so the cap
//     moves where the text wraps without ever changing how wide the page is.
//  2. It starts at 40rem, not at the top. Applied at every width it fought the
//     column on a phone and produced 272px of sideways overflow at 430px.
func TestReadingMeasureCannotWidenThePage(t *testing.T) {
	rule := wikiMainRule(t)
	if !strings.Contains(rule, "min-width: 0") {
		t.Error(".wiki-main has no min-width: 0, so a wide table pushes the shell open")
	}
	if !strings.Contains(rule, "max-width: 100%") {
		t.Error(".wiki-main has no width clamp")
	}
	if !strings.Contains(rule, "overflow-wrap: break-word") {
		t.Error(".wiki-main cannot break a long word, so it widens instead of wrapping")
	}
	// The cap must sit on the grid child, inside a min-width 40rem block.
	if !strings.Contains(wikiTemplate, "@media (min-width: 40rem) {\n        .wiki-main {\n          max-width: 68ch;\n        }") {
		t.Error("the 68ch measure is not capping .wiki-main inside a min-width 40rem block")
	}
	// A cap on .prose is the shape that leaves the column sized to its content.
	if strings.Contains(wikiTemplate, ".prose {\n        max-width: 68ch;\n      }") {
		t.Error("the measure caps .prose, so the grid column stays sized to its content")
	}
	// The project stylesheet must not carry the same cap: it has no sidebar and
	// would be narrowed for no reason.
	if strings.Contains(projectCSS, "max-width: 68ch") {
		t.Error("the project stylesheet also caps .prose at 68ch; it has no sidebar")
	}
}

// The sidebar hover is copied from the dashboard's own nav, so the wiki and the
// product read as one tool. This pins the parts that make it work: the label
// lifts to the main text colour (a background-only hover leaves it looking
// disabled), and the rail marker is what says the whole row is a target.
func TestWikiSidebarHoverMatchesDashboard(t *testing.T) {
	if !strings.Contains(wikiTemplate, ".wiki-side a:hover") {
		t.Fatal("sidebar rows have no hover rule")
	}
	// The lift must change the text colour, not only the background.
	block := wikiTemplate[strings.Index(wikiTemplate, ".wiki-side a:hover"):]
	block = block[:strings.Index(block, "}")]
	if !strings.Contains(block, "color: var(--text)") {
		t.Errorf("hover does not lift the label colour; the row reads as disabled:\n%s", block)
	}
	if !strings.Contains(block, "background: var(--bg-alt)") {
		t.Errorf("hover does not set the flat tint background:\n%s", block)
	}
	// The rail is the row affordance. Without it a reader aims at the words.
	if !strings.Contains(wikiTemplate, ".wiki-side a:hover::before") {
		t.Error("hover does not light the row rail")
	}
	// The rail must be declared with a transparent default, or it shows on every
	// row and marks nothing.
	if !strings.Contains(wikiTemplate, "background: transparent") {
		t.Error("rail has no transparent default, so it marks every row")
	}
	// Focus must be visible: keyboard users get no hover.
	if !strings.Contains(wikiTemplate, ".wiki-side a:focus-visible") {
		t.Error("sidebar rows have no focus ring")
	}
}

// wrapTables is what keeps a wide table from widening the page. It runs on the
// rendered HTML, so it must be idempotent — a table already inside the wrapper
// must not gain a second one, or the box grows on every build.
func TestWrapTablesIsIdempotentAndComplete(t *testing.T) {
	in := `<p>a</p><table><tr><td>x</td></tr></table><p>b</p><table><tr><td>y</td></tr></table>`
	once := wrapTables(in)
	twice := wrapTables(once)

	if !strings.Contains(once, `<div data-table-scroll><table>`) {
		t.Errorf("table was not wrapped: %q", once)
	}
	if strings.Count(once, "data-table-scroll") != 2 {
		t.Errorf("got %d wrappers for 2 tables, want 2", strings.Count(once, "data-table-scroll"))
	}
	if once != twice {
		t.Errorf("second pass changed the output, so wrappers nest:\n once: %q\ntwice: %q", once, twice)
	}
	// Content outside the tables must survive untouched.
	if !strings.Contains(once, "<p>a</p>") || !strings.Contains(once, "<p>b</p>") {
		t.Errorf("surrounding content was lost: %q", once)
	}
	// A page with no table must come back identical, not gain an empty wrapper.
	plain := "<p>no tables here</p>"
	if got := wrapTables(plain); got != plain {
		t.Errorf("plain page changed: %q", got)
	}
}

// An unclosed <table> must not panic or drop the rest of the page. The walk
// looks for the closing tag and stops when it cannot find one; the tail still
// has to be written out.
func TestWrapTablesSurvivesUnclosedTable(t *testing.T) {
	in := `<table><tr><td>x</td></tr><p>tail</p>`
	got := wrapTables(in)
	if !strings.Contains(got, "tail") {
		t.Errorf("tail was dropped: %q", got)
	}
	if !strings.Contains(got, "data-table-scroll") {
		t.Errorf("unclosed table was not wrapped: %q", got)
	}
}

// imageSize decides whether the browser can reserve the box before a lazy image
// arrives. A wrong width makes the page jump; a missing one silently drops the
// reservation. Both are checked against real bytes written to disk, not a mock,
// because the whole point is reading a real file header.
func TestImageSizeReadsRealHeaders(t *testing.T) {
	dir := t.TempDir()
	// A 1x1 PNG is the smallest valid IHDR the wiki can ship.
	png := filepath.Join(dir, "dot.png")
	if err := os.WriteFile(png, []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, // signature
		0, 0, 0, 13, 'I', 'H', 'D', 'R', // chunk length + type
		0, 0, 0x03, 0x20, // width 800
		0, 0, 0x02, 0x58, // height 600
		8, 6, 0, 0, 0, // bit depth, colour type, compression, filter, interlace
	}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	w, h, ok := imageSize(png)
	if !ok || w != 800 || h != 600 {
		t.Errorf("imageSize(png) = %d, %d, %v; want 800, 600, true", w, h, ok)
	}
	// A WebP in the VP8X form the wiki's .webp screenshots use.
	webp := filepath.Join(dir, "shot.webp")
	if err := os.WriteFile(webp, make([]byte, 32), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	buf, err := os.ReadFile(webp)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	copy(buf[0:4], "RIFF")
	copy(buf[8:12], "WEBP")
	copy(buf[12:16], "VP8X")
	// Stamp a 1024x768 canvas: each dimension is 24-bit little-endian, stored as
	// value minus 1, at the offsets the reader above expects.
	for i, v := range []int{1023, 1023 >> 8, 1023 >> 16, 767, 767 >> 8, 767 >> 16} {
		buf[20+i] = byte(v)
	}
	if err := os.WriteFile(webp, buf, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if w, h, ok := imageSize(webp); !ok || w != 1024 || h != 768 {
		t.Errorf("imageSize(webp) = %d, %d, %v; want 1024, 768, true", w, h, ok)
	}
	// A missing file must not fail the build: the image renders without a
	// reserved box, which is a smaller defect than a page that never builds.
	if _, _, ok := imageSize(filepath.Join(dir, "absent.png")); ok {
		t.Error("imageSize() read a file that does not exist")
	}
	// A JPEG is not decoded; returning false is correct, not a failure.
	jpg := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(jpg, []byte{0xFF, 0xD8, 0xFF, 0xE0}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, _, ok := imageSize(jpg); ok {
		t.Error("imageSize() decoded a JPEG, which it does not support")
	}
}

// A wiki directory with no pages is a configuration error, not an empty site:
// publishing nothing would silently drop the documentation.
func TestGenerateWikiFailsWithoutPages(t *testing.T) {
	root := t.TempDir()
	wiki := filepath.Join(root, "wiki")
	if err := os.MkdirAll(wiki, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	err := generateWiki(wiki, filepath.Join(root, "_site"), "luqman-v1/9router-go", "")
	if err == nil {
		t.Fatal("generateWiki() succeeded with no pages, want an error")
	}
}

// The index has to carry the page body, not only its headings. A term that lives
// in a paragraph and never gets a heading of its own must still find the page —
// otherwise searching for a documented feature returns nothing and the reader
// concludes the feature does not exist.
func TestSearchIndexCarriesBodyText(t *testing.T) {
	body := "The auto-ping scheduler keeps quota current without traffic.\n"
	got := searchableText(body)
	for _, want := range []string{"auto-ping", "scheduler", "quota"} {
		if !strings.Contains(got, want) {
			t.Errorf("searchableText() dropped %q: %q", want, got)
		}
	}
	// A heading line is not body text; it is indexed separately with its anchor.
	if strings.Contains(searchableText("# A heading\n\nBody.\n"), "a heading") {
		t.Error("searchableText() included a heading line")
	}
}

// A `#` line inside a fenced code block is content, not a heading: goldmark
// publishes no id for it, so indexing it yields an anchor no page carries and a
// search result that lands nowhere. Every wiki page carries shell and config
// samples, so this is the common case, not an edge case.
func TestHeadingsIgnoresFencedCode(t *testing.T) {
	got := headings(strings.Join([]string{
		"# Real Heading",
		"",
		"```bash",
		"# a comment inside a sample script",
		"PORT=20129 ./9router-go",
		"```",
		"",
		"## Another Real Heading",
		"",
		"~~~",
		"# tilde-fenced comment",
		"~~~",
	}, "\n"))

	if len(got) != 2 {
		t.Fatalf("headings() returned %d entries, want 2: %+v", len(got), got)
	}
	if got[0].text != "Real Heading" || got[0].anchor != "real-heading" {
		t.Errorf("first = %+v, want {Real Heading real-heading}", got[0])
	}
	if got[1].text != "Another Real Heading" || got[1].anchor != "another-real-heading" {
		t.Errorf("second = %+v, want {Another Real Heading another-real-heading}", got[1])
	}
}

// Anchors in the index must be the ids goldmark publishes, or a search result
// links to the top of the page instead of the section it matched.
func TestAnchorIDMatchesGoldmarkOutput(t *testing.T) {
	root := t.TempDir()
	wiki := filepath.Join(root, "wiki")
	if err := os.MkdirAll(wiki, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A heading whose published id drops punctuation and lowercases.
	writeFixture(t, filepath.Join(wiki, "pg.md"), "---\ntitle: PG\n---\n\n# Usage & Analytics\n")
	out := filepath.Join(root, "_site")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := generateWiki(wiki, out, "luqman-v1/9router-go", "9.9.9"); err != nil {
		t.Fatalf("generateWiki: %v", err)
	}

	page := readFile(t, filepath.Join(out, "pg.html"))
	index := readFile(t, filepath.Join(out, "search-index.json"))

	// The id the renderer emitted for the <h1>. The search box also carries an
	// id, so the first id=" in the document is not the heading's.
	h1Start := strings.Index(page, "<h1")
	if h1Start < 0 {
		t.Fatal("rendered page has no <h1>")
	}
	rest := page[h1Start:]
	idStart := strings.Index(rest, `id="`)
	if idStart < 0 {
		t.Fatal("rendered <h1> has no id")
	}
	rest = rest[idStart+4:]
	idEnd := strings.Index(rest, `"`)
	publishedID := rest[:idEnd]

	if !strings.Contains(index, `"anchor":"`+publishedID+`"`) {
		t.Errorf("index anchor does not match published id %q", publishedID)
	}
}

// The desktop nav is taller than a short window, so it has to scroll on its
// own. Without a height bound plus overflow, the sticky sidebar spills past
// the fold with no way to reach the lower links — the page scrolls, the sidebar
// does not, and the last entries are simply gone.
// Measured before the fix: 21 links, sidebar 1103px tall against a 600px
// viewport, overflow-y visible. After the first attempt it still failed: the
// box was 680px against a 700px column, so it stopped 20px short of the bottom.

// The wiki's "new" badge marks pages that document a release newer than the one
// the wiki was built from, so a reader does not take an experimental flag as the
// shipped contract. The direction of the comparison is the whole feature: a wiki
// built from 1.9.15 must not flag a page documented in 1.9.14, and equality must
// mean no badge. The parts are compared numerically, because a string compare
// orders 1.10.0 before 1.9.0.
func TestReleaseNewer(t *testing.T) {
	tests := []struct {
		name  string
		page  string
		built string
		want  bool
	}{
		{"page ahead of the wiki", "1.9.14", "1.9.13", true},
		{"same release", "1.9.14", "1.9.14", false},
		{"page behind the wiki", "1.9.14", "1.9.15", false},
		{"experimental build ahead of a stable manifest", "1.9.15-exp.16", "1.9.14", true},
		{"experimental build of the documented line", "1.9.15-exp.16", "1.9.15", false},
		{"major bump ahead", "2.0.0", "1.9.15", true},
		{"minor ten ahead of nine", "1.10.0", "1.9.0", true},
		{"minor ten behind nine", "1.9.0", "1.10.0", false},
		// A value that is not a version cannot be ordered. Flagging every page on
		// a typo would make the badge noise, so it reads as current instead.
		{"not a version", "notaversion", "1.9.14", false},
		{"empty", "", "1.9.14", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := releaseNewer(tt.page, tt.built); got != tt.want {
				t.Errorf("releaseNewer(%q, %q) = %v, want %v", tt.page, tt.built, got, tt.want)
			}
		})
	}
}

func TestWikiSidebarScrollsOnDesktop(t *testing.T) {
	// Pin the base rule, not the mobile one: the sheet overrides it with its
	// own overflow-y, and that one is already covered.
	base := wikiTemplate[:strings.Index(wikiTemplate, "@media")]
	side := base[strings.Index(base, ".wiki-side {"):]
	side = side[:strings.Index(side, "}")]
	if !strings.Contains(side, "height: calc(100vh") {
		t.Errorf("desktop sidebar has no viewport-bounded height; a short window loses the lower links:\n%s", side)
	}
	if !strings.Contains(side, "overflow: hidden") {
		t.Errorf("desktop sidebar does not bound its overflow; the nav spills past the fold:\n%s", side)
	}
	// The height must equal the distance from the top of the column to the
	// bottom of the window, so the box reaches the bottom edge. Measured: the
	// masthead is 68px and the shell adds 32px of padding, so the column starts
	// 100px down and the box needs 100vh minus 6.25rem — 7.5rem stopped 20px
	// short and the footer floated above the bottom edge.
	if !strings.Contains(side, "100vh - 6.25rem") {
		t.Errorf("sidebar height does not reach the bottom of the window:\n%s", side)
	}
	// The sticky offset must match that same distance, or the column starts
	// lower than the box was sized for and the bottom edge stops short again.
	if !strings.Contains(side, "top: 6.25rem") {
		t.Errorf("sidebar sticky offset does not match its height:\n%s", side)
	}
	// The middle child is the only part that scrolls, so the header and footer
	// stay pinned by the flex layout. min-height:0 is what lets a flex child
	// shrink and scroll at all; without it the child keeps its content height
	// and pushes the footer off the bottom.
	if !strings.Contains(base, ".wiki-side-scroll {\n        flex: 1 1 auto;\n        min-height: 0;\n        overflow-y: auto;") {
		t.Error("the sidebar's middle child is not the scroll container")
	}
	// A wheel over the nav must not scroll the page once the nav bottoms out.
	if !strings.Contains(side, "overscroll-behavior: contain") {
		t.Errorf("sidebar does not contain overscroll; scrolling past its end scrolls the article:\n%s", side)
	}
}
