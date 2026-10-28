### Upstream translation failure fails over instead of returning 200

When an upstream responds with JSON that cannot be translated to Claude format (such as an upstream error envelope or non-Message body), return 502 UpstreamError so combo/router fallback can try the next connection, rather than serving an untranslatable body under HTTP 200.
