### fix(translator): synchronize cloaked tool names in forced tool_choice

- Rewrites forced `tool_choice` target names when tool declarations are cloaked or sanitized with custom suffixes (`_ide`), preventing upstream HTTP 400 schema mismatch errors.
- Parity with upstream `decolua/9router#1600` (closes `#1592`).
