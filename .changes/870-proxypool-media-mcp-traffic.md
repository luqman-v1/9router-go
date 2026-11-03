### fix(proxy-pool): route media and MCP traffic through the configured pool

- In media forwarders (Antigravity image generation and Grok web media) and MCP forwarders, route outbound requests through the connection's assigned proxy pool via `GetClientForConnection` (porting upstream `decolua/9router` PR #5370).
- Prevents media and MCP requests from bypassing configured proxy pools.
