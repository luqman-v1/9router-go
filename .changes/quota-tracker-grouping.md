### feat(quota): group the Quota Tracker rows by provider, scope models per account

- The tracker rendered every connection as one flat list. Rows are now folded into collapsible provider sections (icon, label, account count); the first provider opens by default and the rest start collapsed. The provider/account filters, sorting and pagination are unchanged.
- `groupConnectionsByProvider` keeps the caller's row order inside each section and the first-seen provider order, so a grouped list reads the same way as the flat one.
- Edit Connection now shows a Codex-only "Models this account may serve" list, written to `providerSpecificData.enabledModels`. The backend already skips an account whose list omits the requested model, so a free Codex account can be pinned to one model while a Plus account keeps the rest — no more 400 from picking the wrong account and no Plus quota spent on a model the free tier could serve.
