### fix(tray): start daemon from tray menu option and strip .exe in gatewayBinaryName

- **CLI tray menu startup** (`cmd/9router-go/menu.go`): updated `startTray` (menu option `6) Run in background with tray icon`) to start the background gateway daemon when `daemon.RunningPID() == 0` (tolerating `daemon.ErrAlreadyRunning`) before spawning the tray companion, matching `openDashboard`.
- **Windows tray sibling gateway name resolution** (`cmd/9router-tray/actions.go`): stripped a trailing `.exe` suffix (case-insensitive) in `gatewayBinaryName` / `gatewayBinaryNameFromArg0` before stripping `-tray`, so renamed Windows tray binaries such as `opencode-go-tray.exe` resolve `opencode-go.exe` instead of falling back to `9router-go.exe`.
- **Tests** (`cmd/9router-go/menu_test.go`, `cmd/9router-tray/actions_test.go`): added unit tests covering `startTray` daemon startup order, skip-when-running, `ErrAlreadyRunning`/error propagation, and `gatewayBinaryNameFromArg0` `.exe` handling.
