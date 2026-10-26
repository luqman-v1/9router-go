### feat(prompt-injection): add Prompt Injection Pro feature (advanced-only)

- New `promptInjections` and `promptInjectionTemplates` tables in SQLite.
- Dashboard API: `/api/prompt-injection/*` for global, per-key, per-model, and per-key-model injections.
- Chat pipeline applies stacked injections after jailbreak, ordered global → key → model → key+model.
- Two modes: single system prompt prepend, or scripted chat history insertion.
- Template library for reusable injection content.
- Backend gated by `settings.promptInjectionEnabled` (default off).
- Frontend: new `PromptInjectionView` sidebar entry, hidden by default, revealed in the Advanced preset.
- Profile Settings Advanced block exposes a toggle to enable/disable the feature.
