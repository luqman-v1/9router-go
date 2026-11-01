### fix(translator): auto-heal tool argument aliases and strip extraneous fields

Non-Claude models (e.g. OpenAI Codex, GPT, DeepSeek) frequently produce parameter aliases or hallucinate extra metadata when calling tools defined with strict Claude Code schemas:
- `Read`: `path`, `filename`, `filePath` instead of `file_path`.
- `Write`: `path`, `filename` instead of `file_path`; `contents`, `text`, `body`, `data` instead of `content`.
- `Edit`: `old_text`, `old_str` instead of `old_string`; `new_text`, `new_str` instead of `new_string`; `replaceAll` instead of `replace_all`.
- `Grep` / `Glob`: `query`, `regex`, `search` instead of `pattern`; `dir`, `directory`, `folder` instead of `path`.
- Hallucinated fields such as `description`, `title`, `comment`, `explanation`, and `thought` placed inside tool inputs.

Fixes applied:
- Normalize parameter aliases for `Read`, `Write`, `Edit`, and `Grep`/`Glob` in `sanitizeToolArgs`.
- Strip hallucinated metadata fields inside tool inputs to comply with strict schemas.
