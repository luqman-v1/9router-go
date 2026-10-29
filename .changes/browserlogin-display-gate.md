### fix(browserlogin): the no-display gate sees the session's discovered display

Follow-up to the display-env resolution: the launch gate in `Run` and `RunDeepSeek` still read `os.Getenv("DISPLAY")` / `os.Getenv("WAYLAND_DISPLAY")` directly and refused before `resolveDisplayEnv()` ever ran — so a gateway started by a service manager was still told "this machine has no display" on a desktop with a live session, which is exactly the scenario the resolution exists for. The unit tests passed because they exercised `resolveDisplayEnv` alone and never the gate.

Both launches now resolve the environment once and decide the gate on the result (`envNamesDisplay`): an environment the resolution filled in from the session's Wayland or X11 socket satisfies the gate, and a machine with no display variables and no session socket still fails with the original, accurate error.

Verified: `go build ./internal/browserlogin/`; `go test ./internal/browserlogin/ ./internal/handlers/dashboard/` green, including the new gate cases (a discovered socket satisfies the gate, an empty-valued variable names no display).
