### Added
- **Zai GLM Coding (dual-auth) & GLM China parity (upstream decolua/9router#068ce87d, #9dbdca0e)**:
  - Added ZCode CLI polling OAuth flow for `glm` (`Zai GLM Coding`): `glmStart` mints the poll token and browser authorize URL; `glmPoll` polls `https://zcode.z.ai/api/v1/oauth/cli/poll`, exchanges the Z.AI token for a platform business JWT at `https://api.z.ai/api/auth/z/login`, queries customer info/projects, and automatically creates/copies the coding-plan API key (`zcode-api-key`).
  - Stored minted coding-plan key in both `apiKey` and `accessToken` with `authMethod: "cli_poll"`, `zcodeJwtToken`, and `zaiBusinessToken`. No refresh grant upstream (expiry requires re-login, identical to official ZCode CLI).
  - Wired dual-auth buttons (`OAuth` and `API Key`) on the dashboard for `glm` (`authModes: ["oauth", "apikey"]`, category `oauth`).
  - Added GLM usage quota tracker (`fetchGLMUsage` and `parseGLMQuotaResponse`) supporting both `CREDIT_LIMIT` (Session 5h, Weekly 7d, custom Limit) and `TOKENS_LIMIT` for international (`https://api.z.ai/api/monitor/usage/quota/limit`) and China (`https://open.bigmodel.cn/api/monitor/usage/quota/limit`).
  - Preserved strict provider isolation: `glm` (international Zai GLM Coding) and `glm-cn` (GLM China) maintain distinct catalog entries and transport endpoints.
