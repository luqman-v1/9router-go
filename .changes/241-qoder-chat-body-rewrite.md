fix(qoder): rewrite the chat body into Qoder's own payload before signing — Closes #121

Every Qoder chat request failed with HTTP 400 on every registered model,
on a freshly authorized account:

```json
{"error":{"message":"{\"code\":\"400\",\"message\":\"[FAIL]node:agent_router
msg:None flow nodes found for router agent_router\"}"}}
```

`ForwardQoder` COSY-signed and forwarded the client's OpenAI body verbatim.
Qoder's `agent_chat_generation` does not accept an OpenAI-shaped request: it
routes to a node called `agent_router`, that node finds no flow for the
request, and answers 400. The COSY signature, the endpoint and the model id
were all correct, which is why the failure read as an account problem rather
than a gateway one. Upstream's Node build answers the same request 200,
because it rewrites the body first.

**What changed**

- `qoder_request.go` (new) — maps the OpenAI chat body onto Qoder's payload:
  `chat_task`, `session_type`, `agent_id`, `task_id`, `chat_context`,
  `parameters`, `business`, and the derived `session_id` / `chat_record_id` /
  `request_set_id` / `request_id`. System turns are hoisted out of `messages`,
  multipart content is flattened except image blocks, and documents become
  short stubs instead of inlined bytes.
- `qoder_encoding.go` (new) — Qoder's WAF-bypass body encoding (base64 →
  third-rotation → character substitution). The encoded bytes are what the
  COSY signature covers; signing the plaintext is a signature error.
- `qoder_catalog.go` (new) — `model_config` comes from the live
  COSY-signed model list, cached per credential for an hour and coalesced
  through `singleflight`. A model the catalogue does not publish now fails
  with a message naming it instead of reaching Qoder as an unroutable payload.
- `qoder_credentials.go` (new) — a Personal Access Token is exchanged for a
  short-lived job token before chat, since a PAT cannot sign COSY requests.
- `qoder_context_tier.go` (new) — the 200K/400K/1M context-window escalation
  the IDE performs, so a long session is not rejected by the default tier.
- `qoder_endpoints.go` (new) — the regional endpoint table, now shared with
  the dashboard. `qoder` and `qoder-cn` keep separate hosts and are never
  routed to one another.
- The chat request now carries `&Encode=1`, plus `Accept: text/event-stream`,
  `Cache-Control: no-cache`, `X-Model-Key`, `X-Model-Source` and
  `Accept-Encoding: identity` — gzip makes Qoder's CDN re-validate the
  signature, so identity is required.
- A connection missing `userId` or a token now answers a clean 401 naming the
  fix, rather than an opaque failure from inside the signer.

**Scope beyond the literal report**

Upstream's attachments rewrite (uploading inlined images to
`/api/v2/image/upload`) is not ported; OpenAI `image_url` and Claude `image`
blocks are converted to Qoder's image block form and sent as references. The
dashboard's model-list fetch now shares the executor's endpoint table and
credential exchange rather than keeping a second copy that could drift.

**Verification**

`go test -race ./internal/proxy/executor/ ./internal/handlers/dashboard/` —
707 passed. `qoder_body_test.go` captures the outbound request at a fake
upstream, decodes the Qoder encoding, and pins the payload keys, the derived
identities (stable across turns, fresh per request), `Encode=1`, the header
set, and that `Cosy-Bodyhash` covers the encoded bytes actually sent.
Before this change the same probe showed the client's OpenAI body on the wire
with no `Encode=1`, no `X-Model-Key`, and `Accept-Encoding: gzip`.