### Bug Fixes

- Stop advertising `br, zstd` in `Accept-Encoding` headers across DeepSeek Web executor, attest, and Grok dashboard validation to prevent receiving compressed payloads the runtime cannot decode (upstream `decolua/9router#5539` commit `97b2c545` parity).
