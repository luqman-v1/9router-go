### docs(ssrf): correct the dial-guard record left by #1171

- The dial-time socket guard is skipped while `ALLOW_PRIVATE_NETWORKING` is on (the default), so it refuses no address class on any dial. The provider-node write-gate comments, the create-gate test comment, and the provider-share-url spec no longer describe it as a backstop.
- `.env.example` documents `ALLOW_PRIVATE_NETWORKING`, the strict opt-out.
