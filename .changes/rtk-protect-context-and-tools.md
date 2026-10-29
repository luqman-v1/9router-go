### fix(tokensaver): stop compressing assistant/user text blocks and protected tools in RTK

**Context preservation.** RTK compression is meant for command outputs inside
`tool_result` blocks. The Go implementation was additionally compressing
plain `type: "text"` blocks inside Claude format message arrays, causing
assistant responses and user prompts in conversation history to undergo line
deduplication and truncation. This corrupted multi-turn context and caused
agents to "forget" previous reasoning. Plain text blocks are now left untouched,
matching the Node upstream reference.

**Protected tools.** The protected tools check (`Read`, `Grep`, `Edit`, `Write`,
`patch`, `anchor`, `glob`, `view`, `list_dir`) now completely skips compression,
matching Node's `isProtectedTool() -> continue` behavior. File reads of 250+
lines are no longer sliced in the middle by `readNumbered`, ensuring agents
receive complete source files.
