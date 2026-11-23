### perf(sse): compute request token estimate once per stream session

Retain the seeded prompt estimate in `promptEstimates` across chunks instead of
deleting it on first read, so subsequent chunks and deferred terminals reuse
the cached basis (issue #1399). Adds `TestPromptEstimateCachedAcrossChunks`.
