### fix(models): resolve model publishing, prefix matching, and unpublishing bugs

Fix model publishing, unpublishing, and prefix matching across catalog endpoints:
- Persist deleted models to `disabledModels` and invalidate live catalog cache in `HandleDeleteProviderModels` and `HandleDeleteCustomModel` so compatible live nodes stop re-publishing deleted models.
- Enforce strict `enabledModels` pinning in `appendConnectionModels` to prevent unpinned `customModels` and legacy `modelAliases` from bypassing the connection allowlist.
- Support bidirectional prefix matching in `disabledModelIndex` and `isDisabled` so prefixed entries (e.g. `nvidia/...`, `cl/...`) match stripped candidates.
- Add bare entries in `ProviderModelKinds["nvidia"]` and dual-lookup support in `GetProviderModelKind` to prevent non-LLM models (e.g. `parakeet-ctc-1.1b-asr` STT) from publishing as LLMs.
