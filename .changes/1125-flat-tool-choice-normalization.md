### fix(translator): normalize flat Responses API tool_choice to nested function form

- Normalizes flat Responses-style `{"type": "function", "name": "..."}` tool_choice into standard nested `{"type": "function", "function": {"name": "..."}}` in `normalizeToolChoice`.
- Local fix: prevents flat function specifications from being dropped or demoted to "auto" when filtering/normalizing requests for Chat Completions backends.
