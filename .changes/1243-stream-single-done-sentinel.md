### fix(stream): emit a single [DONE] sentinel in passthrough streams

- Ensures SSE passthrough stream flush never appends duplicate `[DONE]` sentinels when upstream ends with an unterminated `data: [DONE]`, matching merged upstream `decolua/9router@9ae6e147`.