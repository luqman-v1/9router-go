### Dashboard: self-hosted fonts and pre-compressed assets

The first dashboard open was slow, and the "Connecting to 9router-go Localhost
Gateway (:20130)..." screen was the visible symptom. The gateway itself was
never the bottleneck: every boot endpoint answers in under 5 ms. The delay was
in the bytes the browser had to fetch before the app could mount.

Three causes, all measured in a real Chromium against a running gateway:

- **Two render-blocking stylesheets on a third-party origin.** Inter, JetBrains
  Mono and Material Symbols came from `fonts.googleapis.com` and
  `fonts.gstatic.com`. A blocked CDN stalls the first paint completely: with
  the stylesheet requests left hanging, the app did not mount for 21 s. A
  working CDN still costs a DNS lookup, a TLS handshake and two round trips
  to a host that has nothing to do with the gateway the operator is running.
- **A 3.9 MB icon font.** The Material Symbols variable font ships every glyph
  the family defines, on every axis, live.
- **An uncompressed bundle.** The embedded asset handler had no compression, so
  the 1.2 MB script and the 114 kB stylesheet crossed the wire as-is.

What changed:

- Inter, JetBrains Mono and Material Symbols are now vendored into
  `web/src/assets` and served from the gateway's own origin. The Google Fonts
  links are gone from the entry document.
- The Material Symbols font is instanced down to the axes the stylesheet
  pins anyway (`wght` 400, `GRAD` 0, `opsz` 24, `FILL` 0..1): **3908 KB →
  349 KB**, with every glyph kept, so an icon name added to the catalog later
  still renders. `bun run vendor:fonts` regenerates it.
- The built assets are pre-compressed at build time and the Go handler serves
  the `.br` or `.gz` variant the client's `Accept-Encoding` allows, with the
  matching `Content-Encoding` and a `Vary: Accept-Encoding`. Brotli is
  preferred over gzip on a quality tie, because Chrome sends `gzip, deflate,
  br` with no quality values and the header order says nothing about
  preference. The compression runs as part of `bun run build` — `vite build`
  clears `dist`, so a build that skipped it would leave the gateway embedding
  an uncompressed bundle, and the handler would fall back to the original
  file without anything failing. `web/embed_compression_test.go` now pins the
  variants' existence so that omission fails the build.

Three defects found while reviewing the change, all fixed here:

- **Range requests stopped working.** The rewrite replaced `http.FileServer`
  with a plain write, which silently dropped range support: a client asking
  for part of a file got the whole file with a 200, and a client asking past
  the end got a 200 instead of a 416. The body goes through
  `http.ServeContent` again, handed the variant's own size so a range is
  resolved against the bytes that are on the wire.
- **`Vary: Accept-Encoding` was only set on the compressed path.** The
  identity body and the compressed body are different bytes for the same
  URL, so a shared cache that stored one and handed it to a client that
  asked for the other would break the page. The header is now set on every
  negotiated response.
- **A compressed variant could be larger than the original.** The handler
  prefers the compressed variant whenever the client accepts it, so a
  variant that grew would make the client download more bytes than the
  identity body. The compressor now skips a file whose variant is not
  smaller.

Two more, found while checking the ports of the change:

- **The Content-Type depended on the machine.** `mime.TypeByExtension`
  consults the host's MIME database (`/etc/mime.types` and friends) on top
  of Go's builtin table, so the same binary served a different type on a
  different host — and a script served under the wrong type is refused by
  the browser. The types the dashboard ships are now pinned.
- **The lint recipe could leave a stale variant.** `mkdir -p web/dist &&
  printf '<!doctype html>' > web/dist/index.html` exists because
  `//go:embed` needs something to embed. Overwriting a built `index.html`
  left its `.br` variant describing the old document, and the gateway
  prefers that variant. The recipe now writes the stub only when there is
  no build.

Measured on the same machine, fresh browser context per run, gateway serving
its own embedded assets:

| | before | after |
|---|---|---|
| bytes transferred | 5279 KB | 683 KB |
| requests | 23–25 | 21 |
| load event | 322–3355 ms | 251–296 ms |

The spread on the "before" side is the CDN: the same build took 3.2 s in one
run and 12.3 s in the next, while the self-hosted build stayed within 45 ms
of itself. The remaining weight is the JavaScript bundle itself (226 kB
brotli), which is a separate concern from this change.

Re-measured on this branch, in a fresh browser context with the cache
disabled, against the gateway serving its own embedded assets: **644 kB**
transferred over 9 requests, load event at **168 ms**, every request to the
gateway's own origin. The script is served brotli with `Vary:
Accept-Encoding` and a `Content-Length` (1 200 279 → 231 618 bytes), the
stylesheet 114 969 → 15 288. The Material Symbols file is the largest
transfer left (349 kB) and is not compressible further — woff2 is already
deflate-based.

Verification: `go test ./web/...` (new compression contract: the negotiated
encoding, the `Vary` header on every negotiated response, the decoded body
matching the original file, the identity fallback, the SPA fallback, a
missing asset still being a 404, the compressed variants being present in the
build output, and range requests answered with 206/416);
`bun test src scripts` (508 pass, including a guard that fails the build if a
font CDN link comes back, a size budget for the icon font, and a compressor
test that a variant is never larger than the original);
`bun run ratchet:svelte` (0 unresolved identifiers, baseline tightened from
81 to 78); `oxlint --deny-warnings`; `go build ./...`; the cross-compile gate
for all five release targets; the dashboard e2e suite (42 pass); and the
integration suite.
