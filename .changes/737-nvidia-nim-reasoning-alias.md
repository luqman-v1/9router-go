### Fixes
- **NVIDIA NIM**: Normalized assistant-message `reasoning` aliases to `reasoning_content` and removed `reasoning` before forwarding (upstream `decolua/9router#5247` parity, resolving #736), avoiding NIM's duplicate-field HTTP 400 error when clients send both.
