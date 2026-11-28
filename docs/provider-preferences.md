# Request-level provider preferences

A client can steer which providers serve one request, without the operator
touching a combo. Pass a `provider` object in the request body on the chat,
messages and responses lanes:

```json
{
  "model": "my-combo",
  "messages": [{"role": "user", "content": "hello"}],
  "provider": {
    "order": ["groq", "deepseek"],
    "ignore": ["antigravity"],
    "allow_fallbacks": false
  }
}
```

| Field | Effect |
|---|---|
| `order` | Try these providers first, in this sequence. Members outside the list keep their relative order behind the listed ones. |
| `only` | Restrict candidates to these providers. Nothing else may serve the request. |
| `ignore` | Exclude these providers. |
| `allow_fallbacks` | Default `true`. Set `false` to turn `order` into a hard allowlist — no provider outside it may serve the request. |

## Semantics

- **Combo requests** filter and reorder the combo's member list. A removed
  member is never attempted: an `ignore`d upstream receives no request at
  all, not a failed attempt followed by a fallback.
- **Single-model requests** are rejected with `400` when the one provider
  they resolve to is excluded. A client that asked to avoid a provider
  must never be served by it silently.
- **Empty survivor sets are an error**, not a silent fall-through to the
  unfiltered list.
- The object is a 9router-go routing hint and is stripped from the body
  before it is forwarded upstream, so a strict provider never sees the
  field. It never reaches translation, caches or usage accounting either.
- An absent object costs nothing: the field is only parsed when the body
  actually carries one.

## What `only` / `ignore` / `order` match

Provider ids as the gateway spells them: `deepseek`, `groq`, `openai`,
`anthropic`, and the alias spellings (`oa` → `openai`) — a match is against
the canonical id either way. A combo member on a custom provider node
(`myprefix/model`) matches by that node's id and by its prefix.

## Not in v1

`sort` (price / throughput / latency), `zdr`, `max_price` and
`data_collection` are deliberately out: the combo strategies and the
breaker/health stacks already own load selection here, and those fields
would duplicate them without adding a decision the operator cannot already
configure. The parse is fail-open — an object carrying only those fields
changes nothing today — so adding them later is additive.

## Provenance

Contract follows OpenRouter's provider preferences object
(`provider.order` / `only` / `ignore` / `allow_fallbacks`), the shape
clients of multi-provider routers already speak. Implementation is native
Go: no upstream parity claim is made for it.