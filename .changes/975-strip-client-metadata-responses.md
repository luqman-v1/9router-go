### strip client_metadata on Responses passthrough

When a Responses request is passed through to an upstream Responses endpoint (or when relaying responses), `client_metadata` is stripped from the returned response body so downstream third-party OpenAI-compatible providers and strict schema validators do not reject the response with `additionalProperties "client_metadata" not allowed` (upstream decolua/9router v0.5.35).
