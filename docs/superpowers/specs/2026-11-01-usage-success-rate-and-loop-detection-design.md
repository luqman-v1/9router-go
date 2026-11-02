# Thiết kế: Hiển thị Success Rate ở Usage Dashboard và Phát hiện Hallucination / Stuck Loop cho Model

Ngày: 2026-11-01  
Trạng thái: Hoàn tất Builder Check, trình người dùng duyệt trước khi lập kế hoạch triển khai.

---

## 1. Mục tiêu và Bối cảnh (Intent & Context)

### Vấn đề:
1. **Thiếu chỉ số Success Rate tổng thể**:
   - Người dùng hiện chỉ thấy *Total Requests, Input Tokens, Cached Tokens, Output Tokens, Est. Cost* ở tab Overview KPI và các chỉ số tích lũy ở Lifetime Cards.
   - Mặc dù bảng *Node Health* đã có cột `Success %` cho từng node cụ thể, nhưng dashboard chưa có chỉ số tổng quát phản ánh tỷ lệ request thành công/thất bại theo từng khoảng thời gian (`Today`, `24h`, `7d`, `All`).
   - Các request lỗi (`RecordFailureUsage`) hiện chưa được hạch toán đầy đủ vào `usageHistory` và chưa được đẩy vào stream `RecentRequests` khiến giao diện không hiển thị trực quan các chấm đỏ lỗi theo thời gian thực.

2. **Khó nhận biết Model bị kẹt Hallucination / Stuck Loop**:
   - Khi chạy các agent tự động (Claude Code, Roo Code, Codex, Cursor), model thường gặp tình trạng kẹt vòng lặp:
     - Liên tục gọi cùng một tool không đổi (ví dụ lặp lại `Bash` hoặc `Read` nhiều lần mà không hoàn thành tác vụ).
     - Lặp lại chu kỳ 2 tool hoặc 3 tool luân phiên ($A \to B \to A \to B \to A \to B$, ví dụ `Read` $\leftrightarrow$ `Edit`).
     - Lặp lại cùng một lỗi schema/command $\ge 3$ lần mà không biết tự sửa.
   - Dự án mã nguồn mở `agentblazo/blazo` (`packages/detection`) sử dụng các quy tắc tất định (deterministic rules) phân tích chuỗi `spans` và `errors` mang lại độ tin cậy cao, không tốn thêm token LLM và độ trễ gần như bằng 0 ($< 0.1\text{ms}$).

---

## 2. Các quyết định kỹ thuật từ Builder Check (Engineering Decisions)

1. **Phạm vi phân tích Loop (Session Scope)**:
   - **Quyết định**: Phân tích phi trạng thái (stateless) trực tiếp trên `requestBody.messages` của mỗi request.
   - **Lý do**: Các agent như Claude Code / Cursor luôn gửi toàn bộ lịch sử hội thoại (gồm tất cả các lượt tool use và tool result trước đó) trong mỗi turn. Việc phân tích chuỗi này ngay trên payload gửi lên vừa nắm trọn vẹn diễn biến của agent session, vừa giữ gateway hoàn toàn stateless, không cần session engine phức tạp.

2. **Xử lý ngắt request từ phía Client (Client Aborts / HTTP 499)**:
   - **Quyết định**: Phân loại request bị người dùng ngắt (Ctrl+C / timeout client) thành `status: "canceled"`, **không tính vào `TotalErrors`** khi tính `SuccessRate`.
   - **Lý do**: Tránh làm sai lệch chỉ số chất lượng của model khi người dùng thường xuyên ngắt sinh văn bản trong quá trình sử dụng interactive CLI.

3. **Điểm kiểm tra Loop (Detection Source)**:
   - **Quyết định**: Kiểm tra trên mảng `messages` của `requestBody` (hỗ trợ cả OpenAI `tool_calls` và Claude `tool_use`).
   - **Lý do**: Tránh can thiệp vào streaming response buffer (`metrics.ResponseBuf`) đang truyền tải realtime, đảm bảo hiệu năng tối đa và không có nguy cơ parse lỗi SSE chunks.

4. **Lưu trữ và Đồng bộ cảnh báo (Persistence & Sync)**:
   - **Quyết định**: 
     - Lưu mảng `anomalies: []LoopFinding` vào `requestDetails.data` để xem chi tiết lâu dài.
     - Đưa trường `loopWarning?: string` vào in-memory ring `tracker.recentRing` để SSE stream đẩy realtime và tab Overview giữ được badge cảnh báo khi reload.

---

## 3. Thiết kế Chi tiết: Success Rate trên Usage Dashboard

### 3.1. Backend Aggregation (`internal/handlers/usage_stats.go`, `usage_dashboard.go`, `chat/usage.go`)
- **Ghi nhận request lỗi**:
  - Trong `RecordFailureUsage` (`internal/handlers/chat/usage.go`):
    - Ghi nhận vào `usageHistory` với `status: "error"` (hoặc `status: "canceled"` nếu client abort), `cost: 0`, `promptTokens: 0`, `completionTokens: 0`.
    - Gọi `usagetracker.GetTracker().PushRecent(...)` với `Status: "error"` để SSE stream hiển thị chấm đỏ realtime.
    - Cập nhật số đếm lỗi trong `usageDaily` (`errors` count).
- **Tính toán Success Rate**:
  - Trong `UsageStatsResponse` (`/api/usage/stats`):
    ```go
    type UsageStatsResponse struct {
        TotalRequests         int      `json:"totalRequests"`
        TotalErrors           int      `json:"totalErrors"`
        SuccessRate           *float64 `json:"successRate"` // (TotalRequests - TotalErrors) / TotalRequests * 100
        ...
    }
    ```
  - Trong `dashboardLifetime` (`/api/usage/dashboard`):
    - Bổ sung `SuccessRate *float64` tính trên toàn bộ lịch sử requests.

### 3.2. Frontend Presentation
- **Overview KPI Cards (`SummaryKpiCards.svelte`)**:
  - Thêm thẻ thứ 6: **Success Rate**.
  - Hiển thị:
    - Giá trị: `${stats.successRate.toFixed(1)}%` (nếu chưa có request thì hiển thị `—`).
    - Subtext: `${totalRequests - totalErrors}/${totalRequests} requests`.
    - Màu sắc:
      - Xanh lá (`text-success`): $\ge 95\%$
      - Vàng hổ phách (`text-warning`): $80\% - 94.9\%$
      - Đỏ (`text-red-500`): $< 80\%$
- **Lifetime Cards (`UsageDashboard.svelte`)**:
  - Bổ sung thẻ `Lifetime Success Rate` vào danh sách thẻ Lifetime bên cạnh `Cache Hit Rate`.

---

## 4. Thiết kế Chi tiết: Hallucination & Stuck Loop Detection

### 4.1. Engine phát hiện loop (`internal/detection`)
Package Go độc lập `internal/detection`:
- Đầu vào: danh sách `messages` từ request (chuẩn hóa cả OpenAI `tool_calls` và Claude `tool_use`).
- 3 Quy tắc tất định (Deterministic Rules theo chuẩn Blazo):
  1. `consecutive_tool`: Cùng 1 tool được gọi $\ge 3$ lần liên tiếp.
     - Ví dụ: `[Bash, Bash, Bash]` $\to$ Cảnh báo: `Possible loop: "Bash" called 3 times in a row`.
  2. `cycle_pattern`: Chuỗi 2 hoặc 3 tool lặp lại $\ge 2$ chu kỳ ở phần đuôi hội thoại.
     - Chu kỳ 2 tool: $[A, B, A, B]$ $\to$ `Possible loop: pattern [Read, Edit] repeated 2 times`.
     - Chu kỳ 3 tool: $[A, B, C, A, B, C]$ $\to$ `Possible loop: pattern [Read, Edit, Bash] repeated 2 times`.
  3. `repeated_error`: Cùng 1 lỗi hoặc cùng 1 command lỗi trong `tool_result` lặp lại $\ge 3$ lần trong phiên.
     - `Error "command failed: exit code 1" occurred 3 times`.
- Cấu trúc trả về:
  ```go
  type FindingSeverity string
  const (
      SeverityWarning  FindingSeverity = "warning"
      SeverityCritical FindingSeverity = "critical"
  )

  type LoopFinding struct {
      Rule     string          `json:"rule"`     // consecutive_tool | cycle_pattern | repeated_error
      Severity FindingSeverity `json:"severity"` // warning | critical
      Message  string          `json:"message"`
      Tool     string          `json:"tool,omitempty"`
      Count    int             `json:"count,omitempty"`
  }
  ```

### 4.2. Giao diện Cảnh báo (Frontend UI)
1. **Recent Requests List (Overview Tab & Details Tab)**:
   - Cạnh tên Model: nếu có `req.loopWarning`, hiển thị icon cảnh báo màu hổ phách/cam ⚠️ kèm tooltip nội dung phát hiện (ví dụ: `⚠️ Loop: "Bash" called 4 times in a row`).
2. **Request Detail Modal Dialog (`RequestDetailsTab.svelte`)**:
   - Nếu request có `anomalies`: hiển thị một card cảnh báo riêng phía trên phần Request Messages:
     - Header: ⚠️ **Anomaly Detected: Possible Loop**
     - Severity badge: Warning (vàng) hoặc Critical (đỏ).
     - Chi tiết vi phạm: mô tả rõ tool nào lặp lại, chu kỳ lặp là gì, hoặc lỗi lặp lại bao nhiêu lần.

---

## 5. Ranh giới & Ràng buộc (Non-goals & Constraints)

- **Non-goals**:
  - Không tự động ngắt kết nối (kill switch) hoặc từ chối request của client khi phát hiện loop; 9router-go chỉ đóng vai trò cảnh báo trực quan cho người dùng theo dõi và can thiệp.
  - Không dùng mô hình AI phụ hoặc LLM để đoán loop (tránh phát sinh chi phí token và độ trễ).
- **Constraints**:
  - Hiệu năng phân tích loop trong Go phải đạt sub-millisecond ($< 0.1\text{ms}$), không ảnh hưởng đến throughput proxy.
  - Tương thích 100% với cả 2 giao thức phổ biến: OpenAI format (`tool_calls`) và Anthropic Claude format (`tool_use`).
  - Không phá vỡ backward compatibility của API `/api/usage/stats` và `/api/usage/dashboard`.

---

## 6. Kế hoạch Kiểm thử & Xác minh (Verification Plan)

1. **Unit Tests Backend**:
   - `internal/detection/detect_test.go`:
     - Test lặp công cụ liên tiếp: 1 lần, 2 lần (pass), 3 lần (trigger warning), $\ge 5$ lần (trigger critical).
     - Test chu kỳ $[A, B, A, B]$ và $[A, B, C, A, B, C]$.
     - Test chuỗi không có loop, chuỗi công cụ xen kẽ hợp lệ.
     - Test parse tool calls từ cả payload OpenAI và Claude format.
   - `internal/handlers/usage_stats_test.go`:
     - Test tính toán `TotalErrors` và `SuccessRate` với các kịch bản: 100% success, 50% success, 0 request.
2. **E2E / Component Tests Frontend**:
   - Test render card `Success Rate` trong `SummaryKpiCards.svelte`.
   - Test render icon cảnh báo ⚠️ trên `recentRequestsCard` và modal detail.
3. **Build & Lint Verification**:
   - Chạy `go test ./...`
   - Chạy `golangci-lint run` / `staticcheck ./...`
   - Chạy `pnpm check` (svelte-check) và `pnpm build` trong `web/`.
