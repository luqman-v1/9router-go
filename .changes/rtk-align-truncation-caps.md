### fix(tokensaver): align RTK truncation caps with upstream Node reference

**Reasoning & output safety.** The Go RTK implementation previously enforced an
aggressive hard cap of `maxChars = 8000` (~2000 tokens) and `maxLines = 100` on
all command and tool outputs, severing long logs, directory trees, and terminal
outputs mid-sentence. In the Node reference (`open-sse/rtk/constants.js`),
the raw limit is `RAW_CAP = 10MB` and smart truncation only engages when a
payload spans at least `SMART_TRUNCATE_MIN_LINES = 250` lines.

**Limits alignment.**
- Default `maxChars` is now aligned to 10MB (`RawCap = 10 * 1024 * 1024`),
  preventing premature char-truncation on routine development commands.
- Default `maxLines` is aligned to 250 lines (`SmartTruncMin`), with
  `generic-output` keeping 120 head lines and 60 tail lines matching Node
  `smartTruncate.js`.
