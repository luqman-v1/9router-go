### Streaming: fill missing tool-call ids on the passthrough paths

- A streamed tool-call fragment that opens a new index without an id now gets one minted (and reused for later fragments of the same index, per stream) on the OpenAI-to-OpenAI passthrough, so strict OpenAI-compatible clients stop aborting the turn with "Expected 'id' to be a string".
- Wired on both passthrough lanes: the SSECopy relay and the chat handler's untranslated ScanStream path. Chunks without `tool_calls` keep their zero-copy relay.
