### fix(validate): send x-opencode-session for opencode-go connection test

- Send a deterministic `x-opencode-session: validate-<hash>` header during `opencode-go` connection validation to prevent HTTP 400 `missing_session_id`.
