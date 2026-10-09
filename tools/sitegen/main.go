// Command sitegen renders README.md into the GitHub Pages site published at
// https://luqman-v1.github.io/9router-go.
//
// The homepage is the README, not a second copy of it: a landing page written
// separately drifts from the README within a release, and every fix then has to
// be made twice. So the build reads README.md, renders it, and wraps it in the
// small amount of chrome a hosted page needs (a header with the three links a
// visitor actually wants, a footer, a stylesheet).
//
// Relative links inside the README are the other half of the problem. On
// github.com `docs/screenshots/providers.png` resolves against the repository;
// on the Pages site it resolves against the site root and 404s. Images are
// copied into the output, and prose links to repository files are rewritten to
// their github.com blob URL.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

func main() {
	readmePath := flag.String("readme", "README.md", "markdown file rendered as the site homepage")
	assetsDir := flag.String("assets", "site", "directory of static files copied verbatim into the site")
	outDir := flag.String("out", "_site", "directory the site is written to")
	repo := flag.String("repo", "luqman-v1/9router-go", "owner/name used to resolve relative repository links")
	flag.Parse()

	if err := generate(*readmePath, *assetsDir, *outDir, *repo); err != nil {
		fmt.Fprintln(os.Stderr, "sitegen:", err)
		os.Exit(1)
	}
}

func generate(readmePath, assetsDir, outDir, repo string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	body, err := renderMarkdown(readmePath, outDir, repo)
	if err != nil {
		return fmt.Errorf("render %s: %w", readmePath, err)
	}

	if err := copyFile(filepath.Join(assetsDir, "style.css"), filepath.Join(outDir, "style.css")); err != nil {
		return err
	}

	// The redirect answers on /tg.html, on /tg, and on /tg/. GitHub Pages
	// resolves a bare /tg to tg.html, but a crawler or a mistyped trailing
	// slash should not land on the 404 page, so all three are written from the
	// one source file. See site/tg.html for why the invite link lives there.
	for _, dest := range []string{"tg.html", "tg/index.html"} {
		if err := copyFile(filepath.Join(assetsDir, "tg.html"), filepath.Join(outDir, filepath.FromSlash(dest))); err != nil {
			return err
		}
	}

	page, err := renderPage(body, repo)
	if err != nil {
		return fmt.Errorf("render page: %w", err)
	}

	return writeFile(filepath.Join(outDir, "index.html"), page)
}

func renderMarkdown(readmePath, outDir, repo string) ([]byte, error) {
	src, err := os.ReadFile(readmePath)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithAttribute()),
		// The README wraps its title block in <div align="center"> and hides the
		// environment variable table in <details>. Both are the author's markup
		// for GitHub's own renderer, so dropping them here would silently delete
		// content the repository depends on.
		goldmark.WithRendererOptions(goldmarkhtml.WithUnsafe()),
	)

	doc := md.Parser().Parse(text.NewReader(src))

	images := map[string]struct{}{}
	rewriteRelativeLinks(doc, repo, images)

	for rel := range images {
		if err := copyFile(filepath.Join(filepath.Dir(readmePath), filepath.FromSlash(rel)), filepath.Join(outDir, filepath.FromSlash(rel))); err != nil {
			return nil, err
		}
	}

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return nil, fmt.Errorf("render html: %w", err)
	}
	return buf.Bytes(), nil
}

// rewriteRelativeLinks points every repository-relative link at github.com and
// records the image ones for copying. Anchors, absolute URLs and site-absolute
// paths are left untouched.
func rewriteRelativeLinks(doc ast.Node, repo string, images map[string]struct{}) {
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch n := node.(type) {
		case *ast.Link:
			n.Destination = blobURL(repo, n.Destination)
		case *ast.Image:
			// A badge hosted on shields.io is an absolute URL and must be left
			// alone; isAsset only matches extension, so the scheme check that
			// blobURL does has to come first or every badge is "copied" from a
			// path named after its https:// prefix.
			if hasScheme(string(n.Destination)) || strings.HasPrefix(string(n.Destination), "/") {
				return ast.WalkContinue, nil
			}
			if !isAsset(n.Destination) {
				n.Destination = blobURL(repo, n.Destination)
				return ast.WalkContinue, nil
			}
			images[string(n.Destination)] = struct{}{}
		}
		return ast.WalkContinue, nil
	})
}

// blobURL turns `docs/DATABASE.md#section` into its github.com blob URL. Input
// that does not name a repository file is returned unchanged.
func blobURL(repo string, dest []byte) []byte {
	raw := string(dest)
	if raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "/") || hasScheme(raw) {
		return dest
	}
	// A link pointing outside the repository cannot be resolved against it.
	if strings.HasPrefix(raw, "..") {
		return dest
	}

	file, fragment, hasFragment := strings.Cut(raw, "#")
	if file == "" {
		return dest
	}

	url := fmt.Sprintf("https://github.com/%s/blob/main/%s", repo, file)
	if hasFragment {
		url += "#" + fragment
	}
	return []byte(url)
}

func hasScheme(raw string) bool {
	idx := strings.Index(raw, ":")
	if idx < 1 {
		return false
	}
	scheme := raw[:idx]
	for i, r := range scheme {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9', r == '+', r == '-', r == '.':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func isAsset(dest []byte) bool {
	file, _, _ := strings.Cut(string(dest), "#")
	switch strings.ToLower(path.Ext(file)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".avif", ".ico":
		return true
	}
	return false
}

func renderPage(body []byte, repo string) ([]byte, error) {
	tmpl, err := template.New("page").Funcs(template.FuncMap{
		"blob": func(p string) string {
			return fmt.Sprintf("https://github.com/%s/blob/main/%s", repo, p)
		},
	}).Parse(pageTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	data := struct {
		Body template.HTML
		Repo string
	}{Body: template.HTML(body), Repo: repo}

	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}

func copyFile(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	return writeFile(dest, data)
}

func writeFile(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", dest, err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}
