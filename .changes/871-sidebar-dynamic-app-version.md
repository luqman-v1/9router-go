### fix(dashboard): inject build-time version into sidebar instead of hardcoded fallback

- In `web/vite.config.ts`, read the single source of truth `VERSION` file at build time and inject `__APP_VERSION__` via Vite's `define` configuration.
- In `web/src/components/Sidebar.svelte`, initialize `version` with `__APP_VERSION__` and remove the obsolete hardcoded `'v1.9.9'` fallback.
- Added `web/src/vite-env.d.ts` declaring `__APP_VERSION__`.
- Prevents initial render from flashing `'v1.9.9'` on newer releases before `api.getSystemVersion()` resolves, providing an accurate 0ms version display without layout shift.
