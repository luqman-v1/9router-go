# Provider Share URL — Implementation Plan (Phase 1: single key, single provider)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator share a custom OpenAI/Anthropic-compatible provider node as one link (`/v1/add#prov=…&url=…&api=…&models=…`) and let a receiver turn that link into a live provider with a confirmation modal.

**Architecture:** A fragment-only public SPA route (`GET /v1/add`) plus two Svelte modals. No new REST endpoint, no new table, no new dependency — every write reuses the existing dashboard API (`/api/provider-nodes`, `/api/connections`, `/api/providers/{id}/models`, `/api/models/custom`). The API key lives in the URL fragment so it never reaches the server, an access log, or a `Referer`.

**Tech Stack:** Go stdlib (one route line), Svelte 5 runes, TypeScript, Bun test.

**Spec:** `docs/superpowers/specs/2026-11-01-provider-share-url-design.md` — the parameter contract lives in its §5, the four delivery phases in its §11.

**This plan covers phase 1 only.** Phases 2–4 (`models=` filters, `keys=` bulk credentials, `set=` multi-provider) are separate PRs against this base, each with its own plan once phase 1 has shipped. Do not build them here.

## Phase 1 scope
Parameters: `prov`, `url`, `api`, `models` (`auto`/`ask`/`none`/id list), `prefix`, `type`, `apiType`, `name`.

## Global Constraints
- The fragment must never be sent to the server: no fetch, no analytics, no logging of the key.
- `/v1/add` is registered in `SetupServerRouter`, **never** inside `SetupRoutes` (that group is behind `RequireApiKey`).
- No new module in `go.mod`; no new runtime dependency in `web/package.json`.
- Every write goes through the existing dashboard REST group behind `RequireDashboardAuth`. The link page is the only public part.
- Do not edit `CHANGELOG.md`; write `.changes/<pr>-provider-share-url.md`.

## Review Focus
1. **Credential leakage.** A key in a query string lands in access logs. The design puts it in the fragment; a reviewer must reject any refactor that moves it.
2. **Silent overwrite.** A link whose `baseUrl` differs from a node already on the receiver must not rewrite the live node without asking.
3. **Route 404 trap.** `/v1/add` registered only in the test table returns 404 on the real binary (the `9router-go-dashboard-route-two-tables` class of bug).
4. **Fragment loss across login.** A receiver without a session loses the fragment on the `/login` redirect unless it is stashed and restored.
5. **`authType` correctness.** A compatible node stores `authType: "compatible"`. Sending `"apikey"` sends the row down the wrong probe path.

---

### Task 1: Register `GET /v1/add` as a public SPA route

**Files:**
- Modify: `internal/handlers/router.go` (in `SetupServerRouter`, beside `/connections` and `/providers` at lines 522 and 528)
- Test: `internal/integration/provider_share_url_test.go` (new, `//go:build integration`)

**Interfaces:**
- Consumes: `web.Handler()` (already built as `webH` at `router.go:504`)
- Produces: `GET /v1/add` → the embedded SPA document, HTTP 200, no auth required

- [ ] **Step 1: Write the failing integration test**

```go
//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// TestV1AddServesTheSPA pins the one thing a package test cannot see: the real
// binary's route table. A /v1/add registered only in dashboard.RegisterRoutes
// (the test table) answers 404 here while every unit test stays green.
func TestV1AddServesTheSPA(t *testing.T) {
	env := newEnv(t)

	res := env.Get(t, "/v1/add")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /v1/add = %d, want 200", res.Status)
	}
	if !strings.Contains(res.Body.String(), "<div id=\"app\"") && !strings.Contains(res.Body.String(), "<script") {
		t.Fatalf("GET /v1/add did not serve the SPA document: %.120s", res.Body.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=integration ./internal/integration/ -run TestV1AddServesTheSPA -v`
Expected: FAIL with `GET /v1/add = 404, want 200`

- [ ] **Step 3: Register the route**

In `internal/handlers/router.go`, after `r.Get("/connections", webH.ServeHTTP)` (line 522):

```go
// Provider share link: a fragment-only URL that the dashboard SPA resolves.
// Registered here, not under /dashboard/*, because the receiver may not have a
// session yet — the SPA gates the write itself and preserves the fragment
// across the login redirect. The fragment never reaches the server, so a key
// embedded in the link is never logged.
//
// It also must not go into SetupRoutes: that whole group is mounted inside
// RequireApiKey (router.go:627-631), so a /v1/add registered there would answer
// 401 to the browser that is supposed to open it.
r.Get("/v1/add", webH.ServeHTTP)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -tags=integration ./internal/integration/ -run TestV1AddServesTheSPA -v`
Expected: PASS

- [ ] **Step 5: Assert the fragment never reaches the server**

Add a second case to the same file that proves the contract the whole design rests on — a key in the fragment is invisible server-side:

```go
func TestV1AddFragmentIsNotSentToTheServer(t *testing.T) {
	env := newEnv(t)

	// The fragment is stripped by the HTTP client, exactly as a browser does.
	res := env.Get(t, "/v1/add")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /v1/add = %d, want 200", res.Status)
	}
	// If this ever fails, the key has moved into a query string.
	if strings.Contains(res.Request.URL.RawQuery, "api=") {
		t.Fatal("share link put a credential in the query string")
	}
}
```

Run: `go test -tags=integration ./internal/integration/ -run 'TestV1Add' -v`
Expected: PASS

- [ ] **Step 6: Verify with a real binary**

```bash
make build
./bin/9router-go &  # or make dev
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:20130/v1/add   # expect 200
curl -s http://127.0.0.1:20130/v1/add | head -c 80                        # expect <!doctype html>
```

---

### Task 2: `web/src/lib/providerShare.ts` — encode/decode

**Files:**
- Create: `web/src/lib/providerShare.ts`
- Create: `web/src/lib/providerShare.test.ts`

**Interfaces:**
- Produces: `buildProviderShareURL(origin, link): string`, `parseProviderShareHash(hash): ProviderShareLink | null`, `deriveNodeType(prov): {type, apiType}`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from 'bun:test'
import { buildProviderShareURL, parseProviderShareHash } from './providerShare'

describe('parseProviderShareHash', () => {
  it('returns null without prov', () => {
    expect(parseProviderShareHash('#url=https://x.test/v1')).toBeNull()
  })

  it('accepts a leading ? inside the fragment', () => {
    const got = parseProviderShareHash('#?prov=openai-compatible-chat-myprov')
    expect(got?.prov).toBe('openai-compatible-chat-myprov')
  })

  it('splits bulk credentials and drops empty entries', () => {
    const got = parseProviderShareHash('#prov=openai-compatible-chat-p&api=sk-1,,sk-2,')
    expect(got?.api).toEqual(['sk-1', 'sk-2'])
  })

  it('treats auto/ask/none as keywords and a comma list as ids', () => {
    expect(parseProviderShareHash('#prov=p&models=auto')?.models).toBe('auto')
    expect(parseProviderShareHash('#prov=p&models=gpt-4,gpt-5')?.models).toEqual(['gpt-4', 'gpt-5'])
  })

  it('refuses a non-http url', () => {
    expect(parseProviderShareHash('#prov=p&url=javascript:alert(1)')?.url).toBe('')
  })

  it('derives type and apiType from the id', () => {
    expect(parseProviderShareHash('#prov=anthropic-compatible-x')?.type).toBe('anthropic-compatible')
    expect(parseProviderShareHash('#prov=openai-compatible-responses-x')?.apiType).toBe('responses')
  })
})

describe('buildProviderShareURL', () => {
  it('round-trips every accepted case', () => {
    const link = {
      prov: 'openai-compatible-chat-myprov',
      url: 'https://api.x.com/v1',
      api: ['sk-1', 'sk-2'],
      models: 'auto' as const,
      prefix: 'myprov',
      name: 'My Provider',
      type: 'openai-compatible',
      apiType: 'chat',
    }
    expect(parseProviderShareHash(buildProviderShareURL('https://g.test', link))).toEqual(link)
  })

  it('omits credentials entirely when the list is empty', () => {
    const url = buildProviderShareURL('https://g.test', {
      prov: 'p', url: 'https://api.x.com/v1', api: [], models: 'none',
      prefix: 'p', name: '', type: 'openai-compatible', apiType: 'chat',
    })
    expect(url).not.toContain('api=')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && bun test src/lib/providerShare.test.ts`
Expected: FAIL (module not found)

- [ ] **Step 3: Implement the module**

```ts
export interface ProviderShareLink {
  prov: string
  url: string
  api: string[]
  models: 'auto' | 'ask' | 'none' | string[]
  prefix: string
  name: string
  type: string
  apiType: string
}

const MODEL_KEYWORDS = ['auto', 'ask', 'none'] as const

/** Mirrors providerNodeLiteralsForID (internal/handlers/dashboard/provider_node_id.go). */
export function deriveNodeType(prov: string): { type: string; apiType: string } {
  if (prov.startsWith('anthropic-compatible-')) return { type: 'anthropic-compatible', apiType: '' }
  if (prov.startsWith('custom-embedding-')) return { type: 'custom-embedding', apiType: '' }
  if (prov.startsWith('openai-compatible-responses-')) return { type: 'openai-compatible', apiType: 'responses' }
  if (prov.startsWith('openai-compatible-chat-')) return { type: 'openai-compatible', apiType: 'chat' }
  return { type: 'openai-compatible', apiType: 'chat' }
}

function isHTTPURL(raw: string): boolean {
  try {
    const u = new URL(raw)
    return u.protocol === 'http:' || u.protocol === 'https:'
  } catch {
    return false
  }
}

export function parseProviderShareHash(hash: string): ProviderShareLink | null {
  if (!hash) return null
  const raw = hash.replace(/^#/, '').replace(/^\?/, '')
  const params = new URLSearchParams(raw)
  const prov = (params.get('prov') || '').trim()
  if (!prov) return null
  const { type, apiType } = deriveNodeType(prov)
  const rawURL = (params.get('url') || '').trim()
  const rawModels = (params.get('models') || '').trim()
  let models: ProviderShareLink['models'] = 'none'
  if (MODEL_KEYWORDS.includes(rawModels.toLowerCase() as (typeof MODEL_KEYWORDS)[number])) {
    models = rawModels.toLowerCase() as 'auto' | 'ask' | 'none'
  } else if (rawModels) {
    const ids = rawModels.split(/[,\n]/).map((s) => s.trim()).filter(Boolean)
    if (ids.length) models = ids
  }
  return {
    prov,
    url: isHTTPURL(rawURL) ? rawURL : '',
    api: (params.get('api') || '').split(',').map((s) => s.trim()).filter(Boolean),
    models,
    prefix: (params.get('prefix') || '').trim(),
    name: (params.get('name') || '').trim(),
    type,
    apiType,
  }
}

export function buildProviderShareURL(origin: string, link: ProviderShareLink): string {
  const params = new URLSearchParams()
  params.set('prov', link.prov)
  if (link.url) params.set('url', link.url)
  if (link.api.length) params.set('api', link.api.join(','))
  if (Array.isArray(link.models)) {
    if (link.models.length) params.set('models', link.models.join(','))
  } else if (link.models !== 'none') {
    params.set('models', link.models)
  }
  if (link.prefix) params.set('prefix', link.prefix)
  if (link.name) params.set('name', link.name)
  return `${origin.replace(/\/+$/, '')}/v1/add#${params.toString()}`
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && bun test src/lib/providerShare.test.ts`
Expected: PASS

- [ ] **Step 5: Lint**

Run: `cd web && bunx oxlint --deny-warnings src/lib/providerShare.ts`
Expected: no output

---

### Task 3: `ShareProviderModal.svelte`

**Files:**
- Create: `web/src/components/connections/ShareProviderModal.svelte`

**Interfaces:**
- Props: `{ isOpen, providerId, providerName, isCompatibleNode, node?, connections, onClose }`
- Uses: `api.getCustomModels()`, `api.exportProviderKeys(providerId)`, `copyToClipboard`, `buildProviderShareURL`

**Checkbox/radio set** (from spec §8.2):

| Control | Default |
|:--|:--|
| Credential scope radio: None / Single / All keys (bulk) | None |
| Credential picker (Single) | first active connection |
| Model mode radio: Auto-fetch / Specified / None / Ask receiver | Auto-fetch |
| Model list textarea (Specified) | seeded from `api.getCustomModels()` filtered to this provider |
| ☑ Include base URL / prefix / display name | all on |

- [ ] **Step 1: Scaffold the modal** following `ExportKeysModal.svelte`'s structure exactly: `fixed inset-0 z-50` backdrop + `relative w-full max-w-lg rounded-2xl border border-border bg-surface p-6`, `role="button"` backdrop with Escape handling, `max-h-[85vh] flex flex-col` with a scrolling body.

- [ ] **Step 2: Wire the live preview**

```ts
const builtURL = $derived(buildProviderShareURL(window.location.origin, {
  prov: providerId,
  url: includeURL ? (node?.baseUrl || '') : '',
  api: credentialScope === 'none' ? [] : credentialScope === 'single' ? [pickedKey] : allKeys,
  models: modelMode === 'specified' ? modelIDs : modelMode,
  prefix: includePrefix ? (node?.prefix || '') : '',
  name: includeName ? (providerName || '') : '',
  type: node?.type || 'openai-compatible',
  apiType: node?.apiType || 'chat',
}))
```

Render it in a `<input readonly>` (not a `<p>`) so the operator can select-all and copy manually when the clipboard API is unavailable.

- [ ] **Step 3: The two warnings**

```svelte
{#if credentialScope !== 'none'}
  <p class="rounded-lg border border-warning/40 bg-warning/10 p-3 text-xs text-text-main">
    This link contains a live API key. Anyone who obtains it can spend your quota.
    Treat it like a password.
  </p>
{/if}
{#if builtURL.length > 2000}
  <p class="…">
    This link is {builtURL.length} characters — too long for most chat clients and browser
    history. Copy the credentials with <strong>Export Keys</strong> instead and share the
    link without them.
  </p>
{/if}
```

- [ ] **Step 4: Copy handler**

```ts
async function copyLink() {
  await copyToClipboard(builtURL)
  copied = true
  setTimeout(() => (copied = false), 1500)
}
```

- [ ] **Step 5: Verify in a browser**

```bash
cd web && bun run build && cd ..
make dev
# open /dashboard/providers/<a-compatible-node>, click Share, toggle each control,
# confirm the preview changes and the warnings appear/disappear at the right thresholds.
```

---

### Task 4: Wire the Share button into `ProviderDetailView.svelte`

**Files:**
- Modify: `web/src/components/connections/ProviderDetailView.svelte`

- [ ] **Step 1: Import and state**

```ts
import ShareProviderModal from './ShareProviderModal.svelte'
let showShareProviderModal = $state(false)
```

- [ ] **Step 2: Add the button at both call sites** — the compatible-node header action row (line 3377, beside **Export Keys**) and the registered-provider action row (line 4147). Use the existing button class string, `link` as the Material Symbols icon:

```svelte
<button
  type="button"
  onclick={() => (showShareProviderModal = true)}
  class="…bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
>
  <span class="material-symbols-outlined text-[18px]">link</span>
  Share
</button>
```

- [ ] **Step 3: Mount the modal** beside the existing `<ExportKeysModal …/>` at line 5376:

```svelte
<ShareProviderModal
  isOpen={showShareProviderModal}
  providerId={providerId}
  providerName={providerName}
  isCompatibleNode={!!selectedNode}
  node={selectedNode}
  connections={providerConnections}
  onClose={() => (showShareProviderModal = false)}
/>
```

- [ ] **Step 4: Verify** — both buttons open the modal; a registry provider hides `url`/`prefix`/`type`.

---

### Task 5: `AddProviderFromLinkModal.svelte`

**Files:**
- Create: `web/src/components/connections/AddProviderFromLinkModal.svelte`

**Interfaces:**
- Props: `{ isOpen, link: ProviderShareLink, onClose, onDone: (prov: string, added: number, skipped: number) => void }`
- Uses: `api.getProviderNodes()`, `api.createProviderNode()`, `api.createConnection()`, `api.validateProviderNode()`, `api.getConnectionModels()`, `api.saveCustomModelsBulk()`

- [ ] **Step 1: Prefill from the link** into `$state` fields: `formBaseURL`, `formPrefix`, `formAPIKey` (masked with a reveal toggle), `formName`, `formModels`, `formModelMode`. Node id is displayed read-only.

- [ ] **Step 2: Validate button** → `api.validateProviderNode({ baseUrl, apiKey, type })`, render the returned `{valid, error}` as a badge. Never writes.

- [ ] **Step 3: The write sequence**

```ts
async function addProvider() {
  busy = true
  try {
    const nodes = await api.getProviderNodes()
    const existing = nodes.find((n) => n.id === link.prov)
    if (existing && link.url && existing.baseUrl && existing.baseUrl !== formBaseURL) {
      // Never silently rewrite a live endpoint. Ask.
      const useStored = confirm(
        `This instance already has ${link.prov} at ${existing.baseUrl}.\n\n` +
          `OK = keep the stored endpoint.\nCancel = use the link's ${formBaseURL}.`
      )
      if (useStored) formBaseURL = existing.baseUrl
    }
    if (!existing) {
      await api.createProviderNode({
        name: link.name || link.prov,
        prefix: formPrefix,
        apiType: link.apiType || 'chat',
        baseUrl: formBaseURL,
        type: link.type,
        urlSuffix: suffixOf(link.prov),
      })
    }
    const created = await api.createConnection({
      provider: link.prov,
      authType: 'compatible',   // NOT 'apikey' — that is the wrong probe path for a node
      apiKey: formAPIKey,
      name: formName || `${link.name || link.prov} Key`,
      priority: 1,
    })
    let added = 0
    let skipped = 0
    if (formModelMode === 'auto') {
      const res = await api.getConnectionModels(created.id)
      const ids = (res.models || []).map((m) => (typeof m === 'string' ? m : m.id || m.model)).filter(Boolean)
      ;({ added, skipped } = await saveModels(ids))
    } else if (formModelMode === 'specified' && formModels.trim()) {
      ;({ added, skipped } = await saveModels(formModels.split(/[,\n]/).map((s) => s.trim()).filter(Boolean)))
    }
    onDone(link.prov, added, skipped)
  } catch (err) {
    error = err instanceof Error ? err.message : String(err)
  } finally {
    busy = false
  }
}

async function saveModels(ids: string[]) {
  if (!ids.length) return { added: 0, skipped: 0 }
  const res = await api.saveCustomModelsBulk({
    providerAlias: link.prov,
    models: ids.map((id) => ({ id, type: 'llm' })),
  })
  return { added: res.added?.length || 0, skipped: res.skipped?.length || 0 }
}
```

- [ ] **Step 4: Render the 409 plainly.** `api.createProviderNode` rejects a taken suffix with a typed 409. Show `error` verbatim in the footer — do not collapse it into "Something went wrong".

- [ ] **Step 5: Verify** — a fake upstream via `env.NewUpstream` in the integration test (Task 6), plus a browser run against a real second instance.

---

### Task 6: `App.svelte` wiring

**Files:**
- Modify: `web/src/App.svelte`
- Test: `internal/integration/provider_share_url_test.go` (extend)

- [ ] **Step 1: Detect and hold the link**

```ts
import { parseProviderShareHash, type ProviderShareLink } from './lib/providerShare'
let pendingAddLink = $state<ProviderShareLink | null>(
  typeof window !== 'undefined' ? parseProviderShareHash(window.location.hash) : null
)
```

- [ ] **Step 2: Stash across the login redirect.** In `checkAuth()`, before the `navigate('login', true)` call (line ~199):

```ts
if (typeof window !== 'undefined' && pendingAddLink) {
  sessionStorage.setItem('9router_pending_add', window.location.hash)
}
```

Restore it on any mount, before the auth check resolves:

```ts
onMount(() => {
  const stashed = sessionStorage.getItem('9router_pending_add')
  if (stashed && !pendingAddLink) {
    const parsed = parseProviderShareHash(stashed)
    if (parsed) pendingAddLink = parsed
  }
  sessionStorage.removeItem('9router_pending_add')
  …
})
```

`sessionStorage`, not `localStorage`: a pending link must not outlive the tab.

- [ ] **Step 3: Guard the normalisation at `App.svelte:211`** so it does not strip the fragment while an add link is open:

```ts
if (isAuthenticatedState && (rawPath === '/' || rawPath === '/dashboard') && !pendingAddLink) {
  window.history.replaceState({ tab: activeTab }, '', TAB_ROUTES[activeTab])
}
```

- [ ] **Step 4: Render the modal** inside the authenticated branch, beside `<Toasts />`:

```svelte
{#if pendingAddLink}
  <AddProviderFromLinkModal
    isOpen
    link={pendingAddLink}
    onClose={() => (pendingAddLink = null)}
    onDone={(prov, added, skipped) => {
      pendingAddLink = null
      loadData()
      navigate('connections', false, prov)
      toast(`${added} model(s) added to ${prov}${skipped ? `, ${skipped} skipped` : ''}`)
    }}
  />
{/if}
```

- [ ] **Step 5: Verify the login round-trip in a browser** — with `requireLogin` on, open the link unauthenticated, log in, and confirm the modal appears with the fields pre-filled.

---

### Task 7: Integration coverage for the whole flow

**Files:**
- Create/extend: `internal/integration/provider_share_url_test.go`

- [ ] **Step 1: The happy path**

```go
func TestProviderShareLinkAddsNodeConnectionAndModels(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m-1"},{"id":"m-2"}]}`))
			return
		}
		chatCompletionResponse(w)
	})

	const prov = "openai-compatible-chat-shareme"
	// 1. the node, exactly as the modal creates it from the link
	nodeRes := env.Post(t, "/api/provider-nodes", map[string]any{
		"name": "Share Me", "prefix": "shareme", "apiType": "chat",
		"baseUrl": up.URL + "/v1", "type": "openai-compatible", "urlSuffix": "shareme",
	})
	if nodeRes.Status != http.StatusCreated {
		t.Fatalf("create node: %d %s", nodeRes.Status, nodeRes.Body.String())
	}
	if nodeRes.JSON["node"].(map[string]any)["id"] != prov {
		t.Fatalf("node id = %v, want %s", nodeRes.JSON["node"].(map[string]any)["id"], prov)
	}
	// 2. the key, with authType compatible
	connRes := env.Post(t, "/api/connections", map[string]any{
		"provider": prov, "authType": "compatible", "apiKey": "sk-shared", "name": "Share Me Key",
	})
	if connRes.Status != http.StatusOK && connRes.Status != http.StatusCreated {
		t.Fatalf("create connection: %d %s", connRes.Status, connRes.Body.String())
	}
	// 3. the catalogue, fetched through the connection id
	modelsRes := env.Get(t, "/api/providers/"+connRes.JSON["id"].(string)+"/models")
	if modelsRes.Status != http.StatusOK {
		t.Fatalf("list models: %d %s", modelsRes.Status, modelsRes.Body.String())
	}
	// 4. the chat request actually reaches the shared upstream
	chat := env.Post(t, "/v1/chat/completions", ChatBody(prov+"/m-1", false))
	if chat.Status != http.StatusOK {
		t.Fatalf("chat through the shared node: %d %s", chat.Status, chat.Body.String())
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test -tags=integration ./internal/integration/ -run TestProviderShareLink -v`
Expected: PASS. If the helpers differ from the names above, match the file's existing helpers rather than inventing new ones.

- [ ] **Step 3: Confirm the fragment is absent from the log**

```bash
make dev 2>&1 | tee /tmp/gw.log &
curl -s -o /dev/null 'http://127.0.0.1:20130/v1/add#prov=openai-compatible-chat-x&api=sk-secret-value'
grep -c 'sk-secret-value' /tmp/gw.log   # must be 0
```

---

### Task 8: Gates, changelog fragment, hand-off

- [ ] **Step 1: Svelte ratchet**

```bash
make vet-svelte
```
Expected: no unresolved identifiers; the total error count does not grow. If it legitimately drops:
```bash
cd web && bun run ratchet:svelte -- --update
```

- [ ] **Step 2: Build and report the weight**

```bash
cd web && bun run build && du -sh dist && find dist -type f -printf '%s %p\n' | sort -rn | head -8
```
Record the numbers. A new modal that adds no dependency should be a few KB; a jump needs a reason.

- [ ] **Step 3: Go gates**

```bash
rtk go test ./...
rtk go test -tags=integration ./internal/integration/ -run TestProviderShareLink -v
rtk go test ./web/...
golangci-lint run --new-from-rev="$(git merge-base origin/main HEAD)" ./...
```

- [ ] **Step 4: Changelog fragment (never `CHANGELOG.md`)**

Create `.changes/<pr-number>-provider-share-url.md`:

```md
### feat(share): one link that adds a provider on another instance
A compatible provider node is now shareable as a single URL:
`/v1/add#prov=…&url=…&api=…&models=…`. The credential rides in the fragment, so it
never reaches the server, an access log or a `Referer`. The share modal offers
credential scope (none / single / bulk), model mode (auto-fetch / specified /
none / ask the receiver), and optional base URL, prefix and display name, with a
warning once the link passes 2000 characters.

The receiving side is a confirmation modal that creates-or-adopts the node,
stores the key with `authType: "compatible"`, and imports the catalogue through
the existing batch endpoint. A link whose base URL differs from a node already on
the instance asks instead of rewriting it. Verified against a fake upstream:
node, connection, models and a chat request through the new alias all land, and
the embedded key appears nowhere in the gateway log.
```

- [ ] **Step 5: CHANGELOG guard**

```bash
{ rtk git diff --name-only HEAD; rtk git diff --name-only main...HEAD; \
  rtk git status --porcelain | cut -c4-; } | rtk grep -qx CHANGELOG.md \
  && echo "VIOLATION: CHANGELOG.md must not appear in a PR diff (§7.6)"
```
Expected: no output.

## Definition of Done

- `GET /v1/add` returns the SPA document on a real binary (integration test, not a package test).
- `parseProviderShareHash` / `buildProviderShareURL` round-trip every accepted case, and refuse a non-HTTP `url`.
- The share modal renders a live preview, warns on an embedded credential, and warns above 2000 characters.
- The receiver modal writes node + connection + models, and never rewrites a differing live `baseUrl` without asking.
- The link survives a `/login` redirect.
- `make vet-svelte`, `bun run build`, `rtk go test ./...` and the integration suite all pass.
- A browser smoke test on a real gateway sends one chat request through the added node.
- `.changes/<pr>-provider-share-url.md` exists; `CHANGELOG.md` is untouched.
