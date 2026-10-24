### Ollama: System One decision models (clef, clef-flash)

- Ollama Cloud and Ollama Local support System One: `clef`, `clef-flash` (and `clef:cloud` on the cloud lane) join the catalog as systemone-kind models, with `/v1/systemone` endpoints configured for both providers.
- Saved host overrides join the registry path without doubling a `/v1` or `/v1/systemone` the host already includes (upstream `buildSystemoneUrl`); a chat-completions override keeps swapping to the systemone lane, and providers without a path keep their `/systemone` suffix.
- The web picker lists the three ids as systemone-kind rows.
