### Fixed

- Ensure the `[DONE]` SSE sentinel is always its own event. When an upstream terminal chunk arrives without a blank-line separator, the gateway now inserts one before the sentinel so OpenAI clients do not join both data lines into one payload and fail with `JSONDecodeError: Extra data`.
