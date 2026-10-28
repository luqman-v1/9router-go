### Match Node's default User-Agent and Accept headers on OpenAI requests

ForwardOpenAI now defaults to `User-Agent: node` and `Accept: */*` to match Node's `undici` global fetch behavior, unless explicitly overridden by provider StaticHeaders. This prevents custom providers behind strict WAFs (such as ZenMux) from rejecting Go requests with 500 HTML error pages.
