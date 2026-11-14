### fix(translator): normalize flat Responses API tool_choice to nested function form

- Normalizes flat Responses API `{"type": "function", "name": "..."}` tool_choice into standard nested `{"type": "function", "function": {"name": "..."}}` format.
- Parity with upstream `decolua/9router#5636`.
