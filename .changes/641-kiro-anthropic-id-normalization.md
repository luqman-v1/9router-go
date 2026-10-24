### Kiro: fold Anthropic model ids onto upstream aliases

- Strip Anthropic date snapshot suffixes and map keyword families (sonnet/opus/haiku/fable, deepseek/glm/minimax/qwen/kimi/gpt) onto the dotted Kiro aliases in the kiro upstream path, fixing `400 INVALID_MODEL_ID` for clients that send the Anthropic spelling.
- Fix the capitalized `MiniMax-M2.5` catalog id down to the accepted `minimax-m2.5`.
