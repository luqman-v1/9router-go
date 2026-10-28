### fix(proxy): decide the message/delta truthiness on the raw text, not on a decoded value

`internal/proxy.IsZeroContentResponse` picks between the two OpenAI carrier
fields the way upstream `decolua/9router` does
(`open-sse/services/combo.js`, `isZeroContentResponse`):

```js
const msg = choice.message || choice.delta;
```

JavaScript's `||` tests **truthiness**, not presence, so a present-but-falsy
`message` — `""`, `false`, `0` — also falls through to `delta`. #446 made the
port reproduce that rule, but it did so by decoding the whole `message` value
into `any` and inspecting the decoded value. That has two consequences.

**It is expensive.** The decode allocates the entire message on every probe —
a long `content` string included — to answer a question the value's first byte
already answers. Measured on a body with a 40000-byte content string, against
the tree before #446 and against it after:

| | allocs/op | B/op | ns/op |
|---|---|---|---|
| before #446 | 11 | 164182 | 156262 |
| after #446 | 22 | 205611 | 295718 |
| this change | 11 | 164187 | 167408 |

So #446 doubled the allocations of this function and made it ~90% slower on a
large body, on a path every 2xx response through the combo fallback walks
(`internal/proxy/executor/openai.go`,
`internal/proxy/executor/responses_bridge.go`,
`internal/handlers/chat/forward.go`).

**It is wrong on values Go cannot decode.** A number outside float64 range
(`1e400`, directly or nested inside an object) is a value `encoding/json/v2`
refuses to decode, and a failed decode was read as "the value is absent" — so
the port fell through to `delta` where upstream reads the value as truthy and
never looks at `delta`. Upstream's `JSON.parse("1e400")` is `Infinity`, which
is truthy; `JSON.parse("1e-400")` is `0`, which is falsy.

`probeMessageFallsThroughToDelta` now classifies the raw text by its first
byte: `null` and `false` fall through, `true` does not, an empty string
literal falls through while `"0"` and `"false"` do not, an object or an array
is always truthy (even an empty one — upstream reads it as the message), and
a number is falsy only when it is zero, `-0` included, or underflows to zero.

The upstream function was extracted verbatim from `combo.js` and run under
`bun` over a 32-body corpus, and the port's `IsZeroContentResponse` was run
over the same bodies:

| | divergences from upstream |
|---|---|
| before #446 | 8 |
| after #446 | 6 |
| this change | 0 |

The six the merged tree still had were all the out-of-range-number cases
above; the two it fixed here are the ones #446 got wrong.

The new cases in `internal/proxy/zero_content_test.go` are fix tests: the four
that pin the out-of-range behaviour fail on the merged tree and pass after
this change.
