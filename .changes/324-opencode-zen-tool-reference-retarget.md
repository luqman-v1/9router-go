### opencode-zen: keep forced tool choices and tool history pointing at a real tool

The legacy `/zen/v1` gate fingerprints the lowercase tool quartet, so
`ConcealFingerprintTools` renames the caller's capitalised Claude Code tools
(`Bash`, `Read`, …) before the request leaves the gateway. The response side
already restored the caller's spelling, but the request could still reference a
name upstream no longer had:

- A forced Chat Completions `tool_choice` nests its name under `function`, and
  only the flat `tool_choice.name` shape was retargeted, so a
  `{"type":"function","function":{"name":"Bash"}}` choice kept naming `Bash`
  after the tool had been renamed to `bash`. The flat branch also assigned the
  raw `FingerprintToolKey` result, which retargets a case-variant only when a
  same-named rename exists — the old guard was right, and it is preserved.
- Prior-turn tool calls (`messages[].tool_calls[].function.name`, Claude
  `tool_use` blocks, Responses `function_call` / `custom_tool_call` items) were
  never rewritten, so a conversation that had already called `Read` kept
  replaying `Read` against a tools array declaring only `read`.

One pass now retargets `tool_choice` and every history reference across all
three wire shapes. A forced choice the body cannot resolve — its declaration was
dropped as a duplicate of an exact canonical one — is removed rather than
forwarded dangling, and the rename map records that spelling as unresolvable
(`""`) so the response side keeps the canonical name instead of blanking it.

Deliberately unchanged, because it already matched the gate: a client tool that
is already exactly `read` keeps its own description and schema (the gate only
checks names on this lane), and a body that already carries the canonical
quartet is returned byte-identical. The `/inference` lane and its
`ConcealOpenCodeV2FingerprintTools` path are untouched.

Verified with `go test -count=1 ./internal/translator/ ./internal/proxy/...
./internal/handlers/chat/`, including new cases for the nested Chat choice,
tool-history retargeting in all three shapes, an exact `read` definition
surviving untouched, and the dropped-duplicate marker.
