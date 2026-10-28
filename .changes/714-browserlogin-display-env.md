### fix(browserlogin): open the login browser with the user session's display

A gateway started by a service manager (systemd, a supervisor) inherits no `DISPLAY` or `WAYLAND_DISPLAY`. The browser-login flow for `deepseek-web` and `gemini-web` then refused to open a browser even on a desktop machine that has a live session — the window exists, the environment just does not name it.

`resolveDisplayEnv` now fills in the session display when the process environment lacks one: the Wayland socket under `$XDG_RUNTIME_DIR` first, then the X11 sockets under `/tmp/.X11-unix`. Both browser launches (`Run` for Gemini, `RunDeepSeek`) run with that environment, so the login window opens on the operator's actual desktop. An environment that already names a display is left untouched, and a machine with no session socket still fails with the original clear error.

Verified: `go build ./...`; `go test ./internal/browserlogin/` green, including the resolver cases (Wayland discovered, display preserved, no phantom socket advertised).
