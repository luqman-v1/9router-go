### fix(media): rotate cloudflare-ai image requests across connections on quota

An image generation to a `cloudflare-ai`/`cf` model failed the whole request when the selected account was out of its daily free Workers AI neuron allocation: the lane returned Cloudflare's quota refusal (`4006` / HTTP 429) to the client instead of trying the next connection, while chat completions already rotate accounts on retryable upstream errors (issue #988).

The cloudflare image lane now mirrors that fallback:

- **Retry loop**: on a retryable account-scoped refusal (`4006` quota, `429`, `401`, `403`) the lane parks the failed connection, excludes it for the remainder of the request via `GetBestConnectionWithContext`'s `excludeIDs`, and retries the next best account — up to `cloudflareMaxRetries` (6), terminating early once every account is excluded.
- **Cooldown persistence**: a quota-drained account is parked until the next UTC midnight (when Cloudflare resets the 10k-neuron budget) via `LockConnectionRateLimit`, so the account selector skips it for the rest of the day — tomorrow it re-enters rotation automatically. Non-quota refusals park for 5 minutes.
- **Pinned connections don't rotate**: when the caller pinned a specific connection (e.g. a combo member), the refusal is surfaced directly and the fallback walk stays with the caller, matching `handleAccountFallback`'s pinned branch.
- **Non-retryable failures** (e.g. 400 model-not-found) still pass through to the client untouched.
- Per-connection URL rebuilding means each account's own `accountId` is dialled during the walk.

**Verified**: `go build ./cmd/9router-go/` OK, `go vet` and `go test ./internal/handlers/media/` pass, including the new `TestHandleImages_CloudflareRotationOnQuota` — account A returns `4006`, account B a real image, the request succeeds via B, A's `rateLimitedUntil` is set, and a second request never re-hits A. (Live smoke: the same behavior was observed against the reporter's accounts — priority-1 exhausted, priority-2 generating.)
