### Changed
- Released captured executor request buffers (`Body`, `ConnData`, `ToolNameMap`) immediately post-dispatch and reset `ResponseBuf` after usage/failure logging to bound RSS memory on low-spec hardware (fixes #688, upstream `OmniRoute#17269` parity).
