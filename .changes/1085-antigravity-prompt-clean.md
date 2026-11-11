### fix(antigravity): strip Claude billing header and sanitize prompt rewrites

- Add table-driven unit test coverage in \internal/translator/antigravity_test.go\ asserting that \x-anthropic-billing-header:\ lines and competing Claude SDK boilerplate are stripped from system prompts (behavior implemented on main via #444).
