### fix(translator): repair truncated tool arguments to prevent json marshal failure

When an upstream model hits `max_tokens` or the generation output limit while generating a tool call (such as a large `Bash` command with `finish_reason: length`):
- The `function.arguments` JSON string cuts off mid-string or mid-object (e.g. `{"command": "cat << 'EOF'...` without closing quotes or braces).
- `sanitizeToolArgs` failed to parse the truncated JSON and returned the raw unclosed string.
- When marshaling the translated Claude message, `encoding/json/v2` validates `jsontext.Value` and fails with `cannot marshal from Go jsontext.Value: unexpected EOF within "/content/0/input/command"`, dropping the entire response with HTTP 502 Bad Gateway.

Fixes applied:
- Add `RepairTruncatedJSON` to close unclosed strings, keys, arrays, and objects in truncated JSON payloads.
- Use `SafeToolCallInput` to ensure `tool_use.input` is always a valid `jsontext.Value` before marshaling.
