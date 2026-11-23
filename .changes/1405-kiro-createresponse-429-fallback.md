### feat(kiro): add CreateResponse (/v1/responses) fallback on 429 USER_REQUEST_RATE_EXCEEDED

Port upstream decolua/9router#0957919c6 (#5631): when the legacy Kiro generateAssistantResponse endpoint returns 429 USER_REQUEST_RATE_EXCEEDED, replay the same turn against POST runtime.<region>.kiro.dev/v1/responses, converting conversationState and streaming chunks to OpenAI SSE. Region is validated against KIRO_RUNTIME_REGIONS; uncovered or injected values keep us-east-1.
