### feat(bedrock): AWS Bedrock runtime with SSO, SigV4 and EventStream streaming (part 2 of 2)

Wires Amazon Bedrock into routing, the catalog and the dashboard on top of the AWS
primitives added in part 1. Two provider entries share one executor, because Bedrock
model families speak different wire formats and each therefore gets its own entry rather
than failing mid-stream after the call is billed.

- `internal/proxy/bedrock.go`, `internal/proxy/executor/bedrock.go` — converse and invoke,
  SigV4-signed per request from resolved credentials, AWS EventStream unwrapped back
  into SSE. On the Anthropic entry the unwrapped stream is handed to the existing Claude
  handlers rather than a second translation path, so usage accounting, the
  `/v1/responses` bridge, terminal synthesis and the `[DONE]` sentinel all work as they
  do on any other Claude-wire provider. Bedrock reports throttling and validation
  failures as in-band frames rather than HTTP statuses, and a stream that closes before
  its terminal event is now failed rather than presented as a finished answer.
- Registry — `bedrock` (`br`, Claude wire) and `bedrock-xai` (`brx`, Chat Completions)
  with aliases, model catalogs, per-provider pricing, and `UpstreamIsAnthropic`, a new
  `ProviderConfig` flag: the existing Claude-wire detection keys off `api.anthropic.com`,
  so a gateway host needs its own signal for the response to be translated.
- Dashboard — an AWS credential form on `AddConnectionModal`, gated on the new
  `credentialForm` catalog flag rather than a provider id, so a later AWS entry is not
  left with no way to enter a profile. Profile mode makes the API key optional.
  `accessKeyId` and `profile` join the echoed provider-specific data; the session token
  does not, since it is a credential.

Verified with `go test ./...`, `make test-integration`, `make vet-svelte`, and a live
smoke against a fake Bedrock runtime: the gateway signs the request (session token both
sent and signed), drops `model` from the body, pins `anthropic_version`, hits the
streaming invoke path, and returns the upstream's frames as Chat Completions SSE with
usage accounted. No AWS account is required for any of it.