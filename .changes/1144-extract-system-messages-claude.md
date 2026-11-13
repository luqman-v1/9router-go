### fix(translator): extract in-body system messages to top-level system for Claude Messages

- Extracts and removes `role: "system"` and `"developer"` messages from `messages[]` and promotes their text to the top-level `system` parameter for Claude Messages API requests, preventing upstream HTTP 400 rejection.
- Parity with upstream `decolua/9router#1600` (closes `#1580`).
