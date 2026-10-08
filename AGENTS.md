# 9router-go — Team Engineering & AI Collaboration Guidelines

This repository (`9router-go`) is the high-performance, native Golang implementation and companion of [**decolua/9router**](https://github.com/decolua/9router).

---

## 1. Upstream Source of Truth & Local Reference

Whenever you implement features, fix bugs, add providers, update routing logic, or verify API parity, **always treat the upstream project as the behavioral specification**.

- **Upstream Repository**: `https://github.com/decolua/9router`
- **Local Clone Path**: `/Users/luqmannul.hakim/htdocs/9router`

### Parity Protocol
1. **Always Inspect Local Upstream First**:
   - Before writing or modifying logic, inspect the corresponding code in `/Users/luqmannul.hakim/htdocs/9router`.
   - Check upstream git history, PRs, issues, and vitest unit tests in `/Users/luqmannul.hakim/htdocs/9router/tests/` to understand edge cases, headers, and protocol nuances.
2. **Behavioral Compatibility (100% Contract Match)**:
   - Request and response schemas for `/v1/*` (Chat Completions, Messages, Embeddings, Audio, Models).
   - Dashboard endpoints `/api/*` (Connections, Combos, ProxyPools, Settings, ProviderNodes, Usage, Auth).
   - Combo expansion, model fallback, account rotation, and strike-breaker quota handling.
   - SQLite database compatibility (`~/.9router/db/data.sqlite`).
3. **Changelog Tracking**:
   - When porting features or fixes, reference the upstream commit/issue/PR in `CHANGELOG.md` (e.g. `upstream decolua/9router#4197 parity`).

### Secondary Reference: OmniRoute (source of many feature requests)

Some feature issues in this repo are not parity requests against `decolua/9router` — they are **port requests sourced from OmniRoute**, cited by a file path inside that repo (e.g. issue #38 cites `src/lib/usage/codexResetCredits.ts`).

- **Reference Repository**: `https://github.com/diegosouzapw/OmniRoute` — TypeScript/Next.js, MIT, a *sibling* gateway project (not this repo's upstream). It vendors its own `open-sse/` tree under the `@omniroute/open-sse` package alias, so its provider/executor layout is recognizable but **not** the same as `decolua/9router`'s.
- **When an issue cites OmniRoute**, treat the cited file as the *feature specification*. Do not guess its contents and do not port from memory.

**OmniRoute Porting Protocol**
1. **Fetch the cited file first**, from raw: `https://raw.githubusercontent.com/diegosouzapw/OmniRoute/main/<path>`. Fetch the related UI component too (dashboard modals live under its `src/app/` tree) — the port must match the dashboard *behavior*, not just the API shape.
2. **Port the contract, not the code**: endpoints, request/response payloads, error status codes, and filter/sort rules. Never transliterate TypeScript into Go (see §4.A).
3. **Map by responsibility** — OmniRoute `src/lib/usage/*.ts` → `internal/handlers/dashboard/` + `web/src/api/client.ts`; `open-sse/executors/` → `internal/proxy/executor/`; `src/app/` components → `web/src/`.
4. **OmniRoute is secondary**. Where it conflicts with `decolua/9router`, the §1 parity rules win; record the deliberate divergence in `CHANGELOG.md`.
5. **Provider IDs are not portable verbatim.** OmniRoute has its own catalog. Map to `9router-go` provider IDs and obey §3 (strict provider isolation — no cross-provider aliasing or model hijacking).
6. **Preserve upstream error semantics.** OmniRoute surfaces typed error classes with explicit status codes (e.g. `409 no_credit`); port that distinction rather than collapsing every upstream failure into one generic error.

---

## 2. Architecture & Codebase Mapping

| Upstream Path (`/Users/luqmannul.hakim/htdocs/9router`) | 9router-go Path | Description & Role |
| :--- | :--- | :--- |
| `open-sse/translator/` | `internal/translator/` | Protocol format converters (OpenAI ↔ Claude Messages ↔ Gemini ↔ Antigravity). |
| `open-sse/executors/` | `internal/proxy/executor/`, `internal/proxy/` | Upstream provider callers, SSE stream adapters, tool ID repair, cloaking. |
| `open-sse/providers/registry/`, `open-sse/config/providerModels.js` | `internal/providers/` | Provider definitions (120+), model catalogs (`registry_models.go`), aliases (`aliases.go`), OAuth (`oauth.go`). |
| `open-sse/handlers/chatCore.js`, `src/sse/handlers/chat.js` | `internal/handlers/chat/` | Chat routing, combo fallback, fusion, resolution, quota handling. |
| `open-sse/rtk/` | `internal/tokensaver/` | Prompt token compression (RTK), Caveman, and Ponytail system prompts. |
| `src/app/api/` (Next.js API routes) | `internal/handlers/dashboard/`, `internal/handlers/` | Dashboard REST & SSE APIs (connections, combos, proxypools, usage, settings). |
| `src/lib/db/` | `internal/db/` | SQLite database layer (using pure-Go `modernc.org/sqlite`). |
| `src/app/` (Next.js React 19 Frontend) | `web/src/` | Dashboard UI: Built with **Svelte 5 + Vite 8**, embedded into the binary via `web/embed.go`. |
| `cli/` (npm launcher package) | `cmd/9router-go/main.go` | Single-binary CLI launcher and runtime daemon. |
| `tests/` (Vitest suites) | `*_test.go` | Go unit and integration test suites. |

---

## 3. Provider Independence & Anti-Hardcoding Principles (STRICTLY MANDATORY)

Every upstream provider in this repository (`antigravity`, `opencode`, `claude`, `codex`, `cline`, `freebuff`, `xai`, `gemini-cli`, `qoder`, etc.) is **strictly distinct, independent, and isolated**. You **MUST NEVER** merge, conflate, cross-alias, or hijack one provider into another.

### A. Strict Provider Isolation (No Cross-Hijacking)
1. **Never Confuse or Conflate Providers**:
   - `antigravity` is **NOT** `opencode`. They have completely separate upstream APIs, different authentication mechanisms (Google Cloud Code OAuth tokens vs OpenCode public tokens), and separate model catalogs.
   - A request targeted at `ag/<model>` or `antigravity/<model>` must **always and only** be routed to the Antigravity executor.
   - An OpenCode model (e.g. `space-bunny-free`, `muse-spark-*`, `big-pickle`, `*-free`) belongs exclusively to `opencode` (`oc/`). It must **never** be injected into, aliased to, or redirected from Antigravity.
2. **Zero Cross-Provider Aliasing**:
   - `ResolveProviderProxyPoolID` and connection lookups must only check genuine aliases (e.g. `ag` ↔ `antigravity`, `oc` ↔ `opencode`, `cline` ↔ `clinepass`). Never fall through from `antigravity` to `opencode` or vice versa.

### B. Ban on Hardcoded Model Rewrites
- **No Substring-Based Provider Switching**:
  - ❌ `if (provider == "antigravity" || provider == "ag") && strings.Contains(model, "muse-spark") { provider = "opencode" }` (Strictly forbidden).
  - ❌ `if isOpenCodeModel(model) { provider = "opencode" }` (Strictly forbidden).
- **Uniform Provider Handling**:
  - Every provider must handle all of its models uniformly ("seluruh provider disamakan").
  - Routing decisions must be driven by explicit catalog registration, model aliases in the database, custom provider node prefixes, or standard `provider/model` wire syntax—**never by ad-hoc string inspections**.

### C. General vs Custom Abstractions
- **Shared / General Abstractions**:
  - If a mechanism is general across providers (e.g. streaming SSE adapters, HTTP retry with direct fallback on proxy errors, token estimation, client session resolution), encapsulate it into an agnostic, generally named package or helper:
    - ✅ `proxy.NewFallbackTransport(...)`
    - ✅ `proxy.ForwardOpenAI(...)`
    - ✅ `handlerutil.ExtractSessionID(r)`
    - ✅ `translator.ConcealFingerprintTools(...)`
- **Provider-Specific Custom Logic**:
  - If a provider has unique protocol requirements, custom headers, or quirky payload envelopes, build a dedicated, self-contained executor or handler file:
    - ✅ `internal/translator/antigravity.go` (Google Cloud Code assist envelope)
    - ✅ `internal/proxy/executor/freebuff.go` (Freebuff multi-session & client_id cloaking)
    - ✅ `internal/proxy/executor/opencode.go` (OpenCode fingerprinting & Responses API transformation)
    - ✅ `internal/handlers/media/antigravity_image.go` (Antigravity image generation)
  - **Rule**: Provider-specific custom logic must stay strictly within that provider's execution path. It must **never** leak into generic routing, sibling provider paths, or universal middleware.

### D. Dashboard & UI Parity Discipline
- **Respect Registry Flags (`modelsFetcher`)**:
  - The "Suggested free models" UI section must strictly depend on the provider's `modelsFetcher` declared in the catalog.
  - Never add artificial `else if (providerId === '...')` blocks in `ProviderDetailView.svelte` to inject models from one provider into another.
  - Buttons like `Import from /models` must only appear for providers that genuinely support dynamic catalog discovery upstream (`cline`, `clinepass`, `qoder`, `qoder-cn`).

---

## 4. Golang Engineering & Optimization Principles
> **CORE MANDATE: Do NOT blindly transliterate JavaScript / TypeScript into Go!**  
> While the observable external behavior and API contract must match upstream 100%, the internal Go implementation must leverage Go's performance, strong typing, low memory footprint, and idiomatic concurrency.

### A. Performance & Zero-Allocation Streaming
- **Stream, Don't Buffer**:
  - Upstream JS often buffers entire response bodies or converts buffers to strings. In Go, stream SSE chunks directly using `io.Reader`, `io.Writer`, `bufio.Reader`, or `bufio.Scanner`.
  - Avoid buffering whole LLM streaming responses in memory unless modification/repair is strictly required.
- **Minimize Memory Allocations**:
  - Reuse byte slices and buffers with `bytes.Buffer` or `sync.Pool` in hot proxy forwarding paths.
  - Avoid unnecessary `[]byte(str)` and `string(bytes)` conversions in hot loops.
  - Use `json.RawMessage` to forward opaque/passthrough payload segments without unmarshaling into generic intermediate `map[string]any`.
- **HTTP Transport Hygiene**:
  - Reuse `*http.Client` instances with pooled `http.Transport` (`MaxIdleConnsPerHost`, keep-alives enabled, appropriate response header timeouts). Never instantiate an unconfigured `&http.Client{}` per request.

### B. Concurrency & Goroutine Safety
- **Context Propagation**:
  - Always accept and propagate `context.Context` down to database queries, HTTP requests, and background workers. Respect client disconnections (`r.Context()`) immediately to abort in-flight upstream LLM calls.
- **Prevent Goroutine Leaks**:
  - Every spawned goroutine must have a deterministic lifecycle tied to a context or channel exit condition.
- **Concurrency Control & Synchronization**:
  - Guard mutable shared state (token caches, strike counters, rate limiters, proxy pools) using unexported `sync.RWMutex` / `sync.Mutex` followed immediately by `defer mu.Unlock()`.
  - Never concurrently read/write Go maps without mutex protection.
  - Use `golang.org/x/sync/singleflight` to collapse duplicate concurrent requests for identical external resources (e.g. OAuth token refresh, remote catalog sync).

### C. Type Safety vs Dynamic JSON
- **Strong Typing Over `map[string]any`**:
  - Use strongly typed structs for known wire protocols (OpenAI Chat Completions, Anthropic Messages, Gemini GenerateContent, Antigravity envelopes).
  - Use unexported fields or typed wrappers instead of passing untyped maps across package boundaries.
  - Reserve `any` / `map[string]any` strictly for dynamic, provider-specific, or open-ended passthrough payloads.
- **Clean JSON Serialization**:
  - Use accurate struct tags (`json:"fieldName,omitempty"`). Omit zero-value fields when required by upstream provider specs.

### D. Code Style, Modularity & Cognitive Complexity
- **Cognitive Complexity**: Maintain SonarQube cognitive complexity $\le 15$ per function/method.
- **Guard Clauses**: Keep the happy path left-aligned using early returns. Avoid nested `if/else` ladders (3+ levels forbidden). Decompose branching into focused private helper functions.
- **File Size Discipline**: Target files $\le 300$ LoC (modularize files $>400$ LoC into focused files or subpackages).
- **Dependency Injection**: Use Uber Fx (`go.uber.org/fx`) in `internal/app/` for clean wiring. Usecases and handlers must never perform defensive `nil` checks on injected dependencies—fail fast during startup container initialization.
- **Functional Helpers**: Prefer [`github.com/samber/lo`](https://github.com/samber/lo) (`lo.Map`, `lo.Filter`, `lo.Contains`, `lo.Ternary`, `lo.ToPtr`) for clean slice operations instead of handwritten boilerplate loops.

### E. Error Handling
- **Wrap Errors with `%w`**: Always provide caller context: `fmt.Errorf("receiver.MethodName: %w", err)` or `fmt.Errorf("op: %w", err)`.
- **Error Inspection**: Use `errors.Is(err, target)` for sentinels and `errors.As(err, &target)` for custom types. Never rely on `strings.Contains(err.Error(), ...)`.
- **Zero Panic Policy**: No `panic()` in handlers, services, or proxy execution. Recover gracefully and emit appropriate HTTP/SSE error payloads.

---

## 5. Idiomatic Go Unit Testing Convention (`testing.T`)

All unit tests in this repository **MUST** use Go's standard library `testing` package (`testing.T`). Do **NOT** introduce heavy external BDD test frameworks (such as Ginkgo/Gomega or testify suite) to keep `go.mod` clean, compilation fast, and the test suite unified across all packages.

### A. Table-Driven Tests Pattern
For functions with multiple inputs, edge cases, or status transformations, always structure tests using table-driven tests:

```go
func TestResolveModelAlias(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "resolves known alias",
			input:    "cbcn",
			expected: "codebuddy-cn",
			wantErr:  false,
		},
		{
			name:     "canonical id passes through",
			input:    "openai",
			expected: "openai",
			wantErr:  false,
		},
		{
			name:     "unknown alias returns error",
			input:    "unknown-xyz",
			expected: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveModelAlias(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveModelAlias() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.expected {
				t.Errorf("ResolveModelAlias() = %q, want %q", got, tt.expected)
			}
		})
	}
}
```

### B. HTTP Handler Testing (`httptest`)
Use `net/http/httptest` (`httptest.NewRecorder()`, `httptest.NewRequest()`, and `httptest.NewServer()`) for testing endpoints, SSE streams, middleware, and reverse proxy forwarding:
- Verify HTTP response status codes: `if rec.Code != http.StatusOK { t.Fatalf(...) }`.
- Verify response headers (e.g. `Content-Type: text/event-stream`).
- Inspect response body or unmarshal JSON into typed structs rather than `map[string]any`.

### C. Testing Best Practices
1. **Meaningful Subtest Names**: Use clear `t.Run("describes behavior/condition", ...)` naming.
2. **Fail Fast vs Accumulate**:
   - Use `t.Fatalf()` when a failure invalidates subsequent checks (e.g. nil pointer, unexpected error, failed setup).
   - Use `t.Errorf()` when verifying independent fields of a struct.
3. **No External Network Dependencies in Unit Tests**:
   - Use `httptest.NewServer` or mock clients to simulate upstream LLM providers and OAuth endpoints.
   - Tag live/upstream-dependent network tests clearly (e.g. `live_e2e_test.go` or guard with env checks) so that standard unit tests pass completely offline.
4. **Deterministic & Isolated**:
   - Tests must clean up temporary database files or use in-memory SQLite (`:memory:`) where applicable.

### D. Feature Integration Tests (`internal/integration`, `integration` build tag)

Unit tests call handlers directly or mount a hand-built router, so they cannot see a regression in the wiring production actually uses. The suite in `internal/integration/` closes that gap: it boots `app.ProvideRouter` (the real middleware stack and route table) on a real HTTP listener against a temporary SQLite database, with every provider call intercepted by a fake `httptest` upstream seeded through `providerConnections.data.baseUrl`. `internal/integration/bootfx/` boots the full fx graph — `DatabaseModule` and `ServerModule` included — once, in its own test binary.

1. **Run them with `make test-integration`** (or `go test -tags=integration ./internal/integration/...`). The tag keeps `go test ./...` fast; the CI `integration` job runs the suite on every PR, so a regression in routing, auth, account rotation, or usage accounting fails on the PR that introduces it instead of on a user's next request.
2. **Use the harness helpers, never raw setup.** `newProviderEnv(t)` is the common fixture: an `Env` with one DeepSeek connection aimed at a fake upstream. `newEnv(t)` is the bare gateway. Both open the database, apply the real schema bootstrap, and seed a client API key; `newEnv` also pins the token savers off so upstream payloads stay comparable byte for byte. Add connections with `env.AddConnection(t, ...)` and combos with `env.AddCombo(t, ...)`.
3. **Every helper takes the running test as its first argument.** `Env` holds no `*testing.T`: capturing the parent would make a failing `t.Run` call `FailNow` on the parent from the subtest's goroutine, which the `testing` package reports as "subtest may have called FailNow on a parent test" and attributes to the wrong line.
4. **Never let a test reach the network.** A provider must be an `env.NewUpstream(t, ...)` fake, and the fake must assert what the gateway sent (`upstream.Last(t).Model(t)`, `.Header`). `AddConnection` reads the stored row back and fails when the fake URL did not persist, because an empty `data.baseUrl` falls through to the real provider URL from `providers.KnownProviders`. No real provider credential may be required for the suite to pass.
5. **Do not boot `app.DatabaseModule` outside `bootfx/`, and boot it only once there.** `db.InitGlobalDatabase` is a process-wide `sync.Once` and `fxApp.Stop()` closes that handle for good, so a second boot in the same binary would reuse a closed database and an already-cancelled shutdown context.
6. **Assert observable behaviour, not wiring.** A useful case pins a contract a client depends on (the status a client sees, the model the provider receives, the row written to `usageHistory`). Testing that a route exists, or that a handler forwards to itself, proves nothing. When a subtest looks for one row in a listed collection, select it by id and fail when it is absent — a loop that only errors on a match passes vacuously when the list comes back empty.

---

## 6. Frontend Engineering & Svelte 5 Standards (`web/`)

The dashboard UI in `web/` is built with **Svelte 5 + Vite 8 + TypeScript + Tailwind CSS**, compiled to `web/dist`, and embedded directly into the Go binary via `embed.go` (`//go:embed all:dist`).

> **Upstream Parity Notice**: While upstream `9router` uses Next.js 16 and React 19 (`useState`, `useEffect`, JSX), `9router-go` uses **Svelte 5**. All UI features, views, and modals from upstream must be ported to idiomatic Svelte 5.

### A. Svelte 5 Runes (STRICTLY MANDATORY)
Always write modern Svelte 5 code using Runes. **Never use legacy Svelte 3/4 syntax.**

1. **Component Props (`$props`)**:
   - Define an explicit TypeScript `interface Props { ... }` or inline type.
   - Use `$props()` to destructure with defaults:
     ```svelte
     <script lang="ts">
       interface Props {
         title: string
         isOpen?: boolean
         count?: number
         onClose?: () => void
       }

       let {
         title,
         isOpen = false,
         count = 0,
         onClose = () => {},
       }: Props = $props()
     </script>
     ```
   - Use `$bindable()` for two-way bound props: `let { activeTab = $bindable('endpoint') } = $props()`.
   - ❌ **Forbidden**: `export let title = ''` (legacy Svelte 3/4 syntax).

2. **Reactivity (`$state`, `$derived`, `$effect`)**:
   - **State**: Use `$state(initialValue)` for local reactive variables: `let loading = $state(false)`.
   - **Derived Values**: Use `$derived(expression)` instead of legacy `$:` labels:
     ```svelte
     let filteredItems = $derived(
       items.filter(i => i.name.toLowerCase().includes(search.toLowerCase()))
     )
     ```
   - **Side Effects**: Use `$effect(() => { ... })` instead of legacy `$:` reactive statements:
     ```svelte
     $effect(() => {
       if (isOpen) {
         loadData()
       }
     })
     ```

3. **Snippets over Slots (`Snippet` & `{@render}`)**:
   - Use Svelte 5 snippets for slot content:
     ```svelte
     <script lang="ts">
       import type { Snippet } from 'svelte'
       let { children, footer }: { children?: Snippet; footer?: Snippet } = $props()
     </script>

     <div class="content">{@render children?.()}</div>
     {#if footer}<div class="footer">{@render footer()}</div>{/if}
     ```
   - ❌ **Forbidden**: `<slot />` or `<slot name="footer" />`.

4. **Modern Event Handlers**:
   - Use lowercase HTML event attributes: `onclick={handleClick}`, `onkeydown={handleKeyDown}`, `onchange={handleChange}`.
   - ❌ **Forbidden**: `on:click={handleClick}` (legacy Svelte 3/4 syntax).

---

### B. Upstream React 19 $\rightarrow$ Svelte 5 Porting Matrix

| Upstream Next.js / React 19 | 9router-go Svelte 5 Equivalent |
| :--- | :--- |
| `useState(initial)` | `let val = $state(initial)` |
| `useMemo(() => compute(x), [x])` | `let computed = $derived(compute(x))` |
| `useEffect(() => { ... }, [deps])` | `$effect(() => { ... })` |
| `const ref = useRef(null)` | `let el = $state<HTMLElement \| null>(null); <div bind:this={el}>` |
| `{condition && <Component />}` | `{#if condition}<Component />{/if}` |
| `{items.map(item => <Row key={item.id} />)}` | `{#each items as item (item.id)}<Row {item} />{/each}` (always key with `(item.id)`) |
| `value={val} onChange={e => setVal(e.target.value)}` | `bind:value={val}` |
| `props.children` | `{@render children?.()}` |

---

### C. Design Tokens & Styling (Tailwind CSS)

Always use the established design tokens defined in `web/src/index.css` for consistent appearance across light and dark themes:

- **Brand (Terracotta)**: `bg-brand-500`, `text-brand-500`, `bg-primary`, `text-primary`, `hover:bg-primary-hover` (`#e56a4a`).
- **Surfaces**: `bg-surface`, `bg-surface-2`, `bg-surface-3`, `bg-sidebar`.
- **Borders**: `border-border`, `border-border-subtle`.
- **Text**: `text-text-main`, `text-text-muted`, `text-text-subtle`.
- **Radii**: `rounded-[10px]`, `rounded-[14px]`, `rounded-2xl`.
- **Shadows**: `shadow-[var(--shadow-warm)]`, `shadow-[var(--shadow-elev)]`.
- **Icons**:
  - Google Material Symbols: `<span class="material-symbols-outlined text-[18px]">icon_name</span>`
  - Lucide Svelte: `import { IconName } from 'lucide-svelte'; <IconName size={18} />`

---

### D. API Integration & Routing

1. **Centralized API Client**:
   - All HTTP requests to backend endpoints must go through `web/src/api/client.ts` (`api.getConnections()`, `api.getCombos()`, `api.getSystemVersion()`, etc.).
   - Define strongly typed TypeScript interfaces in `client.ts` or view-specific `types.ts`.
2. **Single Page Routing**:
   - Navigation tabs are managed via `web/src/lib/router.ts` (`ActiveTab` and `TAB_ROUTES`).
   - Modal dialogs should handle `Escape` key and click-outside backdrop dismissals.


### E. Svelte Type-Checking (`make vet-svelte`)

`tsconfig.app.json` includes `src`, but **`tsc` cannot parse `.svelte` files at all** — every Svelte `<script lang="ts">` block in this repo went untyped. The consequence is concrete: `bun run build`, `oxlint`, and `vite build` all report success for a component that calls a function it never imported, and the failure only appears when the code runs — a `ReferenceError` in the browser, after the button was clicked. That shipped in `QuotaTrackerView.svelte:300` (commit `3525284c` renamed the mint to `newResetCreditIdempotencyKey` and updated only the import) and was found by a user, not by CI.

`svelte-check` closes the hole but reports ~92 pre-existing type errors, so it runs as a **ratchet** (`web/scripts/svelte-check-ratchet.ts`) rather than a hard gate:

1. **Unresolved identifiers fail the build outright** — `Cannot find name`, `Cannot find module`, missing exports. These are the fatal class: each one is a `ReferenceError` waiting for a user click, and unlike a stylistic type mismatch they take a whole feature down.
2. **Total error count is pinned** in `web/scripts/svelte-check-baseline.json` and may only shrink. Every new type error fails the build.

Run it on any frontend change. When the count legitimately drops:

```bash
cd web && bun run ratchet:svelte -- --update
```

Never widen the baseline to make a failure disappear — fix the error, or split the PR and land the debt reduction on its own. The `--tsconfig tsconfig.app.json` flag is mandatory: the default resolves `tsconfig.json`, which has `files: []`, and silently checks **zero** components.

---

## 7. Upstream Sync Workflow (Step-by-Step)

When tasked with syncing a feature, bugfix, or provider from upstream:

1. **Investigate Upstream**:
   ```bash
   # Check recent upstream commits, tags, or file changes
   git -C /Users/luqmannul.hakim/htdocs/9router log -n 5 --oneline
   # Inspect specific upstream file or test
   cat /Users/luqmannul.hakim/htdocs/9router/open-sse/...
   ```
2. **Locate Equivalent Component**:
   - Backend: Use the **Architecture & Codebase Mapping** table in Section 2 (`internal/...`).
   - Frontend: Identify matching page/component in `web/src/components/...`.
3. **Design for Go & Svelte 5**:
   - Backend: Plan Go-specific optimizations (streaming buffers, strong types, mutexes, singleflight).
   - Frontend: Convert React logic into Svelte 5 runes (`$state`, `$derived`, `$effect`, `bind:value`).
4. **Implement & Test**:
   - Backend: Write code in `internal/...` and unit tests in `*_test.go` (table-driven tests using `testing.T`).
   - Frontend: Write/update components in `web/src/`, verify `cd web && bun run build`, **and** run `make vet-svelte`.
     `tsc -b` cannot read `.svelte` files, so a Svelte script block calling a function it never imported compiles, lints and bundles clean, then throws a `ReferenceError` the first time a user clicks it (issue #130). `make vet-svelte` is the only gate that sees that class of bug.
5. **Verify**:
   ```bash
   rtk go test ./...
   make build
   make vet-svelte   # svelte-check ratchet — see §6.E
   ```
6. **Update Changelog**:
   - Add entry to `CHANGELOG.md` under `[Unreleased]` detailing the parity sync.

7. **Branch & PR Targeting (`main` ONLY — MANDATORY)**:
   - **Every pull request in this repository targets `main`.** Never open a PR
     into `dev`, and never merge into `dev`. This is the operator's standing
     decision, not a convention to weigh per-PR.
   - `dev` is the **integration** branch: work accumulates there and is merged
     onward into `main` through a single `dev` → `main` PR. It is not a merge
     target for feature branches.
   - **Branch from clean `origin/main`**, never from `dev`, a release tag, or
     another feature branch:
     ```bash
     rtk git fetch origin
     rtk git worktree add ../9router-go-<slug> -b fix/<slug> origin/main
     ```
   - Set the base explicitly, and pass the repo when creating the PR so a
     mismatched default cannot silently redirect it:
     ```bash
     gh pr create --repo luqman-v1/9router-go --base main --head fix/<slug> \
       --title "..." --body "..."
     ```
   - **A file that does not exist on `main` blocks the PR, not the branch.** If
     the code you need to fix only exists on `dev`, the PR targeting `main`
     cannot carry that fix: pointing at `main` drags every `dev` commit into
     the diff (45 commits / 169 files on the fix for the Cache Analytics 503)
     and leaves the PR `CONFLICTING`. Split instead — land the part that applies
     to `main`, and hold the rest on a branch until the `dev` → `main` PR
     lands, after which it is a one-commit cherry-pick. Say so explicitly in
     the PR body rather than quietly widening the base.
   - **Check the diff size before opening.** `gh pr view <n> --json changedFiles,
     additions` must show only the files you touched. If it shows more than
     that, the branch was cut from the wrong place — see the previous rule.
   - Resolve `CHANGELOG.md` `[Unreleased]` conflicts **additively**: two entries
     side by side, newest information appended, never one dropped.

---

## 8. Daily Commands Cheat-Sheet

```bash
# Run unit tests
rtk go test ./...
rtk go test ./internal/providers/... -v

# Run the feature integration suite (real router, real DB, fake upstreams)
make test-integration
make vet-integration

# Build frontend SPA (Svelte 5 / Vite 8 via Bun)
make web-build
# Or directly inside web/
cd web && bun run build

# Svelte type-check ratchet (blocks unresolved identifiers, pins type debt)
make vet-svelte

# Build binary (automatically builds web SPA into web/dist first)
make build

# Run dev server with live go run (PORT=20130 default)
make dev

# Git operations (always prefix with rtk)
rtk git status
rtk git diff
```
