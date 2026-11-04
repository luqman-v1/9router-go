### fix(gemini): preserve tools and functionCall/functionResponse for Gemini-native clients on /v1beta/models/*

- In `internal/translator/gemini.go`, parse `tool_choice` from OpenAI requests and map it to Gemini's `toolConfig.mode`: `none` → `NONE` (no tool declarations), `required` → `ANY`, `auto`/unset → `VALIDATED` (default). Previously `tool_choice` was ignored and tools were always emitted with `VALIDATED` mode, so `tool_choice: none` still sent function declarations to Gemini.
- Unit tests in `internal/translator/gemini_tool_preserve_test.go` cover tool_choice mapping (none/required/auto), functionCall and functionResponse preservation in both OpenAI→Gemini and Gemini→OpenAI directions, and stream chunk functionCall preservation.
- Parity with upstream decolua/9router#5386 (fixes #5385): tools, functionCall, and functionResponse parts survive the OpenAI ↔ Gemini native round trip instead of being dropped.
