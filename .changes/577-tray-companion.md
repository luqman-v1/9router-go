### feat(tray): give the tray its own binary and verify identity before stop

The tray was linked into `9router-go`, which put a GUI toolkit in the path of
every release build. It now ships as its own binary and the core never learns it
exists (`docs/specs/tray-companion-architecture.md`).

- `cmd/9router-tray/` is the GUI binary; `internal/tray/` holds the decisions it
  makes, with no GUI import, so the rules are testable without a display.
  `cmd/9router-go/tray_windows.go` and `tray_other.go` are deleted.
- `cmd/9router-go/menu.go` keeps item 6 ("Run in background with tray icon") and
  spawns the sibling binary instead of importing it. Only the sibling next to the
  running executable is tried: a `PATH` lookup could pick up a different
  install's tray, which would then control a gateway that is not this one.
- **Stop follows the supervisor, not the token.** A backend that owns the
  process (systemd-user, launchd, openrc, runit) is stopped through
  `service.Stop()` with no token sent at all. `windows-task` is refused rather
  than stopped, because `schtasks /end` is `TerminateProcess` and the gateway
  never gets to drain. Only an unsupervised process reaches `daemon.Stop()`, and
  only after the socket owner has been verified against the recorded PID.
- `internal/tray/socket_owner_linux.go` proves the listener belongs to the
  daemon by matching the port's inode in `/proc/net/tcp{,6}` against
  `/proc/<pid>/fd`. Every other platform fails closed: `Stop` is refused rather
  than sent to a process that was not proven to be ours. That is a visible
  limitation, not an oversight — §5.3 of the spec says so, and §9 names the UDS
  control channel as the Phase 2 fix.
- `internal/tray/state.go` distinguishes four states, not two. A service probe
  that reports "no autostart entry" is normal `Stopped`; only `ActiveUnknown`
  renders `Unknown`. An unreadable pid file never renders `Stopped` — offering
  Start from that state is how a user gets `ErrAlreadyRunning` for a click they
  did not cause.
- Menu: `9Router Go (Port N)` / `(Stopped)` / `(Port N in use by foreign process
  (PID M))` / `(Unknown)`, plus Start, Stop, a real Auto-start checkbox, Open
  Dashboard and Quit Tray. systray has no menu-open callback, so the items are
  built once and refreshed in place every second — a menu drawn once at startup
  says "Running" forever, which is the lie C5 forbids.
- `Quit Tray` releases the icon and does not touch the gateway. The Node CLI's
  Quit stops the server; this one deliberately does not.

**Upgrade note:** the tray is a separate artifact and is not in any
`CROSS_TARGETS` build. `gui.go` carries `//go:build windows || tray` so
`go vet ./...` and `go test ./...` still pass on a Linux host without
`libayatana-appindicator3-dev`; on macOS and Linux the real GUI needs
`go build -tags tray ./cmd/9router-tray/`. Windows needs no tag — systray
reaches the Win32 API directly there. A build without the tag compiles a stub
that fails at startup with the flag that fixes it, rather than starting and
never drawing an icon.

- Evidence: `go test ./internal/tray/ ./cmd/9router-go/` passes; the identity
  tests were mutation-checked by flipping `StopSupervised` to `StopVerified`,
  which fails the four supervised backends as expected. `CGO_ENABLED=0` builds
  pass for all five `CROSS_TARGETS`, and `go list -deps ./cmd/9router-go/` names
  no GUI package on linux, darwin or windows. `go vet ./...` passes on linux and
  darwin, and the Windows build of `cmd/9router-tray/` is now a CI step.
- Not verified: the §8 acceptance matrix (W1–W6, M1–M5, L1–L5) needs three real
  desktops. Cross-compiling proves nothing about whether an icon appears.
