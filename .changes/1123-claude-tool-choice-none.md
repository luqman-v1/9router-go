### fix(translator): map Claude tool_choice none to OpenAI string none

- Translates Claude Messages API `{"tool_choice": {"type": "none"}}` into standard OpenAI `"tool_choice": "none"`.
- Parity with upstream `decolua/9router#5636`.
