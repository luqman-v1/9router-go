# Đặc tả Kỹ thuật Discovery: Nén Tin nhắn Trực tiếp với Headroom (In-Flight Chat Compression G14)

**Mã phân hệ:** G14 (Headroom In-Flight Chat Compression), G18 (SafeTransport / SSRF Protection), G03 (Downstream Lifecycle / Context Cancellation)  
**Trạng thái:** CONDITIONAL Discovery Gate (Task 2)  
**Vị trí tài liệu:** `internal/headroom/headroom_discovery.md` (repo `D:/Code/9router-go`)  
**Tài liệu tham chiếu:** `.superpowers/sdd/2026-10-19-9router-go-optimizations/task-2-brief.md`, `internal/db/settings.go`, `internal/headroom/detect.go`

---

## 1. Tổng quan và Bối cảnh (Context & Problem Statement)

### 1.1. Bản chất của Nén Tin nhắn Trực tiếp (In-Flight Chat Compression)
Trong các phiên làm việc kéo dài của AI Agent (như Cline, Roo Code, Claude Desktop, Cursor), lịch sử hội thoại (`messages`) tăng trưởng rất nhanh do các lượt trao đổi mã nguồn, log thực thi công cụ (`tool_result`), diff và dữ liệu ngữ cảnh. Việc gửi toàn bộ ngữ cảnh chưa qua xử lý lên các mô hình LLM dẫn đến:
1. **Chi phí token bùng nổ**: Token đầu vào chiếm phần lớn chi phí API hàng tháng.
2. **Nguy cơ tràn cửa sổ ngữ cảnh (Context Window Exhaustion)**: Khi lịch sử vượt quá giới hạn mô hình, yêu cầu bị từ chối với lỗi HTTP 400.
3. **Độ trễ gia tăng (TTFT - Time To First Token)**: Kích thước prompt càng lớn, thời gian xử lý pre-fill của LLM upstream càng lâu.

### 1.2. Vai trò của Headroom Proxy trong 9Router
Headroom là một tiến trình proxy ngoài (external compression proxy), hỗ trợ các thuật toán nén ngữ cảnh thông minh (lossy semantic compression, AST tree-sitter compression, Kompress-v2 ML model).
9Router tích hợp Headroom theo luồng:
```
Client ──► 9Router Gateway ──► Headroom (/v1/compress) ──► 9Router Gateway ──► Upstream Provider (LLM)
                                       │
                                [Fail-Open Gate]
```
- Headroom hoạt động như một bộ lọc tiền xử lý (pre-filter) ngay trước khi request được gửi sang upstream LLM executor.
- **Nguyên tắc cốt lõi**: Nén là một tính năng tối ưu hóa bổ trợ, **tuyệt đối không được phép làm đứt gãy luồng chính** hoặc gây sai lệch cấu trúc dữ liệu của client.

### 1.3. Mục tiêu của CONDITIONAL Discovery Gate G14
CONDITIONAL Discovery Gate G14 thiết lập:
1. **Hợp đồng chuẩn hóa** cho endpoint `/v1/compress` giữa 9Router Go và Headroom proxy.
2. **Quy chuẩn kiểm định vùng cho phép (Permitted-Region Diff)**: Bảo vệ bất khả xâm phạm cấu trúc tin nhắn, định danh công cụ, chữ ký tư duy và khối đa phương tiện.
3. **Cơ chế chống CCR Phantom**: Ngăn chặn hoàn toàn việc nhận dạng token nén CCR mà gateway không thể phục hồi.
4. **Vòng đời Downstream & Fail-Open an toàn**: Đảm bảo fail-open khi compressor gặp sự cố, nhưng ngắt tức thì khi client hủy kết nối (G03).
5. **Hạ tầng mạng an toàn (G18)**: Chống SSRF với các endpoint tùy chỉnh.

---

## 2. Đặc tả Hợp đồng Endpoint `/v1/compress`

### 2.1. Địa chỉ Endpoint và Phương thức
- **URL**: Kế thừa từ `settings.HeadroomUrl`, mặc định là `http://localhost:8787/v1/compress`.
- **Phương thức HTTP**: `POST`.
- **Headers**:
  - `Content-Type: application/json`
  - `Authorization: Bearer <HEADROOM_API_KEY>` (nếu có cấu hình API key; nếu không có thì bỏ qua).

### 2.2. Cấu trúc Request Payload
9Router Go gửi lên Headroom payload theo định dạng JSON chuẩn:
```json
{
  "messages": [
    {
      "role": "user",
      "content": "Text nội dung cần nén..."
    },
    {
      "role": "assistant",
      "content": "Nội dung assistant...",
      "tool_calls": [
        {
          "id": "call_12345",
          "type": "function",
          "function": {
            "name": "read_file",
            "arguments": "{\"path\": \"main.go\"}"
          }
        }
      ]
    }
  ],
  "model": "gpt-4o",
  "config": {
    "mode": "lossy_inline"
  }
}
```
**Quy tắc cấu hình**:
- `mode`: Bắt buộc là `"lossy_inline"`.
- Không sử dụng chế độ CCR (`mode: "ccr"`) vì 9Router không hỗ trợ endpoint `headroom_retrieve`.
- Không gửi trường `frozen_message_count` tùy tiện.

### 2.3. Cấu trúc Response Payload từ Headroom
Headroom phản hồi kết quả nén dạng JSON:
```json
{
  "messages": [
    {
      "role": "user",
      "content": "Text đã được rút gọn..."
    },
    {
      "role": "assistant",
      "content": "Nội dung assistant...",
      "tool_calls": [
        {
          "id": "call_12345",
          "type": "function",
          "function": {
            "name": "read_file",
            "arguments": "{\"path\": \"main.go\"}"
          }
        }
      ]
    }
  ],
  "tokens_before": 1250,
  "tokens_after": 780,
  "tokens_saved": 470,
  "compression_skipped": false,
  "skip_reason": "",
  "ccr_hashes": []
}
```

---

## 3. Giới hạn Thời gian và Cơ chế Fail-Open

### 3.1. Ràng buộc Timeout (Timeout Bounds)
- Thời gian chờ tối đa cho một lượt nén được kế thừa chặt chẽ từ cấu hình hệ thống:
  ```go
  settings.HeadroomTimeoutMs // Mặc định 3000ms (từ internal/db/settings.go)
  ```
- Tuyệt đối không tự ý định nghĩa timeout tùy tiện hoặc hardcode các giá trị không đồng bộ với cấu hình chung.
- Quản lý timeout thông qua `context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)`.

### 3.2. Cơ chế Fail-Open Tuyệt đối
Khi xảy ra một trong các sự cố sau từ phía compressor:
1. **Compressor Timeout**: Yêu cầu vượt quá `timeoutMs`.
2. **Lỗi mạng (Network Error)**: Connection refused, socket drop đột ngột, DNS failure.
3. **HTTP Status Code bất thường**: Proxy trả về mã lỗi HTTP 4xx hoặc 5xx.
4. **JSON Malformed**: Phản hồi không parse được JSON hợp lệ.

**Hành vi xử lý**:
- Client **fail-open an toàn**: Lập tức hủy kết nối tới compressor, giữ nguyên 100% `body` gốc và trả về `(body, false, nil)`.
- Không phát sinh lỗi gián đoạn pipeline xử lý chat, không làm sập gateway, không panic.

---

## 4. Quy tắc Kiểm định Vùng cho phép (Permitted-Region Diff)

Một rủi ro lớn của các thuật toán nén LLM bên ngoài là việc vô tình làm thay đổi cấu trúc dữ liệu, cắt mất tham số công cụ, hoặc làm đảo lộn ngữ cảnh đối thoại. 9Router Go áp dụng bộ lọc kiểm định cấu trúc nghiêm ngặt (Strict Structural Guard).

### 4.1. Vùng Được phép Nén (Permitted Regions)
Chỉ duy nhất nội dung text thông thường được phép rút gọn:
- Trường `content` dạng chuỗi (`string`) của tin nhắn `user`.
- Trường `content` dạng chuỗi (`string`) của kết quả thực thi công cụ (`tool` / `tool_result`).
- Khối `{"type": "text", "text": "..."}` nằm trong mảng `content`.

### 4.2. Vùng Bất khả Xâm phạm (Inviolable Regions)
Bất kỳ sự thay đổi nào đối với các thành phần sau đều bị coi là **vi phạm cấu trúc (Structure Tampering)** và kết quả nén sẽ bị **TỪ CHỐI NGAY LẬP TỨC**:

| STT | Thành phần bảo vệ | Tiêu chuẩn kiểm tra | Hành vi khi vi phạm |
|---|---|---|---|
| 1 | **Số lượng tin nhắn** | `len(candMessages) == len(srcMessages)` | Từ chối, giữ nguyên payload gốc |
| 2 | **Thứ tự các roles** | `candMessages[i].role == srcMessages[i].role` cho mọi `i` | Từ chối, giữ nguyên payload gốc |
| 3 | **Định danh Tool Call** | Khớp chính xác `tool_call_id` và `id` trên từng message | Từ chối, giữ nguyên payload gốc |
| 4 | **Tên và chữ ký hàm** | `function.name` và `function.arguments` phải khớp 100% | Từ chối, giữ nguyên payload gốc |
| 5 | **Khối suy luận (Thinking)** | `thinking`, `redacted_thinking`, `thought`, `thought_signature`, `reasoning_content` phải giữ nguyên byte-for-byte | Từ chối, giữ nguyên payload gốc |
| 6 | **Khối đa phương tiện** | Khối `image`, `image_url`, `document`, `inlineData`, base64/URL phải nguyên vẹn byte-for-byte | Từ chối, giữ nguyên payload gốc |

---

## 5. Cơ chế Phòng vệ CCR và Tiết kiệm Ảo (CCR & Phantom Guard)

### 5.1. Cấm Tuyệt đối CCR (Context Compression Retrieval)
Headroom hỗ trợ chế độ CCR (thay thế văn bản bằng mã hash và lưu blob trong kho riêng để truy xuất lại sau). Tuy nhiên:
- 9Router là một stateless gateway, không triển khai endpoint hoặc bộ đệm lưu trữ `headroom_retrieve`.
- Nếu mô hình LLM nhận được tin nhắn chứa mã hash CCR mà không có nội dung thực, LLM sẽ sinh kết quả rác hoặc từ chối xử lý.

**Các tầng phòng thủ chống CCR**:
1. **CCR Hashes Guard**: Kiểm tra trường `ccr_hashes` trong response JSON. Nếu tồn tại bất kỳ phần tử nào (`len(ccr_hashes) > 0`), từ chối ngay.
2. **Deep Byte Scan Guard**: Quét toàn bộ phản hồi dạng byte thô tìm chuỗi `<<ccr:`. Nếu phát hiện marker `<<ccr:`, từ chối ngay lập tức.

### 5.2. Kiểm soát Tiết kiệm Ảo (Phantom Savings Guard)
- Khi `compression_skipped: true`: Headroom chủ động bỏ qua việc nén (do văn bản quá ngắn hoặc không tìm thấy điểm tối ưu). Gateway giữ nguyên payload gốc.
- Khi `tokens_saved <= 0`: Không có hiệu quả tiết kiệm thực tế, gateway giữ nguyên payload gốc.

---

## 6. Quản lý Vòng đời Downstream & Hủy Bỏ Tức thì (G03)

### 6.1. Hủy bỏ Tức thì từ Downstream (Downstream Cancellation)
Tuân thủ nghiêm ngặt tiêu chuẩn vòng đời tài nguyên G03 của 9Router:
```go
if ctx.Err() != nil {
    return nil, false, ctx.Err()
}
```
- Khi client phía dưới ngắt kết nối (`ctx.Done()`), `HeadroomClient` lập tức dừng mọi hoạt động I/O mạng đang diễn ra tới Headroom.
- Không tiếp tục gửi request hay chờ phản hồi từ compressor khi client đã hủy phiên.
- Trả về trực tiếp lỗi của context (`context.Canceled` hoặc `context.DeadlineExceeded`), ra tín hiệu dừng toàn bộ pipeline dispatch.

### 6.2. Phân biệt Trạng thái Lỗi
- **Downstream Canceled**: Dừng toàn bộ pipeline, trả về lỗi non-nil.
- **Compressor Error / Timeout**: Fail-open an toàn, tiếp tục chuyển tiếp payload gốc với `err == nil`.

---

## 7. Bảo vệ Mạng An toàn (G18 SafeTransport & SSRF Defense)

### 7.1. Chống SSRF cho Headroom URL Tùy chỉnh
Người dùng có thể cấu hình `HeadroomUrl` tùy ý trong bảng cài đặt dashboard. Điều này tiềm ẩn nguy cơ SSRF (Server-Side Request Forgery) nhắm vào hạ tầng nội bộ, mạng private hoặc cloud metadata.

### 7.2. Tích hợp SafeTransport và Cơ chế Cho phép Loopback
- `HeadroomClient` tích hợp `proxy.NewSafeTransport()` từ `internal/proxy/ssrf.go`.
- Khi URL trỏ đến loopback (`localhost`, `127.0.0.1`, `::1`, `0.0.0.0`) được xác thực qua `IsLoopbackHeadroomUrl`:
  Context được gắn cờ `proxy.WithAllowLoopback(ctx)`, cho phép kết nối tới tiến trình Headroom cục bộ.
- Nếu URL trỏ đến các dải IP riêng tư khác (10.0.0.0/8, 192.168.0.0/16, AWS metadata 169.254.169.254):
  `SafeTransport` tự động chặn kết nối với lỗi `ErrSSRFBlocked`, bảo vệ an toàn tuyệt đối cho hệ thống.

---

## 8. Ma trận Kiểm thử Hợp đồng Client (Client Contract Test Matrix)

Ma trận kiểm thử được hiện thực hóa trong `internal/headroom/client_test.go` (`TestHeadroomClient_CompressContract`):

| Tên Test Case | Kịch bản Kiểm thử | Kỳ vọng Xử lý |
|---|---|---|
| `SuccessfulCompression` | Mock `/v1/compress` trả về text đã rút gọn hợp lệ | Trả về payload nén mới, `compressed == true`, `err == nil` |
| `StructureTamperingRejected` | Mock server làm mất `tool_calls`, đổi thứ tự message, hoặc thay đổi tham số hàm | Phát hiện vi phạm cấu trúc, trả về payload gốc, `compressed == false` |
| `CCRHashesRejected` | Mock server trả về `ccr_hashes: ["hash_1"]` | Từ chối kết quả nén, trả về payload gốc, `compressed == false` |
| `CCRMarkerRejected` | Mock server trả về text chứa `<<ccr:blob_123>>` | Từ chối kết quả nén, trả về payload gốc, `compressed == false` |
| `CompressionSkipped` | Mock server trả về `compression_skipped: true` | Trả về payload gốc, `compressed == false`, `err == nil` |
| `TimeoutFailOpen` | Mock server trễ quá `timeoutMs` quy định | Client timeout, fail-open trả về payload gốc, không panic |
| `NetworkErrorFailOpen` | Mock server ngắt kết nối socket đột ngột | Client fail-open trả về payload gốc an toàn |
| `DownstreamCancelStopsDispatch` | `ctx` đã bị hủy trước khi gọi `Compress` | Ngắt I/O ngay lập tức, trả về `context.Canceled`, dừng dispatch |
| `ThinkingAndImagePreservation` | Payload chứa khối `thinking` và `image_url` | Text được nén nhưng thinking và image được bảo tồn 100% |
