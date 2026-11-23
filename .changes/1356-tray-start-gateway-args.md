### fix(tray): invoke gateway start without misplaced --background flag

- **Tray start command** (`cmd/9router-tray/actions.go`): updated `startGateway()` to call `execGatewayCLI("start")` instead of `runGatewayCLI("start", "--background")`, because `start` already launches the background daemon via `startDetached` while `--background` is a root-only flag that failed CLI usage parsing after a subcommand.
- **Regression test** (`cmd/9router-tray/actions_test.go`): added `TestStartGatewayDelegatesToGatewayCLI` verifying the `{"start"}` arguments and error propagation.
