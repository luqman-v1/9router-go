### fix(tray): update tray icon to 9r GO mark and omit unknown PID 0 in port-conflict title

- Net-new local tray icon alignment and UX fix (conflict-free with #1308).
- **`9r GO` tray icon parity** (`cmd/9router-tray/tray.ico`): synced the embedded multi-resolution tray icon (`16x16`, `24x24`, `32x32`, `48x48` 32bpp frames from `web/public/favicon.ico`) with the #1060 `9r GO` rebrand.
- **Compact unknown-PID port-conflict title** (`internal/tray/state.go`, `state_test.go`): omitted `(PID 0)` in `Snapshot.Title()` when `StatePortConflict` has no attributed owner PID (`"9Router Go (Port %d in use)"`), preserving `"by foreign process (PID %d)"` whenever `s.PID > 0`.
