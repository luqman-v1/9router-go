### Fixed
- Preserve name-addressed native Responses function results without inventing a `call_id`. The Codex backend accepts these results by name; adding an ID makes it reject them with `No tool call found for function call output with call_id call_0`.
- Verified against merged decolua/9router master at `1855aa44473e88feac9d3f131b9c0c5fc9706b68`, which passes native input arrays through unchanged; regression coverage includes the body converter and production router in classic and Lite modes.
