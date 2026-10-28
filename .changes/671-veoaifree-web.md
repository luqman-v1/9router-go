---
"9router @ 2026-10-25": veoaifree.com free image + video provider
---

Added the `veoaifree-web` provider (alias `veo-free`): the veoaifree.com free generator, a WordPress AJAX API with no credentials (rate limited to ~6 requests/hour per IP). Video models `veo` and `seedance` submit a generation, poll for results and download the mp4 artifact; the `nano-banana` image model maps the generator's URL/base64 answers onto the OpenAI images response. Artifact downloads are https-only, refuse private-range hosts on every redirect hop, and verify the mp4 magic.
