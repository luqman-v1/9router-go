### fix(translator): stop renaming provider identifiers across the antigravity conversation

The `antigravity` lane rewrote the literal `opencode` provider identifier to
`antigravity` in every request it translated. The rename was not limited to the
system instruction: it ran over the whole conversation, so user messages, tool
calls, tool results and file paths arrived at the upstream with the caller's
own identifiers renamed. A request carrying a
`C:\Users\<user>\.config\opencode\AGENTS.md` path reached the upstream with that
path rewritten, and two provider namespaces in one request collapsed into the
same name, which the strict provider isolation in `AGENTS.md` §3 forbids.

`internal/translator/antigravity.go` no longer performs the rename: the
`opencodeRegex` declaration and the `ReplaceAllStringFunc` block inside
`rewriteBrandingText` are gone, so provider identifiers, config paths and code
identifiers reach the upstream untouched.

Upstream `decolua/9router` keeps the same mapping, scoped to the system
instruction and driven by a table (`ANTIGRAVITY_PROMPT_REWRITES` in
`open-sse/config/appConstants.js`), applied in
`open-sse/executors/antigravity.js` with the reason *"competing-client branding
makes the backend flag the request and answer 429 Quota Exhausted"*; its
introducing commit `dff6484` states the scope: *"Applied in the executor so only
Antigravity requests are rewritten — the shared openai-to-gemini translator also
serves gemini, gemini-cli, vertex and zed, which must not be touched."* The Go
port kept the mapping but widened it to the whole conversation with a hardcoded
word-bounded regex. This change removes the row from **both** slots: the
conversation keeps the caller's vocabulary, and the system instruction also
stops carrying the one row upstream still applies there - so at this commit
the port applies the row nowhere, which is *narrower* than upstream's scope,
not equal to it. The system-instruction half was restored separately, in #444
(`antigravityOpenCodeRegex`, applied only inside `rewriteBrandingText`'s
system-instruction branch), so a branded system prompt is rewritten again
while a conversation is still left alone. The scope of the other three rows -
which this port applies to the conversation as well as to the system
instruction, where upstream applies all four to the system instruction only -
is a divergence that is still open, tracked in #439.

The phrase-level filters upstream still applies stay in place: the Claude Agent
SDK phrases, the `x-anthropic-billing-header:` prefix, the Hermes identity
sentence and the harness markers (system instruction only).

Verified with a table-driven unit test over the system instruction, the user
turn and the assistant turn, and an integration test that boots the real router
against a fake upstream and inspects the Cloud Code envelope that goes out.
Both fail on the pre-fix code.
