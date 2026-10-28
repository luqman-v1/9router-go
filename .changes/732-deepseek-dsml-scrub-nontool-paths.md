### Fix: DeepSeek Web DSML markup no longer leaks into no-tools answers

Requests that did not declare tools took the streaming or non-streaming
content path, which relayed the upstream answer untouched. DeepSeek's web
models reach for their native DSML tool syntax even when no tools are
declared, so those answers occasionally arrived as literal `<｜｜DSML｜｜ …>`
markup — exactly what a cronjob report showed in a Telegram group.

- `deepSeekStreamToClient` now scrubs DSML as deltas arrive, keeping only the
  prose around the block.
- `deepSeekCollectToClient` applies the same scrub to the buffered answer
  before writing the completion document.
- `deepSeekStripDSML` was rewritten as a tag-balanced scrubber: it removes the
  whole malformed block — missing calls opener, unbalanced closers, stray tags —
  instead of one element per pass, which left orphan tags behind.

- The streaming scrubber holds partial tags: the upstream delivers one
  character per frame, so a tag arrives as `<`, then `<｜`, then `<｜｜`.
  Those prefixes are kept back until the next fragment decides whether they
  are markup or prose.

Verified by regression tests pinned to the two leaked shapes (missing calls
opener; mixed bar spellings with an extra invoke closer) across the strip,
streaming, and non-streaming paths, plus live end-to-end turns against
chat.deepseek.com with a real credential — including one- and two-call tool
turns.
