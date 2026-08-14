# Proxy Pool Fixes & Antigravity Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the proxy pool parsing bug and add Edge Relay transport, Antigravity tool cloaking (21 decoy tools + `_ide` suffixing), and Antigravity native image generation to achieve full feature parity with Next.js v0.5.35.

**Architecture:** A modular approach updating DB parsing for proxy pools, introducing an edge relay & noProxy transport resolver in `internal/proxy/`, adding tool cloaking/uncloaking in `internal/translator/antigravity.go` and `internal/translator/gemini.go`, and adding image generation envelope wrapping for Antigravity.

**Tech Stack:** Go 1.22+, SQLite, standard `net/http` transport, JSON translation, SSE streaming.

## Global Constraints
- Do not modify database table schemas; maintain 100% byte compatibility with Next.js.
- Ensure zero regressions for existing OpenAI, Claude, and Gemini proxying.
- Follow TDD: write failing unit tests first, implement minimal code, verify PASS, then commit.

---

### Task 1: Fix `GetProxyPool` DB Parsing (`proxyUrl`, `type`, `noProxy`, `strictProxy`)

**Files:**
- Modify: `internal/db/proxyPools.go`
- Test: `internal/db/proxyPools_test.go`

**Interfaces:**
- Produces: `ProxyPool` struct with `Type`, `NoProxy`, `StrictProxy`, and fallback parsing for single `proxyUrl`.

- [x] **Step 1: Write the failing test**

In `internal/db/proxyPools_test.go`, add:
```go
func TestGetProxyPool_SingleProxyUrl_AndMetadata(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		data TEXT,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		createdAt TEXT,
		updatedAt TEXT
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	repo := New(db)

	// Single proxyUrl string (Next.js format)
	poolData := `{"name":"relay-pool","proxyUrl":"https://relay.example.com","type":"vercel","noProxy":"localhost,*.internal","strictProxy":true}`
	if _, err := db.Exec(`INSERT INTO proxyPools (id, data, isActive) VALUES (?, ?, ?)`, "pool-single", poolData, 1); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pool, err := repo.GetProxyPool("pool-single")
	if err != nil {
		t.Fatalf("GetProxyPool failed: %v", err)
	}
	if pool == nil {
		t.Fatal("expected pool, got nil")
	}
	if len(pool.URLs) != 1 || pool.URLs[0] != "https://relay.example.com" {
		t.Errorf("expected URLs [https://relay.example.com], got %v", pool.URLs)
	}
	if pool.Type != "vercel" {
		t.Errorf("expected Type vercel, got %s", pool.Type)
	}
	if pool.NoProxy != "localhost,*.internal" {
		t.Errorf("expected NoProxy localhost,*.internal, got %s", pool.NoProxy)
	}
	if !pool.StrictProxy {
		t.Errorf("expected StrictProxy true, got %v", pool.StrictProxy)
	}
	if next := pool.NextURL(); next != "https://relay.example.com" {
		t.Errorf("expected NextURL https://relay.example.com, got %s", next)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/db -run TestGetProxyPool_SingleProxyUrl_AndMetadata`
Expected: FAIL (missing fields / empty URLs)

- [x] **Step 3: Write minimal implementation**

In `internal/db/proxyPools.go`, update `ProxyPool` and `GetProxyPool`:
```go
type ProxyPool struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	IsActive    bool     `json:"isActive"`
	URLs        []string `json:"urls"`
	Strategy    string   `json:"strategy"` // "round-robin" or "random"
	Type        string   `json:"type"`     // "http", "vercel", "cloudflare", "deno"
	NoProxy     string   `json:"noProxy"`
	StrictProxy bool     `json:"strictProxy"`
	index       uint64
}

// In GetProxyPool:
	if urls, ok := raw["urls"].([]any); ok {
		for _, u := range urls {
			if s, ok := u.(string); ok && s != "" {
				pool.URLs = append(pool.URLs, s)
			}
		}
	}
	if len(pool.URLs) == 0 {
		if singleURL := handlerutil.GetString(raw, "proxyUrl"); singleURL != "" {
			pool.URLs = append(pool.URLs, singleURL)
		}
	}
	pool.Type = handlerutil.GetString(raw, "type")
	if pool.Type == "" {
		pool.Type = "http"
	}
	pool.NoProxy = handlerutil.GetString(raw, "noProxy")
	if sp, ok := raw["strictProxy"].(bool); ok {
		pool.StrictProxy = sp
	}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/db -run TestGetProxyPool`
Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/db/proxyPools.go internal/db/proxyPools_test.go
git commit -m "fix(db): support single proxyUrl, edge relay types and proxy metadata in GetProxyPool"
```

---

### Task 2: Implement NoProxy Matching & Edge Relay Transport Engine

**Files:**
- Create: `internal/proxy/transport.go`
- Create: `internal/proxy/transport_test.go`
- Modify: `internal/handlers/chat/connections.go`

**Interfaces:**
- Produces: `ShouldBypassNoProxy(targetURL, noProxy string) bool`, `ResolveProxyForConnection(connData *ConnectionData, repo Repo) (*ResolvedProxy, error)`.
- Updates `connections.go` to handle legacy proxies and edge relay headers.

- [x] **Step 1: Write the failing test**

In `internal/proxy/transport_test.go`:
```go
package proxy_test

import (
	"testing"
	"9router/proxy/internal/proxy"
)

func TestShouldBypassNoProxy(t *testing.T) {
	tests := []struct {
		target   string
		noProxy  string
		expected bool
	}{
		{"https://api.openai.com/v1", "", false},
		{"https://api.openai.com/v1", "*", true},
		{"https://api.openai.com/v1", "api.openai.com", true},
		{"https://api.openai.com/v1", ".openai.com", true},
		{"https://api.openai.com/v1", "openai.com", true},
		{"https://localhost:8080/v1", "localhost,127.0.0.1", true},
		{"https://api.anthropic.com/v1", "api.openai.com,localhost", false},
	}

	for _, tt := range tests {
		got := proxy.ShouldBypassNoProxy(tt.target, tt.noProxy)
		if got != tt.expected {
			t.Errorf("ShouldBypassNoProxy(%q, %q) = %v, expected %v", tt.target, tt.noProxy, got, tt.expected)
		}
	}
}

func TestBuildEdgeRelayHeaders(t *testing.T) {
	targetURL := "https://api.openai.com/v1/chat/completions"
	relayHeaders := proxy.BuildEdgeRelayHeaders(targetURL, map[string]string{
		"Authorization": "Bearer key",
	})

	if relayHeaders["x-relay-target"] != "https://api.openai.com" {
		t.Errorf("expected x-relay-target https://api.openai.com, got %s", relayHeaders["x-relay-target"])
	}
	if relayHeaders["x-relay-path"] != "/v1/chat/completions" {
		t.Errorf("expected x-relay-path /v1/chat/completions, got %s", relayHeaders["x-relay-path"])
	}
	if relayHeaders["Authorization"] != "Bearer key" {
		t.Errorf("expected Authorization preserved, got %s", relayHeaders["Authorization"])
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/proxy -run TestShouldBypassNoProxy`
Expected: FAIL (types/functions undefined)

- [x] **Step 3: Write minimal implementation**

Create `internal/proxy/transport.go`:
```go
package proxy

import (
	"net/url"
	"strings"
)

// ShouldBypassNoProxy checks whether targetURL matches any pattern in noProxy.
func ShouldBypassNoProxy(targetURL, noProxy string) bool {
	noProxy = strings.TrimSpace(noProxy)
	if noProxy == "" {
		return false
	}
	if noProxy == "*" {
		return true
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())

	patterns := strings.Split(noProxy, ",")
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		if strings.HasPrefix(p, ".") {
			if strings.HasSuffix(host, p) || host == p[1:] {
				return true
			}
		}
		if host == p || strings.HasSuffix(host, "."+p) {
			return true
		}
	}
	return false
}

// BuildEdgeRelayHeaders formats headers for Vercel, Cloudflare, and Deno edge relays.
func BuildEdgeRelayHeaders(targetURL string, existingHeaders map[string]string) map[string]string {
	headers := make(map[string]string, len(existingHeaders)+2)
	for k, v := range existingHeaders {
		headers[k] = v
	}

	parsed, err := url.Parse(targetURL)
	if err == nil {
		headers["x-relay-target"] = parsed.Scheme + "://" + parsed.Host
		path := parsed.Path
		if parsed.RawQuery != "" {
			path += "?" + parsed.RawQuery
		}
		headers["x-relay-path"] = path
	}
	return headers
}
```

Update `internal/handlers/chat/connections.go` to support legacy proxy and pass proxy metadata to `getClientForConnection`.

- [x] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/proxy -run TestShouldBypassNoProxy`
Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/proxy/transport.go internal/proxy/transport_test.go internal/handlers/chat/connections.go
git commit -m "feat(proxy): add noProxy matching, edge relay headers, and legacy connection proxy resolution"
```

---

### Task 3: Implement Antigravity Tool Cloaking & Decoy Tool System

**Files:**
- Modify: `internal/translator/antigravity.go`
- Test: `internal/translator/antigravity_test.go`
- Modify: `internal/translator/gemini.go`

**Interfaces:**
- Produces: `CloakAntigravityRequest(req *GeminiRequest, clientTool string) (*GeminiRequest, map[string]string)`, `UncloakToolName(name string, toolMap map[string]string) string`.

- [x] **Step 1: Write the failing test**

In `internal/translator/antigravity_test.go`:
```go
package translator_test

import (
	"testing"
	"9router/proxy/internal/translator"
)

func TestCloakAntigravityRequest_RenamesAndInjectsDecoys(t *testing.T) {
	req := &translator.GeminiRequest{
		Contents: []translator.GeminiContent{
			{
				Role: "model",
				Parts: []translator.GeminiPart{
					{FunctionCall: &translator.GeminiFunctionCall{Name: "execute_code", Args: map[string]any{"code": "ls"}}},
				},
			},
			{
				Role: "user",
				Parts: []translator.GeminiPart{
					{FunctionResponse: &translator.GeminiFunctionResp{Name: "execute_code"}},
				},
			},
		},
		Tools: []translator.GeminiTool{
			{
				FunctionDeclarations: []translator.GeminiFunctionDecl{
					{Name: "execute_code", Description: "Run code"},
					{Name: "run_command", Description: "Native tool"},
				},
			},
		},
	}

	cloaked, toolMap := translator.CloakAntigravityRequest(req, "")
	if cloaked == nil {
		t.Fatal("expected cloaked request, got nil")
	}

	// execute_code should be renamed to execute_code_ide
	if toolMap["execute_code_ide"] != "execute_code" {
		t.Errorf("expected toolMap[execute_code_ide] = execute_code, got %s", toolMap["execute_code_ide"])
	}

	// Check function call in history was renamed
	if cloaked.Contents[0].Parts[0].FunctionCall.Name != "execute_code_ide" {
		t.Errorf("expected contents functionCall renamed to execute_code_ide, got %s", cloaked.Contents[0].Parts[0].FunctionCall.Name)
	}
	if cloaked.Contents[1].Parts[0].FunctionResponse.Name != "execute_code_ide" {
		t.Errorf("expected contents functionResponse renamed to execute_code_ide, got %s", cloaked.Contents[1].Parts[0].FunctionResponse.Name)
	}

	// Check 21 decoy tools injected
	if len(cloaked.Tools) == 0 || len(cloaked.Tools[0].FunctionDeclarations) < 20 {
		t.Errorf("expected >= 20 function declarations including decoys, got %d", len(cloaked.Tools[0].FunctionDeclarations))
	}
}

func TestUncloakToolName(t *testing.T) {
	toolMap := map[string]string{
		"execute_code_ide": "execute_code",
		"custom_tool_ide":  "custom_tool",
	}

	if un := translator.UncloakToolName("execute_code_ide", toolMap); un != "execute_code" {
		t.Errorf("expected execute_code, got %s", un)
	}
	if un := translator.UncloakToolName("run_command", toolMap); un != "run_command" {
		t.Errorf("expected run_command unchanged, got %s", un)
	}
	if un := translator.UncloakToolName("other_ide", nil); un != "other" {
		t.Errorf("expected other (suffix stripped), got %s", un)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/translator -run TestCloakAntigravityRequest`
Expected: FAIL (functions undefined)

- [x] **Step 3: Write minimal implementation**

In `internal/translator/antigravity.go`, define:
- `AntigravityNativeToolNames` (map of 21 native Antigravity names).
- `AntigravityDecoyTools` (list of 21 declarations with `description: "This tool is currently unavailable."`).
- `CloakAntigravityRequest(req *GeminiRequest, clientTool string) (*GeminiRequest, map[string]string)`
- `UncloakToolName(name string, toolMap map[string]string) string`

In `internal/translator/gemini.go`, integrate `UncloakToolName` into `TranslateGeminiResponseToOpenAI` and `TranslateGeminiChunkToOpenAI`.

- [x] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/translator -run "TestCloak|TestUncloak"`
Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/translator/antigravity.go internal/translator/antigravity_test.go internal/translator/gemini.go
git commit -m "feat(antigravity): add anti-ban tool cloaking and decoy tool injection"
```

---

### Task 4: Implement Antigravity Native Image Generation

**Files:**
- Modify: `internal/translator/antigravity.go`
- Test: `internal/translator/antigravity_test.go`
- Modify: `internal/proxy/gemini.go`

**Interfaces:**
- Produces: `IsAntigravityImageModel(model string) bool`, `ParseImageConfig(model string) (string, string)`, `WrapAntigravityImageRequest(...)`, `FormatAntigravityImageResponse(...)`.

- [x] **Step 1: Write the failing test**

In `internal/translator/antigravity_test.go`:
```go
func TestAntigravityImageModelAndConfig(t *testing.T) {
	if !translator.IsAntigravityImageModel("gemini-3.1-flash-image") {
		t.Error("expected gemini-3.1-flash-image to be image model")
	}
	if !translator.IsAntigravityImageModel("imagen-3.0-generate-002") {
		t.Error("expected imagen-3.0-generate-002 to be image model")
	}
	if translator.IsAntigravityImageModel("gemini-3-flash") {
		t.Error("expected gemini-3-flash NOT to be image model")
	}

	clean, ratio := translator.ParseImageConfig("gemini-3.1-flash-image-16x9")
	if clean != "gemini-3.1-flash-image" || ratio != "16:9" {
		t.Errorf("expected (gemini-3.1-flash-image, 16:9), got (%s, %s)", clean, ratio)
	}

	clean2, ratio2 := translator.ParseImageConfig("gemini-3.1-flash-image-1024x768")
	if clean2 != "gemini-3.1-flash-image" || ratio2 != "4:3" {
		t.Errorf("expected (gemini-3.1-flash-image, 4:3), got (%s, %s)", clean2, ratio2)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/translator -run TestAntigravityImageModelAndConfig`
Expected: FAIL (functions undefined)

- [x] **Step 3: Write minimal implementation**

In `internal/translator/antigravity.go`, implement:
- `IsAntigravityImageModel(model string) bool`
- `ParseImageConfig(model string) (cleanModel, aspectRatio string)`
- `WrapAntigravityImageRequest(prompt, base64Input, projectID, cleanModel, aspectRatio string) ([]byte, error)`
- `FormatAntigravityImageResponse(rawGeminiResp []byte) ([]byte, error)`

In `internal/proxy/gemini.go`, check `IsAntigravityImageModel(modelName)` and route to `WrapAntigravityImageRequest` with forced non-streaming `/v1internal:generateContent`.

- [x] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/translator -run TestAntigravityImageModelAndConfig`
Expected: PASS

- [x] **Step 5: Commit**

```bash
git add internal/translator/antigravity.go internal/translator/antigravity_test.go internal/proxy/gemini.go
git commit -m "feat(antigravity): add native image generation support"
```

---

### Task 5: End-to-End Integration & Full Test Verification

**Files:**
- Test: All test suites across the repository

- [x] **Step 1: Run complete repository test suite**

Run: `go test -v ./...`
Expected: ALL PASS with 0 failures

- [x] **Step 2: Run benchmark & lint check**

Run: `go vet ./...`
Expected: clean

- [x] **Step 3: Update CHANGELOG & commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): record proxy pool fixes, edge relays, antigravity cloaking and image gen"
```
