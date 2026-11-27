# Autoclaw Provider — Full Execution Plan (one-time login)

Goal: land `autoclaw` as a first-class 9router-go provider whose only durable credential is a `refresh_token|device_id|app_key` triple (shipped change: the desktop client's signing secret is operator-supplied, so no third-party secret lives in the tree — see PR #1506). One login anywhere (AutoClaw desktop app, glmproxy, or autoclawpi console), then the gateway refreshes its own access tokens forever via `/userapi/v1/refresh`. No DeepSeek connection, no real credentials, no network, is needed to prove the code.

## Non-goals (explicitly out of scope)

- Browser OAuth dance (portable loopback + Aliyun captcha proof — a separate Phase B)
- Per-account round-robin failover inside the gateway (the operator runs one account; multiple accounts are multiple connections)
- Token WAF auto-retry across accounts inside the gateway (that is the proxy's job; our one account is our exposure)
- Any model rewriting into sibling providers (§3 strict isolation — Autoclaw models answer only as `autoclaw/<id>`)

## 1. Inventory (files to touch)

| File | Change | Kind |
|---|---|---|
| internal/providers/provider_registry.go | Add ProviderMeta spec for autoclaw | data |
| internal/providers/providers.go | Add KnownProviders[autoclaw] config | data |
| internal/providers/capabilities.go | Add providerCapabilities[autoclaw] model windows | data |
| internal/providers/registry_models.go | Add static model id list | data |
| internal/providers/aliases.go | Add ac -> autoclaw | data |
| internal/proxy/oauth/autoclaw.go | Register autoclaw refresher hitting /userapi/v1/refresh | code |
| internal/proxy/executor/autoclaw.go | Executor: MD5-signed headers, X-Request-Model, force stream | code |
| internal/proxy/executor/init.go | Register autoclaw factory | code |
| web/src/lib/models.ts | Add autoclaw block + alias entry | frontend data |
| web/public/providers/autoclaw.png | 32x32 icon, <=60 KB | asset |
| internal/providers/autoclaw_test.go | Table-driven signer/header test (offline) | test |
| internal/providers/autoclaw_refresh_test.go | Refresh round-trip against httptest fake | test |
| internal/proxy/executor/autoclaw_test.go | Executor wire test (httptest upstream) | test |
| .changes/1485-autoclaw-provider.md | Changelog fragment (never CHANGELOG.md) | docs |
| wiki/providers.md | Add Autoclaw row | docs |

## 2. Wire contract

Base: https://autoglm-api.autoglm.ai
Chat: POST /autoclaw-proxy/proxy/autoclaw/chat/completions
Refresh: POST /userapi/v1/refresh body {"source_id":"autoclaw","device_id":"<did>","refresh_token":"<rt>"} -> {"code":0,"data":{"access_token":"NEW","refresh_token":"SAME"}}

Signing headers (app build 1.18.5): x-auth-appid: 100003, x-auth-timestamp: <unix>, x-auth-sign: md5("100003&<ts>&<operator-supplied app_key>"), x-product: autoclaw, x-version: 1.18.5, x-tm: win, x-client-type: pc, x-channel: official, x-lang: zh-CN, x-trace-id: uuid, authorization: Bearer <access_token>, X-Request-Model: <route id> (model lives ONLY in this header; body model ignored), Content-Type: application/json.

Durable credential: refresh_token + device_id. Access token lives ~20h; background refresher rotates it. Only refresh_token|device_id survives a restart.

## 3. Steps

### Phase A — data layer

A1. provider_registry.go: add autoclaw ProviderMeta (CategoryAPIKey, Alias ac, Icon cloud, ServiceKinds [llm], Notice APIKeyURL https://autoclaw.z.ai, no ModelsFetcher — feed needs auth).
A2. providers.go KnownProviders[autoclaw]: BaseURL https://autoglm-api.autoglm.ai, AuthHeader Authorization, AuthScheme bearer, Format openai. MD5 sign is per-request (not StaticHeader).
A3. capabilities.go providerCapabilities[autoclaw]: zai_auto, zai_auto-fast, zai_glm-5-turbo (ctx 200k), zaicoding_glm-5.3, zai_glm-5.3-flash, tdpsk_deepseek-v4-pro, tdpsk_deepseek-v4-flash (ctx 1M, maxout 131k/393k per community-proxy catalog).
A4. registry_models.go: add the 7 model ids.
A5. aliases.go: add "ac": "autoclaw". Check registry_aliases.go.
A6. web/src/lib/models.ts: PROVIDER_ID_TO_ALIAS[autoclaw]=autoclaw + BUILTIN_MODELS_BY_PROVIDER block.
A7. web/public/providers/autoclaw.png <= 60 KB, <= 512 px edge.

### Phase B — code layer

B1. internal/proxy/oauth/autoclaw.go: Register(autoclaw, refreshAutoclaw). Hits /userapi/v1/refresh with source_id/device_id/refresh_token. Returns TokenResult.
B2. internal/proxy/executor/autoclaw.go: ForwardAutoclaw builds MD5-signed headers per request, sets X-Request-Model: req.ModelName, forces stream:true, posts to chat URL, streams SSE via io.Copy.
B3. internal/proxy/executor/init.go: Register("autoclaw", ...) alphabetically.
B4. Connection apiKey field stores refresh_token|device_id; executor splits at request time. Verify internal/proxy/oauth/background.go forwards conn.ProviderSpecificData into Params.ProviderSpecificData; patch if missing. Refresh writes access token into connData.AccessToken via oauth.BuildConnectionUpdate + SyncMirroredAPIKey pattern.

### Phase C — tests (offline, no credentials)

C1. internal/providers/autoclaw_test.go: signer produces exact x-auth-sign for fixed timestamp; X-Request-Model present; authorization Bearer present/absent by key.
C2. internal/providers/autoclaw_refresh_test.go: httptest fake returns {"code":0,"data":{"access_token":"at-2","refresh_token":"rt-2"}}; assert body/headers and tokens returned.
C3. internal/proxy/executor/autoclaw_test.go: httptest upstream sees X-Request-Model: zai_glm-5.3-flash, stream:true forced, response streamed.
C4. catalog_sweep_test.go: add autoclaw rows to no-floor sweep.

### Phase D — gates

D1. go build ./..., go vet ./..., go test ./internal/providers/... ./internal/proxy/... ./internal/proxy/oauth/...
D2. go test -tags=integration ./internal/integration/
D3. Portability loop: all CROSS_TARGETS with CGO_ENABLED=0.
D4. make web-build; cd web && bun test && bun run build && du -sh dist.

## 4. Commits (conventional style feat(scope): imperative)

1. feat(providers): add Autoclaw registry spec, config, capabilities, models, alias
2. feat(oauth): autoclaw refresh-token rotation
3. feat(proxy/executor): Autoclaw signed-request executor
4. feat(web): Autoclaw catalog entry and artwork
5. test: Autoclaw offline tests
6. docs: Autoclaw changelog fragment + wiki row

Commit body: what changed, why, how verified (offline tests named). No Closes # unless an issue exists.

## 5. Changelog fragment

Create .changes/1485-autoclaw-provider.md (placeholder number; rename when PR number known). Format: ### heading + bullets as under ## [Unreleased]. Never edit CHANGELOG.md.

## 6. PR procedure

1. git fetch origin; confirm clean origin/main
2. git worktree add ../9router-go-autoclaw -b feat/autoclaw-provider origin/main
3. Apply Steps A-D in that worktree, committing per section 4
4. git merge-tree --write-tree origin/main <branch> -> exit 0
5. go build ./... && go vet ./... && go test ./...
6. cd web && bun test && bun run build && du -sh dist
7. gh pr create --repo lw-xiong/9router-go --base main --head feat/autoclaw-provider --title "feat(providers): add Autoclaw provider with one-time login" --body "summary + wire contract + offline verification + github-fork-pr-fixes checklist"
8. If CI red -> github-fork-pr-fixes skill to repair

## 7. Binding rules (from AGENTS.md / CLAUDE.md)

- §1 parity protocol: original provider, not a port — merged-only rule does not apply to code we write. Never claim upstream parity.
- §3 strict provider isolation: Autoclaw models resolve ONLY under autoclaw/<id> and ac/<id>. Never alias into zai, qwencloud, opencode.
- §3.E capability gate: static windows pinned from community-proxy catalog; sweep test pins them; no floor values.
- §4.G small hardware: stream SSE via io.Copy, no body buffering.
- §5 testing: testing.T only, table-driven, httptest fakes, no live network in unit tests.
- §7.6 changelog: fragment file only, never edit CHANGELOG.md.
- §7 branch/PR: target main only, branch from clean origin/main, fragment <pr>-<slug>.md.
- §4.F dependencies: no new go.mod dependencies (md5 + stdlib only; crypto/rand + hex instead of uuid unless uuid already in go.mod).
- Frontend: no Svelte changes (models.ts is data); bun test and bun run build pass; artwork <= 60 KB.

## 8. Offline verification

| Check | Command | Expected |
|---|---|---|
| signer determinism | go test -run TestAutoclawSigner -v ./internal/providers/ | x-auth-sign = md5("100003&<ts>&38d2...") exact per row |
| refresh round trip | go test -run TestRefreshAutoclaw -v ./internal/proxy/oauth/ | httptest fake returns at-2/rt-2; body/headers match |
| executor wire | go test -run TestForwardAutoclaw -v ./internal/proxy/executor/ | upstream sees X-Request-Model, stream:true, no model body mutation |
| catalog parity | go test ./internal/providers/ | sweep includes autoclaw, no floor hits |
| frontend | cd web && bun test | providers.test.ts passes (every catalog id + artwork present) |
| build | go build ./... && make web-build | binary + dist clean |
| portability | CROSS_TARGETS loop CGO_ENABLED=0 | all targets build |

## 9. Not verified by this plan

- Live Autoclaw login: needs real refresh_token|device_id from operator (copy from AutoClaw app / glmproxy / autoclawpi console). Manual smoke test.
- APP_KEY/APP_ID constants: captured from desktop client build 1.18.5. Build bump -> 403s until re-dumped. Noted in fragment.
- X-Tm: linux refused by some accounts; win is the working fallback.

## 10. Definition of done

1. Six commits on feat/autoclaw-provider branched from clean origin/main.
2. go build/vet/test ./... green.
3. Integration suite green.
4. cd web && bun test && bun run build green; du -sh dist reported.
5. Portability loop green for every CROSS_TARGET.
6. PR opened targeting main with .changes/1485-autoclaw-provider.md in diff, CHANGELOG.md untouched.
7. Operator pastes refresh_token|device_id into a new Autoclaw connection and chat reaches upstream with X-Request-Model + fresh Bearer (manual, own credential).

Next action: read exact insertion points in provider_registry.go, providers.go, capabilities.go, registry_models.go, aliases.go so data entries land alphabetically and compile. Then write code.
