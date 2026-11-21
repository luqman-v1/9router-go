### fix(windows): isolate USERPROFILE in agy-acp and fix Windows test portability

- Net-new local Windows bugfix and test portability cleanup (keeps `internal/daemon` and `internal/proc` untouched).
- **Windows `agy-acp` env isolation** (`internal/acp/agy/turn.go`): updated `removeEnv` to match variable names before `=` using `envKeyMatches` (`strings.EqualFold` on Windows), stripped all overridden isolation and ambient credential variables before appending, and set `USERPROFILE` alongside `HOME` to the account root directory (`TestBuildCommandIsolatesEnv`).
- **Windows test portability** (`internal/clientconfig/paths_test.go`, `internal/clientconfig/atomicwrite_windows_test.go`, `internal/qodercheckin/checkin_test.go`): guarded the POSIX ledger mode check in `TestQoderCheckinLedgerSurvivesRestart` on Windows and handled Windows 8.3 short temp directory paths cleanly in `TestPathResolution` and `TestJunction_EvalSymlinksIsBlindAndReadlinkIsNot`.
