### Command Code: correct quota display and connection test

- Recognize the short plan ids the API actually returns (`go`, `individual-go-v1`, ...) by folding them onto the full plan ids, so the Go plan resolves its name and 10-credit cap instead of an unbounded "Command Code" row.
- Quota rows now carry `percentage` / `colorPercentage` / `percentageIsUsed` plus an `emptyMessage`; the dashboard colours follow the remaining share and an unused window reads "No usage in this window" rather than a bare N/A.
- Dedicated connection probe for commandcode hitting `/alpha/whoami?limits=1` — the generic key probe cannot validate a key against this upstream.
