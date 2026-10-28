### fix(rtk): match protected tool names case-insensitively and see OpenAI tool errors

Two holes in the RTK skip list, both of which let the gateway rewrite text an
agent needed.

**The protected-tool list was an exact-case map.** It protected only the
clients that happened to spell the tool the way the list did. The repo's own
e2e fixture uses the lowercase spelling, and upstream's RTK filters key on
the lowercase `grep`, so `read` and `grep` were unprotected.

Measured on the pre-fix tree and on this one, over the same request body — a
tool result of 500 lines delivered under the tool name `read`:

| | bytes of the tool result after compression | lines surviving |
|---|---|---|
| before | 209 | 1 |
| after | 13000 (unchanged) | 500 |

A tool name is not a case-sensitive identifier, so the list is matched
case-insensitively now.

**An OpenAI tool error was invisible.** The Anthropic shape carries the
failure in a field (`is_error`, `isError`, `status`), but the OpenAI Chat
Completions shape has no such field:
`{"role":"tool","tool_call_id":…,"content":…}` carries it in the content
text, and the detector did not look there — so a failed command's trace was
compressed away. The detector now reads the text, using the markers the
repo's own `generic-output` filter already treats as an error output
("Error:", "Exception:", "Traceback (most recent call last):"), anchored at
the start of the content: a result that merely mentions the word mid-text is
not an error, so the rule stays in one place and the cost of a false
positive is only that the result is left uncompressed.
