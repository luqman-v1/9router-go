package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// page is one rendered wiki document.
type page struct {
	// Slug is the source file name without its extension, and is the URL the
	// page is published at.
	Slug string
	// Title comes from the frontmatter, falling back to the first heading.
	Title string
	// Order is the frontmatter value used to sort the sidebar.
	Order int
	// Section is the sidebar heading this page sits under. A page without one
	// lands in the first group, which renders without a heading — the same
	// convention the dashboard's top nav group uses.
	Section string
	// Body is the rendered HTML article.
	Body template.HTML
	// Text is the markdown with frontmatter stripped, indexed for search.
	Text string
	// Since is the release that first documented this page, from frontmatter.
	Since string
	// Unreleased is set when Since names a release newer than the one the wiki
	// was built from: the page describes behaviour no published version has, so
	// the sidebar marks it instead of the reader assuming it is the contract.
	Unreleased bool
}

// frontmatter is the small YAML subset the wiki pages use. The pages only need
// title, order, section, and since, so a full YAML parser is not worth the
// dependency; the format is deliberately the shape go-yaml emits for those
// scalars.
type frontmatter struct {
	Title   string
	Order   int
	Section string
	// Since is the release that first documented this page. A page whose since
	// is newer than the release the wiki was built from describes behaviour that
	// no published version has yet, so the sidebar marks it as unreleased rather
	// than letting a reader take an experimental flag as the shipped contract.
	Since string
}

// generateWiki renders every .md in wikiDir into outDir, plus the search index
// the pages' client-side search reads.
func generateWiki(wikiDir, outDir, repo, version string) error {
	entries, err := filepath.Glob(filepath.Join(wikiDir, "*.md"))
	if err != nil {
		return fmt.Errorf("list wiki pages: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no wiki pages in %s", wikiDir)
	}
	sort.Strings(entries)

	pages := make([]*page, 0, len(entries))
	for _, entry := range entries {
		p, err := renderWikiPage(entry, outDir)
		if err != nil {
			return err
		}
		pages = append(pages, p)
	}

	// Pages without an explicit order sort after the ordered ones, alphabetically,
	// so a new page never lands in the middle of the documented order.
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].Order != pages[j].Order {
			return pages[i].Order < pages[j].Order
		}
		return pages[i].Slug < pages[j].Slug
	})
	// A page documenting behaviour newer than the release the wiki claims to
	// describe is marked unreleased. The comparison is on the release line, not
	// the whole string: `1.9.15-exp.16` is a build of the `1.9.15` line, and a
	// page whose since is that build is ahead of a wiki built from the stable
	// `1.9.14` manifest — which is what `9router-go update` advertises. The
	// direction matters: a wiki built from 1.9.15 must not flag pages that were
	// already documented in 1.9.14.
	for _, p := range pages {
		if p.Since == "" || version == "" {
			continue
		}
		if releaseNewer(p.Since, version) {
			p.Unreleased = true
		}
	}

	tmpl, err := template.New("wiki").Funcs(template.FuncMap{
		"blob": func(p string) string {
			return fmt.Sprintf("https://github.com/%s/blob/main/%s", repo, p)
		},
	}).Parse(wikiTemplate)
	if err != nil {
		return fmt.Errorf("parse wiki template: %w", err)
	}

	for _, p := range pages {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, wikiData{Page: p, Pages: pages, Groups: groupBySection(pages), Repo: repo, StableVersion: version}); err != nil {
			return fmt.Errorf("render wiki page %s: %w", p.Slug, err)
		}
		if err := writeFile(filepath.Join(outDir, p.Slug+".html"), buf.Bytes()); err != nil {
			return err
		}
	}

	index, err := renderSearchIndex(pages)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(outDir, "search-index.json"), index)
}

// renderWikiPage parses one markdown file into a rendered page. Links to other
// wiki pages stay relative; links to repository files become github.com blob
// URLs, and the images they reference are copied next to the page.
func renderWikiPage(path, outDir string) (*page, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	fm, body := splitFrontmatter(string(src))
	slug := strings.TrimSuffix(filepath.Base(path), ".md")

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithAttribute()),
		goldmark.WithRendererOptions(goldmarkhtml.WithUnsafe()),
	)

	doc := md.Parser().Parse(text.NewReader([]byte(body)))

	images := map[string]struct{}{}
	wikiLinks := map[string]struct{}{}
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Link:
			dest := string(n.Destination)
			// The destination arrives whole — `api.md#anchor` is one string, not
			// Destination plus a separate fragment. So the .md test has to look
			// at the part before the '#', or every link carrying an anchor falls
			// through to the github.com fallback below.
			file, fragment, hasFragment := strings.Cut(dest, "#")
			switch {
			case strings.HasSuffix(file, ".md") && !hasScheme(dest) && !strings.HasPrefix(dest, "/"):
				// A link to another wiki page. It stays relative so it resolves
				// inside the site, and the .md suffix is swapped for .html
				// because that is what is published.
				// The scheme and site-absolute checks come first: an absolute
				// github.com URL to a repository file also ends in .md, and
				// rewriting that one produced .../COMPARISON.html — a URL that
				// 404s on both the site and GitHub. Only a relative destination
				// names a wiki page.
				slug := strings.TrimSuffix(file, ".md")
				wikiLinks[slug] = struct{}{}
				out := slug + ".html"
				if hasFragment {
					out += "#" + fragment
				}
				n.Destination = []byte(out)
			case isAsset(n.Destination):
				if !hasScheme(dest) && !strings.HasPrefix(dest, "/") {
					images[dest] = struct{}{}
				}
			default:
				n.Destination = blobURL(defaultRepo, n.Destination)
			}
		case *ast.Image:
			dest := string(n.Destination)
			// A screenshot page carries every image in one document, so without
			// lazy loading the reader waits for all of them before the page is
			// usable. Below the fold they should not be fetched at all until
			// they are scrolled to. This applies to remote images too — the
			// contributor avatars are the first external assets the wiki loads,
			// and 24 of them must not be fetched before the article renders.
			n.SetAttributeString("loading", []byte("lazy"))
			n.SetAttributeString("decoding", []byte("async"))
			if isAsset(n.Destination) && !hasScheme(dest) && !strings.HasPrefix(dest, "/") {
				images[dest] = struct{}{}
				// Dimensions are read from the file so the browser reserves the
				// box before the bytes arrive; a lazy gallery that has no box
				// reflows on every scroll, which is worse than no lazy loading.
				if w, h, ok := imageSize(filepath.Join(filepath.Dir(path), filepath.FromSlash(dest))); ok {
					n.SetAttributeString("width", []byte(strconv.Itoa(w)))
					n.SetAttributeString("height", []byte(strconv.Itoa(h)))
				}
			}
		}
		return ast.WalkContinue, nil
	})

	// Every image the page references must exist, or the published page has a
	// broken picture. Failing the build is what the README path already does.
	for rel := range images {
		if err := copyFile(filepath.Join(filepath.Dir(path), filepath.FromSlash(rel)), filepath.Join(outDirFor(path, outDir), filepath.FromSlash(rel))); err != nil {
			return nil, err
		}
	}

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, []byte(body), doc); err != nil {
		return nil, fmt.Errorf("render %s: %w", path, err)
	}
	// Wrap the tables after rendering, so a wide one scrolls inside its own box
	// instead of widening the page. See wrapTables for why this is not an AST
	// transform.
	rendered := wrapTables(buf.String())

	title := fm.Title
	if title == "" {
		title = firstHeading(body)
	}
	if title == "" {
		title = slug
	}

	return &page{
		Slug:    slug,
		Title:   title,
		Order:   fm.Order,
		Section: fm.Section,
		Body:    template.HTML(rendered),
		Text:    body,
		Since:   fm.Since,
	}, nil
}

// imageSize reads a raster image's pixel dimensions from its header, so the
// generator can emit width and height and the browser can reserve the box before
// the bytes arrive. Only the formats this wiki ships are decoded; anything else
// returns false and the image renders without a reserved box rather than the
// build failing over a screenshot.
func imageSize(path string) (int, int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = f.Close() }()
	// Read what the file has, not a fixed 32: a minimal PNG header is 29 bytes
	// and demanding 32 would reject it. A short read is only fatal when the
	// header itself is too small to hold a signature.
	hdr := make([]byte, 32)
	n, _ := io.ReadFull(f, hdr)
	if n < 24 {
		return 0, 0, false
	}
	hdr = hdr[:n]
	switch {
	// PNG: the IHDR width and height are big-endian uint32 at a fixed offset.
	case hdr[0] == 0x89 && hdr[1] == 'P' && hdr[2] == 'N' && hdr[3] == 'G':
		w := binary.BigEndian.Uint32(hdr[16:20])
		h := binary.BigEndian.Uint32(hdr[20:24])
		return int(w), int(h), w > 0 && h > 0
	// WebP: a RIFF container whose VP8/VP8L/VP8X chunk carries the dimensions.
	case string(hdr[0:4]) == "RIFF" && string(hdr[8:12]) == "WEBP":
		switch string(hdr[12:16]) {
		case "VP8X":
			// RIFF(4) + size(4) + WEBP(4) + VP8X(4) + flags(4) puts the 24-bit
			// little-endian canvas width at byte 20 and the height at 23, each
			// stored as value minus 1. The shifts must be widened to int first:
			// a byte shifted left by 8 overflows uint8 and silently wraps, which
			// read 1023 as 255.
			w := int(hdr[20]) | int(hdr[21])<<8 | int(hdr[22])<<16
			h := int(hdr[23]) | int(hdr[24])<<8 | int(hdr[25])<<16
			return w + 1, h + 1, w > 0 && h > 0
		case "VP8L":
			b := uint32(hdr[21]) | uint32(hdr[22])<<8 | uint32(hdr[23])<<16 | uint32(hdr[24])<<24
			return int(b&0x3FFF) + 1, int((b>>14)&0x3FFF) + 1, true
		case "VP8 ":
			// Lossy VP8: a 14-bit little-endian pair, again widened before the
			// shift so the high byte does not wrap.
			w := int(hdr[26]) | int(hdr[27])<<8
			h := int(hdr[28]) | int(hdr[29])<<8
			return w & 0x3FFF, h & 0x3FFF, w > 0 && h > 0
		}
	}
	return 0, 0, false
}

// defaultRepo is the repository the wiki's repository-relative links resolve
// against. The generator only has one repo to resolve against, so the wiki
// pages do not need their own.
const defaultRepo = "luqman-v1/9router-go"

// tableWrapperAttr marks the element the generator adds around a table.
const tableWrapperAttr = "data-table-scroll"

// wrapTables wraps every top-level <table> in a scrollable <div>. It runs on the
// rendered HTML rather than the AST: goldmark's own TextBlock carries no
// attribute, so an AST wrapper cannot be marked and would be re-applied on every
// build, nesting wrappers without bound. A regex over the finished HTML is
// bounded and idempotent — a table already inside the wrapper is skipped.
func wrapTables(html string) string {
	var b strings.Builder
	rest := html
	for {
		i := strings.Index(rest, "<table>")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		end := strings.Index(rest[i:], "</table>")
		table := rest[i:]
		if end < 0 {
			// No closing tag: goldmark always emits one, so this is a page
			// hand-edited or a truncated write. Take the rest of the document as
			// the table rather than dropping it or leaving it unwrapped.
			b.WriteString(rest[:i])
			b.WriteString("<div " + tableWrapperAttr + ">" + table + "</div>")
			return b.String()
		}
		end += i + len("</table>")
		table = rest[i:end]
		b.WriteString(rest[:i])
		// Already wrapped by an earlier pass — do not nest a second div.
		if strings.HasSuffix(strings.TrimRight(b.String(), " \t\n"), "<div "+tableWrapperAttr+">") {
			b.WriteString(table)
		} else {
			b.WriteString("<div " + tableWrapperAttr + ">" + table + "</div>")
		}
		rest = rest[end:]
	}
}

// outDirFor returns the directory a wiki page's assets are copied into. It
// mirrors the page's own location in the wiki directory so a nested page keeps
// its relative image paths.
func outDirFor(pagePath, outDir string) string {
	return filepath.Join(outDir, filepath.Dir(pagePath))
}

// splitFrontmatter separates a leading `---` delimited block from the body.
// The block is the small key: value form the wiki pages use.
func splitFrontmatter(src string) (frontmatter, string) {
	var fm frontmatter
	if !strings.HasPrefix(src, "---\n") {
		return fm, src
	}
	end := strings.Index(src[4:], "\n---\n")
	if end < 0 {
		return fm, src
	}
	block := src[4 : 4+end]
	body := src[4+end+5:]

	for _, line := range strings.Split(block, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "title":
			fm.Title = value
		case "order":
			fm.Order = atoi(value)
		case "section":
			fm.Section = value
		case "since":
			fm.Since = value
		}
	}
	return fm, body
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// releaseCore reduces a version to the line it belongs to, so two versions can
// be compared without a full semver parser. `1.9.15-exp.16` and `1.9.15` are
// builds of the same release line and compare equal; `1.9.14` does not. The
// wiki needs exactly this question — "does this page describe a release newer
// than the one the wiki was built from?" — and a numeric comparison, because
// equality is the wrong test: a wiki built from 1.9.15 must not flag pages
// documented in 1.9.14.
func releaseNewer(page, built string) bool {
	a, okA := releaseParts(page)
	b, okB := releaseParts(built)
	if !okA || !okB {
		// A version that does not parse as three dot-separated numbers is not a
		// version this comparison can order. Treating it as newer would flag
		// every page on a typo, so it is treated as current instead.
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// releaseParts splits a version into its release line and numeric parts,
// dropping any prerelease suffix: `1.9.15-exp.16` is a build of `1.9.15`, so it
// compares against another release line by its 1.9.15. It reports false when
// the string is not three dot-separated numbers, which keeps a malformed value
// from silently comparing as zero.
func releaseParts(v string) ([3]int, bool) {
	var out [3]int
	core := v
	if i := strings.IndexByte(core, '-'); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// firstHeading returns the text of the document's first heading, used as the
// page title when the frontmatter does not carry one.
//
// It reads the raw markdown rather than walking the parsed tree: the page body
// is already in hand, a first `# ` line is unambiguously the top heading, and
// goldmark's Heading.Text is deprecated with no non-deprecated equivalent that
// is not more code than the line it replaces.
func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "# "))
	}
	return ""
}

// renderSearchIndex writes the JSON the client-side search reads. It carries
// the page title, its slug, and the headings and their anchors, so a query can
// jump straight to a section rather than only to a page.
func renderSearchIndex(pages []*page) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("[")
	for i, p := range pages {
		if i > 0 {
			buf.WriteString(",")
		}
		fmt.Fprintf(&buf, `{"slug":%q,"title":%q,"text":%q,"headings":[`, p.Slug, p.Title, searchableText(p.Text))
		for j, h := range headings(p.Text) {
			if j > 0 {
				buf.WriteString(",")
			}
			fmt.Fprintf(&buf, `{"text":%q,"anchor":%q}`, h.text, h.anchor)
		}
		buf.WriteString("]}")
	}
	buf.WriteString("]")
	return buf.Bytes(), nil
}

type heading struct {
	text   string
	anchor string
}

// searchableText flattens a page body into one lowercase string the client-side
// search can match against. Headings are already indexed separately with their
// anchors, so a query that only appears in a paragraph still finds the page —
// without this, searching for a feature that is described in prose but never
// given its own heading returns nothing.
func searchableText(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b.WriteString(line)
		b.WriteString(" ")
	}
	return strings.ToLower(b.String())
}

// headings extracts the markdown headings of a page body together with the
// anchor goldmark generates for them, so the index matches the published
// heading ids rather than inventing its own.
//
// Fenced code blocks are skipped. A `#` line inside a fence is content, not a
// heading — a comment in a sample script is the common case — and goldmark
// publishes no id for it, so indexing it produces an anchor that no page
// carries and a search result that lands nowhere.
func headings(body string) []heading {
	var out []heading
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level == 0 || level >= len(trimmed) || trimmed[level] != ' ' {
			continue
		}
		text := strings.TrimSpace(trimmed[level:])
		if text == "" {
			continue
		}
		out = append(out, heading{text: text, anchor: anchorID(text)})
	}
	return out
}

// anchorID mirrors goldmark's default heading id: lowercase, spaces to dashes,
// everything that is not a letter or digit dropped.
func anchorID(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	return b.String()
}
