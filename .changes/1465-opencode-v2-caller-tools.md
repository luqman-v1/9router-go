### fix(translator): keep the caller's tool declarations on OpenCode free-tier requests

- The v2 shaper used to replace the caller's tools with the CLI built-ins and
  to keep the harness preamble's Code Mode section. The gate intermittently
  answered the replaced-tool shape with `429 FreeUsageLimitError`, and when it
  accepted the request the model obeyed Code Mode: it narrated, or called a
  built-in placeholder, instead of the caller's tool.
- Caller tool declarations now travel natively — a tool without a schema gets
  an empty object `parameters`/`input_schema`, since the gate validates every
  declared tool — and one inert `"Unavailable placeholder."` entry per built-in
  is appended after them. Dropping the built-in names entirely still 429s, so
  the names stay while the definitions carry no capability.
- The system prompt is the harness preamble minus its Code Mode section and
  tool-preference bullets, followed by the caller's own text, a note stating
  the built-in declarations are inert and the caller's tools are called
  directly by name, and the model-identity note. The preamble's opening stays
  byte-exact: it is the gate's body fingerprint.
- `tool_choice` handling is unchanged; a forced choice naming a caller tool
  still cannot travel and becomes `required`.

Verified live against the gate: 13/13 requests (chat, Claude Messages and
Responses lanes) answered 200 with the caller's tool name in the response,
where the replaced-tool shape answered `429 FreeUsageLimitError` and the
built-ins-only shape called a built-in instead.
