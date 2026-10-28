### fix(media): authenticate embeddings with the Copilot token, stop work for a canceled caller

**Embeddings.** GitHub Copilot authenticates `/v1/embeddings` with the
short-lived token it hands out alongside the OAuth credential, and that token
lives in the connection's `providerSpecificData`. `HandleEmbeddings` never read
it, so every Copilot embeddings request was authenticated with the stored OAuth
credential — which that endpoint does not accept. The token is now set after the
static-header loop, replacing `Authorization` on purpose.

**Canceled requests.** Two dispatch paths kept working for a caller that had
already gone:

- `forwardSystemoneRequest` retries once directly whenever the first `Do`
  returns an error. A canceled request is not a transport failure, and the retry
  opens a second upstream connection for nobody. The retry is now skipped when
  the request context is done.
- The combo loop in `forwardMediaRequest` moved on to the next member when a
  member's request failed. A cancellation became "try the next model". It now
  answers 499 and stops.

Verified with `TestHandleEmbeddings_CopilotTokenOverride`, which seeds a
connection with a `copilotToken` and asserts the upstream sees it. On the
pre-fix code:

```
expected Authorization Bearer tid=copilot-token-abc, got "Bearer sk-token"
```

**No test is claimed for the two cancel guards.** A test was written for the
systemone path and passed identically before and after the change — with a
canceled context, the first `Do` never reaches the network either way, so the
guard's effect is not observable from outside. Rather than ship a test that
asserts nothing, the guards stand on the change being obviously correct and the
reasoning being in the comment. Flagging this rather than burying it.
