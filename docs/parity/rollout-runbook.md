# Rollout runbook — G25b

Tài liệu này ghi **trình tự chạy** một lần chuyển đổi từ Node sang bản Go, và
**cái gì chặn từng bước**. Bằng chứng đo thuộc `docs/parity/delivery-ledger.md`.

Nguyên tắc chung: mọi bước dưới đây chỉ chạy trên **state synthetic**. Bước nào
chạm dữ liệu thật, deployment thật hay client thật đều có gate riêng, và
approval của plan không đủ để vượt gate đó.

## Trước khi chạy bất cứ gì

| Điều kiện | Vì sao |
| --- | --- |
| Một cặp gateway Node + Go, mỗi bên port/data/home/config **riêng** | Dùng chung DB là downgrade schema trên dữ liệu đang phục vụ, và làm mọi phép so sánh vô nghĩa |
| Snapshot nguồn lấy bằng `VACUUM INTO`, không copy tệp | DB chạy WAL; copy `data.sqlite` bỏ sót mọi ghi còn trong `-wal` |
| Không có biến môi trường nào trỏ vào home thật | Test và rehearsal phải cách ly; xem baseline P0 trong delivery ledger |
| Node runtime thật **không** nằm trong test | Rollback hợp lệ phải giữ được nó; rehearsal không được dừng nó |

Kiểm tra nhanh trước khi tin một lần chạy:

```bash
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./internal/handlers/dashboard/ -run 'TestG05' -count=1
```

## Bước 1 — rehearsal synthetic

Corpus phải phủ **cả bốn** trạng thái quyền truy cập, cộng các trường hợp dễ
mất dữ liệu âm thầm:

| Trạng thái | Payload Node | Kỳ vọng trong Go |
| --- | --- | --- |
| unrestricted | `access:{restricted:false, allow:[]}` | 0 bản ghi |
| restricted-empty | `access:{restricted:true, allow:[]}` | đúng một bản ghi `!*` |
| restricted-list | `access:{restricted:true, allow:[m1,m2]}` | một bản ghi mỗi model |
| malformed | hàng không có `id`, `access` sai kiểu | bị bỏ qua, **không** abort cả import |

Thêm alias/combo grants, proxy binding, và một header vượt trần G17 (20 header
hoặc 8192 byte/giá trị) để chắc báo cáo bắt được nó.

Ba lần chạy, ba kỳ vọng khác nhau:

1. **Dry run** (`?dryRun=1`) — ghi **0 byte**. Đếm row trước/sau trên cả bốn
   bảng; một hàng tăng là hỏng.
2. **Apply** lần đầu — atomic. Nếu lỗi giữa chừng, đích phải y nguyên trạng
   thái trước apply; không được có hàng nửa vời.
3. **Apply** lần hai — idempotent. Cùng corpus, cùng số hàng, không nhân đôi.

Báo cáo dry-run được dán vào ticket và log, nên nó **không được** chứa giá trị
credential. Nó phải nêu tên được thứ sai (`incompatible_header_limit` kèm
`connectionId`, `headerName`, `length`), nếu không thì không hành động được.

Test pin toàn bộ bước này: `TestG05RehearsalRoundTrip`,
`TestG05RehearsalFailureKeepsTargetUnchanged`,
`TestG05RehearsalReportHasNoSecrets`.

## Bước 2 — allow/deny và routing

Rehearsal đếm hàng chỉ nói DB đúng. Nó không nói gateway **hành xử** đúng. Bước
này lái request thật vào mock và so:

- `/v1/models`, Chat, Messages, Responses, media — mỗi đường một lần.
- Key restricted-empty phải bị từ chối ở **mọi** đường, kèm `model_not_allowed`.
- Storage failure phải đóng quyền (503), không mở.
- Combo grant không được cấp quyền gọi trực tiếp leaf model.
- Strict proxy lỗi phải trả 502 và **không** có direct dial nào tới upstream.

Đếm row không thay được bước này: một hàng đúng chỗ với router sai thứ tự sẽ
vẫn cho qua.

## Bước 3 — restart và rollback

Dừng sandbox bằng lifecycle đã xác minh ở Task 7, **không** kill theo port —
kill theo port bắt nhầm process khác và không chứng minh service sạch.

Rollback: artifact **cũ** chạy trên **bản copy data dành riêng cho nó**. Không
cho gateway cũ và mới chung một DB ở bất kỳ thời điểm nào, và không downgrade
schema trên DB đang dùng.

Đo thời gian phục hồi và lặp lại kiểm tra client synthetic sau khi lên lại.

## Bước 4 — gate dữ liệu thật

**Dừng ở đây** nếu chưa được cấp quyền đọc snapshot thật. Approval của
chương trình này không đủ.

Khi có quyền: lập phạm vi riêng, snapshot nhất quán có WAL, giữ source DB không
đổi, sao lưu permissions/Windows ACL cùng dữ liệu, và viết báo cáo không chứa
secret.

## Bước 5 — gate live canary

Cần quyền deployment/provider/account. Port, data, home, config phải tách biệt.
Workload phải đã qua các gate ở trên. Người vận hành chọn client và tỉ lệ
traffic; Node vẫn là đường rollback.

Một canary trên một provider **không** là bằng chứng full parity. Quota, billing
và live access ghi riêng, không bypass gate của provider.

## Bước 6 — gate primary cutover

Cần người dùng xác nhận thao tác chuyển chính. Không tự uninstall, disable hay
restart Node.

Phạm vi chỉ được gọi là "bản Go đầy đủ" khi mọi subsystem task và gate
tray/cloud đa nền tảng đều có bằng chứng. Cloud còn blocked **không** cản một
canary hợp lệ, nhưng cản nhãn đó.

## Trạng thái hiện tại

| Bước | Trạng thái | Chặn bởi |
| --- | --- | --- |
| 1 — rehearsal synthetic | **xong ở mức source** | — (test tự dựng DB synthetic) |
| 2 — allow/deny + routing | chưa | cần gateway sandbox chạy được; G04 đã có test riêng |
| 3 — restart/rollback | chưa | cần artifact P3; artifact bị chặn vì thiếu Bun |
| 4 — dữ liệu thật | chưa | chưa được cấp quyền đọc snapshot thật |
| 5 — live canary | chưa | chưa được cấp quyền deployment |
| 6 — cutover | chưa | chưa có xác nhận của người dùng |

Bước 1 đã chạy được vì nó không cần gì ngoài repo: DB synthetic do test tự
dựng, không network, không process. Bước 2 trở đi đều cần thứ nằm ngoài repo.
