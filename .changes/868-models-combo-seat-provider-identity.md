### fix(v1/models): resolve combo seat provider identity from live connections

- In `internal/handlers/chat/models_list.go` and `resolution.go`, resolve combo seat provider identities from live connection records before resolving model capabilities (porting upstream `decolua/9router` PR #5347).
- Ensures combo seats referencing custom connection aliases or provider IDs inherit accurate model capabilities (vision, reasoning, context limits) in `/v1/models` instead of unmapped defaults.
- Added unit tests in `internal/handlers/chat/combo_seat_limits_test.go`.
