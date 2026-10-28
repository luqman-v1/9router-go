### fix(translator): rewrite competing-client branding in the antigravity system instruction

Upstream `decolua/9router` sanitizes competing-client branding in the system
instruction before a request leaves the gateway. It drives this from a rule
table (`ANTIGRAVITY_PROMPT_REWRITES` in
`open-sse/config/appConstants.js:187-195`) with four rows, and its executor
applies all of them to `requestWithoutTools.systemInstruction.parts` only
(`open-sse/executors/antigravity.js:302-309`), with the reason:

> Rewrite competing-client branding in system prompts (e.g. Zed's Claude
> prompt, OpenCode naming) so Antigravity doesn't flag the request with a 429
> Quota Exhausted.

Two of those rows were wrong in the port.

**The Hermes identity row.** The port rewrote exactly one phrasing — the
legacy sentence `You are Hermes Agent, an intelligent AI assistant created by
Nous Research.` — and left every other variant in place, so a caller whose
system prompt identified itself as Hermes in any other wording reached the
Antigravity backend with the fingerprint intact. The replacement made it
worse: it rewrote the matched sentence to `You are Hermes Agent. You are an
intelligent AI assistant.`, which still carries the token the rule exists to
remove. The pattern now matches the whole family of variants and the match is
replaced wholesale with a neutral identity:

```
You are Hermes Agent, an intelligent AI assistant.          -> You are an AI assistant.
You are Hermes.                                             -> You are an AI assistant.
You are Hermes Agent, an intelligent AI assistant, created by Nous Research.
                                                            -> You are an AI assistant.
You are Hermes Agent, built by Nous Research.               -> You are an AI assistant.
You are Hermes, an AI assistant.                            -> You are an AI assistant.
You are Hermes, an AI agent.                                -> You are an AI assistant.
```

Upstream reached this in commit `a61fc6a0` (*"fix(antigravity): rewrite all
Hermes identity variants, not just the legacy sentence"*), whose message
states the reason: the old rule matched only that one sentence, the current
Hermes versions use `You are Hermes Agent, built by Nous Research.` and
similar, and those *slipped through and got a fake 429 RESOURCE_EXHAUSTED*.
The Go port carried upstream's pre-`a61fc6a0` rule.

**The `opencode` row.** The port applied the first three rows to the system
instruction *and* to every text part of the conversation. #435 removed the
fourth row — the `opencode` → `antigravity` rename — from both slots, so the
port now applies it nowhere, including the system instruction where upstream
still applies it. Measured on the Cloud Code envelope that leaves the gateway:

```
in : "You are Hermes, an AI agent. Working dir C:/Users/admin/.config/opencode/AGENTS.md"
out: unchanged — literal 'opencode' still present
```

Upstream's table over the same input rewrites the branding:

```
"Running opencode, opencode-zen, opencode-go at C:/Users/adima/.config/opencode/AGENTS.md"
  -> "Running antigravity, antigravity-zen, antigravity-go at C:/Users/adima/.config/antigravity/AGENTS.md"
"You are OpenCode, a coding agent."  -> "You are Antigravity, a coding agent."
"YOU ARE OPENCODE."                   -> "YOU ARE ANTIGRAVITY."
"Opencode is fine."                  -> "antigravity is fine."
```

This restores that row at upstream's scope: the system instruction only. The
conversation is untouched, so the caller's own identifiers, config paths and
code identifiers reach the upstream unchanged — the property #435 established
and the one its own fragment describes as *"the conversation keeps the
caller's vocabulary"*. Upstream's replacement mapping is reproduced exactly,
including the three capitalisations it names (`OpenCode` → `Antigravity`,
`OPENCODE` → `ANTIGRAVITY`, everything else → `antigravity`) and its
unbounded, case-insensitive match, so `opencode-zen` and `.config/opencode/`
are rewritten as they are upstream.

The integration test that covers this lane parsed the prompt fields from the
top level of the Cloud Code body, but the payload is nested under `request`.
Both part lists therefore came back empty and every assertion below the parse
— the ones that would have caught these regressions — was silently skipped,
so the test passed against the unfixed code. The level is now asserted first
and the parsed parts are required to be non-empty, so an empty parse fails
loudly.

Verified with table-driven unit tests over the system instruction and the
conversation, and with the integration test, which boots the real router
against a fake upstream and inspects the envelope that goes out. All fail on
the pre-fix code.

This is upstream commit `a61fc6a0` (*"rewrite all Hermes identity variants"*)
and `dff6484` (*"fix(antigravity): sanitize competing-client branding in
system prompts"*) parity. The scope question for the other three rows —
whether the whole table should apply to the system instruction only — is
tracked in #439.
