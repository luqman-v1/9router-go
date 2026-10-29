### perf(executor): stream mislabelled SSE live, and transport hygiene from the latency sweep

The generic OpenAI executor (every `openai-compatible-*` custom node) and
CodeBuddy classified a streaming request whose upstream mislabels the body
with a non-event-stream Content-Type by reading the whole body first — a
provider that streams SSE under `application/json` paid for the entire
generation before its first byte. A bounded head sniff decides instead: the
sniffed head plus the live body stream frame by frame, and only a genuine
document is read whole.

Also: the unregistered `executor.ForwardGemini` (zero callers, buffered the
whole response before streaming) is deleted — its stream helpers stay for
`ForwardOpencodeGo`'s gemini route — and the MITM proxy now shares one
`http.Client` instead of building a fresh transport per intercepted request.
