### fix(dashboard): probe connection with natural test message and active opencode validation

- Replace single-word probe strings (`ping`, `test`) across provider and node validators with a short natural message (`Hi there, how are you?`).
- Run a live chat probe for `opencode` connections instead of automatically marking NoAuth providers valid, surfacing genuine quota and rate limit errors (such as `429 FreeUsageLimitError`) on the dashboard test button.
