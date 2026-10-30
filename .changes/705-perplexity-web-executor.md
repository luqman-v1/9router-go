### fix(providers): give perplexity-web an executor

The registry entry carried a base URL, a cookie-shaped credential and a model
list, but no executor — so a request fell through to `ForwardOpenAI` and POSTed
an OpenAI body at the SSE endpoint, which answers neither.

The lane is not OpenAI-compatible in either direction. The request is a
`query_str` envelope carrying the search focus, the model preference and the
backend conversation id; the answer is Perplexity's own JSON event stream whose
content lives in markdown blocks that repeat cumulatively, so the reader has to
emit only the new tail or the answer says everything twice.

Three details matter for a usable lane:

- the **search steps** the stream reports (`SEARCH_WEB`, `READ_RESULTS`, plan
  goals) become `reasoning_content`, which is what an OpenAI client displays as
  thinking;
- the **citation markers** `[1]`, the `<grok:…>` tool tags and the `<response>`
  wrapper are stripped — they are Perplexity's own scaffolding, not answer text;
- a **follow-up** continues the same backend thread by sending only the new
  question, keyed on an FNV-1a hash of the history. Resending the whole
  conversation forks it.

An expired session answers 401 with a message that says which cookie to
re-paste, because that is the only recovery the operator has.
