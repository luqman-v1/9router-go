### fix(deepseek-web): accept `thinking` as object or bool in request envelope

The inbound thinking pass rewrites `thinking` into the anthropic-style object
form (`{"type":"enabled","budget_tokens":10000}` / `{"type":"disabled"}`), but
`deepSeekRequestEnvelope.Thinking` was a plain `bool`, so `encoding/json/v2`
failed the whole envelope with `cannot unmarshal JSON object into Go bool
within "/thinking"` and the turn returned a fallback-upstream 502 before any
upstream call.

`Thinking` is now a `deepSeekThinkingFlag` whose `UnmarshalJSON` accepts both
a plain bool and the object form, treating the object as enabled unless its
`type` is `"disabled"`. No routing logic changes: `resolveDeepSeekModelOptions`
reads the same effective on/off value.

Verified with `go test ./internal/proxy/executor/ -count=1` (green) including
the new table-driven `TestDeepSeekRequestEnvelopeThinkingShapes` covering
absent, bool true/false, and enabled/disabled/adaptive objects.
