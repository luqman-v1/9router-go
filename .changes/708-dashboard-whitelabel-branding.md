### feat(dashboard): white-label the sidebar with an operator-set product name and logo

The dashboard sidebar rendered the project's own name and favicon as literals:
`Sidebar.svelte` hardcoded `9router-go` in the `<h1>`, the `alt` text and the
`<img src="/favicon.svg">`, so an operator reselling the gateway under their own
product had no way to rebrand it without rebuilding the bundle.

Two optional settings now carry the branding, and the sidebar renders whatever
they hold:

- `appName` — the product name shown in the sidebar header. Trimmed, capped at
  60 runes (the header block is fixed-width beside a 36px logo tile, so a longer
  string would wrap the header or push the version line out of the aside).
- `appLogoUrl` — the logo the sidebar's `<img>` points at. An `https://` URL, or
  a local absolute path such as `/logo.png` for an operator serving the asset
  themselves behind a reverse proxy. Capped at 500 characters.

An empty value falls back to the project defaults, so an operator who never
opens the card sees exactly what the sidebar showed before. The Settings page
gains a **White-label branding** card with both fields and a live preview of the
logo.

The logo can also be **uploaded** rather than linked, which is what makes the
branding usable on a gateway with no public host: `POST /api/settings/logo`
takes a multipart `file` field, writes it to `DATA_DIR/uploads/logo.<ext>` under
a fixed name, and rewrites `appLogoUrl` to `/uploads/logo.<ext>` — a path this
gateway serves itself, so the sidebar's `<img>` points at the operator's own
box. `GET /uploads/*` serves the stored asset behind the same dashboard page
gate as `/dashboard/*`, so an install that requires a login does not expose its
branding to an anonymous caller. The accepted formats are PNG, SVG, WebP and
JPEG, capped at 2 MB; the cap is enforced on the bytes read rather than on the
client's declared `Content-Length`, and the stored name is fixed rather than
taken from the client's filename, so a filename carrying `../` cannot escape the
uploads directory. Replacing the logo with a different extension removes the
previous file, so a stale asset is never served by a URL the operator no longer
chose.

Clearing `appLogoUrl` removes the uploaded file with it. The previous value is
read from the stored row before the write, and the file is deleted only after
the write succeeds — a failed write must leave the setting and its file in
agreement, not one pointing at a deleted asset. Only an asset this gateway
uploaded is removed: a path the operator serves themselves, such as
`/my-logo.png`, survives the clear, because deleting a file the gateway does not
own is not what clearing a setting means. The stored name is rebuilt from the
extension rather than taken from the stored value, so a path that survived a hand
edit cannot address a file outside the uploads directory. `DATA_DIR/uploads/` is
not part of a database backup, so a restore does not bring the logo back; the
operator re-uploads.

Validation lives in `internal/handlers/dashboard/whitelabel.go`, following the
shape `navvisibility.go` already established: the same normalizer runs on write
(`applyWhitelabelUpdate`, called from `HandleUpdateSettings`) and on read
(`sanitizeWhitelabel`, called from `sanitizeSettings`), so a row written by an
older build or by hand reaches the dashboard in the shape the sidebar expects.
A protocol-relative `//host` URL is refused — it silently re-points the request
at whatever host the operator typed, which is not the asset they meant to serve
— as are `http:`, `javascript:` and `data:` URLs. An unusable value is stored as
an explicit null rather than deleted, because `UpdateSettingsRaw` merges into
the stored row and dropping the map entry would leave the previous branding in
place.

This ports the white-label half of `luqman-v1/9router-go`#431
(`feat/dashboard-i18n-whitelabel`), which bundles three unrelated pieces. The
i18n layer, the `/proc`-based system-metrics endpoint and the default
landing-page move from `/dashboard/endpoint` to `/dashboard` are deliberately
**not** ported: the landing-page move is what broke that PR's own e2e suite
(`web/e2e/harness.ts` waits on the glob `**/dashboard/**`, which the bare
`/dashboard` path does not match, so all 18 e2e tests timed out), and it
truncated the top-bar title to nothing at 320px. The branding settings, the
sidebar render and the Settings card are the parts that stand on their own.

Verified with table-driven unit tests over both normalizers
(`internal/handlers/dashboard/whitelabel_test.go`) and over the upload/serve
handlers (`internal/handlers/dashboard/logo_upload_test.go` — the stored file,
the extension allowlist, the size cap, the single-slot replacement, the
traversal rejection and the missing-logo 404), and end-to-end against a real
gateway binary: a fresh install shows the defaults, a written brand reaches the
sidebar, an insecure or protocol-relative logo is cleared rather than kept,
clearing both fields restores `9router-go` + `/favicon.svg`, and an SVG picked
through the Settings card's own upload button lands in `DATA_DIR/uploads` and is
served back with its media type. The Settings card was exercised in a real
browser, including a save through the page's own Save Changes button.

One defect was found by that browser run and fixed here: the upload client sent
`getAuthHeaders()`, which pins `Content-Type: application/json`. On a `FormData`
body that overrides the boundary `fetch` sets itself, so the server received a
multipart body labelled as JSON and the upload silently failed. The client now
sends only the auth header and lets `fetch` set the content type.
