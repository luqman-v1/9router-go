### feat(media): uc-web provider (uncensored.com persona image + video)

upstream decolua/9router 38a0c449 parity, uc-web scope (part of #624). Image
and video generation over the un-metered internal API, where the credential is
the full uncensored.com Cookie header: every request mints a 60s Clerk
`__session` JWT from the durable `__client` cookie (legacy `sid` claim first,
else the `/v1/client` session listing with the active session winning), and
Clerk's rotated `__client` is persisted back onto the connection so the
rotation chain never drifts from the stored cookie.

The image lane (`/v2/image-gen`) carries the full upstream model catalog
(seedream / nano-banana / gpt-image / wan / flux tiers) with aspect resolution
from the `size` parameter (direct ratio, closest WxH match, first-allowed
fallback) and resolution pinning for tiered models. The video lane
(`/image_to_video`) posts a null blob for text-to-video and, for
image-to-video, uploads the decoded input through the signed-URL endpoint
using the account uid from the minted JWT before generating with the blob
name; the pre-determined result URL is HEAD-polled to 200 (honouring the
response's `timeout_seconds`) and then downloaded. The registry gains the
entry, the `uc` alias, and the image/video kinds so `/v1/models` keeps them
out of the chat list; the dashboard validation probe mints a session without
spending a generation.

Verified: 8 new tests against fake Clerk + persona endpoints covering the JWT
decode, the cookie jar and rotation swap, session minting (both paths plus the
401 guidance), the full image flow including aspect resolution from a WxH
size, resolution pinning, unknown-model 400s, both video modes including the
signed-URL upload, the aspect table, and the validation probe. `go test
./...` green apart from the pre-existing environmental browserlogin failure,
the integration suite green, `golangci-lint --new-from-rev` clean. The
remaining #624 sub-items (chatgpt-web, muse-web, veoaifree-web, grok-web
executor, perplexity-web path, generic web-login + browser pool) land as
their own PRs.
