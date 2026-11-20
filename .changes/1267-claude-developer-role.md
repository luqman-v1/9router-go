### Fixed

- Claude connections no longer fail with HTTP 400 `Unexpected role "developer"`: `developer` messages are now folded into the top-level `system` prompt, like `system` (#1265).
