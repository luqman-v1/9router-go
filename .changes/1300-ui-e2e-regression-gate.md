### Added

- A mandatory live UI regression gate for `web/` (AGENTS.md §6.H): a PR that changes what the dashboard shows, copies, submits, or names must carry a `web/e2e/` test that drives a real gateway in Chromium and asserts the observable result — the rendered text, clipboard contents, or submitted request. The new test must fail on the base and pass with the fix, both results shown in the PR. Screenshots must be element-scoped and capture the thing that changed. Display and storage aliases must stay separate (upstream keeps `providerDisplayAlias` and `providerStorageAlias` distinct).
- §5.C.5: changing or deleting an existing test assertion is now a contract change that needs a source of truth and an explicit justification in the PR body; a test that only asserts the new behaviour is a recording, not verification.

### Why

- PR #1259 (`929acc66`) changed the provider page's Copy button to a helper that returned the raw provider id for every registered provider. The button copied `opencode/<model>` where the gateway serves `oc/<model>`. `bun test src scripts` stayed green, the svelte ratchet stayed green, and the PR carried screenshots — of a different part of the page. No gate rendered the button and read the clipboard.

### Verification

- Added `web/e2e/providerCopyModelPrefix.test.ts`: boots a real gateway in Chromium at an isolated data dir, opens `/dashboard/providers/opencode`, asserts every rendered model id carries the published `oc/` prefix, presses the first Copy button and reads the clipboard. It **fails on base** (`opencode/...`) and **passes with the fix** (`oc/...`), both observed locally against rebuilt bundle and binary.
- `bun test src scripts` → 888 pass / 0 fail. Full e2e suite still green on CI.
