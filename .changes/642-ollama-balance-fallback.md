### Ollama Cloud: /api/balance quota fallback

- Fall back to `https://ollama.com/api/balance` when `/api/usage` limits are empty, since Ollama Cloud moved active quotas off `/api/usage`.
- Parse session (5h) and weekly (7d) remaining percentages with their reset timestamps, the USD allowance window (`period.until`), and purchased credits.
- Pass the balance metadata (`isCreditBalance`, `currency`, `unit`, `unlimited`) through to the dashboard quota table.
