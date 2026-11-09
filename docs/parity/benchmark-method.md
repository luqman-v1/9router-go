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

### TTFT đo được mà không cần seam mới

Seam đã có: `internal/proxy.ScanStream` phát **từng event hoàn chỉnh** ngay khi
nó tới, không đợi hết body. Runner gọi nó trên `io.TeeReader` để vẫn giữ nguyên
bytes cho gate terminal, và bấm giờ ngay trong callback khi event đầu tiên được
phân loại là semantic. Không cần parser mới trong package proxy; phần còn thiếu
là **classifier**, và classifier thuộc về runner vì nó phải trả lời câu hỏi của
runner ("client thấy token đầu tiên lúc nào"), không phải câu hỏi của gateway.

`classifyEvent` phủ ba wire shape gateway phục vụ:

| shape | semantic | metadata / không tính |
| --- | --- | --- |
| Chat Completions | `choices[0].delta.content`, `.tool_calls`, `.function_call`, `.reasoning_content`, `.reasoning`, `choices[0].text` | `delta.role`, `content:""`, `finish_reason`, `choices:[]` + `usage` |
| Claude Messages | `content_block_delta` với `text_delta`/`thinking_delta`/`input_json_delta`, `content_block_start` với `tool_use`/`thinking` | `message_start`, `message_delta`, `message_stop`, `content_block_start` với `text` rỗng |
| Responses | `response.output_text.delta`, `response.function_call_arguments.delta`, `response.custom_tool_call_input.delta`, `response.reasoning_summary_text.delta`, `response.reasoning_text.delta` | `response.created`, `response.in_progress`, `response.completed` |

`type:"error"` / `response.failed` / envelope `{"error":...}` là **typed error**:
stream kết thúc mà không có câu trả lời, nên không sinh TTFT. Payload không phải
JSON, hoặc shape không mô hình hoá, đọc là metadata — đoán sẽ tạo một TTFT mà
client chưa từng nhận.

TTFT chỉ tính trên request success. `BenchResult` in số `samples` cạnh TTFT, và
một mức concurrency không có semantic event nào hiện **n/a**, không hiện `0ms`:
trung bình của rỗng là 0, và số 0 đó đọc như "token đầu tiên tức thì".

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

Lấy từ OS process counter của **PID gateway đã xác minh** — PID sở hữu socket
đang listen, không phải process cha và không phải số heap Go của runner:

- Windows: `Get-Process` → `CPU` (giây, delta theo khoảng lấy mẫu),
  `WorkingSet64`, `PeakWorkingSet64`.
- Linux: `/proc/<pid>/status` (`VmRSS`, `VmHWM`) và `/proc/<pid>/stat`.
- macOS: công cụ OS sẵn có; đơn vị phải ghi kèm.

Nếu OS không có metric tương đương, ghi "unsupported", không thay bằng Go heap.
Startup đo tới readiness của gateway synthetic, không cộng thời gian build UI.
Khoảng lấy mẫu cố định, giữ raw sample.

## Cặp sandbox: cách dựng

Mỗi bên một gateway thật, state synthetic riêng, không chung DB:

| | Node | Go |
| --- | --- | --- |
| artifact | bản standalone đã publish (`0.5.99-local.4`), không build lại trong lúc đo | `go build ./cmd/9router-go` |
| port | 20928 | 20130 |
| `DATA_DIR` | thư mục riêng, ngoài home thật | thư mục riêng, ngoài home thật |
| state | `POST /api/provider-nodes` → `POST /api/providers` → `POST /api/keys` → `PUT /api/models/alias` | `POST /api/provider-nodes` → `POST /api/connections` → `POST /api/keys` → `PUT /api/models/alias` |
| upstream chung | `127.0.0.1:20199` (mock) | `127.0.0.1:20199` (mock) |

Cả hai gọi **cùng một mock upstream**, nên chi phí mock không thuộc về bên nào.

Model id phải resolve giống nhau ở hai bên. Một model alias trỏ tới **node id**
(`openai-compatible-chat-<uuid>/gpt-4o`), không trỏ tới prefix — vì prefix
`bench` là provider alias trên Node và provider id trên Go, nên nó resolve về
hai chỗ khác nhau và một bên sẽ 404.

## CPU: đo theo workload cố định, không theo "% một core"

Một cửa sổ tải song song (cả hai gateway cùng lúc) chỉ nói **bao nhiêu CPU
cả hai dùng**, chứ không nói **mỗi request tốn bao nhiêu CPU**. Bên nào phục vụ
nhiều request hơn sẽ dùng nhiều CPU hơn, và đó không phải là bất lợi của nó.

Vì vậy CPU-per-request đo bằng cách chạy **một gateway một lượt**, workload cố
định (`-requests N`, concurrency cố định), rồi lấy `ΔCPU / N`. Thứ tự hai bên
đảo qua các lượt để hiệu ứng "chạy trước/chạy sau" rơi vào dữ liệu chứ không
rơi vào kết luận.

Kết quả đo được (2026-10-20, concurrency 50, 2000 request/lượt, 3 lượt):

| bên | CPU ms/request | RPS | WorkingSet64 |
| --- | ---: | ---: | ---: |
| Go | 4.2162 ± 0.2657 | 588.57 | 55.3 MB |
| Node | 3.6197 ± 0.6976 | 318.40 | 356.8 MB |

Hai bên phải đo với cùng chế độ observability. Lượt đầu để Go ghi
`requestDetails` cho mọi request trong khi Node tắt (`OBSERVABILITY_ENABLED=false`);
tắt observability ở Go đưa nó từ 458.6 lên 588.6 RPS và từ 4.98 xuống 4.22
ms/request. Bảng trên là lượt đã tắt observability ở Go cho khớp.

Hai bên đọc ngược nhau: **Node tốn ít CPU mỗi request hơn, nhưng chậm hơn và dùng
nhiều RAM hơn ~6×**. Nghĩa là chỗ Node bị nghẽn không phải CPU mà là event
loop. Báo cáo phải nói đúng điều đó — "Go nhanh hơn" và "Go tiết kiệm CPU hơn"
là hai câu khác nhau, và chỉ câu thứ nhất đúng ở đây.

## Startup

Đo từ lúc launch process tới lúc gateway trả 200 trên route public của nó
(`/health` ở Go, `/api/version` ở Node — `/api/hello` nằm sau dashboard guard
nên trả 401 và không phải readiness signal). Không cộng thời gian build UI: cả
hai bên chạy artifact đã build sẵn.

## Ma trận 12 ô: 12 hình dạng request × 5 mức concurrency

Mỗi ô là một cặp (wire format, feature). Ba lượt, thứ tự Node/Go đảo mỗi lượt,
300 request/mức. Cả hai gateway gọi **cùng** mock upstream trên loopback, nên
số đo mô tả chi phí gateway + translator, không mô tả mạng hay model.

Kết quả 2026-10-20 (Go đã gồm fix session key, xem dưới):

| cell | conc 1 | conc 10 | conc 25 | conc 50 | conc 100 |
| --- | ---: | ---: | ---: | ---: | ---: |
| chat-json-reason | 1.57x | 1.89x | 2.21x | 2.11x | 1.99x |
| chat-json-text | 1.71x | 1.83x | 1.93x | 2.22x | 2.36x |
| chat-json-tool | 1.61x | 1.90x | 1.98x | 1.96x | 1.97x |
| chat-sse-image | 1.59x | 1.84x | 2.03x | 1.90x | 1.90x |
| chat-sse-reason | 1.67x | 1.95x | 2.18x | 2.18x | 2.27x |
| chat-sse-text | 1.81x | 2.30x | 2.33x | 2.16x | 2.33x |
| chat-sse-tool | 1.54x | 2.20x | 2.27x | 1.96x | 2.23x |
| msgs-json-text | 1.76x | 1.87x | 2.21x | 2.01x | 1.95x |
| msgs-sse-text | 1.61x | 2.22x | 2.38x | 2.46x | 2.40x |
| msgs-sse-tool | 1.56x | 1.85x | 2.12x | 2.12x | 2.15x |
| resp-json-text | 1.61x | 1.96x | 2.02x | 2.10x | 2.12x |
| resp-sse-text | 1.62x | 2.36x | 2.44x | 2.62x | 2.65x |

Số trong ô là `RPS(Go) / RPS(Node)`, trung bình 3 lượt. Go cao hơn ở **60/60**
cặp. Min 1.54x, trung bình 2.03x, max 2.65x. Tổng 108 000 request, **0 fail /
0 semantic error** ở cả hai bên.

Nhiễu: stdev/mean của RPS qua 3 lượt có median 6.3 %, p90 12.2 %, max 24.8 %
(`chat-sse-image` conc 50, phía Go). Biên an toàn hẹp nhất — `(ratio − 1) /
độ bất định tổng hợp` — là **3.4**, nên mọi cặp đều vượt nhiễu, kể cả cặp xấu
nhất. Không cặp nào ≤ 1.00x.

### Lỗi đã tìm ra trong lúc đo

Lượt matrix đầu tiên bị nhiễu bởi một crash thật, không phải nhiễu đo: Go chết
với `concurrent map writes` ở mức concurrency ≥ 25 trên lane `/v1/messages` có
tool. Nguyên nhân: khoá session stream sinh từ `time.Now().UnixNano()` một
mình; đồng hồ Windows nhảy ~15.6 ms nên 200 request đồng thời chỉ thấy **hai**
giá trị nano giây khác nhau, 104 request dùng chung một khoá ⇒ chung một
`*StreamState` ⇒ translator ghi map ngoài lock. Sửa bằng
`translator.NewSessionKey(prefix)` thêm bộ đếm `atomic.Uint64`. Sau khi sửa,
ô `msgs-sse-tool` chạy 300/300 sạch ở cả 5 mức.

Bảng trên là lượt chạy **sau** khi sửa. Raw của lượt hỏng giữ ở
`raw/matrix-stale-crash/`, không dùng để trích số.

## TTFT theo semantic event: kết quả 2026-10-20

7 ô streaming × 5 mức × 3 lượt, 300 request/mức, thứ tự Node/Go đảo mỗi lượt.
42 file raw, **0 fail / 0 semantic**, và mọi ô đều có semantic event ở mọi mức
(không ô nào phải in `n/a`).

| cell | c1 | c10 | c25 | c50 | c100 |
| --- | ---: | ---: | ---: | ---: | ---: |
| chat-sse-image | 0.57x | 0.36x | 0.25x | 0.16x | 0.19x |
| chat-sse-reason | 0.56x | 0.34x | 0.23x | 0.15x | 0.12x |
| chat-sse-text | 0.52x | 0.27x | 0.17x | 0.13x | 0.19x |
| chat-sse-tool | 0.57x | 0.28x | 0.21x | 0.16x | 0.17x |
| msgs-sse-text | 0.55x | 0.29x | 0.25x | 0.18x | 0.17x |
| msgs-sse-tool | 0.57x | 0.31x | 0.23x | 0.18x | 0.21x |
| resp-sse-text | 0.57x | 0.30x | 0.24x | 0.17x | 0.16x |

Số trong ô là `TTFT(Go) / TTFT(Node)`, nhỏ hơn 1 là Go tới semantic event sớm
hơn. Go thấp hơn ở **35/35** cặp: min 0.12x, mean 0.29x, max 0.57x. Nói cách
khác, client của Go thấy token đầu tiên sớm hơn **1.75×–2×** ở concurrency 1 và
**2.8×–8×** từ concurrency 10 trở lên. Tỷ lệ tăng theo tải, đúng như một gateway
chặn ở event loop so với một gateway không chặn.

Đọc đúng: đây là thời gian **gateway + translator** tới event đầu tiên trên
loopback, không phải latency model. Không được cộng hay trừ số này với TTFT của
provider thật, và không được trình bày nó như "Go nhanh hơn model".

## Chưa làm

| Hạng mục | Trạng thái |
| --- | --- |
| Nén bật/tắt (RTK, headroom) | chưa — mock không có traffic đủ lớn để nén đổi hình dạng request |
| Provider thật (không mock) | ngoài phạm vi — P4 đo gateway, không đo upstream thật |
| TTFT trên nhánh lỗi (typed error, stream cắt) | test đã pin (`TestMeasureTTFTRejectsTypedError`), nhưng matrix không có ô lỗi |

