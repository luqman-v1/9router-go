package web

import (
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

//go:embed dist/*
var DistFS embed.FS

// Handler returns an http.Handler that serves the embedded SPA files with index.html fallback for client-side routing.
//
// The assets are served pre-compressed. `web/scripts/precompress.ts` writes a
// `.br` and a `.gz` sibling next to every compressible file at build time,
// and this handler picks the best variant the client's Accept-Encoding
// allows. The reason is the deployment this gateway ships into: the dashboard
// is served from the gateway itself, so the bundle crosses the operator's own
// link — often a phone on mobile data — and an uncompressed 1.2 MB script is
// the single largest thing in the first paint.
//
// Compressing at request time was the alternative and was rejected: this
// gateway runs on a 1 vCPU box and already spends its CPU on streaming LLM
// responses, and the bundle never changes between releases, so paying the
// compression cost per request buys nothing the build-time variant did not.
func Handler() http.Handler {
	subFS, err := fs.Sub(DistFS, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Dashboard web assets not available", http.StatusNotFound)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}

		// A file in the embedded FS is served as itself: the CSS, the JS, the
		// SVG, the PNG.
		if isFile(subFS, name) {
			applyAssetHeaders(w, name)
			serveFile(w, r, subFS, name)
			return
		}

		// A path with a file extension that is not in the FS is a missing
		// asset, not a client-side route: answering it with the entry document
		// would hand the browser HTML for a script it asked to execute.
		if strings.Contains(path.Base(name), ".") && !strings.HasSuffix(name, ".html") {
			http.NotFound(w, r)
			return
		}

		// Everything else is a client-side route, which the SPA resolves on
		// its own. The entry document answers it.
		if !isFile(subFS, "index.html") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		serveFile(w, r, subFS, "index.html")
	})
}

// isFile reports whether `name` is a regular file in the embedded FS.
func isFile(subFS fs.FS, name string) bool {
	f, err := subFS.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return !stat.IsDir()
}

// applyAssetHeaders sets the cache policy and the content type for an asset.
//
// The entry document names the content-hashed bundle. Embedded files carry no
// ModTime, so there is nothing for a browser to revalidate against: without
// the no-cache policy it is free to keep serving an old entry, and therefore
// an old bundle, after an upgrade. The hashed assets are the opposite — their
// name changes with their content, so they can be kept forever.
func applyAssetHeaders(w http.ResponseWriter, name string) {
	switch {
	case name == "index.html":
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasPrefix(name, "assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case name == "sw.js":
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Service-Worker-Allowed", "/")
	case name == "manifest.webmanifest":
		w.Header().Set("Content-Type", "application/manifest+json")
	}
}

// serveFile serves `name` from the embedded FS, preferring the pre-compressed
// variant the client's Accept-Encoding allows.
//
// The negotiation is deliberately conservative: brotli only when the client
// explicitly asks for it, gzip as the widely supported fallback, and the
// plain file when the client asks for neither. A variant that exists but
// cannot be opened falls back to the original rather than failing the
// request — a missing compression variant must never cost the operator their
// dashboard.
//
// The `Vary: Accept-Encoding` header is required: without it a shared cache
// that stored the brotli variant would hand it to a client that cannot read
// it.
func serveFile(w http.ResponseWriter, r *http.Request, subFS fs.FS, name string) {
	// The response body depends on the request's Accept-Encoding even when no
	// variant is served: the identity body and the compressed body are
	// different bytes for the same URL. A shared cache that stored one and
	// handed it to a client that asked for the other would break the page,
	// so Vary is set on every path — not only on the compressed one.
	w.Header().Set("Vary", "Accept-Encoding")

	encoding, variant := negotiateEncoding(r.Header.Get("Accept-Encoding"))
	if variant != "" {
		if serveVariant(w, r, subFS, name+variant, name, encoding) {
			return
		}
	}

	// No usable variant: serve the original.
	serveVariant(w, r, subFS, name, name, "")
}

// serveVariant writes one file from the embedded FS to the response.
//
// It reports whether it wrote the file. When `encoding` is empty the file is
// written as-is; otherwise it is written with the matching Content-Encoding,
// which is what tells the browser to decode it.
//
// The body goes through http.ServeContent, which is what answers range
// requests (206, Content-Range, 416) and the conditional requests. The old
// handler answered ranges too, through http.FileServer; dropping that was a
// silent regression — a client that asked for part of a file got the whole
// file with a 200 instead of a 206, and a client that asked past the end got
// a 200 instead of a 416.
//
// ServeContent is handed the *variant's* own size, so a range is resolved
// against the bytes that are actually on the wire. That matters for a
// compressed variant: the range is a range of the compressed bytes, which is
// the representation the client sees.
//
// ServeContent refuses to set a Content-Length for a response that carries a
// Content-Encoding — it cannot know that the length it computed is the
// length of the compressed body. The result is a chunked response with no
// length, which costs the browser a chunk header per read. So the length is
// set here, where the size is known.
//
// A range request is the one case where ServeContent sets the length
// itself, and it sets it to the length of the range rather than of the whole
// file. Setting it before the call would be wrong for a range, so it is only
// set when there is no range.
func serveVariant(w http.ResponseWriter, r *http.Request, subFS fs.FS, name, contentName, encoding string) bool {
	f, err := subFS.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", contentTypeFor(contentName))
	}

	seeker, ok := f.(io.ReadSeeker)
	if !ok {
		data, readErr := io.ReadAll(f)
		if readErr != nil {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return true
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		// The client can disappear mid-write. The status line and headers are
		// already on the wire by then, so there is nothing left to report and
		// no second response to send — the bytes that did arrive are the ones
		// that count.
		_, _ = w.Write(data)
		return true
	}

	if r.Header.Get("Range") == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	}
	http.ServeContent(w, r, contentName, stat.ModTime(), seeker)
	return true
}

// contentTypeFor returns the media type for a served asset.
//
// It exists because the served path sets the type itself rather than letting
// http.FileServer infer it from the request URL — the SPA fallback serves
// index.html for a request whose URL says something else entirely.
//
// The types the dashboard ships are pinned rather than looked up.
// mime.TypeByExtension consults the machine's MIME database (/etc/mime.types
// and friends) on top of Go's small builtin table, so the same binary serves
// a different Content-Type on a different host — and a script served as the
// wrong type is refused by the browser. The pinned list covers every
// extension in the built dashboard; anything else falls back to the lookup.
func contentTypeFor(name string) string {
	switch path.Ext(name) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".json":
		return "application/json"
	case ".webmanifest":
		return "application/manifest+json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/vnd.microsoft.icon"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// negotiateEncoding picks the compression variant to serve.
//
// It returns the Content-Encoding value and the file suffix of the variant
// ("br" or "gz"), or empty strings when the client asked for nothing this
// handler can provide.
//
// The `Accept-Encoding` header is a list with optional quality values
// ("gzip;q=0.5", "*", "identity"). Only an explicit `br` or `gzip` counts:
// a wildcard is not enough, because `br` and `gzip` are not always
// interchangeable with whatever else the client accepts, and the entry
// document has to be readable by the browser that asked for it.
//
// On equal quality the smaller encoding wins, so brotli is preferred over
// gzip. Chrome sends "gzip, deflate, br" with no quality values at all, and
// a handler that keeps the first match on a tie hands that browser the
// larger of the two — the header order says nothing about preference.
func negotiateEncoding(header string) (encoding string, suffix string) {
	if header == "" {
		return "", ""
	}

	// Preference on a quality tie. Lower is better.
	preference := map[string]int{"br": 0, "gzip": 1}

	bestName := ""
	bestSuffix := ""
	bestQ := -1.0
	bestPreference := 0

	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		rank, supported := preference[name]
		if !supported {
			continue
		}

		q := 1.0
		for _, field := range fields[1:] {
			field = strings.TrimSpace(field)
			if !strings.HasPrefix(strings.ToLower(field), "q=") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(field[2:]), 64)
			if err != nil {
				continue
			}
			q = parsed
		}
		// q=0 means "not acceptable", so the variant is never chosen.
		if q <= 0 {
			continue
		}

		if q > bestQ || (q == bestQ && rank < bestPreference) {
			bestName = name
			bestSuffix = nameSuffix(name)
			bestQ = q
			bestPreference = rank
		}
	}

	if bestName == "" {
		return "", ""
	}
	return bestName, bestSuffix
}

// nameSuffix maps a Content-Encoding name to the suffix the pre-compressed
// variant was written under.
func nameSuffix(name string) string {
	if name == "br" {
		return ".br"
	}
	return ".gz"
}
