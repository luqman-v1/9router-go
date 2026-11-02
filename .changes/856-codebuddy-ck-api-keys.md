### fix(codebuddy): support WorkBuddy ck_ API keys across headers, test, validate, and plan-restriction fallback

- In `internal/proxy/executor/codebuddy.go`, support WorkBuddy API keys starting with `ck_` (porting upstream `decolua/9router` PR #5342).
- When `ck_` API key is detected: send dual auth headers (`Authorization: Bearer` and `X-API-Key`), omit `X-Domain`, set `X-User-Id: "anonymous_" + last8(key)`, and set `X-IDE-Type: WorkBuddy` and `X-Agent-Runtime: local-agent`.
- Map assistant `reasoning_content` to `reasoning` in message history translation for both CN and Intl endpoints.
- In `internal/providers/errorclassify.go`, add classification rules for error codes 11158 (model not in plan), 14018 (credit exhausted, 2m cooldown), and 11140 (account restricted, 30m cooldown).
- Added comprehensive unit tests in `internal/proxy/executor/codebuddy_test.go` and `internal/providers/errorclassify_codebuddy_test.go`.
