### Fixes

- **proxy**: Recognize `delta.reasoning` in SSE stream chunk parsing alongside `delta.reasoning_content` so open-weights reasoning models (e.g. SGLang/Qwen3.8 emitting `delta.reasoning`) are treated as valid non-empty streaming content and not dropped.
