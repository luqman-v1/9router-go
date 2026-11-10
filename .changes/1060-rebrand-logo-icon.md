### Rebrand: replace the "network hub" logo and icons with the 9r GO mark

The dashboard favicon, the PWA icons, and every logo the generated project page
and wiki draw were still the old terracotta "network hub" plate - a mark that no
longer matches the product. This replaces the whole set with the new 9r GO
wordmark (orange plate, white "9r GO" glyph) generated from a single 512px
source, so the tab, the installed app, the login page, the sidebar, and the
public docs all show the same mark.

- `web/public/favicon.svg`, `favicon.ico`, `icons/icon-{192,512}.png|.svg`: all
  six brand assets regenerated from the new mark. The `.svg` files keep their
  filenames (so `Sidebar`'s `/favicon.svg` default, `LoginView`, and
  `manifest.webmanifest` references stay valid) and embed the optimized artwork
  as a data URI, so they render identically to the source everywhere.
- `tools/sitegen`: the project page and wiki drew the old hub in five places -
  two favicons and three inline wordmark glyphs. The mark now lives in one
  shared `brandMarkPNG` constant (`brand.go`) referenced by both templates, so
  the tab icon and every inline glyph can never drift back to drawing their own.
- Updated the `sitegen` tests that pinned the old hub core (`#FF5C35`) to pin
  the shared brand mark instead, keeping the original contract: the pages must
  draw the product's own icon, not a stand-in.

Verified by rendering the generated favicon/PWA icons and the generated wiki
page in a real browser (the new mark shows in the masthead), plus
`go build ./...` and `go test ./tools/sitegen`.
