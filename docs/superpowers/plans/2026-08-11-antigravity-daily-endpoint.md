# Antigravity Daily Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate all hardcoded `cloudcode-pa.googleapis.com` endpoints to `daily-cloudcode-pa.googleapis.com` for the `antigravity` provider to avoid strict rate limits.

**Architecture:** A simple find-and-replace of the base URL strings within the provider definition and router tests to ensure they point to the staging/daily environment used by the native IDE clients.

**Tech Stack:** Go, standard library net/http, standard testing framework.

## Global Constraints

- Do not modify Next.js configurations; focus strictly on `9router-go`.
- Ensure tests still pass after changing URL constants.

---

### Task 1: Update Provider Base URLs

**Files:**
- Modify: `internal/providers/providers.go`
- Modify: `internal/handlers/chat/antigravity_project.go`

**Interfaces:**
- Consumes: N/A
- Produces: Base URLs resolving to `daily-cloudcode-pa.googleapis.com`.

- [ ] **Step 1: Write the failing test / Verify existing configuration**

Run: `grep "https://cloudcode-pa.googleapis.com" internal/providers/providers.go internal/handlers/chat/antigravity_project.go`
Expected: Output showing the production URLs still exist in the files.

- [ ] **Step 2: Write minimal implementation (providers.go)**

In `internal/providers/providers.go`, find `BaseURL:    "https://cloudcode-pa.googleapis.com"` and replace it:

```go
	"antigravity": {
		BaseURL:    "https://daily-cloudcode-pa.googleapis.com",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		Format:     "gemini-native",
	},
```

Also find the secondary provider configuration (if any) that uses `/v1internal` and replace it:

```go
	"antigravity-ide": {
		BaseURL:    "https://daily-cloudcode-pa.googleapis.com/v1internal",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		Format:     "gemini-native",
	},
```

- [ ] **Step 3: Write minimal implementation (antigravity_project.go)**

In `internal/handlers/chat/antigravity_project.go`, modify the package-level variables:

```go
var loadCodeAssistURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
var onboardUserURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser"
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go build ./...`
Expected: PASS with no compilation errors.

- [ ] **Step 5: Commit**

```bash
git add internal/providers/providers.go internal/handlers/chat/antigravity_project.go
git commit -m "fix(providers): use daily environment for antigravity endpoints"
```

---

### Task 2: Update MITM Proxy Tests & DNS configurations

**Files:**
- Modify: `internal/mitm/dns.go`
- Modify: `internal/mitm/server.go`
- Modify: `internal/mitm/mitm_test.go`

**Interfaces:**
- Consumes: N/A
- Produces: MITM proxy successfully capturing requests directed to `daily-cloudcode-pa.googleapis.com`.

- [ ] **Step 1: Write the failing test**

Run: `go test ./internal/mitm -v`
Expected: This might pass if `daily-cloudcode-pa.googleapis.com` is already partially supported, but we must ensure it is the *only/primary* expectation in tests. We can check `mitm_test.go` to see if it specifically checks for `cloudcode-pa`.

- [ ] **Step 2: Write minimal implementation (dns.go & server.go)**

In `internal/mitm/dns.go`, ensure `daily-cloudcode-pa.googleapis.com` remains, but you can leave `cloudcode-pa.googleapis.com` for backward compatibility. (No change strictly needed, but verify both are present).

In `internal/mitm/server.go`, verify:
```go
	"cloudcode-pa.googleapis.com":       handlers.HandleAntigravity,
	"daily-cloudcode-pa.googleapis.com": handlers.HandleAntigravity,
```
(No change needed if both already exist).

- [ ] **Step 3: Write minimal implementation (mitm_test.go)**

In `internal/mitm/mitm_test.go`, update test cases to explicitly test the `daily-` domain if they are hardcoded to the production one.

```go
// Look for instances of:
// if d == "cloudcode-pa.googleapis.com"
// and update/duplicate to test "daily-cloudcode-pa.googleapis.com"
	if !strings.Contains(entries, "cloudcode-pa.googleapis.com") && !strings.Contains(entries, "daily-cloudcode-pa.googleapis.com") {
		t.Error("expected cloudcode-pa.googleapis.com or daily-cloudcode-pa.googleapis.com in domains")
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mitm -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mitm/mitm_test.go
git commit -m "test(mitm): cover daily-cloudcode-pa endpoint in proxy tests"
```
