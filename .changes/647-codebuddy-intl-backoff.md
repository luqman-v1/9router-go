### CodeBuddy Intl: back off blocked connections

- Provider-scoped error rules for codebuddy-intl: HTTP 400 "request illegal" / "conversation id missing" are account/IP blocks and now take the existing exponential backoff with its ceiling, instead of falling through to the request-scoped 4xx path that set no cooldown and never moved the backoff level.
- The combo and failover paths pass the member's canonical provider id into error classification, so `cbai` aliases resolve; the same texts stay request-scoped for every other provider.
