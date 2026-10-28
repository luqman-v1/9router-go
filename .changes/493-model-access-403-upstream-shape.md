### fix(chat): restore upstream's 403 envelope for a model-access denial

A key that is not allowed to use the requested model was refused with a body of
the port's own invention: `Model not allowed for this API key`, `type:
permission_denied`, `code: model_not_allowed`. Upstream decolua/9router builds
that refusal in one place — `errorResponse(FORBIDDEN, keyAccessDeniedMessage(requested))`
through `buildErrorBody` — so its body is `type: permission_error`, `code:
insufficient_quota` (upstream `ERROR_TYPES[403]`) and the client-facing message
`This API key is not allowed to use model "<model>"`, which upstream builds in
`keyAccessDeniedMessage` (`src/shared/constants/keyAccess.js`, called at
`src/sse/services/keyAccess.js:115`). The port matched upstream before #451 and
drifted on all three wire fields when the denial was given its own literal
envelope.

`writeModelAccessError` now writes the model-access 403 through the shared
writer (`handlerutil.WriteJSONError`), so the envelope is the port's single 403
shape again, and the message is upstream's exact sentence with the model taken
from the typed error's `Model` field and quoted in double quotes. The typed
error's own `Error()` sentence is unchanged — it stays the internal/log wording.
The 503 policy-unavailable branch and the 400 fallback are unchanged.

Verified with `go build ./...`, `go vet ./...`, `go test -count=1
./internal/handlers/chat/...` and `go test -count=1 -tags=integration
./internal/integration/...`: the existing fail-closed cases now assert the
upstream `type`/`code` and that the message names the model, and
`TestWriteModelAccessError_ForbiddenBodyMatchesUpstream` pins the whole body
(exactly three fields, `permission_error`, `insufficient_quota` and the exact
message string) for a bare id, a provider-qualified id and a combo name. The
integration suite's media-endpoint denial case was updated to the same exact
sentence.
