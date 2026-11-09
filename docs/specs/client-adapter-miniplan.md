# Mini-Plan Triển khai — Task 3: Bộ Adapter 9 Client (G24b)

**Repo:** `9router-go` · **Ngày:** 2026-10-23
**Đặc tả chi tiết:** `docs/specs/client-schema-matrix.md` (553 dòng, Discovery Gate)
**Đặc tả phụ thuộc:** `docs/specs/safe-file-operations.md` (Task 1) · `docs/specs/config-patch-spec.md` (Task 2) · `connect-cli-spec.md` §3.2.4 (Task 5, tier precedence)

**Trạng thái:** Mini-plan trình duyệt — **chưa được duyệt**. Chưa viết dòng mã nguồn nào.

---

## 0. Tóm tắt điều hành

Task 3 là **adapter layer**: nó biết *client nào, tệp nào, key nào*, và nó **không** biết gì về network, CLI flags, hay exit code. Ba thứ đó thuộc Task 4/5.

Điều kiện tiên quyết đã có trong cây nguồn (không phải giả định):

| Prerequisite | Trạng thái | Bằng chứng |
| --- | --- | --- |
| Task 1 — `resolveGuard`, `writeExclusiveBackup`, `WriteFileAtomic` | **có** | `internal/clientconfig/safefile.go:127`, `backup.go:35`, `atomicwrite.go:74` |
| Task 2 — `Patch(src, format, edits)` | **có**, kèm hạn chế ở §1.2 | `internal/clientconfig/configpatch.go:80` |
| Task 4 — URL validate, redirect policy, password, dry-run | **có** | `connect_url.go`, `connect_redirect.go`, `connect_password*.go`, `connect_dryrun.go` |
| Package `internal/clientconfig/` | **tồn tại** | 7036 LoC, 0 importer tới trước Task 5 |
| Task 3 — adapter | **chưa có** | Không có `adapter.go`, `paths.go`, `claude.go`, … |

**Task 3 viết được ngay** — không bị chặn bởi Task 1/2/4/5. Nó bị chặn chỉ bởi STOP gate của chính mini-plan này.

---

## 1. Ràng buộc kế thừa — hai điều đã đo, không phải đọc từ spec

Spec §5 mô tả chuỗi gọi lý thuyết. Đối chiếu với code thật cho hai kết quả khác nhau, và cả hai đều đổi thiết kế.

### 1.1 Task 1 chỉ dùng được từ **trong** package `clientconfig`

`go doc ./internal/clientconfig` liệt kê đúng ba hàm công khai có đụng đĩa:

```
func WriteFileAtomic(g guard, content []byte, h hooks) error
func BackupAndWrite(g guard, oldContent, content []byte, now func() time.Time, h hooks) (WriteResult, error)
```

`guard` và `hooks` là **type chưa export**. Một package khác không dựng được `guard` (`resolveGuard` cũng không export), không truyền được `hooks`, và không gọi được `writeExclusiveBackup` (nhận `rootFS`, cũng chưa export).

**Hệ quả:** adapter **phải** nằm trong `package clientconfig`. Đây không phải lựa chọn phong cách — nó là hệ quả của việc Task 1 giữ đường ghi ở mức package-private. Spec §8.1 đã đặt `internal/clientconfig/claude.go` và điều đó tình cờ đúng, nhưng vì lý do khác (gom nhóm); lý do thật là **không có API công khai nào khác**.

### 1.2 `Patch` chỉ ghi được **scalar**, và chỉ **thêm hoặc sửa** — không xoá, không tạo container

`Patch` nhận `[]Edit{KeyPath []string, Value string, CreateIfMissing bool}`. Ở tầng format:

```go
// jsonc.go:222 — gặp giá trị là object hoặc array:
if m.val.isContainer() {
    return nil, fmt.Errorf("%w: value of %q is not a scalar", ErrUnsupportedStructure, part)
}
```

Ba giới hạn, mỗi cái chặn một nhóm yêu cầu của Bảng 1:

| Yêu cầu trong `client-schema-matrix.md` | `Patch` hiện tại | Cần gì |
| --- | --- | --- |
| `claude` ghi 6 env **scalar** (`env.ANTHROPIC_BASE_URL`, …) | **làm được** | `Edit{KeyPath: ["env","ANTHROPIC_BASE_URL"], Value: v, CreateIfMissing: true}` |
| `claude` reset: **xoá** 6 key, rồi xoá `env` nếu rỗng | **không làm được** | primitive `Delete` |
| `opencode`: `provider["9router"] = {npm, options:{baseURL, apiKey}, models:{…}}` | **không làm được** | ghi object lồng |
| `droid`: chèn phần tử vào **đầu mảng** `customModels` rồi re-index toàn mảng | **không làm được** | sửa mảng |
| `codex` (TOML): `model_providers["9router"].http_headers.Authorization` | **làm được** nếu bảng đã có; `CreateIfMissing` tự thêm `[table]` | |
| `crush`/`pi`/`omp`: `providers["9router"] = { type, base_url, api_key, models:[…] }` | **không làm được** | ghi object + mảng |

**Đây là phát hiện chính của mini-plan này.** Spec §5 giả định `Patch` phủ được toàn bộ Bảng 1; thực tế nó chỉ phủ nhóm **scalar**. Không có primitive nào cho object/mảng/xoá, và `ErrUnsupportedStructure` là fail-closed — adapter gọi `Patch` cho `provider["9router"]` sẽ nhận lỗi, không phải ghi sai.

### 1.3 Ba lựa chọn, và cái nào là đúng

| # | Cách | Ưu | Nhược |
| --- | --- | --- | --- |
| **A** | Mở rộng `Edit` thành `func(*jsonNode) []byte` (thêm `Delete bool`, `ValueJSON string`, `InsertAt`) | Một chỗ, đúng tinh thần "line-range splice" | Sửa Task 2 đã APPROVED; mỗi format cần scanner riêng cho cấu trúc — JSON ~150 LoC, TOML ~200, YAML ~200. Rủi ro hồi quy cao nhất |
| **B** | Adapter tự dựng byte cho **cả object** đã serialize, dùng `Edit` với `Value` là JSON/TOML/YAML đã encode, thêm primitive `Delete` | `Patch` giữ nguyên; chỉ thêm đúng một khái niệm | Vẫn cần `Delete`, và cần "thay container" — vẫn là mở `Edit` |
| **C** | Tách **adapter writer** khỏi `Patch`: adapter tự parse-serialize **chỉ node của nó** qua CST sẵn có (`parseJSONC` trả `jsonNode` + token offsets), rồi splice đúng vùng | Không đụng `Patch`; dùng lại lexer/CST đã có và đã test | Adapter phải tự viết splice — trùng lặp ~40 LoC mỗi format |

**Đề xuất: A, nhưng thu hẹp còn đúng hai primitive.** Thêm vào `Edit`:

```go
type Edit struct {
	KeyPath         []string
	Value           string
	CreateIfMissing bool
	// ValueJSON ghi đè Value khi khác rỗng: giá trị là một container đã
	// serialize đúng format đích (JSON object/array, TOML inline table, YAML
	// flow/block). Adapter chịu trách nhiệm encode; Patch chỉ splice.
	ValueJSON string
	// Delete xoá hẳn key khỏi container cha. Xung đột với Value/ValueJSON.
	Delete bool
}
```

Lý do chọn A thay vì C: `Patch` đã có `verifyJSONC`/`verifyTOML`/`verifyYAML` gọi sau mỗi splice — đó là bất biến "output luôn parse lại được" (`ErrInternalInconsistency`). Viết splice song song trong adapter là **nhân đôi** chỗ phải giữ bất biến đó. Một primitive `ValueJSON` giữ mọi đường ghi đi qua cùng một bộ verify.

Lý do **không** chọn thêm `Delete` rời mà giữ trong `Edit`: `Edit` đã có `CreateIfMissing`; thêm `Delete` là cặp đối xứng tự nhiên và cả hai đều là "ý định của caller", không phải "cách ghi".

### 1.4 D1–D7 của spec §8.3 — chốt luôn

Spec §8.3 đã đề xuất; mini-plan này **giữ nguyên toàn bộ** vì không có phát hiện nào chống lại:

| # | Đề xuất | Giữ? |
| --- | --- | --- |
| D1 | `deepseek-tui` merge thay vì ghi đè | **Giữ** — và mục này **rời Task 3** (xem §1.5) |
| D2 | `cowork` ngoài phạm vi | **Giữ** |
| D3 | `omp` dùng splicer | **Giữ** — `FormatYAML` đã có |
| D4 | Bảng 2 ngoài `connect` | **Giữ** — Task 3 chỉ 9 client Bảng 1 |
| D5 | Zed không cần adapter | **Giữ** |
| D6 | Header `managed by 9Router` một lần | **Giữ** — `rãnh` này rời Task 3 (§1.5) |
| D7 | `kilo` không vá VSCode settings | **Giữ** |

### 1.5 `deepseek-tui`, `jcode`, `forge`, `codewhale` không nằm trong Bảng 1

Spec §8.1 liệt kê `dotenv.go` cho `jcode` — nhưng `jcode` là **Bảng 2**, và D4 nói Bảng 2 ngoài `connect`. Tương tự `deepseek-tui`/`forge`/`codewhale` (D1, D6) đều là Bảng 2.

**Hệ quả:** `dotenv.go` **không thuộc Task 3**. Bỏ khỏi bố cục tệp. Nếu cần cho dashboard sau này thì nó là task riêng có mini-plan riêng — đúng như D4 đã nói, chỉ là §8.1 tự mâu thuẫn với §8.3 ở chỗ này.

---

## 2. Bố cục tệp (điều chỉnh từ §8.1)

```
internal/clientconfig/
├── adapter.go        # interface Adapter, registry 9, Helpers dùng chung   ~130
├── paths.go          # phân giải đường dẫn 3 OS (§2 spec)                  ~110
├── v1url.go          # quy tắc "/v1" — chỉ cline không có                 ~40
├── claude.go         # JSONC, 6 env scalar, tier precedence                ~100
├── codex.go          # TOML, provider table, header auth                   ~120
├── opencode.go       # JSONC, provider object + model prefix               ~110
├── droid.go          # JSON, mảng customModels + re-index                  ~90
├── crush.go          # JSON, providers["9router"] object                   ~70
├── pi.go             # JSON, merge provider, models array                  ~80
├── omp.go            # YAML, delete-file branch                            ~90
├── kilo.go           # JSON, auth["openai-compatible"]                     ~70
├── cline.go          # JSON, HAI tệp state+secrets, origin trần            ~110
└── adapter_test.go   # S1–S3 cho cả 9                                       ~420
```

Tổng ~1430 LoC nguồn, không tệp nào > 300. **Bỏ** `crush_pi_omp.go` (gộp ba client vào một tệp khó đọc hơn ba tệp nhỏ) và `kilo_cline.go` (cùng lý do), và **bỏ** `dotenv.go` (§1.5).

Ghép `crush`/`pi`/`omp` vào một tệp từng được đề xuất để tiết kiệm chỗ, nhưng ba client đó khác nhau ở đúng chỗ nguy hiểm nhất: `omp` xoá tệp, `crush`/`pi` chỉ dọn `providers` rỗng. Spec §7.1 đã cảnh báo "xếp chúng vào nhóm này là đọc nhầm §6". Tách tệp làm ranh giới đó hiển nhiên.

---

## 3. Interface và seam

```go
// Adapter là một client Bảng 1. Không có method nào chạm network, cwd, hay
// environment; mọi đường dẫn đã được paths.go giải xong trước khi Apply chạy.
type Adapter interface {
	ID() string
	// Plan trả những gì Apply sẽ làm, không chạm đĩa. --dry-run gọi hàm này.
	Plan(opts Options) ([]FilePlan, error)
	// Apply ghi. Trả ErrAlreadyPatched khi lần chạy thứ hai không đổi byte nào.
	Apply(opts Options) (Result, error)
	// Reset khôi phục. Cùng ngữ nghĩa lỗi.
	Reset() (Result, error)
}

type FilePlan struct {
	Path   string // tuyệt đối, đã resolve
	Format Format
	Edits  []Edit
	Delete bool // Pi/omp: xoá tệp khi kết quả chỉ còn khung rỗng
}

type Options struct {
	BaseURL string // chưa thêm /v1; adapter tự quyết theo client
	APIKey  string
	Model   string
	Tiers   map[string]string // claude: fable/opus/sonnet/haiku → model
}

type Result struct {
	Backups []string // "tệp → .bak.<ts>", để Task 5 in
	Changed bool
}
```

**Vì sao `Plan` tách khỏi `Apply`:** D1/D2 của Task 4 §5.3 đòi `--dry-run` = 0 ghi. Nếu `Apply` là hàm duy nhất thì dry-run phải là một cờ bên trong, và mọi nhánh đều phải nhớ kiểm tra cờ đó — đúng loại lỗi mà `countingRoot` sinh ra để bắt. `Plan` là hàm thuần, không có đường nào tới đĩa, nên dry-run **không thể** ghi nhầm.

**Seam test:** adapter không nhận path tuyệt đối từ ngoài. Test dựng `t.TempDir()` và truyền `Options{Home: tmp}`; `paths.go` giải theo `Options.Home`. Đây là điều kiện để S1–S3 chạy được mà không đọc home thật (§9 spec cấm).

---

## 4. Thứ tự sub-task

| # | Nội dung | Phụ thuộc | Điều kiện dừng |
| --- | --- | --- | --- |
| **3.0** | Mở `Edit`: `ValueJSON`, `Delete` + xử lý trong `patchJSONC`/`patchTOML`/`patchYAML` + test | — | Mỗi format: set container, xoá key, xoá key cuối ⇒ dọn container cha. `verify*` gọi sau mỗi splice |
| **3.1** | `adapter.go` + `paths.go` + `v1url.go` | — | 9 đường dẫn khớp §3 trên 3 OS; không chuỗi nào còn `~` hay biến chưa mở; test bảng 9×3 |
| **3.2** | `claude.go` | 3.0, 3.1 | Apply→Reset byte-identical **trừ** `hasCompletedOnboarding`; tier vắng ở cả hai nguồn ⇒ **giữ nguyên** key |
| **3.3** | `codex.go` | 3.0, 3.1 | `Authorization` xuất hiện **đúng một** lần sau 3 lần Apply; reset dọn `agents` rỗng |
| **3.4** | `opencode.go` | 3.0, 3.1 | Tiền tố `9router/` đúng; reset chỉ chạm model của ta |
| **3.5** | `droid.go` | 3.0, 3.1 | Re-index đúng sau chèn; reset lọc sạch marker `custom:9Router` |
| **3.6** | `crush.go`, `pi.go` | 3.0, 3.1 | Reset **không** xoá tệp; chỉ dọn `providers` rỗng |
| **3.7** | `omp.go` | 3.0, 3.1 | Chỉ còn `providers:` trơ ⇒ xoá tệp; có key khác ⇒ giữ |
| **3.8** | `kilo.go`, `cline.go` | 3.0, 3.1 | `cline` ghi origin **trần**; hai tệp reset cùng lượt, lỗi tệp 2 báo cả hai |
| **3.9** | Tích hợp: `Registry()` trả 9, test S1–S3 toàn bộ | 3.2–3.8 | S1 byte-identical, S2 phần lạ nguyên vẹn, S3 malformed ⇒ 0 ghi |

**3.0 là sub-task mới**, không có trong spec §8.2. Nó tồn tại vì §1.2: không có nó thì 7/9 adapter không viết được, và viết chúng bằng cách tự splice sẽ bỏ qua `verify*`.

---

## 5. Tiêu chí "xanh"

Kế thừa §8.4, cộng hai điều kiện đo được:

- **S1** — với mỗi 9 client: `Apply` rồi `Reset` trên fixture synthetic trong `t.TempDir()` ⇒ **byte-identical** với bản gốc, trừ ngoại lệ đã khai (`hasCompletedOnboarding`).
- **S2** — fixture chứa key lạ, comment, thứ tự cố ý ⇒ sau `Apply` còn nguyên **byte-for-byte**.
- **S3** — fixture malformed ⇒ `Apply` lỗi fail-closed, **không** tạo `.bak`, **không** ghi đĩa. Kiểm bằng `countingRoot`.
- **S4** — `go test ./internal/clientconfig/` xanh trên Windows 11, macOS, Linux thật; không cross-compile.
- **S5** (mới) — `go build ./cmd/9router-go/` vẫn xanh và `go list -deps ./cmd/9router-go | grep clientconfig` **vẫn rỗng**: Task 3 chưa được Task 5 gọi, nên binary chưa được phình. Đây là chốt chống "tích hợp sớm".
- **S6** (mới) — không tệp nào > 300 LoC; `gofmt` sạch trên bản LF.

---

## 6. Tuyên bố Dừng (STOP Gate Declaration)

> **GATE STOP CONDITION (G24b) — Task 3:**
>
> **KHÔNG** viết `internal/clientconfig/adapter.go`, `paths.go`, `v1url.go`, `claude.go`, `codex.go`, `opencode.go`, `droid.go`, `crush.go`, `pi.go`, `omp.go`, `kilo.go`, `cline.go`, `adapter_test.go`, và **KHÔNG** mở rộng `Edit` trong `configpatch.go`, cho tới khi:
>
> 1. Reviewer phê duyệt tài liệu này (mini-plan), **và**
> 2. §1.3 (A/B/C) được chốt — cụ thể là `Edit` có nhận `ValueJSON` + `Delete` hay không, **và**
> 3. D1–D7 (§1.4) được xác nhận, **và** §1.5 (bỏ `dotenv.go`) được chấp nhận.

### 6.1 Trạng thái hiện tại (đã kiểm chứng 2026-10-23)

| Kiểm tra | Kết quả |
| --- | --- |
| `internal/clientconfig/adapter.go` tồn tại? | **Không** |
| `paths.go` / `v1url.go` / 9 file adapter? | **Không** |
| `go list -deps ./cmd/9router-go \| grep clientconfig` | **rỗng** — Task 4 vẫn 0 importer |
| `Patch` xử lý container? | **Không** — `ErrUnsupportedStructure` (`jsonc.go:222`) |
| `Edit` có `Delete`? | **Không** |
| Task 1 API gọi được từ package khác? | **Không** — `guard`/`hooks`/`rootFS` chưa export |
| `go.mod` bị sửa? | **Không** — và sẽ không |

---

## 7. Tham chiếu

| Tài liệu / mã | Dùng cho |
| --- | --- |
| `docs/specs/client-schema-matrix.md` §3 | 9 client: đường dẫn, key, gotcha |
| `docs/specs/client-schema-matrix.md` §5.2 | Ngữ nghĩa lỗi (rỗng, parse lỗi, already-patched, dọn container) |
| `docs/specs/config-patch-spec.md` §3.1 | Bất biến line-range splice |
| `internal/clientconfig/configpatch.go:80` | `Patch` — set scalar, fail-closed trên container |
| `internal/clientconfig/jsonc.go:200-234` | `patchJSONC` — nơi `ValueJSON`/`Delete` phải đi qua |
| `internal/clientconfig/write.go:24` | `BackupAndWrite` — backup + atomic, cùng lượt |
| `internal/clientconfig/connect_dryrun.go:46` | `countingRoot` — S3 dùng lại, không viết mới |
| `connect-cli-spec.md` §3.2.4 | Tier precedence của `claude` |
| `docs/superpowers/plans/2026-10-19-9router-go-desktop.md:93` | Stop condition mà mini-plan này thoả |
