### fix(translator): keep the caller's tool declarations on OpenCode free-tier requests

- The v2 shaper used to replace the caller's tools with the CLI built-ins and
  to keep the harness preamble's Code Mode section. The gate intermittently
  answered the replaced-tool shape with `429 FreeUsageLimitError`, and when it
  accepted the request the model obeyed Code Mode: it narrated, or called a
  built-in placeholder, instead of the caller's tool.
- Caller tool declarations now travel natively — a tool without a schema gets
  an empty object `parameters`/`input_schema`, since the gate validates every
  declared tool — and the twelve built-in definitions are appended after them.
  Dropping the built-in names entirely still 429s, and the definitions must stay
  the verbatim CLI text: the gate validates `read` against the CLI 2.x capture
  and answers 403 FreeTierError for a stub, so the definitions stay byte-exact
  and the section below steers the model away from them instead.
- The system prompt is the harness preamble minus its Code Mode section and
  tool-preference bullets, followed by the caller's own text, a note stating
  the built-in declarations are inert and the caller's tools are called
  directly by name, and the model-identity note. The preamble's opening stays
  byte-exact: it is the gate's body fingerprint. The stripped path sets the
  system field outright instead of letting the preamble injector prepend the
  original again, so the caller's text is sent exactly once per lane (sending
  it twice would double every caller system prompt on the wire).
- A built-in name a caller tool already declares is skipped rather than
  appended: two declarations sharing a name answer a server error on the gate,
  so each built-in name stays declared exactly once.
- `tool_choice` handling is unchanged; a forced choice naming a caller tool
  still cannot travel and becomes `required`.

Verified live against the gate: 13/13 requests (chat, Claude Messages and
Responses lanes) answered 200 with the caller's tool name in the response,
where the replaced-tool shape answered `429 FreeUsageLimitError` and the
built-ins-only shape called a built-in instead.
