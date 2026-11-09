### fix(combo): treat Claude redacted thinking as first output in target budget gate (#1028)

Recognize Claude `redacted_thinking`, `thinking`, and `signature_delta` chunks as valid stream output activity in the combo first-output gate.

- Treat Claude adaptive thinking frames (`redacted_thinking`, `thinking`, `signature_delta`) as stream activity in `internal/proxy/stream_peeker.go`.
- Prevent combo model response timeouts and zero-content failovers from prematurely aborting or cycling away from deep-thinking Claude streams.
- Add unit tests verifying thinking chunks satisfy the first-output gate and allow subsequent stream tokens to pass while silent streams time out as expected.
