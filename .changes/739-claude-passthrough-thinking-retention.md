### Fixes
- **Claude Passthrough**: Preserved blank-text `thinking` and `redacted_thinking` blocks on Claude native passthrough (upstream `decolua/9router#5233` parity, resolving #738). Prevents silent loss of reasoning context across multi-turn Claude Code sessions and ensures Anthropic thinking responses are recognized as contentful in zero-content checks.
