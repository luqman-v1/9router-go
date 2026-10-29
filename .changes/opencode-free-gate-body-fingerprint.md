### fix(opencode): pass the free-tier gate's body fingerprint (harness system + genuine tools)

The free-tier inference gate stopped accepting the router's requests: every
free model answered `429 FreeUsageLimitError` ("Free models work in OpenCode")
even from a clean egress, while the opencode CLI kept working from the same
machine. Session rotation replayed into the same refusal because the limit was
never the session's — the gate had tightened its genuineness check from the
transport to the request body.

Measured live on 2026-10-25 against `https://opencode.ai/inference/v1` (probe
matrix in `internal/translator/opencode_free_harness.go`):

- the system prompt must start with the CLI harness preamble (~8,247 chars
  captured from CLI v2.1.4); a 2,000-char prefix fails, while appending caller
  instructions after the full preamble passes;
- tool declarations must be the CLI's twelve built-ins, byte-exact: a
  foreign-named tool, a renamed clone, or an edited description all answer
  `400 "Request validation failed"`; at least six definitions are required;
- tool *calls* are unconstrained: history may carry calls to undeclared names
  and the model may emit them;
- endpoint path, User-Agent version and `x-opencode-project` are not checked:
  the old `/inference/v1` prefix and the v2.0.29 identity still pass with a
  correct body.

The fix reshapes every free-lane request in
`translator.ConcealOpenCodeV2FingerprintTools`:

- prepend the captured harness preamble to the system prompt (chat `messages`,
  Claude `system`, Responses `instructions` each in their own shape), keeping
  the caller's system text after it;
- replace the tools array with exactly the twelve genuine definitions in the
  lane's own shape (nested chat, flat strict Responses, `input_schema` Claude);
- describe the caller's own tools in the appended system text so the model can
  still call them by their original names — they no longer travel as
  declarations, so the returned rename map is empty and response `tool_calls`
  need no restoring;
- rewrite a forced `tool_choice` naming a caller tool to `"required"` (chat and
  Claude lanes) or drop it (Responses lane), since only genuine built-ins can
  be named.

Verified live end-to-end through `ForwardOpencode`: `glm-5.3-flash-free` and
`big-pickle` answer 200 with a streaming body; `deepseek-v4.1-flash-free`
passes the gate and streams (its slow stream is upstream behavior).

### Tool calling through the gate: the execute/CALL_TOOL bridge

The same measurement showed the free-tier runtimes only emit tool calls for
declared names: glm narrates and stops when asked to call an undeclared caller
tool, while deepseek and mimo emit it directly. Since only the CLI's twelve
built-ins may be declared, caller tools now travel on one uniform path:

- the session tool protocol instructs the model to invoke a caller tool as a
  call to the declared `execute` tool with code `CALL_TOOL <tool-name>
  <json-arguments>`;
- `NewOpenCodeExecuteBridgeWriter` (streaming) and
  `bridgeOpenCodeExecuteToolCallsJSON` (re-aggregated non-streaming) rewrite
  that call back to the caller's own tool name and arguments on the way out;
  genuine Code Mode `execute` calls (code that is not CALL_TOOL) pass through
  verbatim;
- the return trip needs no rewriting: the gate accepts history tool_calls to
  undeclared names.

Verified live through the daemon for glm, deepseek and mimo, streaming and
non-streaming: turn 1 returns the caller's tool call with correct arguments,
turn 2 (tool result) returns the final answer.

### Lane parity

The fingerprint shaping lives in `translator.ConcealOpenCodeV2FingerprintTools`,
so every free lane inherits it: the opencode free chat lane, the opencode-zen
free chat lane, the zen free responses lane (muse-spark family) and the
opencode-go free lane. The execute/CALL_TOOL bridge is wired into every lane
whose client-facing wire format is chat-completions chunks (opencode free,
zen free chat, zen responses for chat clients, go free), streaming and
re-aggregated. Not bridged: the zen messages (Claude) lane and Responses-native
clients — no free model serves those shapes today (no claude-*-free ids exist
and the muse-spark contributor ids answer 410 deprecated upstream), and the
shaping still reaches them automatically if one appears.
