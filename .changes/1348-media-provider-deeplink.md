### fix(web): re-evaluate media provider deep-link after catalog loads

- **Media provider deep-link reactivity** (`web/src/App.svelte`): added a reactive `catalogLoaded` flag toggled right after `loadProviderCatalog()` populates the non-reactive `PROVIDER_CATALOG` array, and guarded `selectedMediaCatalogItem` and `selectedProviderMeta` with it so direct navigation and browser reloads on `/dashboard/media-providers/<kind>/<providerId>` mount `MediaProviderDetail` instead of falling back to `MediaKindView`.
- **E2E regression test** (`web/e2e/mediaProviderDeeplink.test.ts`): added Playwright coverage verifying both cold navigation to `/dashboard/media-providers/image/stability-ai` and page reloads keep `MediaProviderDetail` rendered directly.
