### fix(model test ping): sanitize HTML error pages into readable dashboard chips

- probeErrorText now detects HTML error bodies and strips them to plain text instead of rendering raw '<!DOCTYPE html>' in UI chips.
- Cloudflare WAF / challenge pages ('cloudflare' + 'attention required') summarize as 'Cloudflare WAF challenge (Attention Required!)'.
- Otherwise the <title> text is used when present; remaining tags are removed before truncation at probeTextLimit (240 chars).
- Unit tests cover 429 Cloudflare, 502 with title, bare HTML, truncation, and pass-through of non-HTML error bodies.
