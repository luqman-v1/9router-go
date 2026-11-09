# Đặc tả Kỹ thuật Discovery: Dịch Định dạng Trực tiếp Pre-Pivot (Direct Routes Discovery)

**Mã phân hệ:** G06, G07, G08, G10, G26  
**Trạng thái:** CONDITIONAL Discovery Gate (Task 1)  
**Vị trí tài liệu:** `internal/translator/direct_routes_discovery.md` (repo `D:/Code/9router-go`)  
**Tài liệu tham chiếu:** `docs/superpowers/specs/2026-10-18-9router-go-translation-design.md`, `docs/superpowers/plans/2026-10-19-9router-go-translation.md`

---

## 1. Tổng quan và Bối cảnh (Context & Problem Statement)

### 1.1. Hiện trạng Pivot trong `HandleMessages`
Trong 9Router Go hiện tại, hàm `HandleMessages` (`internal/handlers/chat/chat.go:259–278`) xử lý các request gửi tới endpoint Claude Messages (`POST /v1/messages`). Nếu provider đích không phải là `claudeNative` (chỉ giới hạn ở Anthropic native hoặc `minimax-code`), hệ thống lập tức thực hiện chuyển đổi trung gian sang OpenAI Chat Completions:

```go
openaiBody, err := translator.TranslateClaudeToOpenAI(body)
```

Payload `openaiBody` này sau đó trở thành `workingBody` và được truyền vào các vòng lặp fallback/combo (`handleMessagesComboFallback` trong `combo.go`).

### 1.2. Hậu quả Suy hao Dữ liệu (Pivot Data Loss)
OpenAI Chat Completions (`/v1/chat/completions`) là định dạng trung gian có sự suy hao cấu trúc nghiêm trọng so với định dạng native của Claude Messages:
1. **Mất cờ lỗi `is_error: true` của Tool Result:**
   - Trong Claude Messages: `{ "type": "tool_result", "tool_use_id": "...", "content": "...", "is_error": true }`.
   - Trong OpenAI Chat Completions: `{ "role": "tool", "tool_call_id": "...", "content": "..." }`.
   - OpenAI schema hoàn toàn không có trường `is_error`. Cờ này bị xóa bỏ hoàn toàn trong `convertClaudeMessage` (`request.go:170–205`). Khi downstream provider (Responses, Gemini, Kiro) nhận payload chuyển tiếp từ OpenAI, nó không còn biết tool execution có lỗi hay không.
2. **Mất mát khối suy nghĩ (Thinking / Reasoning Blocks) và Chữ ký (Thought Signatures):**
   - Claude hỗ trợ các block suy nghĩ `{"type": "thinking", "thinking": "...", "signature": "..."}` và cấu hình ngân sách suy luận (`budget_tokens`).
   - Pivot sang OpenAI biến đổi thinking thành chuỗi `reasoning_effort` mức cao ("low", "medium", "high") và làm rơi rụng hoàn toàn `signature` và cấu trúc block chi tiết.
3. **Phân rã ảnh inline trong Tool Result:**
   - Claude cho phép tool result chứa mảng đa phương tiện: text kết hợp hình ảnh inline dạng base64.
   - Khi pivot sang OpenAI, các khối ảnh bị tách ra thành text placeholder `[Image from tool result ...]` và chèn vào turn user riêng lẻ.
4. **Hiện tượng "Double Translate" trong Combo Fallback:**
   - Khi một combo chứa nhiều leaf model thuộc các họ provider khác nhau (ví dụ: `[kiro/gpt-5.6-sol, gemini/gemini-2.0-flash, muse/muse-spark-1.3, openai/gpt-4o]`), việc convert sớm sang OpenAI khiến các model không phải OpenAI đều phải nhận payload qua 2 lần chuyển đổi: `Claude -> OpenAI -> Kiro`, hoặc `Claude -> OpenAI -> Gemini`.
   - Quá trình chuyển đổi kép làm gia tăng độ trễ CPU, tăng rủi ro lỗi cú pháp schema, và triệt tiêu khả năng tận dụng các tính năng native của provider đích.

---

## 2. Điểm Rẽ nhánh Pre-Pivot trong `HandleMessages` (`chat.go`)

### 2.1. Kiến trúc Pre-Pivot Dispatcher
Điểm rẽ nhánh pre-pivot cần được đặt tại `HandleMessages` trước khi thực hiện bất kỳ phép chuyển đổi nào.

```
Incoming Request: POST /v1/messages
           │
           ▼
┌──────────────────────────────────────────────┐
│  Extract Model & Read rawClaudeBody          │
│  (Bảo toàn rawClaudeBody nguyên bản)         │
└──────────────────────┬───────────────────────┘
                       │
                       ▼
┌──────────────────────────────────────────────┐
│        Pre-Pivot Target Dispatcher           │
└──────┬───────────────┬──────────────┬────────┘
       │               │              │
       ▼               ▼              ▼
┌──────────────┐┌──────────────┐┌──────────────┐┌──────────────────────┐
│ Target:      ││ Target:      ││ Target:      ││ Target:              │
│ Responses    ││ Gemini       ││ Kiro         ││ Claude Native        │
│ (G06 Direct) ││ (G07 Direct) ││ (G08 Direct) ││ (Passthrough)        │
└──────┬───────┘└──────┬───────┘└──────┬───────┘└──────────┬───────────┘
       │               │              │                    │
       │               │              │ (Only if target    │
       │               │              │ is standard        │
       │               │              │ OpenAI Chat)       │
       │               │              │                    ▼
       │               │              │        ┌───────────────────────┐
       │               │              │        │ Pivot via             │
       │               │              │        │ TranslateClaudeToOpenAI│
       │               │              │        └───────────┬───────────┘
       ▼               ▼              ▼                    ▼
┌──────────────────────────────────────────────────────────────────────┐
│                    Execution / Forward Engine                        │
└──────────────────────────────────────────────────────────────────────┘
```

### 2.2. Bảng Ma trận Điều phối Pre-Pivot (Pre-Pivot Dispatch Matrix)

| Provider / Model Pattern | Wire Target | Cơ chế Xử lý |
|---|---|---|
| `claude`, `anthropic`, `minimax-code` | `claude` | **Native Passthrough**: `SanitizeClaudePassthrough`, `AnchorClaudeCache`. |
| `muse`, `muse-spark-*`, Kimi Code (Responses lane), OpenCode Zen (Responses lane) | `openai-responses` | **Direct G06**: Chuyển đổi trực tiếp sang Responses wire (`/v1/responses`). |
| `gemini`, `google`, `gemini-*`, `opencode-go` (Gemini lane) | `gemini` | **Direct G07**: Chuyển đổi trực tiếp sang Gemini `contents` + `systemInstruction`. |
| `kiro`, `kr` | `kiro` | **Direct G08**: Chuyển đổi trực tiếp sang `conversationState` JSON envelope. |
| Các provider OpenAI tiêu chuẩn (`openai`, `deepseek`, `groq`, `openrouter`, ...) | `openai` | **Pivot**: Sử dụng `TranslateClaudeToOpenAI` như hiện tại. |

---

## 3. Cơ chế Bảo toàn Original Body trong Combo & Account Fallback

### 3.1. Vấn đề Immutability của Original Body
Trong kịch bản combo fallback:
- Client gửi request vào model combo: `combo-high-performance = [kiro/gpt-5.6-sol, gemini/gemini-2.0-flash, muse/muse-spark-1.3, deepseek/deepseek-chat]`.
- Giả sử attempt 1 (`kiro/gpt-5.6-sol`) trả về 429 hoặc timeout.
- Hệ thống failover sang attempt 2 (`gemini/gemini-2.0-flash`).
- Nếu request body đã bị convert sang Kiro format ở attempt 1, việc tái sử dụng body đã convert cho attempt 2 là không thể.
- Nếu request body bị convert sang OpenAI format ở đầu handler, cả Kiro và Gemini đều bị mất dữ liệu lỗi `is_error` và blocks suy nghĩ.

### 3.2. Thiết kế Giải pháp: Immutable `RawRequestBody`
1. **Lưu trữ Context / Forward Params:**
   - Trong `forwardRequestParams`, bổ sung trường:
     ```go
     RawClaudeBody []byte // Byte slice nguyên vẹn nhận được từ r.Body của /v1/messages
     SourceFormat  string // "claude"
     ```
2. **Per-Leaf Translation (Chuyển đổi theo từng lá combo):**
   - Không thực hiện chuyển đổi hàng loạt trước vòng lặp combo.
   - Trong vòng lặp `for _, entry := range models` (`combo.go:763`):
     - Xác định `leafTargetFormat := resolveTargetFormat(modelInfo.Provider, modelInfo.Model)`.
     - Sử dụng `RawClaudeBody` để sinh ra payload gửi đi cụ thể cho `leafTargetFormat`:
       - Nếu `leafTargetFormat == "openai-responses"`: gọi direct converter G06 từ `RawClaudeBody`.
       - Nếu `leafTargetFormat == "gemini"`: gọi direct converter G07 từ `RawClaudeBody`.
       - Nếu `leafTargetFormat == "kiro"`: gọi direct converter G08 từ `RawClaudeBody`.
       - Nếu `leafTargetFormat == "claude"`: passthrough từ `RawClaudeBody`.
       - Nếu `leafTargetFormat == "openai"`: gọi `TranslateClaudeToOpenAI` từ `RawClaudeBody`.
3. **Không Biến đổi (Immutability):**
   - `RawClaudeBody` là chỉ đọc (read-only). Không một converter nào được phép mutate slice này.

---

## 4. Đặc tả Chi tiết 3 Đường Dây Wire Mappings

### 4.1. Đường 1: Claude $\rightarrow$ Responses Wire (G06)

#### Cấu trúc Request Đích
Đích đến là OpenAI Responses API (`/v1/responses`).
- **Endpoint**: `/v1/responses`
- **Root Envelope**:
  ```json
  {
    "model": "<upstream_model>",
    "instructions": "<system_prompt>",
    "input": [ ...items... ],
    "stream": true
  }
  ```

#### Quy tắc Ánh xạ Phần tử Input Items:
1. **System Prompt**:
   - Ánh xạ sang trường `instructions` cấp cao nhất của envelope (hoặc item `{ "type": "message", "role": "developer", "content": [...] }`).
2. **User / Assistant Text**:
   - Claude `{ "role": "user", "content": "..." }` $\rightarrow$ Item `{ "type": "message", "role": "user", "content": [{ "type": "input_text", "text": "..." }] }`.
   - Claude `{ "role": "assistant", "content": "..." }` $\rightarrow$ Item `{ "type": "message", "role": "assistant", "content": [{ "type": "output_text", "text": "..." }] }`.
3. **Tool Calls / Uses**:
   - Claude `{ "type": "tool_use", "id": "toolu_01", "name": "calc", "input": {...} }` $\rightarrow$
     Item `{ "type": "function_call", "call_id": "toolu_01", "name": "calc", "arguments": "{\"dividend\":1,\"divisor\":0}" }`.
   - **Bảo toàn ID**: Giữ nguyên chuỗi ID gốc `toolu_01`, không sinh ID giả dạng `call_xxx`.
4. **Tool Result & `is_error`**:
   - Claude `{ "type": "tool_result", "tool_use_id": "toolu_01", "content": "...", "is_error": true }` $\rightarrow$
     Item `{ "type": "function_call_output", "call_id": "toolu_01", "output": "{\"error\":\"...\",\"is_error\":true}", "is_error": true }`.
   - **Bảo toàn lỗi**: Cờ `is_error` và thông điệp lỗi được giữ nguyên vẹn trong payload Responses.
5. **Inline Images trong Tool Result**:
   - Ánh xạ thành các mục `input_image` có cấu trúc mang base64 URI, thay vì chuyển thành placeholder text như đường pivot.
6. **Thinking Blocks**:
   - Claude thinking block $\rightarrow$ Item `{ "type": "reasoning", "summary": "...", "signature": "..." }`.

---

### 4.2. Đường 2: Claude $\rightarrow$ Gemini Wire (G07)

#### Cấu trúc Request Đích
Đích đến là Google Gemini API (`generateContent` / `streamGenerateContent`).
- **Endpoint**: `/v1beta/models/<model>:streamGenerateContent`
- **Root Envelope**:
  ```json
  {
    "systemInstruction": {
      "parts": [{ "text": "<system_prompt>" }]
    },
    "contents": [ ...contents... ],
    "tools": [ ...tool_declarations... ]
  }
  ```

#### Quy tắc Ánh xạ:
1. **System Instruction**:
   - Ánh xạ trực tiếp sang `systemInstruction` cấp cao nhất. Tuyệt đối không nối vào nội dung của user turn đầu tiên.
2. **Contents Array**:
   - Role mapping: `user` $\rightarrow$ `"user"`, `assistant` $\rightarrow$ `"model"`.
   - `tool_use` $\rightarrow$ Part `{ "functionCall": { "name": "...", "args": {...} } }`.
   - `tool_result` $\rightarrow$ Part `{ "functionResponse": { "name": "...", "response": { "result": "...", "is_error": true } } }` nằm trong turn `role: "user"`.
   - Cờ `is_error: true` được bảo toàn trong trường `response` của `functionResponse`.
3. **Làm sạch Schema Công cụ (Schema Sanitization)**:
   - Gemini từ chối (HTTP 400 "Invalid tool parameters") nếu schema chứa các từ khóa không được hỗ trợ.
   - Bắt buộc chạy `CleanParametersSchema`:
     - Loại bỏ `$ref`, `definitions`, `$defs`, `$schema`.
     - Loại bỏ `additionalProperties`, `prefixItems`, `const`.
     - Loại bỏ các extension vendor `x-*`.

---

### 4.3. Đường 3: Claude $\rightarrow$ Kiro Wire (G08)

#### Cấu trúc Request Đích
Đích đến là AWS CodeWhisperer / Kiro API (`generateAssistantResponse`).
- **Giao thức**: HTTP POST native JSON.
- **Root Envelope**:
  ```json
  {
    "conversationState": {
      "chatTriggerType": "MANUAL",
      "conversationId": "<uuid_session>",
      "currentMessage": {
        "userInputMessage": {
          "content": "<content_with_instructions>",
          "modelId": "<model_id>",
          "origin": "AI_EDITOR",
          "userInputMessageContext": {
            "toolResults": [ ... ],
            "tools": [ ... ]
          }
        }
      },
      "history": [ ...prior_turns... ]
    }
  }
  ```

#### Quy tắc Ánh xạ:
1. **Xử lý System Prompt**:
   - Kiro gateway trả về HTTP 400 nếu nhận `systemPrompt` ở root envelope.
   - System prompt được chèn vào nội dung turn user dưới dạng:
     ```
     <instructions>
     <system_prompt>
     </instructions>

     <user_message>
     ```
2. **Lịch sử Lượt trò chuyện (History & Strict Turn Alternation)**:
   - Kiro đòi hỏi tính luân phiên nghiêm ngặt user / assistant. Các turn cùng role liên tiếp phải được gộp (merge).
   - Turn user cuối cùng được tách ra làm `currentMessage.userInputMessage`. Tất cả các turn trước đó đưa vào mảng `history`.
3. **Tool Results và Bảo toàn `is_error`**:
   - Được gắn vào `userInputMessageContext.toolResults`:
     ```json
     {
       "toolUseId": "toolu_01",
       "status": "error",
       "content": [{ "text": "division by zero error" }]
     }
     ```
   - Khi `is_error == true`: `status = "error"`.
   - Khi `is_error == false`: `status = "success"`.
4. **Tool Declarations**:
   - Khai báo danh mục công cụ trong `userInputMessageContext.tools`.

#### Cấu trúc Response Wire của Kiro:
- Phản hồi từ Kiro là luồng nhị phân **AWS EventStream** (`application/vnd.amazon.eventstream`), không phải SSE text.
- Mỗi frame nhị phân gồm:
  - 4 bytes: Total Byte Length.
  - 4 bytes: Headers Length.
  - 4 bytes: Prelude CRC32.
  - Headers: cặp key-value (ví dụ `:event-type`, `:message-type`).
  - Payload: chuỗi JSON sự kiện (ví dụ `assistantResponseEvent`, `toolUseEvent`).
  - 4 bytes: Message CRC32.
- Bộ giải mã EventStream đọc payload trực tiếp và phát sinh Claude SSE chunk tương ứng.

---

## 5. Kiến trúc Stream / JSON Response Adapters

Phản hồi từ upstream trả về client Claude Messages cũng phải được chuyển đổi trực tiếp mà không qua trung gian OpenAI Chat Completions:

```
Upstream Wire Output
  ├── Responses Stream / JSON ──> [ResponsesToClaudeAdapter] ──> Claude SSE / JSON
  ├── Gemini Stream / JSON    ──> [GeminiToClaudeAdapter]    ──> Claude SSE / JSON
  └── Kiro EventStream Frames ──> [KiroToClaudeAdapter]      ──> Claude SSE / JSON
```

### 5.1. Bảng chuyển đổi sự kiện Stream sang Claude SSE

| Upstream Wire Event | Ý nghĩa | Sự kiện Claude SSE đích |
|---|---|---|
| `response.output_item.added` (Responses)<br>`candidate.content` (Gemini)<br>`assistantResponseEvent` (Kiro) | Bắt đầu khối text / xuất hiện token | `content_block_start` (`type: "text"`), `content_block_delta` (`text_delta`) |
| `reasoning` item (Responses)<br>`thought` part (Gemini) | Block suy nghĩ / reasoning token | `content_block_start` (`type: "thinking"`), `content_block_delta` (`thinking_delta`) |
| `function_call` (Responses)<br>`functionCall` (Gemini)<br>`toolUseEvent` (Kiro) | Lệnh gọi công cụ | `content_block_start` (`type: "tool_use"`), `content_block_delta` (`input_json_delta`) |
| `response.completed` (Responses)<br>`finishReason` (Gemini)<br>Stream EOF (Kiro) | Hoàn tất lượt trả lời | `content_block_stop`, `message_delta` (`stop_reason`, `usage`), `message_stop` |

---

## 6. Tiêu chí Dừng (Stop Criteria) & Kế hoạch Thực thi Mã nguồn

### 6.1. Tiêu chí Dừng của Task 1 (CONDITIONAL Discovery Gate)
- [x] Đã hoàn thành tài liệu discovery `internal/translator/direct_routes_discovery.md`.
- [x] Đã xây dựng ma trận test hợp đồng `internal/handlers/chat/direct_routes_dispatch_test.go`.
- [ ] ~~Ma trận ban đầu phủ 5 khía cạnh cốt lõi~~ — **bản đầu không đạt, đã thay bằng bản dưới.**
  Bản đầu mô tả ba wire shape nhưng assert trên chính literal do nó tự dựng: mảng `input[]`
  của Responses, `functionResponse` của Gemini và envelope `conversationState` của Kiro đều
  được marshal rồi đọc lại ngay trong tệp test; bảng dispatch là helper định nghĩa tại chỗ
  không có caller production. Neutron-check: đổi `is_error` trong fixture Claude thành `false`
  — đúng thuộc tính mà `ResponsesWire` tuyên bố kiểm — để lại suite **xanh**. Bốn trên năm
  subtest không thể đỏ, nên chúng mô tả wire shape chứ không kiểm nó. Cùng lớp lỗi với
  `thought_signature_store_persistent_test.go` đã xoá ở G26.

  Ma trận hiện tại chỉ assert qua symbol production:
  1. `TestDirectRoutes_PivotLosesToolErrorState` — tiền đề G06–G08 tồn tại để xoá bỏ; đỏ khi
     pivot bắt đầu giữ `is_error` hoặc signature (dấu hiệu route trực tiếp mất lý do tồn tại).
  2. `TestDirectRoutes_GeminiSchemaSanitization` — đi qua `translator.CleanParametersSchema`.
  3. `TestDirectRoutes_KiroEventStreamDecoding` — đi qua `providers.NewEventStreamReader`, gồm
     cả ca CRC hỏng phải bị từ chối.
  4. `TestDirectRoutes_ComboLeavesShareAnImmutableBody` — đỏ khi converter ghi ngược vào body
     của client.
  5. Bốn hợp đồng chưa có seam production (`ResponsesWire`, `GeminiWire`, `KiroEnvelopeShape`,
     `DispatchMatrix`) là `t.Skip` nêu đúng tên seam còn thiếu. Test bị skip không có giá trị
     "pass"; assertion tự dựng thì có — nên skip là lựa chọn trung thực.
- [x] Neutron-check từng assertion còn sống (mỗi fix đỏ khi bị đảo): tắt kiểm CRC message frame
  → `KiroEventStreamDecoding` đỏ; thay `CleanParametersSchema` bằng identity → đỏ; chèn ghi đè
  một byte tại chỗ trong `TranslateClaudeToOpenAI` → immutability đỏ.
- [x] Bài test đã biên dịch sạch và PASS khi chạy offline:
  ```bash
  GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -v ./internal/handlers/chat/... -run TestDirectRoutes
  ```
  Kết quả: 4 PASS, 4 SKIP, 0 FAIL.
- [x] **Dừng lại (STOP)**: Không tự ý viết mã converter giả lập hoặc can thiệp drop data vào source code nghiệp vụ khi chưa qua review chấp thuận tài liệu discovery.

### 6.2. Kế hoạch Triển khai Tiếp theo (Subsequent Tasks)
Sau khi Task 1 hoàn thành và được kiểm duyệt, quá trình cài đặt mã nguồn sẽ được chia thành các PR/Task tuần tự độc lập:
1. **Task / PR G06 (Claude $\rightarrow$ Responses Direct):**
   - Viết `internal/translator/claude_responses.go`: chuyển đổi hai chiều Claude Messages $\leftrightarrow$ Responses API.
   - Unit test bao phủ request mapping và response stream adapter.
2. **Task / PR G07 (Claude $\rightarrow$ Gemini Direct):**
   - Nâng cấp `internal/translator/gemini.go`: thêm hàm direct converter `ClaudeToGemini` và `GeminiToClaude`.
   - Bảo toàn cờ `is_error` và tích hợp `CleanParametersSchema`.
3. **Task / PR G08 (Claude $\rightarrow$ Kiro Direct):**
   - Bổ sung `ClaudeToKiroDirect` trong `internal/translator/kiro.go`.
   - Bộ giải mã AWS EventStream **đã có** tại `internal/providers/eventstream.go` (dùng chung
     với Bedrock, đã phủ CRC và biên frame); chỉ cần adapter sự kiện Kiro → Claude SSE, không
     viết lại decoder.
4. **Tích hợp Pre-Pivot Dispatcher:**
   - Cập nhật `HandleMessages` (`chat.go`) và `handleMessagesComboFallback` (`combo.go`) để sử dụng
     resolver pre-pivot và bảo toàn `RawClaudeBody`. Khi đặt resolver vào production, bốn test
     `TestDirectRoutes_*` đang skip sẽ được thay bằng assertion thật — tên seam trong
     `skipUntilProductionSeam` là danh sách việc phải làm.
