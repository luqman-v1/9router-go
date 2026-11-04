### fix(providers): preserve image parts for Cloudflare AI vision models

Cloudflare Workers AI vision models (Kimi K2.6, Mistral Small 3.1) had their
`image_url` content parts flattened into a single text string whenever a turn
held more than one block, so an image sent beside text reached the model as a
dead placeholder instead of an image.

Only flatten content to a string when every part is text; any non-text part
(`image_url`, `file`, …) keeps the content as an array so vision models
receive their image parts. `collapseTextParts` now joins text-only parts with
newlines and returns the array unchanged otherwise.

Mark `Vision: true` for `@cf/mistralai/mistral-small-3.1-24b-instruct` and
`@cf/moonshotai/kimi-k2.6` in the `cloudflare-ai` capability table so the
request path preserves their image parts, and add unit tests covering both
models plus the flatten behavior.
