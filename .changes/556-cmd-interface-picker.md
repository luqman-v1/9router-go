### Bare `9router-go` shows an interface picker, and a Windows tray mode

- Running the binary with no arguments on a terminal now offers a numbered menu — open the dashboard, start in the background, start in the foreground, stop, status, tray — with the live daemon PID in the header, instead of silently booting the server. A service unit, container, or pipe has no TTY and still boots exactly as before.
- The same executable answers to `opencode-go`: help and error text follow `os.Args[0]`, and `make build` now produces both names.
- Tray mode on Windows runs the gateway in the background with an icon (CGO-free, via `github.com/getlantern/systray`); on other platforms the menu item points at `9router-go start`, which is the equivalent there.
