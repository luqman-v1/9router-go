### fix(combo): recognize Zed's 403 context-window refusal as a model-fit error

Zed rejects an oversized prompt with a `403` carrying
`Context window exceeded: 103326 tokens exceeds the limit of 100000
tokens`. `IsModelFitError` gated on 400/413/422, so the refusal fell to
the generic 403 rule and the combo locked the connection on the model
for two minutes:

```
WRN fallback upstream failed provider=zed model=claude-haiku-5-5 status=403 error=... Context window exceeded ...
WRN combo locked on retryable error provider=zed model=claude-haiku-5-5 status=403 cooldown_s=120
WRN combo skip unhealthy provider=zed model=claude-haiku-5-5
WRN request POST /chat/completions status=429 ...
```

Every combo request for the next two minutes skipped the member as
unhealthy and the combo answered 429, even though the prompt, not the
account, was at fault.

The fix adds 403 to `ModelFitStatusCodes`, so the model-fit failover
(#338) advances to the next combo member instead of locking a healthy
connection. The body-pattern gate is unchanged: a 403 permission
refusal without a context-limit phrase still lands on the generic 403
rule.

Verified: `go test ./internal/providers/ -run ModelFit` covers Zed's
403 context-window body (fit failure) and a 403 permission refusal (not
a fit failure); `go test ./internal/providers/
./internal/handlers/chat/` passes.

### fix(chat): strip the provider-node prefix from the wire model id

A provider node publishes its models as `<prefix>/<id>`, but the prefix
only routes the request to the node's connection. The generic
openai-compatible lane wrote `modelInfo.Model` verbatim onto the
upstream body, so a node fronting `api.sakoo.biz.id` received
`bansos/gpt-6-luna` and answered `400 no model endpoints`:

```
WRN fallback upstream failed provider=openai-compatible-chat-852b35dc-… model=bansos/gpt-6-luna status=400 error=… no model endpoints …
```

The member was dead weight in every combo it joined — every turn spent
one failing upstream attempt before falling through to the next member.

The fix adds `wireModelForNode`, which removes one exact
`<prefix>/` match for compatible node providers (openai-compatible,
anthropic-compatible, custom-embedding) before the body model is
written, on the single-model chat, messages and responses paths and
both combo loops. Lock keys, usage rows and capability lookups keep the
published id; only the wire changes. Registry providers and unprefixed
ids are untouched.

Verified: `go test ./internal/handlers/chat/ -run TestWireModelForNode`
pins the strip on an exact prefix match and the pass-through for
unprefixed ids, foreign prefixes, a longer prefix sharing the same text,
registry providers, and nodes without a registered prefix.

