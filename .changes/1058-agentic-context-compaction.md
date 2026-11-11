### feat(tokensaver): compact agentic context by model budget

- Compact intermediate conversation tool results when estimated tokens exceed the target model context window threshold or explicit request budget.
- Truncate large tool outputs into structured summaries (`[output compacted: ... bytes]`) while strictly preserving system instructions, initial task objectives, error tool results, and the most recent N turns.
- Use UTF-8 rune-boundary slicing for preview content to ensure clean JSON serialization without character splitting on CJK or emoji text.
