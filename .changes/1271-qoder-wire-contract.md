### Fixed

- Qoder chat requests now match the working client's wire contract: the `X-Request-Id` header is the same UUID signed into the COSY payload, the chat request sends `User-Agent: qodercli/1.0.0`, and `developer` messages are hoisted into the top-level `system` field alongside `system` instead of being left in `messages`.

### Verification

- `go test ./internal/proxy/executor/ -run 'TestBuildQoderCosyHeaders_RequestIDMatchesPayload|TestQoderApplyChatHeaders_UsesCLIUserAgent|TestForwardQoder_HoistsDeveloperIntoSystem' -count=1` fails on base and passes with the fix.
