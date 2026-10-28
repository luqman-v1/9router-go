### fix(dashboard): request payload modal 404s for model ids containing a slash

The request detail id is the `requestDetails` primary key built as
`<unix-millis>-<model>` (`internal/handlers/chat/usage.go`), so a slashed custom
model id (`deepseek/deepseek-v4-1-flash`, valid since #584) puts a real `/` into
the key. The dashboard percent-encodes it (`web/src/api/client.ts`), so the wire
path carries `%2F`; chi matches the route on `RawPath` but hands
`r.PathValue("id")` the still-encoded segment, and the repository lookup missed
every such row — the Analytics → Recent Requests payload panel returned
`404 request detail not found` even though the row existed.

`HandleRequestDetail` now `url.PathUnescape`s the path value before the
repository read, restoring the payload modal for every existing row. Regression
covered by `TestHandleRequestDetail_SlashedModelID`, which drives a real chi
route with an encoded slashed id (the existing tests set `PathValue` directly
and so never exercised the encoding step); the test fails on the pre-fix parent
with the exact 404 from the report and passes on the head.

Verified: `gofmt`/`go vet` clean on the touched files; the handlers suite's
usage/detail tests pass (`TestHandleRequestDetail_SlashedModelID`, list paging,
detail-by-id companions). The id-keying shape (`<millis>-<model>`) remains — a
URL-safe key redesign is left as a follow-up since changing the primary key
would invalidate existing stored rows and touches the client contract.
