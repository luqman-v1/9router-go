### deepseek-web parity with upstream (nonce binding, schema validation, citations, done-terminator)

Closes the behavioural gaps between the Go deepseek-web executor and upstream
`decolua/9router` (`open-sse/executors/deepseek-web.js`,
`open-sse/translator/deepseekWebTools.js` / `deepseekWebToolBinding.js`).

**Tool calls**

- Per-request `_nonce` binding: the tool prompt now renders every schema with a
  `_nonce` const property (plus `required`) and requires the binding in the
  reply; the parser rejects a call whose binding mismatches and never forwards
  the `_nonce` argument to the client. The nonce is minted once per request in
  `ForwardDeepSeekWeb` and threaded through prompt + parser, matching
  `serializeDeepSeekToolPrompt` / `getToolNonce` (`_nonce` upstream).
- JSON-Schema argument validation via `github.com/santhosh-tekuri/jsonschema/v5`
  (pure Go, draft 2020-12, mirrors upstream's Ajv2020 with `strict:false`,
  `validateFormats:false`). A bound call violating its declared schema is
  rejected; DSML invokes are always schema-validated, matching
  `finalizeParsedToolCalls`; the JSON envelope path validates calls that carry
  `_nonce` (missing binding tolerated, as upstream).
- Malformed-envelope repair retry once: an answer that carries tool markup or a
  declared-tool envelope that failed to parse triggers one fresh-session
  completion with the contract restated (upstream's
  `hasMalformedDeepSeekToolIntent` → repair flow). A second malformed answer
  fails the turn with 502. `deepSeekToolTurnToClient` now returns the
  accumulator to settle with, so a repaired turn settles against the fresh
  session.

**Search/citations**

- Citation block rewritten to upstream's format: `[N]: [title](url)` lines
  ascending by `cite_index`, blank-line separated (was `\n[N] title (url)`).
  Results without a `cite_index` are filtered.
- `[citation:N]` markers rewritten to `[N]` on search models, removed entirely
  on `search-silent` models; `FINISHED` and a leading `SEARCH|WEB_SEARCH|SEARCHING`
  transport token are stripped from content (`formatStreamContent` /
  `cleanDeepSeekToken`).
- `fold` model ids are treated as search models (`isSearchModel`).

**Stream / prompt**

- 750ms post-FINISHED drain: the upstream sometimes holds the completion body
  open past `FINISHED` (search results still in flight) and clients hung on it.
  The stream scan now bounds the tail like upstream's done-terminator and
  answers the turn instead of waiting for the close.
- Markdown image references (`![alt](url)`) are stripped from the rendered
  prompt — the web API has no image slot and the syntax degrades the model's
  answer.

**Verification**

- `go test ./internal/proxy/executor/` (new `deepseek_web_parity_test.go` pins
  every behaviour above: citation format/silent/filter, marker rewrite, fold,
  token cleaning, image strip, nonce mint/bind/reject/strip, schema rejection,
  DSML binding, malformed detection, repair once/give-up/no-nonce).
- Portability gate: all five `CROSS_TARGETS` build with `CGO_ENABLED=0` after
  the new dependency.
