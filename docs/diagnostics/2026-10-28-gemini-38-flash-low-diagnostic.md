# Báo cáo chẩn đoán read-only gateway: Route alias `ag/gemini-3.8-flash-low`

## 1. Ranh giới Client Wire và Handler nội bộ (Verified Source Facts)
- **Endpoint tiếp nhận**: `/v1/chat/completions` (`internal/handlers/router.go:76, 88`), giao thức OpenAI-compatible.
- **Phân giải alias**: `internal/handlers/chat/resolution.go:240-262` map `ag` thành provider `antigravity`, model `gemini-3.8-flash-low`.
- **Luồng nội bộ gateway**:
  - `internal/handlers/chat/fallback.go:999-1003`: `providerCfg.IsGeminiNative()` là `true`, chuyển tiếp qua `forwardGeminiNativeRequest` (`internal/handlers/chat/gemini_handler.go:24`).
  - Giao tiếp với Google Antigravity backend qua `proxy.ForwardGemini` (`gemini_handler.go:102`).
- **Wire trả về cho Client (OpenAI format, không phải raw Gemini)**:
  - Non-stream: `internal/handlers/chat/gemini_handler.go:567` gọi `translator.TranslateGeminiResponseToOpenAI(body)` chuyển đổi sang OpenAI ChatCompletion JSON.
  - Stream (SSE): `internal/handlers/chat/gemini_handler.go:481` gọi `translator.TranslateGeminiChunkToOpenAI(unwrapped, geminiState)` chuyển đổi từng SSE chunk sang OpenAI `chat.completion.chunk`.
  - Client SDK (như ADK `_openai_common.py:479-480`) tự map `finish_reason: "length"` từ gateway thành enum nội bộ `MAX_TOKENS`.

---

## 2. Mapping `max_tokens` / `generationConfig` (Verified Source Facts)
- **Trích xuất `max_tokens`**: `internal/translator/gemini.go:439-442` đọc `oreq.MaxTokens` hoặc `oreq.MaxCompletionTokens` (giá trị probe: 1024).
- **Trích xuất intent**: `internal/translator/gemini.go:446-457` & `internal/thinking/intent.go:50-53`. Request không có `reasoning_effort` / `thinking` / `thinking_budget` dẫn đến `intent = nil`.
- **Tạo generationConfig**: `internal/translator/gemini.go:464-478`:
  - `intent == nil` khiến `thinking.GeminiConfig` trả về `(nil, 0)`.
  - Không khởi tạo `genConfig["thinkingConfig"]`.
  - Khối nâng floor 8192 (`gemini.go:472-475`) không thực thi.
  - `genConfig["maxOutputTokens"] = 1024` (`gemini.go:478`) được gửi nguyên vẹn sang Google.
- **Harden request**: `internal/translator/antigravity.go:658-682` chỉ can thiệp khi `thinkingBudget > 0` (cho `gemini-budget`), không can thiệp vào `maxOutputTokens: 1024`.

---

## 3. Mapping `finish_reason` (Verified Source Facts)
- **Bảng mapping**: `internal/translator/gemini.go:1149-1158` (`geminiFinishToOpenAI`):
  ```go
  case "MAX_TOKENS":
      return "length"
  default:
      return "stop"
  ```
- **Non-stream**: `internal/translator/gemini.go:651-655` map `MAX_TOKENS` -> `finish_reason: "length"`.
- **Stream**: `internal/translator/gemini.go:842-875` chunk cuối mang `candidate.FinishReason`: nếu upstream là `MAX_TOKENS` map thành `length`, nếu `STOP` map thành `stop`.

---

## 4. Tách Reasoning / Content (Verified Source Facts)
- **Phân luồng trong gateway**:
  - Non-stream: `internal/translator/gemini.go:614-623`
  - Stream: `internal/translator/gemini.go:774-779`
  ```go
  if part.Text != "" && (part.Thought == nil || !*part.Thought) {
      delta["content"] = part.Text
  }
  if part.Text != "" && part.Thought != nil && *part.Thought {
      delta["reasoning_content"] = part.Text
  }
  ```
- **Quy tắc chuyển tiếp**: Gateway phụ thuộc hoàn toàn vào cờ boolean `thought: true` của từng Part trong JSON phản hồi từ upstream. Mọi text part có `thought == nil` hoặc `*thought == false` đều được đưa vào `content`.

---

## 5. Phân định Giả thuyết chưa kiểm chứng (Unverified Hypotheses)
1. **Con số ~980 thought tokens**:
   - *Tình trạng*: **Giả thuyết, không đo được**.
   - *Cơ sở*: Chưa có raw upstream metadata/payload thực tế để xác minh `thoughtsTokenCount`. Con số này chỉ là phỏng đoán số học từ hiện tượng tổng tokens vs completion tokens 41.
   - *Source fact*: Non-stream parse `UsageMetadata.CandidatesTokenDetails.ReasoningTokens` (`gemini.go:143-146`), nhưng stream chunk (`gemini.go:156-162`) không có struct field này.
2. **Cơ chế lọt content tiếng Anh phân tích dataset vào text**:
   - *Tình trạng*: **Giả thuyết, chưa có raw upstream payload kiểm chứng**.
   - *Cơ sở*: Tài liệu Google API nêu rằng `includeThoughts: false` sẽ lược bỏ thought text, không khẳng định việc thiếu `includeThoughts` làm mất flag và chuyển thành content text.
   - *Source fact*: Gateway không can thiệp hay sửa đổi nội dung part; việc nội dung phân tích lọt vào `content` là do upstream trả về part đó mà không gắn cờ `"thought": true`. Không thể kết luận thiếu `includeThoughts` gây ra leak hay bật `true` sẽ triệt để hết leak nếu chưa có bằng chứng upstream raw payload.
