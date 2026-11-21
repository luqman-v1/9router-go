### fix(daemon,tray): preserve spaces in Windows pidfile paths and polish Windows tray build

- `internal/daemon/pidfile.go` (`parseClaim`): split only on the first space/tab on the first line instead of `strings.Fields`, so executable paths containing spaces (such as `C:\Program Files\...` or `C:\Users\<Name With Space>\...`) no longer fail `claim.owner()` and wipe `gateway.pid`. Added unit tests in `internal/daemon/pidfile_test.go`.
- `internal/proc/executable.go` (`Executable`): normalize image paths via `resolveImagePath` (`filepath.EvalSymlinks`) as documented, fixing Windows 8.3 short-path mismatches (`TestLiveExecutableMatchesSelfExecutable`).
- `Makefile` (`cross-tray-one`): pass `-H=windowsgui` when `CROSS_OS=windows` so `9router-go-tray` launches without a console window.
- `cmd/9router-tray/` and `internal/service/`: set `HideWindow: true` + `CREATE_NO_WINDOW` on Windows child CLI/service probes (`runGatewayCLI`, `service.run`) so GUI tray actions and the 1 s `schtasks.exe` status poll do not flash console windows, and surface Windows tray action errors via `windows.MessageBox` instead of invisible `fmt.Println` (preserving C6).
