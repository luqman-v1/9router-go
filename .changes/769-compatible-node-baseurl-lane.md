### fix(providers): a custom node's baseUrl is a prefix, so its lane is appended again

A custom provider node whose base URL carries a path beyond a version prefix
received the request at the base URL itself. Pointing an `openai-compatible`
node at `https://developer.amd.com.cn/radeon/api/v1` sent the POST to that exact
path, the upstream answered 404, and the operator saw nothing more than
`HTTP 404: 404 Not Found` (#768).

The completion helper used here leaves a multi-segment path alone, which is
correct for the Ollama Local host field — an endpoint the operator typed — and
wrong for a node, whose `baseUrl` is a prefix by contract. Upstream appends the
lane unconditionally (`open-sse/executors/base.js` `buildUrl`:
`` `${normalized}${path}` ``, with the path chosen by
`resolveOpenAICompatibleApiType`), so the same configuration answers 401 there
instead of 404. Verified against the live host: the bare path answers 307/404
while `/chat/completions` answers 401, i.e. the route exists and only the key is
missing.

`compatibleNodeLaneURL` now completes a node's base URL by that contract, and
the shared helper keeps its shape rule for the registry and Ollama Local
overrides. The lane follows the node shape rather than a guess: `/messages` for
`anthropic-compatible-*`, and `/responses` or `/chat/completions` for
`openai-compatible-*` by its stored `apiType` — falling back to the node id for
`responses`, as upstream's `resolveOpenAICompatibleApiType` does, because a
connection row written before the dashboard began syncing `apiType` carries
none. A `custom-embedding-*` node keeps `/chat/completions`, because the
embeddings handler derives its own URL from the resolved config and would
otherwise append `/embeddings` twice.

A base URL that already names a route is used as-is. The route table is the
parent helper's full list rather than only the lanes this function appends:
`/chat/completions`, `/messages`, `/responses`, `/completions`, `/chat`,
`/embeddings`, `/api/chat` and `/api/generate`. Those last four matter because a
node that stored `/v1/chat`, `/v1/completions` or an Ollama-shaped `/api/chat`
was dialled exactly as configured before this rule existed — appending a lane
behind one would have turned a working node into a doubled path. A bare host or
a `/v1` root resolves exactly as it did before, so a node that already worked
keeps the URL it had.

Both branches that build a node config go through it — the connection's own
`providerSpecificData.baseUrl` and the node row's `baseUrl` fallback — because
each carried its own copy of the completion.

Tests: `internal/handlers/chat/compatible_node_lane_test.go` covers the URL
mapping across all three node shapes and both `apiType` lanes, the resolution
through `getProviderConfig` on both branches, and a request through the real
handler whose fake upstream asserts the path it was dialled on. The
end-to-end case fails on the pre-fix tree with
`upstream saw path "/radeon/api/v1", want "/radeon/api/v1/chat/completions"`
and passes after. Five further cases pin the shapes this change must NOT move —
a stored `/completions`, `/chat`, `/api/chat` and `/api/generate`, plus a
`responses` node with no stored `apiType` — and each fails with a doubled or
wrong path when the rule is reverted. `go test ./internal/handlers/chat/
./internal/handlers/media/ ./internal/proxy/... ./internal/handlers/dashboard/`
is green.
