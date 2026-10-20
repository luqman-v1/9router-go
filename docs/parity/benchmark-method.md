# Benchmark method — G25a

Tài liệu này ghi **cách đo**, không ghi kết quả. Số liệu thuộc
`docs/parity/delivery-ledger.md`.

## Điều runner không được làm

`benchmark/runner.go` bản trước boot một `startProxyServer` tự viết — một
reverse proxy HTTP vài chục dòng chuyển tiếp sang mock. Nó không đi qua
handler, translator, executor, auth, DB hay bất kỳ đường nào của gateway thật.
Mọi số RPS/p50/p99 từ nó là số của `net/http` stdlib, không phải của 9Router Go.

Nay runner **không**:

- boot gateway, mở listener, hay bind port nào,
- `exec`/kill process,
- mở hay tạo SQLite, ghi `data.sqlite`,
- đọc home thật, `.env`, credential hay log runtime.

Target là một URL do người vận hành chỉ định (`-target` hoặc `BENCH_TARGET`)
trỏ tới gateway đã dựng sẵn trong sandbox G05/G00 với state synthetic riêng.

```bash
go run benchmark/runner.go \
  -target http://127.0.0.1:PORT/v1/chat/completions \
  -key "$BENCH_KEY" \
  -concurrency 1,10,25,50,100 -requests 500 -stream=true
```

Chạy hai lần, mỗi gateway một lần (Node và Go dựng riêng), cùng máy, cùng mock,
cùng payload. Đảo thứ tự hai lượt để thấy nhiễu.

## Ba mốc thời gian, ba nghĩa khác nhau

`measureOnce` trả `sample` gồm `HeaderLatency`, `FirstBodyByte`, `Total`.

| Mốc | Đo từ lúc gửi request tới | Nghĩa |
| --- | --- | --- |
| `HeaderLatency` | byte đầu của response header | gateway quyết định xong, chưa gửi nội dung |
| `FirstBodyByte` | byte đầu tiên của body | chữ đầu tiên nhìn thấy trên dây |
| `Total` | `io.ReadAll` xong | hết response, kể cả trailer |

**`FirstBodyByte` không phải TTFT.** Nó là byte đầu tiên, mà byte đầu tiên của
một SSE hợp lệ thường là metadata, keepalive, hoặc một `data:` rỗng. TTFT thật
phải đo **sau khi parser của từng client format nhận ra semantic event đầu
tiên** — một text delta, một tool call delta, một reasoning delta, hay một
typed error. Ba mốc trên không phân biệt nổi các trường hợp đó, nên không được
in bất kỳ cột nào của chúng dưới nhãn "TTFT".

Chưa có seam parser semantic-event dùng chung trong repo: `ScanStream` chỉ đưa
data payload, `completionProbe` và `carriesValue` là helper nội bộ package
proxy. Gate TTFT phải dựng bản đồ event theo từng client format từ G00 và
translator thật trước, rồi mới thêm mini-plan đo timestamp. Cho tới lúc đó, G25
**chưa xong** — bảng số dưới đây chỉ là header/body latency.

## Success nghĩa là gì

Một request chỉ tính success khi **đủ ba** điều:

1. `client.Do` không lỗi và status `200`,
2. body đọc trọn (không phải EOF sớm, không bị cắt ở `maxBody` = 10 MiB),
3. body có terminal: `data: [DONE]` (OpenAI), `"type":"message_stop"`
   (Anthropic), hoặc `"type":"response.completed"` (Responses); với
   non-stream, body phải parse được thành một JSON document.

Điều 3 là thứ chặn một kịch bản rất cụ thể: proxy chết giữa stream. Header đã
tới, vài delta đã tới, socket im rồi đóng. Nếu chỉ đếm status 200 thì lượt đó
là success, latency thấp giả tạo, và báo cáo khoe một speedup chưa từng xảy ra.

Body bị cắt ở `LimitReader` **không** phải success — `measureOnce` trả lỗi khi
vượt `maxBody`, và `completeBody` trả false khi thiếu terminal.

## Tổng request phải khớp workload

`cfg.Requests` là số request, không phải số "gần đúng". Bản cũ dùng
`Requests / concurrency` nguyên, nên 503 request với 10 worker chỉ bắn 500 và
bảng vẫn trông đầy đủ. Nay là work queue theo index (`atomic.AddInt64`), mọi
index `0..Requests-1` đều được dispatch đúng một lần, phần dư chia cho worker
nào xong trước.

Test pin hai hành vi này: `TestRunBenchmarkDispatchesEveryRequest` (7 request /
3 worker phải thấy đúng 7 hit) và `TestRunBenchmarkRefusesTruncatedStream`
(stream không terminal phải rơi vào `SemanticErr`, `RPS` phải bằng 0).

## Không có success nào thì là failure

`printResultsTable` thoát mã 1 nếu bất kỳ mức concurrency nào có
`SuccessReqs == 0`. RPS tính bằng `success / elapsed`, không phải
`total / elapsed`, nên một lượt toàn lỗi không thể trình bày thành một con số
throughput trông hợp lý.

## Mock đọc `stream` từ body

`mockWantsStream` đọc `{"stream":true}` từ JSON body, chứ không chỉ query
string và `Accept`. Mock cũ nhìn hai thứ sau, nên một gateway stream vì body
sẽ bị đo như JSON. Mock trả 400 khi body không parse được hoặc có document
thứ hai sau document đầu.

Mock bind `127.0.0.1`, không `:port` — không phơi ra mọi interface.

## CPU, RSS, startup

Chưa đo. Chúng phải lấy từ OS process counter của **PID gateway đã xác minh**,
không phải số heap Go của runner:

- Windows: `Get-Process` → `CPU` (giây, delta theo khoảng lấy mẫu),
  `WorkingSet64`, `PeakWorkingSet64`.
- Linux: `/proc/<pid>/status` (`VmRSS`, `VmHWM`) và `/proc/<pid>/stat`.
- macOS: công cụ OS sẵn có; đơn vị phải ghi kèm.

Nếu OS không có metric tương đương, ghi "unsupported", không thay bằng Go heap.
Startup đo tới readiness của gateway synthetic, không cộng thời gian build UI.
Khoảng lấy mẫu cố định, giữ raw sample.

## Chưa làm

| Hạng mục | Trạng thái |
| --- | --- |
| Đo cặp sandbox Node vs Go | chưa — cần gateway dựng sẵn hai bên |
| TTFT theo semantic event | chưa — cần seam parser, xem trên |
| CPU/RSS/startup | chưa — cần PID gateway thật |
| Workload tool/reasoning/images, compression on/off | chưa |
