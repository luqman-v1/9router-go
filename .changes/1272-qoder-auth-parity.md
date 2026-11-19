### Fixed

- Qoder device login now fails fast with a clear `MISSING_UID` error when neither the token poll nor userinfo yields a user id, instead of storing a credential that every later request signs with a placeholder (Qoder answers `403 {"code":"105","message":"Login expired"}`). The user id is resolved from the poll body (`user_id`/`id`) and then userinfo (`id`/`userId`/`user_id`), as a string or a number.
- The persisted Qoder machine id is now derived deterministically from the account's user id — the same `QoderDeriveMachineID` derivation the COSY signer uses — instead of the fresh random UUID minted at device-start, so a re-login no longer presents a brand-new device to the per-client queue.
- Qoder token expiry is now persisted from the poll's `expires_in` (with a bounded 30-day fallback when it is omitted) rather than `expiresAt=null`, and an expired `qoder`/`qoder-cn` credential is skipped at connection selection instead of being dialled upstream. Qoder device tokens carry no refresh grant, so no OAuth refresher is registered; a legacy row with a null expiry stays usable.
- The Qoder device poll now treats a `200` body carrying `errorCode` `NotFound` / `DeviceTokenNonceRequired` as still-pending (it previously ignored `errorCode` and reported a hard `poll_failed: no token`); any other `errorCode` is a real poll failure.

### Why

A Qoder grant without a user id cannot sign requests; a per-login random machine id resets the per-client queue on every re-login; an expired grant with no refresh path should not be dialled; and "still authorizing" must not surface to the client as a login error. These are local compatibility fixes to the Qoder auth/credential path.

### Verification

- New regression tests fail on the base commit and pass with the fix: `TestQoderPoll_MissingUIDFailsLogin`, `TestQoderPoll_StableMachineIDFromUID`, `TestQoderPoll_PersistsTokenExpiry` (expires_in + 30-day fallback), `TestQoderPoll_ErrorCodePending` (`internal/handlers/oauth`), and `TestQoderExpiredConnectionSkippedOnUse`, `TestQoderExpiredConnectionSkippedInFavorOfLive`, `TestQoderNullExpiryConnectionStillUsable` (`internal/handlers/chat`).
- `gofmt -l`, `go vet`, and `go test` are clean for `internal/handlers/oauth` and `internal/handlers/chat`.
