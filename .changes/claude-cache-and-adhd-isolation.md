### fix(tokensaver): preserve Claude prompt cache breakpoints and isolate ADHD mode

**Claude prompt caching.** When appending system prompts to Claude Messages
payloads with block-style `system` arrays, `InjectSystemPromptClaude` now
inserts the prompt before the final `cache_control` block (matching upstream
`open-sse/rtk/systemInject.js:220-230`). Appending to the end of the array
placed the prompt after the cache breakpoint, causing cache misses across turns
and fragmented attention context in Claude Code.

**Preset hygiene.** ADHD mode is removed from the recommended preset in
`TokenSaverView.svelte`. ADHD instructions impose strict brevity constraints
("suppress tangents", "cap lists at 5 items", "end with one 2-minute step") that
contradict deep reasoning and should only be explicitly opted into.
