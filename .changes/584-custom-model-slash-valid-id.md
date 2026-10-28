### Allow slashes in custom model IDs for provider namespaced models

- Removed `/` from `modelIDReserved` in `internal/handlers/dashboard/model_id.go` and `INVALID_PATTERN` in `web/src/lib/bulk-models.ts` so namespaced provider models (such as `deepseek/deepseek-v4-1-flash` or `meta-llama/llama-3.1-70b-instruct`) can be added as custom models.
- Guard against leading, trailing, and consecutive slashes (`//`) so empty or malformed address segments are rejected.
- Updated `DELETE /api/models/custom/*` in `internal/handlers/dashboard/routes.go` and `getURLParam` to correctly route and delete custom model keys that contain slashes in the model ID segment.
- Verified with unit tests in `model_id_test.go`, endpoint tests in `dashboard_test.go`, and parser tests in `bulk-models.test.ts`.
