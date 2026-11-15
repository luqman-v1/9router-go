### test(thinking): add regression guard for reasoning.effort nesting on OpenAI-compatible /responses nodes

- Adds table-driven regression test `TestApplyOpenAIResponsesOnOpenAICompatibleNode` pinning `reasoning.effort` nesting and top-level `reasoning_effort` omission for OpenAI-compatible nodes on the Responses wire.
- Aligns with merged upstream `open-sse/translator/concerns/thinkingUnified.js:138-139,251`.
