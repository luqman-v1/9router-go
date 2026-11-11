### fix(translator+combos): flatten content for cloudflare-ai; accept double-encoded smart rules

- **cloudflare-ai**: forward content as plain strings — Cloudflare Workers AI
  rejects array content with `400 AiError: Bad input: oneOf at '/' not met ...
  Type mismatch of '/messages/0/content', 'array' not in 'string'` (code 5006).
  Port of upstream `decolua/9router` paramSupport `STRIP_RULES`
  `{ provider: "cloudflare-ai", flattenContent: true }`: text blocks are
  concatenated in order; other block kinds contribute nothing.
- **smart-combo**: `ParseRules` now unquotes rows whose rules column holds a
  double-encoded JSON string (`"[{\"pattern\":...}]"`) instead of failing every
  dashboard load with `stored rules unreadable`. Bounded at 3 unquote levels;
  the error text for genuinely unreadable rows is unchanged.
- Unit tests lock both: `TestFilterToOpenAIFormatFlattenContent` (on/off/JSON
  entry) and the table-driven `TestParseRules`.
