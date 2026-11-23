### fix: accept Mistral array content deltas and pin Large 4 to a 1M context window

Mistral's native stream encodes a delta's `content` as an array of typed parts
rather than the plain string the Go translator assumed. Decoding such a chunk
failed outright, so the whole response was dropped; reasoning parts had no
mapping onto Anthropic's `thinking_delta` at all. Upstream fixed both in
`decolua/9router#213558f15` (commit `213558f15`).

- **`OpenAIDelta.UnmarshalJSON`** now accepts `content` as either a string or
  an array. Only parts with `type: "text"` are joined into the existing visible
  `Content` string; every other field (role, reasoning aliases,
  `reasoning_details`, `tool_calls`) keeps its previous decoding path. Plain
  string content is unchanged.
- **Mistral thinking parts** (`type: "thinking"`, whose `thinking` value may be
  a string or an array of text parts) are collected into a wire-only
  `ContentThinking` field and read by `extractReasoningText`. They are emitted
  exactly once as a `thinking_delta` and never as visible text. The DeepSeek
  DSML buffering on the content path is untouched.
- **Capability pin**: `mistral-large-latest`, `mistral-large-4`, and
  `mistral-large-4-0` on the native `mistral` provider resolve to
  `ContextWindow: 1048576` with Vision and Tools enabled, even when the live
  catalog still reports 256000. Older ids such as `mistral-large-2411`,
  reseller-prefixed ids, and `codestral-latest` keep their previous windows.

Regression tests cover the thinking-only, text-array, plain-string, and
thinking→text→tool-call streams, the flattening helpers, field preservation,
and exact context windows against a catalog source reporting 256000. All fail
on `origin/main` and pass on this branch.

Closes #1385
