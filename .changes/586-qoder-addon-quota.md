### Added
- Upstream parity (`decolua/9router` commit `87a74e1b`, `open-sse/services/usage/misc.js`): surface Qoder add-on promotional credits (`addOnQuota`) in the dashboard Quota Tracker (#585).
- Extracted `addOnQuota` in `fetchQoderUsageAt` (`internal/handlers/dashboard/usage_providers.go`) when `total > 0`, including `used`, `total`, `remaining`, and `detailUrl`.
- Normalized `addOn` quota type in `web/src/components/quota/types.ts` to display as `"Add-On Promo"`.
- Added backend unit tests in `internal/handlers/dashboard/usage_ollama_qoder_test.go` and frontend unit tests in `web/src/components/quota/qoderAddOn.test.ts`.
