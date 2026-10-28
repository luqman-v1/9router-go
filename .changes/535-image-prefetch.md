### fix(translator): inline remote images for the providers that cannot fetch a URL themselves

Several downstream formats accept only inline image bytes: the Claude Messages
native wire, the Responses wire (`/v1/responses`), kiro, and the local
open-source models. A request that names an image by `https://` URL reached
those upstreams as a dead string — the model was asked about a picture it was
never given, and the answer was a hallucination rather than an error.

`internal/translator/image_prefetch.go` now downloads a remote image and
rewrites it into an inline `data:` URL before the request leaves the gateway.
It covers the three wire shapes the gateway serves — the OpenAI
`image_url.url`, the Responses `input_image.image_url`, and the Claude
`source.type == "url"` block — and it runs before translation, while the body
is still in the client's own format and the target provider is known.

The rewrite is **fail-open** at every step: a bad scheme, a private address, a
non-image MIME, an oversized body or a refused redirect all leave the body
exactly as it was. A request that cannot be inlined is still a request, and the
provider's own error message is a better answer than the gateway's.

The egress is guarded, because fetching a client-supplied URL is a server-side
request forgery surface:

- **https only**, except an explicitly granted loopback host.
- **Private and reserved addresses are refused** — at the literal-IP check and
  again at dial time, so a DNS rebind that answers with `127.0.0.1` is caught
  after resolution. The blocked set includes the ranges Go's own `net.IP`
  predicates do not cover: `100.64.0.0/10` (RFC6598 carrier-grade NAT, where
  Alibaba Cloud's metadata endpoint `100.100.100.200` lives), `192.0.0.0/24`
  (Oracle's `192.0.0.192`), `198.18.0.0/15`, and the documentation and
  local-use NAT64 prefixes. `localhost` and `metadata.google.internal` are
  refused by name.
- **Redirects are re-validated** at every hop and capped at three, so a public
  URL that redirects to a metadata address is refused rather than followed.
- **A byte cap and an image count cap** bound one request, and the count is
  charged only for an image that was actually inlined — four refused URLs must
  not starve the legitimate image behind them.
- **The MIME allowlist is checked twice**: the declared `Content-Type` must be
  an `image/*` type on the list (SVG is deliberately absent — it can carry
  script), and the *magic bytes* decide the label that ends up in the data URL.
  A GIF served as `image/png` is labelled `image/gif`, because handing the
  provider a label that contradicts the bytes is a lie it will decode wrongly.

Both `image_url` shapes OpenAI accepts are rewritten: the object form
(`{"url": "…"}`) and the bare string. The string form is the one most clients
emit, and skipping it left exactly the requests that would have used it holding
a dead URL.

Only the targets that need it pay the download. `internal/handlers/chat` knows
which providers cannot fetch a URL themselves — gemini, gemini-cli, vertex,
antigravity, ollama, kiro, commandcode, plus any provider configured
`gemini-native` — and the rewrite runs only for those. An OpenAI-compatible
target keeps its remote URLs and pays nothing. A provider whose *model* rides a
different lane is covered by the model's declared `TargetFormat`, so
opencode-go's `gemini-4-argon-contributor` — translated by
`TranslateOpenAIToGemini`, an id absent from the provider table — gets the same
treatment. Upstream (`open-sse/translator/concerns/prefetch.js`) keys that set
by wire format; this table is still keyed by provider id because
`ProviderConfig.Format` here carries only the Gemini variants and `"claude"`,
with no `kiro` or `commandcode` value to compare against, so the id is where
those providers' wire format is actually recorded. That divergence is deliberate
and is stated in the code.

The rewrite preserves everything it does not touch. A plain
`map[string]any` round-trip reads every number as `float64`, which turned
`"seed": 9007199254740993` into `…992`; the top level is decoded as raw JSON
values instead, so a sibling field the rewrite never touches survives byte for
byte.

The private-address predicate is `internal/netguard`, the shared package the
proxy's own SSRF guard already uses, rather than the local copy this change
first carried. The two copies had drifted once and the disagreement left
`100.100.100.200` reachable through this path; one predicate, one place to fix
it.

`netguard.ParseIP` joins it for the same reason. `net.ParseIP` answers nil for a
zoned literal (`::1%lo`, `fd00:ec2::254%eth0`), and every guard in this
repository reads nil as "not an address — allow it". `https://[::1%25lo]:PORT/`
was therefore fetched and inlined, through the pre-flight check and through the
dial-time hook alike. A zone names the interface a packet leaves by; it is not a
different destination. The proxy's own dialer had the same hole and now calls
the same helper.

### Memory

Measured on the 1 vCPU box, 4 images of 15 MB each inlined into one request
(`runtime.MemStats`, prefetch in isolation): output 80 MB, `TotalAlloc` 819.8 MB,
`HeapAlloc` 191.3 MB, `Sys` 575.8 MB. That is the price of the feature at the
`MaxImages` default with the byte cap saturated — six times the payload in
transient allocation, all of it released. A target that never reaches
`wantsInlineImages` allocates and downloads nothing, and the 15 MB raw / 20 MB
base64 cap bounds one image regardless of what the caller asks for.

### Known limitation

Nested numbers inside an image-bearing message are still read as `float64` by
the per-item decode, so a large integer that deep loses precision — the same
thing the unmodified request path does (`handleSingleModel` unmarshals the whole
body into `map[string]any` before this runs). This change is not a regression
against `main`; top-level sibling fields are the ones it preserves byte for
byte.
