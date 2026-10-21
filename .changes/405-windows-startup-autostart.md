### Windows autostart: fall back to the Startup folder when `schtasks` is denied

- `9router-go service install` on Windows reported
  `service: schtasks.exe /create ...: exit status 1: ERROR: Access is denied.`
  and installed nothing. Creating a logon task (`/sc onlogon`) needs an
  Administrator console; an ordinary user cannot create one. All three variants
  (`/it`, explicit `/ru`, bare) are refused identically, so the scheduled-task
  backend was unusable without elevation.
- `internal/service/windows_startup.go`: new `windows-startup` backend that
  writes a windowless `9router-go.vbs` launcher to the per-user Startup folder
  (`%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup`), which Windows runs
  at logon and which needs no elevation. The launcher runs `9router-go start`
  hidden (`wscript.exe` owns no console), so the daemon detaches with no window
  flash. It carries the same `9router-go-autostart` ownership marker as the cron
  and `.desktop` entries.
- `internal/service/service.go`: `Install` tries the logon task first and falls
  back to the Startup folder on failure; `Status` and `Uninstall` cover the new
  backend; `--backend windows-startup` forces it. `control.go` reports
  `ErrNoSupervisor` for it (Startup fires only at logon), so
  `service start/stop/restart` fall back to the pid-file daemon like `cron`.
- `cmd/9router-go/service_cmd.go`: `service status` now shows
  `Inspect: explorer shell:startup` for this backend.
- Verified on Windows: `service install` writes the launcher and starts the
  gateway; `status` reports `windows-startup`; `uninstall` removes it and
  `stop` ends the daemon. `go test ./internal/service/...` green, including the
  new fallback, install, status, uninstall, render, and detect-override tests.
  All five `CROSS_TARGETS` still build with `CGO_ENABLED=0`.
