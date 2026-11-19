### Added

- The wiki's provider page now lists every visible registry provider — 150+, grouped under the same four sections the dashboard renders, each with its wire ID — and the API page documents three routes that shipped after the wiki was written: `GET /api/keys/{id}/budget`, `POST /api/cache/semantic/invalidate`, and the Gemini-native `GET /v1beta/models` / `POST /v1beta/models/{model}`. The drift ratchet's baseline drops from 122 to 0, so the next provider added without a wiki mention fails the deploy immediately.
