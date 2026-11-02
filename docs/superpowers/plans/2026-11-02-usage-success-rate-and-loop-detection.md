# Success Rate & Model Loop Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hiển thị chỉ số Success Rate trên Usage Dashboard (Overview KPI và Details Lifetime) và tự động phát hiện model bị kẹt hallucination / stuck loop để cảnh báo trực quan trên Recent Requests và Request Details.

**Architecture:**
- Xây dựng module Go `internal/detection` phi trạng thái (stateless), phân tích chuỗi tool calls và errors từ `requestBody.messages` để phát hiện vòng lặp (consecutive repeats, multi-tool cycles, repeated errors) theo chuẩn Blazo với độ trễ sub-millisecond.
- Hoàn thiện cơ chế ghi nhận lỗi request trong `internal/handlers/chat/usage.go` (hạch toán vào `usageHistory`, `usageDaily`, `recentRequests` ring, phân biệt client cancel 499 khỏi lỗi hệ thống).
- Bổ sung `SuccessRate` và `TotalErrors` vào API `/api/usage/stats` và `/api/usage/dashboard`.
- Cập nhật giao diện Svelte 5: thêm thẻ Success Rate trên `SummaryKpiCards.svelte` và `UsageDashboard.svelte`, thêm icon cảnh báo ⚠️ trên `recentRequestsCard` và modal detail.

**Tech Stack:** Go 1.24+, Svelte 5 (Runes), TailwindCSS, SQLite.

**Spec:** `docs/superpowers/specs/2026-11-01-usage-success-rate-and-loop-detection-design.md`

## Global Constraints

- **Dependency limits:** Không thêm thư viện ngoài (external dependency) cho detection; sử dụng stdlib Go và các cấu trúc dữ liệu tối ưu có sẵn.
- **Performance:** Module detection phải thực thi $< 0.1\text{ms}$ mỗi request, không làm chậm pipeline proxy.
- **Client aborts:** Yêu cầu bị hủy bởi người dùng (Ctrl+C / client context canceled / 499) được đánh dấu `canceled` và KHÔNG tính vào `TotalErrors`.
- **Backward compatibility:** Không phá vỡ định dạng API cũ của `/api/usage/stats`, `/api/usage/dashboard` và `/api/usage/stream`.
- **UI Design System:** Tuân thủ design token và theme màu hiện có của 9router-go (dark/light, text-success, text-warning, text-red-500, surface, border).

## Review Focus

1. **Client cancellation skew:** Người dùng ấn Ctrl+C ngắt CLI thường xuyên không được làm tụt Success Rate xuống đỏ.
2. **Payload format variations:** Phát hiện đúng tool calls từ cả OpenAI format (`tool_calls`) và Anthropic Claude format (`tool_use` trong content blocks).
3. **Zero-request division:** Khi không có request trong khoảng thời gian chọn, `SuccessRate` phải trả `nil` (`null` trong JSON) và UI hiển thị `—`, không được panic chia cho 0.
4. **SSE Realtime updates:** Khi có request lỗi hoặc có loop warning, SSE `/api/usage/stream` phải đẩy ngay lập tức để UI cập nhật chấm đỏ và icon ⚠️ không cần reload trang.
5. **Memory overhead:** Detection chạy trên `requestBody.messages` đã unmarshal, không tạo thêm buffer lớn hay giữ tham chiếu gây rò rỉ bộ nhớ.

---

### Task 1: Engine phát hiện Hallucination & Stuck Loop (`internal/detection`)

**Files:**
- Create: `internal/detection/types.go`
- Create: `internal/detection/detect.go`
- Test: `internal/detection/detect_test.go`

**Interfaces:**
- Produces:
  ```go
  type FindingSeverity string
  const (
      SeverityWarning  FindingSeverity = "warning"
      SeverityCritical FindingSeverity = "critical"
  )

  type LoopFinding struct {
      Rule     string          `json:"rule"`
      Severity FindingSeverity `json:"severity"`
      Message  string          `json:"message"`
      Tool     string          `json:"tool,omitempty"`
      Count    int             `json:"count,omitempty"`
  }

  type ToolCallItem struct {
      Name      string
      Arguments string
      Error     string
  }

  func ExtractToolCallsFromOpenAIMessages(messages []OpenAIMessage) []ToolCallItem
  func DetectLoops(tools []ToolCallItem, lastError string) []LoopFinding
  ```

- [ ] **Step 1: Viết test thất bại trong `internal/detection/detect_test.go`**

```go
package detection

import "testing"

func TestDetectLoops_ConsecutiveTool(t *testing.T) {
	tools := []ToolCallItem{
		{Name: "Bash"},
		{Name: "Bash"},
		{Name: "Bash"},
	}
	findings := DetectLoops(tools, "")
	if len(findings) == 0 {
		t.Fatalf("expected finding for 3 consecutive Bash calls, got none")
	}
	if findings[0].Rule != "consecutive_tool" || findings[0].Tool != "Bash" {
		t.Errorf("unexpected finding: %+v", findings[0])
	}
}

func TestDetectLoops_CyclePattern(t *testing.T) {
	tools := []ToolCallItem{
		{Name: "Read"}, {Name: "Edit"},
		{Name: "Read"}, {Name: "Edit"},
	}
	findings := DetectLoops(tools, "")
	if len(findings) == 0 {
		t.Fatalf("expected finding for [Read, Edit] cycle, got none")
	}
	if findings[0].Rule != "cycle_pattern" {
		t.Errorf("expected cycle_pattern rule, got %s", findings[0].Rule)
	}
}

func TestDetectLoops_RepeatedError(t *testing.T) {
	tools := []ToolCallItem{
		{Name: "Bash", Error: "command not found"},
		{Name: "Bash", Error: "command not found"},
		{Name: "Bash", Error: "command not found"},
	}
	findings := DetectLoops(tools, "")
	if len(findings) == 0 {
		t.Fatalf("expected finding for repeated error, got none")
	}
	if findings[0].Rule != "repeated_error" {
		t.Errorf("expected repeated_error rule, got %s", findings[0].Rule)
	}
}

func TestDetectLoops_NormalNoLoop(t *testing.T) {
	tools := []ToolCallItem{
		{Name: "Read"}, {Name: "Grep"}, {Name: "Edit"}, {Name: "Bash"},
	}
	findings := DetectLoops(tools, "")
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for normal flow, got %d", len(findings))
	}
}
```

- [ ] **Step 2: Chạy test để xác nhận test báo lỗi**

Run: `go test ./internal/detection/...`  
Expected: FAIL (package does not exist)

- [ ] **Step 3: Triển khai `internal/detection/types.go` và `detect.go`**

Triển khai logic:
1. `consecutive_tool`: đếm chuỗi liên tiếp $\ge 3$. Nếu $\ge 5 \to$ `critical`, $3-4 \to$ `warning`.
2. `cycle_pattern`: kiểm tra chu kỳ 2 tool ($A, B, A, B$) hoặc 3 tool ($A, B, C, A, B, C$) ở đuôi mảng.
3. `repeated_error`: đếm lỗi giống nhau $\ge 3$ lần trong `tools[i].Error`.
4. Hàm helper trích xuất tool calls từ `OpenAIMessage` (cả trường `ToolCalls` và trường `Content` dạng array chứa `tool_use`).

- [ ] **Step 4: Chạy test để xác nhận toàn bộ test passed**

Run: `go test -v ./internal/detection/...`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/detection/
git commit -m "feat(detection): add deterministic hallucination and stuck loop detector"
```

---

### Task 2: Ghi nhận lỗi và Tích hợp Loop Detection vào Request Logging (`internal/handlers/chat`)

**Files:**
- Modify: `internal/usagetracker/tracker.go:37-60`
- Modify: `internal/handlers/chat/usage.go:100-180, 480-530`
- Modify: `internal/handlers/chat/chat.go`
- Test: `internal/handlers/chat/usage_failure_accounting_test.go`

**Interfaces:**
- Consumes: `internal/detection`
- Produces:
  - `usagetracker.RecentRequest.LoopWarning string`
  - `RecordFailureUsage` lưu `status: "error"` hoặc `"canceled"` vào `usageHistory`
  - `RecordFailureUsage` gọi `usagetracker.GetTracker().PushRecent(...)`
  - `detailPayload["anomalies"]` chứa `[]LoopFinding`

- [ ] **Step 1: Viết test cho failure accounting và loop warning trong `internal/handlers/chat/usage_failure_accounting_test.go`**

```go
package chat

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"9router/proxy/internal/db"
	"9router/proxy/internal/usagetracker"
)

func TestRecordFailureUsage_DifferentiatesCanceledFromError(t *testing.T) {
	repo, err := db.NewTestRepo(t)
	if err != nil {
		t.Fatalf("create test repo: %v", err)
	}
	defer repo.Close()

	handler := NewChatHandler(repo)

	// Case 1: Client canceled (context.Canceled)
	ctxCancel, cancel := context.WithCancel(context.Background())
	cancel()
	info := RequestInfo{Provider: "openai", Model: "gpt-4o", Endpoint: "/v1/chat/completions"}
	handler.RecordFailureUsage(ctxCancel, "req-cancel", info, 100, 499, "context canceled")

	// Case 2: Upstream error (500)
	handler.RecordFailureUsage(context.Background(), "req-err", info, 250, 500, "upstream connection refused")

	// Verify in tracker recent ring
	recent := usagetracker.GetTracker().GetRecent()
	if len(recent) < 2 {
		t.Fatalf("expected at least 2 recent requests, got %d", len(recent))
	}
	if recent[0].Status != "error" && recent[1].Status != "error" {
		t.Errorf("expected error status recorded in recent requests")
	}
}
```

- [ ] **Step 2: Chạy test để xác nhận test fail**

Run: `go test -v ./internal/handlers/chat/ -run TestRecordFailureUsage_DifferentiatesCanceledFromError`  
Expected: FAIL

- [ ] **Step 3: Cập nhật `internal/usagetracker/tracker.go` và `internal/handlers/chat/usage.go`**

1. Trong `RecentRequest` (`tracker.go`), thêm trường `LoopWarning string `json:"loopWarning,omitempty"` + `GetRecent() []RecentRequest`.
2. Trong `RecordFailureUsage`:
   - Phân biệt context canceled (`errors.Is(ctx.Err(), context.Canceled)` hoặc 499) $\to$ `status = "canceled"`, ngược lại `status = "error"`.
   - Ghi vào `usageHistory` thông qua `InsertUsageHistory` (status `error`/`canceled`, 0 tokens, 0 cost).
   - Đẩy vào `usagetracker.GetTracker().PushRecent` với đúng status.
   - Gọi `upsertDailyUsageError(provider, model, endpoint, connID, apiKey, status)`.
3. Trong `logUsage` và `RecordFailureUsage`:
   - Gọi `detection.ExtractToolCallsFromOpenAIMessages` trên `req.Messages`.
   - Gọi `detection.DetectLoops(tools, errMsg)`.
   - Nếu có findings: gán `detailPayload["anomalies"] = findings` và `recentReq.LoopWarning = findings[0].Message`.

- [ ] **Step 4: Chạy lại test để xác nhận pass**

Run: `go test -v ./internal/handlers/chat/ -run TestRecordFailureUsage_DifferentiatesCanceledFromError`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/usagetracker/ internal/handlers/chat/
git commit -m "feat(chat): record failure accounting and detect tool loops in request usage"
```

---

### Task 3: Aggregation Success Rate trong Stats & Dashboard Handlers

**Files:**
- Modify: `internal/handlers/usage_stats.go:150-170, 510-560`
- Modify: `internal/handlers/usage_window_cache.go:570-620`
- Modify: `internal/handlers/usage_dashboard.go:40-70, 160-220`
- Test: `internal/handlers/usage_stats_success_rate_test.go`

**Interfaces:**
- Produces:
  - `UsageStatsResponse.TotalErrors int`
  - `UsageStatsResponse.SuccessRate *float64`
  - `dashboardLifetime.SuccessRate *float64`

- [ ] **Step 1: Viết test thất bại trong `internal/handlers/usage_stats_success_rate_test.go`**

```go
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"9router/proxy/internal/db"
)

func TestUsageStats_CalculatesSuccessRate(t *testing.T) {
	repo, err := db.NewTestRepo(t)
	if err != nil {
		t.Fatalf("create test repo: %v", err)
	}
	defer repo.Close()

	// Seed 3 successful requests and 1 error request
	for i := 0; i < 3; i++ {
		repo.InsertUsageHistory("openai", "gpt-4o", "c1", "sk-1", "/v1/chat/completions", 10, 10, 0.01, "success", 20, "{}", "{}")
	}
	repo.InsertUsageHistory("openai", "gpt-4o", "c1", "sk-1", "/v1/chat/completions", 0, 0, 0, "error", 0, "{}", "{}")

	handler := NewUsageStatsHandler(repo)
	req := httptest.NewRequest("GET", "/api/usage/stats?period=today", nil)
	rec := httptest.NewRecorder()
	handler.HandleUsageStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp UsageStatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.TotalRequests != 4 {
		t.Errorf("expected 4 total requests, got %d", resp.TotalRequests)
	}
	if resp.TotalErrors != 1 {
		t.Errorf("expected 1 total error, got %d", resp.TotalErrors)
	}
	if resp.SuccessRate == nil || *resp.SuccessRate != 75.0 {
		t.Errorf("expected 75%% success rate, got %v", resp.SuccessRate)
	}
}
```

- [ ] **Step 2: Chạy test để xác nhận fail**

Run: `go test -v ./internal/handlers/ -run TestUsageStats_CalculatesSuccessRate`  
Expected: FAIL (TotalErrors/SuccessRate not present)

- [ ] **Step 3: Cập nhật `usage_stats.go`, `usage_window_cache.go`, và `usage_dashboard.go`**

1. Cập nhật `UsageStatsResponse` thêm `TotalErrors` và `SuccessRate`.
2. Khi aggregate `byProvider` hoặc `rawAggregate`: tính `TotalErrors = SUM(errors)`.
3. Nếu `TotalRequests > 0`, `rate = math.Round(float64(TotalRequests - TotalErrors)/float64(TotalRequests)*1000)/10`, `SuccessRate = &rate`. Ngược lại `SuccessRate = nil`.
4. Cập nhật `dashboardLifetime` trong `usage_dashboard.go` tính `SuccessRate` tương tự trên toàn bộ lifetime requests.

- [ ] **Step 4: Chạy lại test để xác nhận pass**

Run: `go test -v ./internal/handlers/ -run TestUsageStats_CalculatesSuccessRate`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/
git commit -m "feat(handlers): expose TotalErrors and SuccessRate in usage stats and dashboard"
```

---

### Task 4: Hiển thị Success Rate trên Overview KPI và Details Lifetime Cards (`web/src`)

**Files:**
- Modify: `web/src/api/client.ts:730-760`
- Modify: `web/src/components/analytics/types.ts:1-25`
- Modify: `web/src/components/analytics/SummaryKpiCards.svelte`
- Modify: `web/src/components/analytics/UsageDashboard.svelte:35-80`

**Interfaces:**
- Consumes: `UsageStatsResponse.successRate`, `UsageStatsResponse.totalErrors`, `UsageDashboardResponse.lifetime.successRate`
- Produces: Thẻ Success Rate trên Overview và Details

- [ ] **Step 1: Cập nhật TypeScript interfaces trong `web/src/api/client.ts` và `types.ts`**

Thêm `totalErrors?: number`, `successRate?: number | null` vào `UsageStatsResponse` / `StatsData` và `UsageLifetime`.

- [ ] **Step 2: Cập nhật `web/src/components/analytics/SummaryKpiCards.svelte`**

Thêm card thứ 6:
```svelte
<div class="bg-surface border border-border-subtle rounded-[14px] shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
  <span class="text-text-muted text-sm uppercase font-semibold">Success Rate</span>
  <span class="truncate text-2xl font-bold {rateColor(stats.successRate)}" title={fmtSuccessRate(stats.successRate)}>
    {fmtSuccessRate(stats.successRate)}
  </span>
  <span class="text-[10px] text-text-muted">{fmtSuccessSubtext(stats)}</span>
</div>
```
Cập nhật grid class: `sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-6`.

- [ ] **Step 3: Cập nhật `web/src/components/analytics/UsageDashboard.svelte`**

Thêm thẻ `Lifetime Success Rate` vào danh sách `cards`:
```ts
{
  label: 'Success Rate',
  value: l.successRate != null ? `${l.successRate.toFixed(1)}%` : '—',
  sub: 'Lifetime requests',
  valueClass: l.successRate != null && l.successRate >= 95 ? 'text-success' : l.successRate != null && l.successRate >= 80 ? 'text-warning' : 'text-red-500',
}
```

- [ ] **Step 4: Kiểm tra build TypeScript và Svelte**

Run: `cd web && pnpm check`  
Expected: 0 errors

- [ ] **Step 5: Commit**

```bash
git add web/src/
git commit -m "feat(web): add Success Rate cards to Overview KPI and Details dashboard"
```

---

### Task 5: Cảnh báo Model Loop trên Recent Requests và Request Detail Dialog (`web/src`)

**Files:**
- Modify: `web/src/components/analytics/types.ts:60-120`
- Modify: `web/src/components/analytics/AnalyticsView.svelte:740-800`
- Modify: `web/src/components/analytics/RequestDetailsTab.svelte:200-260, 360-440`

**Interfaces:**
- Consumes: `req.loopWarning`, `item.anomalies`
- Produces: Warning icon ⚠️ trên recent requests list, Loop alert banner trong modal

- [ ] **Step 1: Cập nhật `RequestDetailItem` và `RecentRequest` types trong `types.ts`**

Thêm:
```ts
export interface LoopFindingItem {
  rule: string
  severity: 'warning' | 'critical'
  message: string
  tool?: string
  count?: number
}

// trong RecentRequest & RequestDetailItem:
loopWarning?: string
anomalies?: LoopFindingItem[]
```

- [ ] **Step 2: Cập nhật `recentRequestsCard` trong `web/src/components/analytics/AnalyticsView.svelte`**

Cạnh tên model, nếu `req.loopWarning` tồn tại, hiển thị:
```svelte
<span class="inline-flex items-center gap-0.5 truncate font-mono text-[11px]" title={req.model}>
  {#if req.loopWarning}
    <span class="text-amber-500 text-[12px] shrink-0" title={req.loopWarning}>⚠️</span>
  {/if}
  <span class="truncate">{req.model}</span>
</span>
```

- [ ] **Step 3: Cập nhật `RequestDetailsTab.svelte` (bảng và modal)**

1. Trong cột Model của bảng request: thêm icon ⚠️ nếu có `item.loopWarning` hoặc `item.anomalies?.length`.
2. Trong Detail Modal Dialog: nếu `selectedDetail.anomalies && selectedDetail.anomalies.length > 0`:
   Hiển thị card cảnh báo màu cam nổi bật:
   - Header: ⚠️ **Anomaly Detected: Possible Loop**
   - Duyệt qua từng finding hiển thị badge rule + description message.

- [ ] **Step 4: Kiểm tra Svelte compilation và Typecheck**

Run: `cd web && pnpm check && pnpm build`  
Expected: PASS, 0 errors

- [ ] **Step 5: Commit**

```bash
git add web/src/
git commit -m "feat(web): show loop detection warnings on recent requests and details modal"
```

---

### Task 6: Kiểm thử Tích hợp Toàn diện và Linting (Verification)

**Files:**
- Test: Toàn bộ suite backend và frontend

- [ ] **Step 1: Chạy toàn bộ backend unit & integration test**

Run: `go test -v -race ./internal/detection/... ./internal/handlers/chat/... ./internal/handlers/...`  
Expected: PASS

- [ ] **Step 2: Chạy staticcheck / golangci-lint**

Run: `golangci-lint run internal/detection/... internal/handlers/...` hoặc `go vet ./...`  
Expected: No issues found

- [ ] **Step 3: Chạy Svelte build kiểm tra production assets**

Run: `cd web && pnpm build`  
Expected: Build thành công trong `web/dist/`

- [ ] **Step 4: Commit changelog fragment**

```bash
git add changes/
git commit -m "chore(changes): add fragment for success rate and loop detection features"
```
