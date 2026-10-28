### fix(zed): drop temperature on the Claude lane (rejected on adaptive thinking)

Zed editors send `temperature` on every chat-completions turn. On
`claude-haiku-5-5` the Zed Anthropic lane serves adaptive thinking, and
Anthropic rejects any temperature other than `1` on a thinking request:

> `temperature` may only be set to 1 when thinking is enabled or in adaptive mode

The failing turn surfaced as `400` in the usage log with a "request failed"
response, then burned a fallback attempt per request.

The fix removes the top-level `temperature` field from the Zed Claude
provider request before it is wrapped in the envelope. The backend's
adaptive temperature default is already 1, so dropping the field changes
nothing about the intended sampling — it only stops the gateway from
forwarding a value the API refuses.

This mirrors the upstream parameter rule for Claude-shaped lanes (the
opencode-go / Claude case in `paramSupport.js`, which drops `temperature`
for every `/claude/i` model).

Verified: `go test ./internal/proxy/executor/` includes
`TestBuildZedCompletionBodyAnthropic_StripsTemperature`, which builds the
Zed envelope from a client body carrying `temperature: 0.7` and asserts
`provider_request` carries no `temperature` field.
