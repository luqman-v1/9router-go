### feat(skills): bring AI agent skills into repository and point SkillsView to 9router-go
- **In-Repo Skills**:
  - Add `skills/` directory containing complete, self-contained Agent skills for 9router capabilities (chat, embeddings, image, stt, tts, video, web-search, web-fetch).
  - Update default gateway port to `20130` across all skill setup guides.
  - Update `web/src/components/SkillsView.svelte` to point prompt copy links and GitHub URLs directly to `luqman-v1/9router-go` on branch `main`.
  - Add missing `9router-video` entry to the skills catalog view.
