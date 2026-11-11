### fix(antigravity): strip Claude billing header and sanitize prompt rewrites (#1085)

The `x-anthropic-billing-header:` strip itself already exists on main (merged via #444).
This PR adds table-driven unit test coverage asserting that stripping works.

- `internal/translator/antigravity_test.go`: table-driven unit tests asserting that
  `x-anthropic-billing-header:` lines and competing Claude SDK boilerplate are stripped
  from system prompts (behavior implemented on main via #444).
