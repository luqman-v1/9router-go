### feat(tokensaver): sync doctrine and protocol with upstream token savers

Sync token savers with upstream canonical releases:
- **Caveman**: align system doctrine with upstream v4.1.0 (`JuliusBrussee/caveman`), adopting ASD-STE100 guidelines (20 words max per sentence, active voice, short word preference, bounded tool run status).
- **Ponytail**: align all three levels (lite/full/ultra) with upstream v5.3.3 (`DietrichGebert/ponytail`), adding the scope vs extra features distinction and the `shortcut: <limit>, <when to upgrade>` notation.
- **Headroom**: forward `x-headroom-session-id` on `/v1/compress` calls to isolate prefix cache trackers across parallel Claude Code subagent lineages as expected by upstream Headroom v0.44.0.
- **RTK Filters**: expand `shell-find` match patterns to handle Windows drive letters and backslash path separators (`C:\...`, `.\...`); keep `Author:`/`Date:` lines in `git-log` output as upstream's JS-native filter does.
