### fix(proxy): read `delta` when a present `message` is falsy, matching upstream's `||`

`internal/proxy.IsZeroContentResponse` picked between the two OpenAI carrier
fields with a *presence* test: `message` was used unless it was absent or
`null`, and `delta` was read only in those two cases. Upstream
(`decolua/9router` `open-sse/services/combo.js`, `isZeroContentResponse`)
picks with JavaScript's `||`:

```js
const msg = choice.message || choice.delta;
```

`||` tests truthiness, so a present-but-falsy `message` — `""`, `false`, `0` —
also falls through to `delta`. With the presence test, a body that carried its
text in `delta` and a falsy `message` was classified as carrying no usable
content, so the combo skipped a member upstream would have served.

The two verdicts were compared over an 18-body corpus: the upstream function
extracted verbatim and run under `bun`, the port run over the same bodies. Six
differed before this change and none do after:

| body | before | after | upstream |
|---|---|---|---|
| `{"message":"","delta":{"content":"d"}}` | zero | not zero | not zero |
| `{"message":false,"delta":{"content":"d"}}` | zero | not zero | not zero |
| `{"message":0,"delta":{"content":"d"}}` | zero | not zero | not zero |
| `{"message":-0,…}` / `{"message":0.0,…}` | zero | not zero | not zero |
| `{"message":"","delta":{"tool_calls":[…]}}` | zero | not zero | not zero |

The fall-through is decided by JS truthiness rather than by "is not an
object", which is the narrower rule: `{}`, `[]`, a non-empty string and a
non-zero number are all truthy, so upstream reads them as the message and
never reaches `delta`. Treating arrays or strings as absent would have
introduced a divergence in the opposite direction — `{"message":[],"delta":
{"content":"d"}}` is zero content upstream and must stay zero content here.
`internal/proxy/zero_content_test.go` now pins both sides of the line: the
falsy carriers that fall through, and the truthy ones that hide the delta.

The five fall-through cases fail on the pre-fix code and pass after it;
`go test ./...` is green.
