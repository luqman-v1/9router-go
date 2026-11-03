### fix(tray,menu): build Windows tray binary in cross-release and fall back gracefully when missing
- **Windows Tray Artifact**:
  - Update `Makefile`, `ci.yml`, and `release.yml` to build `9router-go-tray-windows-amd64.exe` alongside the core server in `make cross`. Because `getlantern/systray` uses pure Win32 system APIs on Windows without requiring CGO, it builds cleanly in `CGO_ENABLED=0` cross-compilation environments.
- **Graceful Background Fallback in Menu**:
  - In `cmd/9router-go/menu.go`, when option `6` (Run in background with tray icon) is chosen and the companion tray executable is not found next to the server binary, print an informative notice and fall back to `startDetached` (running in background without tray). This fulfills the user's intent to free the terminal without exiting with an error.
