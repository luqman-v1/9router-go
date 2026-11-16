### Fixed

- A tool call that arrives with no arguments (absent, `{}` or `null`) is now removed from the history sent upstream when another call to the same tool in the conversation does carry arguments, together with the `tool_result` paired with it. Clients validate the call against the tool schema and reject it, then resend the full history — so the model used to read its own bare call and repeat it every turn. Tools that are only ever called without arguments are untouched. Applied on both the Claude passthrough lane and the Claude-to-OpenAI conversion lane.
