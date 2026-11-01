### fix(stream): drop empty tool-call deltas before assigning ids

- In `internal/proxy/sse_toolcall.go`, filter known empty function-call placeholders before assigning tool-call IDs (porting upstream `decolua/9router` PR #5349, fixing #5345).
- Drops all-null, indexed-null, and index-only function fragments that carry no name or argument delta, preventing phantom tool-call emission to downstream clients.
- Removes `delta.tool_calls` entirely if all items in the delta are empty placeholders.
- Preserves custom tool types, provider extensions, meaningful fragments (id-only, name-only, argument fragments), and sibling content fields.
- Added comprehensive unit tests in `internal/proxy/sse_toolcall_test.go`.
