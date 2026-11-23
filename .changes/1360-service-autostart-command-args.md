### fix(service): run windows-task via serve and place global flags before subcommands

- **Foreground `serve` subcommand & `windows-task` target** (`cmd/9router-go/main.go`, `internal/service/windows.go`): added `9router-go serve` (`Action: runServer`) and updated `installWindowsTask` to invoke `serve` after forwarded global flags, so `/it` interactive-logon Scheduled Tasks boot the gateway instead of blocking in the interactive CLI menu.
- **Global flag placement across `windows-run` & `cron`** (`internal/service/service.go`, `internal/service/windows_run.go`, `internal/service/cron.go`): extracted `appendSubcommand(args, sub)` so forwarded global flags precede `start` (matching `urfave/cli` parsing rules).
- **Tests** (`cmd/9router-go/cli_usage_guidance_test.go`, `internal/service/service_*test.go`): added `TestServeSubcommand` and verified global-flag-before-subcommand ordering across `windows-task`, `windows-run`, `cron`, and `.desktop` entries.
