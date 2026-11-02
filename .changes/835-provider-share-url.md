### feat(share): one link that adds a provider on another instance

A compatible provider node is now shareable as a single URL. `GET /add` (and
`/v1/add`, whose `/v1` prefix `RequestLogger` strips before the router runs)
serves the dashboard SPA; the fragment carries the whole payload, so a
credential never reaches the server, an access log or a `Referer`.

**Sharing.** A *Share* button on the provider page opens a modal with three
scopes — one provider, one provider with a block of keys, or several providers
— and a live preview of the exact link. Credential scope defaults to *None*;
carrying a key raises a warning on the preview itself, and a link over 2000
characters is told to use *Export Keys* instead of shipping credentials.

**Parameters.** `prov`, `url`, `prefix`, `type`, `apiType`, `name` and `note`
describe the endpoint; `api=` carries one key and `keys=` a bulk block
(`name|key[|pool=…][|prio=…]`, `;`-separated, mutually exclusive with `api=`);
`models=` takes `auto`, `ask`, `none`, `free`, `caps:<cap>`, `ctx>=<n>`,
`limit=<n>` or an explicit id list, with the filters applied over the catalogue
the node reports; `set=` carries up to eight providers as base64url JSON.
`api`+`keys` and `prov`+`set` are parse errors rather than a silent precedence,
and a `keys=` value containing `;` or `|` is refused with the reason instead of
being truncated.

**Receiving.** The link opens a confirmation modal that creates-or-adopts the
node, stores the key with `authType: "compatible"` (the probe path a node needs),
and imports the catalogue through the existing batch endpoint. A link whose
base URL differs from a node already on the instance asks *keep stored* / *use
link* rather than rewriting it, a taken suffix reports the resolver's 409, and a
bulk block names its connections through `planBulkAdd` so a shared "Key 1"
becomes "Key 2" instead of replacing a stored row. The fragment is stashed in
`sessionStorage` across the `/login` redirect so a receiver without a session
still lands on the modal.

Verified on a booted router: `GET /add` returns the SPA document on both
spellings, the full add flow (node → connection → `/models` → chat request)
reaches a fake upstream, and an embedded key appears nowhere in the gateway log.
Unit: 31 cases over the codec (round-trips for every accepted form, refusals for
the malformed ones, the model filters) plus 8 table cases pinning the node URL
gate. Client weight unchanged at 4.5 MB / 273 KB brotli.

`POST /api/provider-nodes` now applies the same URL gate as
`POST /api/provider-nodes/validate` — `isValidHTTPURL`, `IsCloudMetadataURL` and
`AssertPublicURL`, run after the default base URL is filled in so a body that
omits `baseUrl` still means "the provider's own endpoint". A share link's `url=`
is the one path where an untrusted string reaches the node table: before this a
node pointing at `169.254.169.254` or an RFC1918 address was stored with a 201
and only failed later, on the first request, with a dial error naming an address
the operator never typed. The socket-level guard in `proxy.NewSafeTransport`
still backs the gate up, so the refusal is now both early and clear.
