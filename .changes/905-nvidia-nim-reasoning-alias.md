<<<<<<< HEAD
### Normalize NVIDIA NIM assistant reasoning aliases before forwarding

NVIDIA NIM treats `reasoning` and `reasoning_content` as aliases for one field, so a client that sends both on an assistant turn gets a duplicate-field 400. Assistant `reasoning` is now renamed to `reasoning_content` before the request leaves the gateway (upstream v0.5.112, `ced0cf6`, upstream PR #5247).

- The rename only fills a canonical field that is absent, null, or whitespace, so a non-blank `reasoning_content` the client already sent always wins — the alias is a fallback, never an override.
- `reasoning` is deleted either way, which is what removes the duplicate.
- Only `role: "assistant"` messages are touched; that is the only place clients replay reasoning.

Verified: `nvidia_test.go` covers both branches (alias fills a blank canonical field; a populated canonical field survives and the alias is dropped), and `wire_body_determinism_test.go` pins that the rewrite marshals stably, so the request prefix stays byte-stable across turns.
=======
### Normalize NVIDIA NIM assistant reasoning aliases before forwarding

NVIDIA NIM treats `reasoning` and `reasoning_content` as aliases for one field, so a client that sends both on an assistant turn gets a duplicate-field 400. Assistant `reasoning` is now renamed to `reasoning_content` before the request leaves the gateway (upstream v0.5.112, `ced0cf6`, upstream PR #5247).

- The rename only fills a canonical field that is absent, null, or whitespace, so a non-blank `reasoning_content` the client already sent always wins — the alias is a fallback, never an override.
- `reasoning` is deleted either way, which is what removes the duplicate.
- Only `role: "assistant"` messages are touched; that is the only place clients replay reasoning.

Verified: `nvidia_test.go` covers both branches (alias fills a blank canonical field; a populated canonical field survives and the alias is dropped), and `wire_body_determinism_test.go` pins that the rewrite marshals stably, so the request prefix stays byte-stable across turns.
>>>>>>> 1b5688b (docs(changes): fragments for the Nace and NVIDIA NIM ports)
