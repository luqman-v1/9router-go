# Tray Companion Độc lập: Kiến trúc, Identity Binding và Nghiệm thu Đa nền tảng (G27)

**Date:** 2026-10-20
**Author:** KunN-21 <kunn21.nv@gmail.com>
**Status:** Discovery Gate G27 [CONDITIONAL] — **Documentation Only** (không có Go source nào được viết)
**Target Repository:** `9router-go` (`D:\Code\9router-go`, module `9router/proxy`), HEAD `f5bc08b`
**Toolchain verified locally:** `go1.27.0 windows/amd64` (`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`)

> **Revision history**
>
> | Rev | Ngày | Thay đổi |
> | --- | --- | --- |
> | 1 | 2026-10-20 | Bản đầu. Mọi chữ ký hàm, hằng số và đường dẫn dưới đây được đọc trực tiếp từ working tree tại HEAD `f5bc08b`. Bốn ruling T8-A, T8-B, T8-C, T8-D của controller được ghi nguyên vẹn, không suy diễn lại. |

---

## 0. Tóm tắt Điều hành

`9router-tray` là một **binary riêng**, không phải một chế độ của `9router-go`. Nó quan sát và điều khiển một gateway đang chạy như một tiến trình độc lập, qua đúng những đường mà CLI đã có. Tài liệu này khoá lại kiến trúc đó, cộng với sáu bất biến (§2) và một ma trận nghiệm thu thật trên ba hệ điều hành (§8).

**Bốn phát hiện định hình toàn bộ thiết kế** (chi tiết ở các mục tương ứng):

1. **Gateway không bao giờ là con của tray.** `daemon.Start()` spawn process rồi `cmd.Process.Release()` (`internal/daemon/daemon.go:161`) với `proc.Detached()` (`:146`); gateway do supervisor quản là con của supervisor. Tray không giữ lease nào trên gateway, nên "Quit Tray giữ gateway chạy" không phải một quy ước cần nhớ — nó là hệ quả của việc không có quan hệ cha-con nào để cắt (§6).
2. **`GET /health` là một hằng số công khai, không phải danh tính.** `internal/handlers/router.go:407-410` trả `{"status":"ok"}` vô điều kiện. Bất kỳ ai bind được port cũng trả đúng chuỗi đó trong ba dòng code. Vì vậy nó **không** được dùng làm oracle danh tính (§5.1, §10).
3. **Chỉ `Stop` có thể làm rò `CLIToken()`.** `Start` không cần token; Attach/observe chỉ đọc. Vì vậy toàn bộ máy móc identity binding chỉ áp cho đúng một hành động, và nó dùng lại nguyên quy tắc hai đường của Task 7 thay vì phát minh đường thứ ba (§5.3).
4. **Không có L2 trên Windows và macOS, nên `Stop Gateway` từ tray sẽ từ chối ở đó.** Đó là thuộc tính nền tảng, không phải lỗi của tray. Tray **không được** spawn một kill để lách (§5.4).

**Sáu bất biến, tóm tắt một dòng** — định nghĩa đầy đủ và hệ quả khi vi phạm ở §2:

| # | Bất biến | Tóm tắt |
| --- | --- | --- |
| **C1** | Headless Core | `9router-go` vẫn build `CGO_ENABLED=0` cho mọi cặp trong `CROSS_TARGETS`; không mã GUI nào vào core |
| **C2** | Identity Before Control | Không hành động điều khiển nào trước khi port + PID + data scope được xác minh |
| **C3** | No Token To Foreign Process | `CLIToken()` chỉ rời process khi L1 **và** L2 cùng qua; không có L2 thì từ chối |
| **C4** | Quit Decoupling | Quit chỉ giải phóng GUI và thoát process tray; gateway tiếp tục chạy |
| **C5** | Fail-Closed Client Parsing | Trạng thái không đọc được thì render `Unknown` và chặn điều khiển, không bao giờ hạ xuống `Stopped` |
| **C6** | No Token Leaks | Không token trong argv, log, stdout/stderr, URL, tiêu đề menu, hay thông báo lỗi |

**Ba trạng thái render, và trạng thái thứ ba là trạng thái thật:** `Running`, `Stopped`, `Unknown`. `Info.ActiveUnknown` (`internal/service/service.go:76-80`) có nghĩa "không hỏi được", **không** có nghĩa "không chạy". Một tray in `Stopped` khi thực chất là `Unknown` đang nói dối người dùng, và sẽ khiến họ bấm `Start` trên một gateway đang chạy (§7.1, §10).

**Trạng thái:** Phase 1 đã triển khai trong PR #577 (`cmd/9router-tray/`, `internal/tray/`). §11 ghi lại trạng thái của tài liệu **tại thời điểm nó được viết**; ma trận nghiệm thu §8 vẫn chưa chạy, nên G27 vẫn mở.

---

## 1. Bối cảnh & Mục tiêu

### 1.1 Vì sao tray phải là binary riêng

Ba lý do, mỗi lý do đều là một ràng buộc đã tồn tại trong repo:

| Lý do | Bằng chứng | Hệ quả |
| --- | --- | --- |
| Core phải build được `CGO_ENABLED=0` cho cả năm target | `Makefile:197` `CROSS_TARGETS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64`; vòng kiểm ở `AGENTS.md:161-164` | Một dependency GUI cần cgo trong `cmd/9router-go` sẽ phá cả năm target cùng lúc (`AGENTS.md:171`) |
| GUI là mã theo nền tảng, không phải mã di động | Windows cần `Shell_NotifyIconW`, macOS cần Objective-C runtime, Linux cần StatusNotifierItem/AppIndicator | Đưa GUI vào core biến "một binary di động" thành "một bản build cho mỗi nền tảng", đúng thứ `AGENTS.md:172` cấm |
| Tray có vòng đời người dùng, gateway có vòng đời máy | Bất biến C4 | Hai vòng đời khác nhau không nên chia sẻ một process |

Ngược lại, core **không** cần biết tray tồn tại: không route, không cờ, không endpoint nào trong tài liệu này yêu cầu gateway thay đổi. Đó là điều làm cho G27 trở thành một thay đổi **cộng thêm** thay vì một thay đổi **xâm nhập**.

### 1.2 Chữ ký đã thẩm định tại HEAD `f5bc08b`

Mọi giá trị dưới đây đọc trực tiếp từ source. Không có drift.

| Ký hiệu | Vị trí | Giá trị thực tế |
| --- | --- | --- |
| `daemon.RunningPID()` | `internal/daemon/daemon.go:104` | `func RunningPID() int` — trả 0 khi claim không xác minh được |
| `daemon.Stop()` | `internal/daemon/daemon.go:186` | `func Stop() (StopResult, error)` — POST rồi force-kill sau `stopGraceMS` |
| `daemon.Start()` | `internal/daemon/daemon.go:118` | `func Start(url string, extraArgs []string) (StartResult, error)` |
| `daemon.PIDFile()` | `internal/daemon/daemon.go:67` | `<DATA_DIR>/run/gateway.pid` |
| `daemon.Dir()` | `internal/daemon/daemon.go:62` | `filepath.Join(config.ResolveDataDir(), "run")` |
| `daemonLocalURL()` | `internal/daemon/daemon.go:245` | `"http://127.0.0.1:" + port`, port từ `config.LoadConfig()`, fallback `defaultPort = 20130` (`:37`) |
| `claim.owner()` | `internal/daemon/pidfile.go:100-111` | `pid > 0` **và** `exePath != ""` **và** `proc.Alive(pid)` **và** `proc.Executable(pid)` khớp sau `EvalSymlinks` |
| `daemon.EnvBackground` | `internal/daemon/health.go:26` | `"9ROUTER_BACKGROUND"` |
| `daemon.IsBackgroundProcess()` | `internal/daemon/health.go:34-36` | `os.Getenv(EnvBackground) == "1"` |
| `daemon.HealthPath` | `internal/daemon/health.go:31` | `"/health"` |
| `service.Status()` | `internal/service/service.go:138` | `func Status() (Info, bool)` — backend đầu tiên khớp trong thứ tự `:139-146` |
| `service.Info` | `internal/service/service.go:70-81` | `Backend, Path, Exec, Enabled, Active, ActiveUnknown` |
| `Info.ActiveUnknown` | `internal/service/service.go:76-80` | "could not interrogate", không phải "not running" |
| `service.Install()` | `internal/service/service.go:86` | `func Install(extraArgs []string) (Info, error)` |
| `service.Uninstall()` | `internal/service/service.go:111` | `func Uninstall() ([]Info, error)` |
| `service.Start()` | `internal/service/control.go:21` | `func Start() (Info, error)` → `control("start")` |
| `service.Stop()` | `internal/service/control.go:26` | `func Stop() (Info, error)` → `control("stop")` |
| `service.ErrNoSupervisor` | `internal/service/control.go:13` | `errors.New("service: autostart backend has no supervisor, manage the process directly")` |
| `service.ErrNotInstalled` | `internal/service/service.go:34` | `errors.New("9router-go autostart is not installed")` |
| `controlTimeout` | `internal/service/control.go:17` | `10 * time.Second` |
| `auth.CLIToken()` | `internal/auth/clitoken.go:24` | `func CLIToken() string` — `sha256(rawMachineID + "9r-cli-auth" + cliSecret).hex[:16]` |
| `auth.ValidCLIToken()` | `internal/auth/clitoken.go:36` | so sánh constant-time |
| `auth.CLITokenHeader` | `internal/auth/session.go:34` | `"x-9r-cli-token"` |
| `/health` | `internal/handlers/router.go:407-410` | `w.Write([]byte("{\"status\":\"ok\"}"))` ở `:409` — công khai, không danh tính |
| `/api/version/shutdown` | `internal/handlers/router.go:493` | `r.Post("/api/version/shutdown", HandleShutdown)` |
| Cổng auth của route shutdown | `internal/handlers/router.go:476` | `r.Use(middleware.RequireAdminAuth())` |
| `RequireAdminAuth` | `internal/middleware/dashboard_auth.go:113-124` | session **hoặc** `CLITokenHeader` hợp lệ |
| `IsAlwaysProtectedPath` | `internal/middleware/dashboard_auth.go:45-62` | gồm `/api/version/shutdown` (`:53`), `/api/version/update` (`:54`), `/admin/health/reset` (`:55`), `/api/oauth/cursor/auto-import` (`:56`) |
| `config.ResolveDataDir()` | `internal/config/config.go:90-108` | `DATA_DIR` → `.env` → `%APPDATA%\9router` / `~/.9router` |
| `config.LoadConfig().Port` | `internal/config/config.go:157` | `PORT`, mặc định `20130` (`:48`) |
| `windowsTaskName` | `internal/service/windows.go:10` | `"9router-go"` |
| `unitName` | `internal/service/systemd.go:14` | `"9router-go.service"` |
| `launchdLabel` | `internal/service/launchd.go:14` | `"com.9router.gateway"` |
| `BINARY_NAME` | `Makefile:2` | `9router-go$(BINARY_SUFFIX)` |
| `CROSS_TARGETS` | `Makefile:197` | `linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64` |
| Target `cross-one` | `Makefile:217` | `go build -ldflags="$(LDFLAGS)" -o $(OUT) ./cmd/9router-go/` |

Bổ sung đã đọc và dùng trong tài liệu này:

| Ký hiệu / hằng số | Vị trí | Giá trị |
| --- | --- | --- |
| `stopGraceMS` | `internal/daemon/daemon.go:29` | `5000` (ms) |
| `shutdownAPI` | `internal/daemon/daemon.go:35` | `"/api/version/shutdown"` |
| `daemon.StopResult` | `internal/daemon/daemon.go:55-59` | `struct { Stopped bool; PID int; Reason string }` |
| `daemon.StartResult` | `internal/daemon/daemon.go:48-52` | `struct { PID int; URL string; Existing bool }` |
| `healthClient` | `internal/daemon/health.go:40` | timeout 2 giây |
| `proc.Terminate` | `internal/proc/proc.go:25-39` | SIGTERM, chờ, rồi **tự** `ForceKill` |
| `proc.ForceKill` | `internal/proc/proc.go:42` | `TerminateProcess` (Windows) / `SIGKILL` (Unix) |
| `proc.ErrStopUnsupported` | `internal/proc/proc_windows.go:21` | `"proc: graceful stop unsupported on this platform"` |
| `requestStop` (Windows) | `internal/proc/proc_windows.go:47-49` | luôn trả `ErrStopUnsupported` |
| `controlWindowsTask("stop")` | `internal/service/control.go:151-153` | `schtasks.exe /end /tn 9router-go` = `TerminateProcess` ở tầng scheduler |
| `systemd Restart=always` | `internal/service/systemd.go:51-52` | `Restart=always`, `RestartSec=3` |
| `launchd KeepAlive` | `internal/service/launchd.go:39` | `<key>KeepAlive</key><true/>` |
| `HandleShutdown` | `internal/handlers/shutdown.go:23-34` | 200 trước, rồi `:30` chờ 500 ms, `:32` `shutdown.RequestStop()` |
| `signal.Notify` | `cmd/9router-go/main.go:251` | `os.Interrupt, syscall.SIGTERM` |
| Nhánh select shutdown | `cmd/9router-go/main.go:261-266` | `<-signals` và `<-shutdown.StopRequested()` cùng một `select` |
| `fxApp.Stop` budget | `cmd/9router-go/main.go:276-278` | `20 * time.Second` |
| `daemon.RegisterPID()` gate | `cmd/9router-go/main.go:256-259` | chỉ khi `daemon.IsBackgroundProcess()` |
| Set `9ROUTER_BACKGROUND` | `internal/daemon/daemon.go:145` | `cmd.Env = append(os.Environ(), EnvBackground+"=1")` — **duy nhất** chỗ này |
| Lệnh CLI hiện có | `cmd/9router-go/daemon_cmd.go:21-142` | `daemonURL`, `startDetached`, `stopDetached`, `restartDetached`, `statusDetached`, `logsDetached`, `forwardArgs` |
| Alias lệnh con | `cmd/9router-go/main.go:127-148` | `start`, `stop`, `restart`, `status`, `logs` |
| Lệnh `service` | `cmd/9router-go/main.go:158-193` | `install`, `uninstall`, `start`, `stop`, `restart`, `status` |

### 1.3 Ngoài phạm vi

| Việc | Thuộc về |
| --- | --- |
| Viết `cmd/9router-tray/main.go` hay `internal/tray/` | STOP Gate §11 — Phase 2, sau khi spec này được duyệt |
| Thêm UDS / named-pipe control channel vào gateway | Thay đổi phía gateway, ngoài G27 (§9). Tài liệu này **không** được đọc như thể nó đã tồn tại |
| Thêm L2 (socket-owner lookup) cho Windows/macOS | Task riêng; §5.4 chốt fail-closed cho tới lúc đó, §9 ghi đường nâng cấp |
| Sửa `daemon.Stop()` để bỏ force-kill | Task 7 (`docs/specs/service-drain-coordination.md`) đã định nghĩa đường mới và **cấm** đường cũ |
| Tự động cập nhật tray, auto-update tray | Không có trong G27 |
| Đóng gói / phát hành `9router-tray` lên npm hay release artifact | Phase 2; §8 chỉ định nghĩa nghiệm thu, không định nghĩa kênh phát hành |

---

## 2. Sáu Bất biến

Mỗi bất biến có: phát biểu, cơ chế thực thi, và cái gì hỏng khi vi phạm. Bất biến ở đây là **ràng buộc thiết kế**, không phải khẩu hiệu — mỗi cái đều chỉ ra một dòng code cụ thể phải viết hoặc một đường cụ thể bị cấm.

### C1 — Headless Core

**Phát biểu:** `9router-go` build được với `CGO_ENABLED=0` cho **mọi** cặp trong `CROSS_TARGETS` sau khi G27 hoàn tất. Không mã GUI, không import GUI, không build tag GUI nào lọt vào `cmd/9router-go/`.

**Cơ chế:** `CROSS_TARGETS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64` (`Makefile:197`). `AGENTS.md:161-164` cho một vòng kiểm copy-runnable; `AGENTS.md:171` cấm "any library requiring cgo" và `:172` cấm build tag phá bất kỳ target nào. Tray nằm ở `cmd/9router-tray/`, **ngoài** vòng `cross-one` (`Makefile:217`, `go build … ./cmd/9router-go/`) và ngoài mọi target của `CROSS_TARGETS`.

**Điểm dễ hiểu sai:** "tray được phép dùng cgo" không phải một ngoại lệ của `AGENTS.md §4.F` — nó là kết quả của việc `§4.F` chỉ ràng buộc **binary được ship trong `CROSS_TARGETS`**. Tray là binary thứ hai, không có mặt trong vòng lặp đó. Nếu ai thêm `cmd/9router-tray` vào `CROSS_TARGETS` hoặc vào `cross`, C1 vỡ ngay lập tức và vỡ cho cả năm target cùng lúc.

**Hệ quả lên `./...`:** `go vet ./...`, `go test ./...` và `golangci-lint run ./...` đều duyệt **mọi** package, kể cả tray — nên một import GUI không có build constraint sẽ kéo `pkg-config ayatana-appindicator3-0.1` vào mọi lệnh workspace trên Linux, và làm đỏ ba job CI trên runner không cài `libayatana-appindicator3-dev`. Vì vậy `cmd/9router-tray/gui.go` mang `//go:build windows || tray`: Windows vào constraint không cần tag (systray gọi thẳng Win32 API, không cgo, không pkg-config), macOS/Linux cần `-tags tray` và phải build trên host thật của chúng. Bản build không có tag sinh ra `gui_stub.go`, thoát lúc khởi động kèm tên flag cần thêm — im lặng không hiện icon thì người dùng sẽ đi tìm lỗi ở gateway. Job `Build tray binary` trong `ci.yml` chạy trên `windows-latest` để constraint này không âm thầm che một `gui.go` hỏng.

**Khi vi phạm:** một dependency GUI cần cgo trong core biến "một binary di động" thành "một bản build cho mỗi nền tảng" — đúng thứ `AGENTS.md:172` nói pipeline này tồn tại để tránh.

### C2 — Identity Before Control

**Phát biểu:** Không hành động điều khiển nào — không token, không shutdown, không start — được gửi trước khi **port + PID + data scope** cùng được xác minh.

**Cơ chế:** §5 định nghĩa đúng ba lớp và cách hợp chúng. Port một mình không đủ (bất kỳ process nào cũng bind được). PID một mình không đủ (PID file có thể cũ). Data scope một mình không đủ (nó là đường dẫn, không phải danh tính). Tổ hợp ba thứ là thứ phân biệt "gateway của tôi" với "một thứ gì đó đang giữ cổng tôi muốn".

**Khi vi phạm:** gửi `CLIToken()` tới process lạ — xem C3.

### C3 — No Token To Foreign Process

**Phát biểu:** `CLIToken()` chỉ rời tiến trình tray khi **L1 và L2 cùng đạt**. Không có L2 ⇒ từ chối, không có ngoại lệ, không có "fallback claim-only".

**Cơ chế:** đây là quy tắc hai đường của `docs/specs/service-drain-coordination.md` §3.7, dùng lại nguyên vẹn. Tài liệu đó đã loại bỏ L3 claim-only vì proof-by-effect bảo vệ **output trạng thái**, không bảo vệ **secret**: tại thời điểm quan sát được hiệu ứng thì process chưa xác minh đã cầm token rồi.

**Tại sao đây là bất biến của tray chứ không chỉ của CLI:** `CLIToken()` là credential admin **toàn máy** — 16 hex ký tự suy từ `<DATA_DIR>/machine-id` và `<DATA_DIR>/auth/cli-secret` (`internal/auth/clitoken.go:24-30`). Người nhận token mở được `/api/version/shutdown`, `/api/version/update`, `/admin/health/reset`, `/api/oauth/cursor/auto-import` và các route DB (`internal/middleware/dashboard_auth.go:45-62`, `:113-124`). Một tray là process chạy thường trực trên desktop người dùng, nhìn thấy mọi click — nó là nơi dễ rò nhất nếu ta cho nó tự quyết định khi nào gửi.

**Khi vi phạm:** một process khác principal trên cùng máy nhận được quyền admin gateway. Không có cờ nào bật lại được sự đã mất.

### C4 — Quit Decoupling

**Phát biểu:** `Quit Tray` giải phóng tài nguyên GUI và thoát tiến trình tray. Nó **không** gọi `daemon.Stop()`, `service.Stop()`, `/api/version/shutdown`, hay bất kỳ lệnh dừng nào. Gateway tiếp tục chạy.

**Cơ chế:** §6. Bất biến này được bảo đảm bởi một sự thật cấu trúc chứ không bởi kỷ luật lập trình: tray **không có** quan hệ cha-con với gateway. `daemon.Start()` spawn process rồi `cmd.Process.Release()` (`internal/daemon/daemon.go:161`) với `proc.Detached()` (`:146`), và gateway do supervisor quản là con của supervisor. Không có lease nào để cắt, nên "Quit không dừng gateway" là hệ quả, không phải quy ước.

**Khi vi phạm:** người dùng đóng một icon trang trí và mất một gateway đang stream. Không có thông báo nào giải thích vì sao.

### C5 — Fail-Closed Client Parsing

**Phát biểu:** trạng thái không đọc được thì render `Unknown` và **chặn** điều khiển. Không bao giờ hạ `Unknown` xuống `Stopped` "cho gọn menu".

**Cơ chế:** §7.1. Nguồn của trạng thái là `service.Status()` + `daemon.RunningPID()`. `Info.ActiveUnknown` (`internal/service/service.go:76-80`) tồn tại chính vì lý do này: comment tại chỗ nói "Printing the zero value as \"not running\" would state as fact what is only an unanswered question, which is how the Windows task status stayed wrong on a non-English locale."

**Khi vi phạm:** tray hiện `Stopped` trên một gateway đang chạy ⇒ người dùng bấm `Start Gateway` ⇒ `daemon.Start()` thấy port bận và trả `ErrAlreadyRunning` (`internal/daemon/daemon.go:119-124`). Người dùng vừa được cho biết một điều sai, vừa nhận một lỗi mà họ không gây ra.

### C6 — No Token Leaks

**Phát biểu:** token, secret, hay bất kỳ giá trị suy ra từ `machine-id`/`cli-secret` không xuất hiện trong argv, biến môi trường kế thừa, log, stdout/stderr, URL, tiêu đề menu, hay thông báo lỗi.

**Cơ chế:** token chỉ được đặt vào một header trong một request, đúng như `daemon.requestGracefulStop()` đang làm (`internal/daemon/daemon.go:218`: `req.Header.Set(auth.CLITokenHeader, auth.CLIToken())`). Không có cờ CLI nào nhận token làm tham số. Thông báo lỗi của tray là **mã trạng thái + port + PID**, không bao giờ là header hay giá trị credential.

**Khi vi phạm:** argv và environment của mọi process trên máy đọc được trên nhiều OS (POSIX `/proc/<pid>/cmdline`; Windows cần quyền cao hơn nhưng không phải lúc nào cũng vậy). Rò qua log thì tệ hơn: log được gửi đi khi báo lỗi.

---

## 3. Ranh giới: Core, Tray, và cái gì thuộc về ai

| Thành phần | Binary | CGO | Vòng đời | Được làm gì với gateway |
| --- | --- | --- | --- | --- |
| `cmd/9router-go` (core) | `9router-go` | **Không bao giờ** | do người dùng hoặc supervisor | sở hữu listener, sở hữu DB |
| `cmd/9router-tray` (tray) | `9router-tray` | Được phép | do người dùng đóng/mở | quan sát + điều khiển qua đúng API/CLI đã có |
| `internal/daemon` | — | không | — | spawn/stop gateway (đường CLI) |
| `internal/service` | — | không | — | cài/bật/tắt supervisor entry |
| `internal/clientconfig` | — | không | — | patch cấu hình client (G24a–b, Task 1–5) |

**Quy tắc một chiều:** tray gọi vào core. Core **không biết** tray tồn tại. Không route mới, không cờ mới, không endpoint mới trong tài liệu này. Đó là thứ làm G27 thành thay đổi **cộng thêm**.

**Ngoại lệ duy nhất phải nêu rõ:** §9 ghi một thay đổi **phía gateway** (UDS/named-pipe control channel) như đường nâng cấp Phase 2. Tài liệu này không được đọc như thể nó đã tồn tại — nó không tồn tại.

---

## 4. Khảo sát Thư viện GUI

### 4.1 Cảnh báo về phương pháp — đọc trước

**Khảo sát này dựa trên hiểu biết, KHÔNG kiểm chứng được offline.** `go.mod` không có dependency GUI nào (`require` block: `chi`, `fx`, `urfave/cli/v2`, `viper`, `slog`, `x/sys`, `modernc.org/sqlite`, … — không có `fyne.io/*`, không có `github.com/getlantern/*`), và module cache cũng không có chúng. Brief cấm tải về. Vì vậy:

- Mọi con số phiên bản, license, và trạng thái bảo trì dưới đây là **kiến thức tại thời điểm viết**, không phải quan sát.
- Trước khi adopt, **phải** chạy đủ hai điều kiện của `AGENTS.md §4.F` (§4.4) và đọc license thật từ module tải về.
- Không có tuyên bố nào trong mục này được phép chuyển thành "đã kiểm chứng" mà không có một lần build thử thật.

Điều **được** kiểm chứng offline: `golang.org/x/sys@v0.48.0` có **0 hit** cho `Shell_NotifyIcon` và `NOTIFYICONDATA` trên toàn bộ `windows/` (54 file). Vậy một tray icon trên Windows **không thể** dựng từ `x/sys` một mình — cần GUI library, hoặc tự khai báo `user32`/`shell32` qua `NewLazySystemDLL` + `NewProc` (đường `unsafe`, cùng loại chi phí mà Task 7 đã từ chối cho L2 Windows).

### 4.2 So sánh

| Tiêu chí | `github.com/getlantern/systray` | `fyne.io/systray` | Platform-native (tự viết) |
| --- | --- | --- | --- |
| Cơ chế | cgo trên mọi nền tảng: Cocoa (macOS), GTK/AppIndicator (Linux), Win32 `Shell_NotifyIconW` (Windows) | Fork của bản trên, cùng mô hình cgo | `cgo` + Objective-C (darwin), `x/sys`+`user32` raw (Windows), D-Bus/AppIndicator (Linux) |
| CGO | **Có** — bắt buộc, không build được `CGO_ENABLED=0` | **Có** — bắt buộc | Có trên macOS/Linux; Windows có thể thuần Go nếu tự khai báo `user32` (nhưng cần `unsafe`) |
| Dependency | cgo + GTK dev headers trên Linux khi build | như trên | nhiều nhất: ba implementation độc lập, ba bộ lỗi |
| License | Apache-2.0 (bản gốc) | BSD-3-Clause (fork Fyne) | của chính repo |
| Bảo trì | chậm; bản gốc ít cập nhật | tích cực hơn, gắn với hệ sinh thái Fyne | phụ thuộc nỗ lực nội bộ |
| Độ phức tạp tích hợp | thấp — API callback nhỏ (`systray.Run`, `AddMenuItem`, `SetIcon`) | thấp — tương đương | **cao** — phải tự viết event loop từng OS |
| Chạy được trong CI không GUI | không | không | không |
| Hệ quả với repo này | thêm 1 dependency cgo vào **một binary riêng**; C1 không bị ảnh hưởng | như trên | không thêm dependency nhưng thêm ~3× mã, và vẫn cần cgo trên 2/3 nền tảng |

### 4.3 Ba lựa chọn, thu hẹp

**Khuyến nghị: `fyne.io/systray`** — với điều kiện §4.4 qua. Lý do:

1. **License permissive** (BSD-3-Clause) và bảo trì tích cực hơn bản gốc — hai thuộc tính duy nhất ta thực sự cần ở một GUI shim mỏng.
2. **Bề mặt API nhỏ.** Ta cần đúng bốn thứ: một icon, một menu động nhiều mục, callback chọn mục, và một vòng lặp có thể dừng. Không cần window, không cần canvas, không cần theme system.
3. **Nó là cgo — nhưng chỉ trong `9router-tray`.** C1 nói rõ vì sao điều đó hợp lệ.

**Loại `getlantern/systray`** không phải vì nó sai, mà vì bản fork của Fyne có cùng mô hình với bảo trì tốt hơn; nếu fork có vấn đề license hoặc build, `getlantern` là phương án dự phòng cùng kiến trúc — đổi import, không đổi thiết kế.

**Loại platform-native trừ khi** có yêu cầu cụ thể mà systray không đáp được (ví dụ: cần `NSStatusItem` chi tiết mà shim không lộ ra). Chi phí gấp nhiều lần và bề mặt lỗi là ba nền tảng thay vì một shim.

### 4.4 Điều kiện bắt buộc trước khi adopt (chưa làm)

Hai điều kiện của `AGENTS.md §4.F` áp cho `go.mod` **sau khi** thêm dependency. Chúng chạy trên `CROSS_TARGETS`, tức **core**, và điều kỳ vọng là chúng **không đổi** — nếu đổi, tray đã lọt vào core:

```bash
# Điều kiện 1 — portability của CORE vẫn xanh sau khi thêm tray dep
for pair in $(grep -E '^CROSS_TARGETS *:=' Makefile | sed 's/.*:=[[:space:]]*//'); do
  CGO_ENABLED=0 GOOS=${pair%-*} GOARCH=${pair#*-} \
    go build -o /dev/null ./cmd/9router-go/ || echo "FAIL $pair"
done

# Điều kiện 2 — module graph vẫn coherent
go mod tidy && git diff --exit-code go.mod
```

Điều kiện 1 cần `web/dist` tồn tại trước (`make web-build`) vì `web/embed.go` là `//go:embed all:dist` (`AGENTS.md:166`). Đó là lý do `FAIL` ở lần chạy đầu trên clone sạch **không** phải lỗi portability.

Điều **chưa kiểm chứng và phải kiểm chứng khi build thật**:

| Câu hỏi | Cách trả lời |
| --- | --- |
| `fyne.io/systray` build được trên Windows **không cần** MSYS2/mingw? | `GOOS=windows CGO_ENABLED=1 go build ./cmd/9router-tray/` trên Windows thật |
| Linux build có cần `libayatana-appindicator3-dev` không, và package nào? | build trong container sạch, đọc thông báo lỗi linker |
| macOS build có cần Xcode CLI tools; có ký không? | build trên macOS thật; chạy thử chưa ký |
| Menu item động (thêm/xoá theo trạng thái) có race với event loop không? | test tay: đổi trạng thái liên tục khi menu đang mở |
| Icon RGBA/scale trên HiDPI? | nghiệm thu tay trên màn hình scale 150%/200% |

Không câu nào trong bảng này có thể trả lời bằng `go vet`, `go build` cross, hay đọc source. **Cross-compile không chứng minh được gì về GUI** (§8).

---

## 5. Identity Binding — port + PID + data scope

### 5.1 `/health` không phải danh tính

`internal/handlers/router.go:407-410`:

```go
r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.Write([]byte(`{"status":"ok"}`))
})
```

Đây là một **hằng số công khai**, không tham số, không header, không phụ thuộc state. Bất kỳ ai bind được port đều trả đúng chuỗi đó trong ba dòng code. Vì vậy:

- `probeHealthy()` (`internal/daemon/health.go:121-128`) trả lời đúng **một** câu: "có ai đó đang nghe trên cổng này và trả 200". Nó là điều kiện **cần** để biết một listener tồn tại, và **không bao giờ** là điều kiện **đủ** để gửi token.
- Không có endpoint nào khác trong tài liệu này trở thành oracle danh tính thay thế. `/api/version` và `/api/version/status` cũng công khai (`router.go:411-413`, comment: "Version info is public (upstream PUBLIC_API_PATHS)").

**Hệ quả thiết kế:** tray có thể dùng `/health` để **hiển thị** "có listener", nhưng mọi quyết định điều khiển phải dựa trên §5.3.

### 5.2 Ba lớp, và lớp nào thực sự tồn tại

| Lớp | Câu hỏi nó trả lời | Nguồn | Trạng thái |
| --- | --- | --- | --- |
| **L1** — claim | "PID trong data scope của tôi có đang chạy đúng binary không?" | `claim.owner()` `internal/daemon/pidfile.go:100-111` | Có trên **mọi** nền tảng |
| **L2** — socket owner | "process nào đang giữ cổng tôi sẽ nói chuyện?" | Linux `/proc/net/tcp` + `/proc/<pid>/fd`; Windows: **không có** trong `x/sys@v0.48.0`; macOS: **không có** API unprivileged | **Chỉ Linux** |
| **L3** — claim-only fallback | (đã bị loại) | — | **Không tồn tại** — xem C3 |

`claim.owner()` fail-closed theo đúng nghĩa: `pid <= 0` → false; `exePath == ""` → false (claim dạng số trần, do build cũ ghi, không xác minh được); `!proc.Alive(pid)` → false; `proc.Executable(pid)` lỗi → false; path không khớp sau `resolvedPath` → false. Chỉ một đường về `true`.

### 5.3 Hợp ba lớp thành quyết định

Đây là **cùng** quy tắc hai đường của `docs/specs/service-drain-coordination.md` §3.7, áp cho tray. Không phát minh đường thứ ba.

```
service.Status() → (info, ok)

  ok && Backend ∈ {systemd-user, launchd, openrc, runit}
        ──► SUPERVISED: dừng qua service.Stop(), KHÔNG token, KHÔNG đọc pid file
  ok && Backend == windows-task
        ──► TỪ CHỐI: identity_unverifiable, không token, không /end
  ok == false || Backend == cron
        ──► UNSUPERVISED: cần L1 VÀ L2 trước khi token rời process
```

**Tại sao supervised không cần token:** `service.Stop()` → `control("stop")` → `systemctl --user stop` / `rc-service … stop` / `sv stop …` / `launchctl unload` (`internal/service/control.go:68-144`). Trên systemd, SIGTERM đi vào `main.go:251` (`signal.Notify(signals, os.Interrupt, syscall.SIGTERM)`) và rơi vào cùng nhánh `select` với `shutdown.StopRequested()` (`main.go:262-263`), rồi `fxApp.Stop` (`:278`) — đó **chính là** graceful drain. Không POST, không token, không cần biết PID.

**Tại sao `windows-task` bị từ chối:** `controlWindowsTask("stop")` = `schtasks.exe /end /tn 9router-go` (`internal/service/control.go:151-153`). `/end` là `TerminateProcess` ở tầng scheduler — không drain, không SIGTERM. Windows không nâng được SIGTERM cho chính nó (comment tại `internal/handlers/shutdown.go` và `main.go:265-266`), nên không có đường graceful nào từ supervisor. Và không có L2 trên Windows ⇒ không token. Vậy: từ chối.

### 5.4 Hệ quả phải nói thẳng

**`Stop Gateway` từ tray sẽ từ chối trên Windows và macOS cho tới khi có L2.**

Đó là thuộc tính của nền tảng, **không phải lỗi của tray**. Tray hiển thị đúng thông báo (§7.4) và chỉ sang dashboard. Nó **không được**:

- spawn `taskkill` / `kill` / bất kỳ lệnh kill nào như một "workaround";
- gọi `daemon.Stop()` — hàm đó tự escalate sang `proc.Terminate` rồi `ForceKill` (`internal/daemon/daemon.go:197`, `internal/proc/proc.go:25-39`), vi phạm C2 của Task 7;
- hạ chuẩn binding xuống L1 khi L2 vắng mặt — đó đúng là L3 đã bị xoá.

Người dùng Windows/macOS vẫn có đường dừng hợp lệ: dashboard, hoặc `9router-go stop` trên Linux. Trên Windows, `schtasks /end` từ chính CLI cũng không phải graceful — nên "refuse" ở đây không mất gì so với hiện trạng; nó chỉ **không thêm** một đường mới tệ hơn.

**Trên Linux unsupervised:** L1 + L2 đều phải qua. Nếu port đang bị process lạ giữ, tray báo xung đột cổng và **không gửi gì**.

---

## 6. Vòng đời: Quit ≠ Stop

### 6.1 Bốn thao tác, bốn hệ quả

| Thao tác | Gateway | Tray | Gửi token | Gọi supervisor |
| --- | --- | --- | --- | --- |
| `Quit Tray` | **tiếp tục chạy** | thoát | không | không |
| `Start Gateway` | từ stopped → running | sống | không | có, nếu backend tồn tại (`service.Start()` `control.go:21`) |
| `Stop Gateway` | drain rồi dừng | sống | supervised: **không**; unsupervised Linux: có | có (supervised) |
| `Force Kill` (**không có trong G27**) | kill ngay | sống | không | Task 7 §6.4: chỉ qua `--force`, invocation riêng có xác nhận |

### 6.2 Quit

`Quit Tray` chỉ gọi `systray.Quit()` (hoặc tương đương) và trả khỏi `main`. Nó **không** giữ handle nào tới gateway để đóng.

Cơ chế bảo đảm điều này là cấu trúc, không phải kỷ luật: xem C4. Tray **không** spawn gateway. Khi người dùng bấm `Start Gateway`, tray gọi `service.Start()` hoặc `daemon.Start()`, cả hai đều `Release()` process con ngay (`daemon.go:161`) hoặc giao cho supervisor. Sau đó tray không còn quan hệ nào.

### 6.3 Autostart toggle

Là wrapper mỏng trên `service.Install(extraArgs)` / `service.Uninstall()` (`internal/service/service.go:86`, `:111`). **Mặc định TẮT**, và trạng thái ban đầu đọc từ `service.Status().Enabled` — không phải từ một biến tray tự giữ. `Status()` trả `ok == false` nghĩa là **chưa cài autostart entry** (`internal/service/service.go:137-152`), nên toggle ở trạng thái **OFF** bình thường và bật được ngay; nó **không** phải `Unknown`. Chỉ `Info.ActiveUnknown` mới làm toggle `Unknown` và không bật được (C5), vì lúc đó tray không biết entry hiện có đang bật hay tắt — bật đè lên một entry chưa đọc được là ghi mù.

Toggle **không** phải "tray tự khởi động cùng máy". Nó là "gateway tự khởi động cùng máy", đúng thứ `internal/service` đã làm. Tray tự khởi động cùng máy là việc của OS (Startup folder / Login Items / `.desktop` autostart) và nằm ngoài G27.

### 6.4 Nghiệm thu Quit

`TestTray_QuitDecoupling`: dựng gateway synthetic trên cổng loopback riêng với `DATA_DIR` temp, chờ `GET /health` trả 200, chạy tray, gọi Quit, chờ tray exit, rồi `GET /health` lại **phải** vẫn trả 200 và cùng PID.

Không dùng `sleep` để "chờ tray exit" — chờ process exit thật, có timeout hữu hạn. Đây là bài học từ chính Task 7: một cửa sổ quan sát mơ hồ (5 s vs 6 s) là một lỗi thật.

---

## 7. Menu, Trạng thái, và Thông báo

### 7.1 Bốn trạng thái, không phải hai

| Trạng thái | Nguồn | Menu |
| --- | --- | --- |
| `Running` | `daemon.RunningPID() > 0` **hoặc** `service.Status()` với `Active == true && !ActiveUnknown` | `9Router (Port {port})`, `Open Dashboard`, `Auto-start on Boot`, `Stop Gateway`, `Quit Tray` |
| `Stopped` | không có listener, `daemon.RunningPID() == 0`, và supervisor **không** báo `ActiveUnknown`. `Status()` trả `ok == false` cũng rơi vào đây — xem dưới | `9Router (Stopped)`, `Start Gateway`, `Auto-start on Boot`, `Quit Tray` |
| `Unknown` | `Info.ActiveUnknown` **hoặc** đọc state/pid lỗi | `9Router (Unknown)`, `Quit Tray` — **không** có Start/Stop |
| `PortConflict` | listener có mặt nhưng không xác minh được danh tính, **hoặc** không khớp PID | `9Router (Port {port} in use by foreign process (PID {pid}))`, `Quit Tray` |

**`Unknown` không phải `Stopped`.** Đó là toàn bộ nội dung của C5. Lý do tồn tại của `ActiveUnknown` được ghi ngay tại `internal/service/service.go:76-79`: in zero value thành "not running" là "stating as fact what is only an unanswered question".

**`ok == false` không phải `Unknown`.** `service.Status()` trả `ok == false` khi **không có** autostart entry nào được cài (`internal/service/service.go:137-152` — comment của chính hàm: "the autostart entry in effect, or false when none exists"). Đó là trạng thái bình thường của một máy vừa cài hoặc một người dùng chỉ chạy `9router-go start` không cài service, không phải một câu hỏi chưa trả lời. Vì vậy: `ok == false` + `RunningPID() == 0` + không listener ⇒ `Stopped`, có `Start Gateway`. Chỉ `ActiveUnknown` (backend tồn tại nhưng không hỏi được) hoặc lỗi đọc state/pid mới là `Unknown`. §5.3 vẫn route `ok == false` sang UNSUPERVISED — đó là chuyện **đường nào để dừng**, không phải chuyện trạng thái hiển thị, và hai câu hỏi này độc lập.

### 7.2 `Port {port}` lấy từ đâu

Từ `config.LoadConfig().Port` (`internal/config/config.go:157-159`: `v.GetInt("PORT")`, mặc định 20130), cùng nguồn với `daemonLocalURL()` (`internal/daemon/daemon.go:245-252`). Không đọc port từ pid file — pid file **không** chứa port (định dạng: `PID`, space, `exePath`; xem `internal/daemon/pidfile.go:15-18`).

### 7.3 Data scope

`DATA_DIR` resolve qua `config.ResolveDataDir()` (`internal/config/config.go:90-108`): env `DATA_DIR` → `.env` trong **cwd hiện tại**, rồi `.env` cạnh binary (`internal/config/config.go:134-140`: `envFileCandidates()` trả `{".env"}` rồi mới nối `filepath.Join(filepath.Dir(exe), ".env")`) → `%APPDATA%\9router` (Windows) / `~/.9router` (khác). Hai vị trí `.env` có nghĩa là **cwd là một phần của data scope**: tray và gateway chạy với cwd khác nhau có thể resolve `DATA_DIR` khác nhau, xem cảnh báo ngay dưới. Pid file nằm ở `<DATA_DIR>/run/gateway.pid` (`internal/daemon/daemon.go:62-69`).

Tray dùng **cùng** `ResolveDataDir()` — không tự suy đường dẫn. Đây là điều làm "data scope" thành một phần của identity: nếu hai tray hoặc một tray + một CLI resolve được hai `DATA_DIR` khác nhau, chúng đang nói về hai gateway khác nhau dù cùng port.

### 7.4 Nội dung thông báo lỗi (không chứa secret)

| Ca | Chuỗi hiển thị |
| --- | --- |
| Cổng bị process lạ giữ | `Port {port} in use by foreign process (PID {pid})` |
| Không có L2 trên nền tảng này | `Stop is unavailable on {GOOS}: identity of the port owner cannot be verified. Use the dashboard at http://127.0.0.1:{port}/dashboard` |
| `windows-task` | `Stop is unavailable for the windows-task backend: schtasks /end is not a graceful stop` |
| Status đọc lỗi | `Gateway state unknown — {reason}` |
| Start khi đã chạy | nguyên văn `ErrAlreadyRunning` từ `daemon.Start()` (`internal/daemon/daemon.go:119-124`) |
| Supervisor stop lỗi | `supervisor_stop_failed` + backend, không kèm token |

Không chuỗi nào chứa `CLIToken()`, `api-key`, hay giá trị header.

---

## 8. Ma trận Nghiệm thu Đa nền tảng

### 8.1 Nguyên tắc

**Cross-compile không chứng minh gì về GUI.** `make cross` (`Makefile:204-215`) build `./cmd/9router-go/` — nó **không** chạm `cmd/9router-tray`, và cả nếu có thì một binary tray cross-build cũng không mở được một cửa sổ hay một icon. Mọi mục dưới đây phải chạy trên **host thật** của nền tảng đó.

### 8.2 Ma trận

| # | Nền tảng | Ca | Cách kiểm | Kết quả đạt |
| --- | --- | --- | --- | --- |
| W1 | Windows 11 | Icon hiện trong notification area | chạy `9router-tray.exe`, nhìn | icon hiện trong 10 s, có tooltip |
| W2 | Windows 11 | Menu Running đủ 5 mục | click phải icon | đúng 5 mục §7.1 |
| W3 | Windows 11 | `Stop Gateway` từ chối | click `Stop Gateway` | thông báo §7.4 dòng 2 hoặc 3; gateway **vẫn sống** (`GET /health` 200) |
| W4 | Windows 11 | `Quit Tray` giữ gateway | `Quit Tray`, rồi `curl /health` | 200, cùng PID như trước |
| W5 | Windows 11 | Scheduled Task installed | `9router-go service install`, mở tray | trạng thái `Running` hoặc `Unknown`; **không** `Stopped` |
| W6 | Windows 11 | Gateway dừng, **không** cài autostart | `9router-go stop`, không `service install`, mở tray | `Stopped` với `Start Gateway` có mặt — đây là ca phân biệt `ok == false` với `Unknown` (§7.1) |
| M1 | macOS 14+ | Icon hiện trên menu bar | chạy `9router-tray`, nhìn | icon hiện, không bị cắt |
| M2 | macOS 14+ | Menu + `Open Dashboard` | click | mở `http://127.0.0.1:{port}/dashboard` |
| M3 | macOS 14+ | `Stop Gateway` từ chối (không L2) | click | thông báo §7.4 dòng 2; gateway sống |
| M4 | macOS 14+ | `launchd` integration | `service install` qua launchd, mở tray | trạng thái đọc được từ `launchctl`; không `Unknown` giả |
| M5 | macOS 14+ | `Quit Tray` giữ gateway | như W4 | 200, cùng PID |
| L1 | Linux (GNOME) | Icon hiện qua AppIndicator | chạy `9router-tray` | icon hiện trong top bar |
| L2 | Linux (GNOME) | `systemd-user` stop không token | `service install`, `Stop Gateway` | gateway dừng; **không** có header `x-9r-cli-token` trong bất kỳ request nào (kiểm bằng `tcpdump -A -i lo port {port}`) |
| L3 | Linux | unsupervised + port lạ | chạy `python3 -m http.server {port}`, mở tray | `PortConflict`, `Stop` vắng mặt, không request nào rời tray |
| L4 | Linux | unsupervised + gateway thật | `9router-go start`, mở tray, `Stop Gateway` | L1+L2 qua, drain xong, `GET /health` fail, PID đã chết |
| L5 | Linux (KDE) | Icon hiện qua StatusNotifierItem | như L1 | icon hiện; nếu không, ghi nhận là giới hạn của DE, không phải bug |
| X1 | mọi | Không cgo trong core | `for pair in $(…CROSS_TARGETS…); do CGO_ENABLED=0 … go build ./cmd/9router-go/; done` | 5/5 pass |
| X2 | mọi | Không import GUI trong core | `go list -deps ./cmd/9router-go/` rồi grep ba tên `fyne`, `systray`, `gtk` | 0 dòng |
| X3 | mọi | `TestTray_QuitDecoupling` | `go test ./internal/tray/ -run TestTray_QuitDecoupling` | pass trên Linux CI; W4/M5 phủ hai nền tảng còn lại bằng tay |

### 8.3 Cái gì không được tính là đạt

| Không tính | Vì sao |
| --- | --- |
| `make cross` xanh | build không chạm tray, và cross-build không chạy GUI |
| `go vet` / `go build` trên nền tảng này | không chứng minh icon hiện, menu mở, hay event loop chạy |
| WSL / container | không có tray area |
| Chạy tray trên CI headless | không có display; exit sớm không phải pass |
| "Đã build được cho darwin" | macOS acceptance cần macOS thật, có menu bar |

---

## 9. Đường nâng cấp Phase 2 — UDS / Named-pipe Control Channel

### 9.1 Tại sao đây là đường đúng, không phải `GetExtendedTcpTable`

Task 7 §3.5 ghi `iphlpapi.dll` + `GetExtendedTcpTable` là Phase-2 route cho L2 trên Windows. Nó **đúng**, và nó **chỉ** giải Windows: macOS vẫn không có API unprivileged để hỏi "ai giữ cổng này", nên macOS vẫn fail closed.

Một control channel dùng peer credentials giải **cả ba** nền tảng bằng cùng một mô hình: kernel gắn danh tính vào kết nối, người nhận đọc danh tính đó từ chính socket. Không tra bảng, không `unsafe`, không race với PID reuse.

### 9.2 Chi tiết theo nền tảng (đã kiểm chứng offline trong `golang.org/x/sys@v0.48.0`)

| Nền tảng | Primitive | Vị trí |
| --- | --- | --- |
| Linux | `SO_PEERCRED = 0x11` | `unix/zerrors_linux_amd64.go:373` |
| Linux | `unix.GetsockoptUcred(fd, level, opt) (*Ucred, error)` | `unix/syscall_linux.go:1283` |
| Linux | `Ucred{Pid int32; Uid uint32; Gid uint32}` | `unix/ztypes_linux.go:465-469` (chung cho mọi arch) |
| darwin | `SOL_LOCAL = 0x0` | `unix/zerrors_darwin_arm64.go:1373` |
| darwin | `LOCAL_PEERCRED = 0x1`, `LOCAL_PEERPID = 0x2` | `unix/zerrors_darwin_arm64.go:935`, `:938` |
| darwin | `unix.GetsockoptXucred(fd, level, opt) (*Xucred, error)` | `unix/syscall_darwin.go:488` |
| darwin | `Xucred{Version uint32; Uid uint32; Ngroups int16; Groups [16]uint32}` — **principal, không PID** | `unix/ztypes_darwin_amd64.go:322-327` |
| Windows | `CreateNamedPipe(name, flags, pipeMode, maxInstances, outSize, inSize, defaultTimeout, sa)` | `windows/zsyscall_windows.go:1938` |
| Windows | `ConnectNamedPipe(pipe Handle, overlapped *Overlapped)` | `windows/zsyscall_windows.go:1838` |
| Windows | `GetNamedPipeClientProcessId(pipe Handle, clientProcessID *uint32)` | `windows/zsyscall_windows.go:2539` |
| Windows | `GetNamedPipeServerProcessId(pipe Handle, serverProcessID *uint32)` | `windows/zsyscall_windows.go:2563` |
| Windows | `GetCurrentProcessId()` | `windows/zsyscall_windows.go:2355` |
| Windows | `PIPE_ACCESS_DUPLEX = 0x3`, `PIPE_ACCESS_INBOUND = 0x1` | `windows/types_windows.go:3474`, `:3472` |
| Windows | `PIPE_TYPE_BYTE = 0x0`, `PIPE_WAIT = 0x0`, `PIPE_UNLIMITED_INSTANCES = 255` | `windows/types_windows.go:3483`, `:3479`, `:3488` |

**Trên darwin, `LOCAL_PEERCRED` cho principal, `LOCAL_PEERPID` cho PID** — phải đọc cả hai. Một mình `Xucred` không đủ để bind theo PID.

### 9.3 Vì sao đây là Phase 2 và KHÔNG phải hành vi hiện tại

**Gateway hôm nay không nghe trên UDS hay named pipe.** Kiểm chứng:

```
grep -rn "ListenUnix\|DialUnix\|\\\\.\\\\pipe\|net.Listen(\"unix\")" --include=*.go internal/ cmd/
```

**0 hit.** Không listener nào là UDS hay named pipe: mọi listener của gateway đều là TCP — chi (`internal/app/server.go:103`), MITM trên `127.0.0.1:443` (`internal/mitm/server.go:106`), và codex OAuth proxy (`internal/handlers/oauth/codex_proxy.go:166`). Vì vậy:

- Tài liệu này **không** được đọc như thể tray có thể "dùng control channel" hôm nay.
- §5 định nghĩa hành vi **hôm nay**: fail closed trên Windows/macOS.
- Thêm control channel là thay đổi **phía gateway**, ngoài G27, cần spec riêng và một lần review bảo mật riêng (nó thêm một listening surface).

### 9.4 Chi phí so sánh

| Đường | Cần `unsafe` | Cần khai báo layout nhị phân | Giải macOS | Thay đổi gateway |
| --- | --- | --- | --- | --- |
| `GetExtendedTcpTable` | **có** | **có** (`MIB_TCPTABLE_OWNER_PID`, `MIB_TCPROW_OWNER_PID`) | không | không |
| Named pipe (Windows) | **không** | **không** | — | **có** |
| UDS (POSIX) | **không** | **không** | **có** | **có** |

Đây là lý do đường control channel được ghi ở đây dù nó ngoài phạm vi: nó là đường **duy nhất** trong ba đường giải được cả ba nền tảng, và nó không cần `unsafe` ở đâu cả.

---

## 10. Cái tài liệu này KHÔNG xác minh

Khai báo tường minh, vì một tài liệu không nói ra giới hạn của nó sẽ bị đọc như thể không có.

| Không xác minh | Hệ quả |
| --- | --- |
| `/health` là danh tính | **Nó không phải.** Hằng số công khai (`router.go:409`). Chỉ dùng để biết có listener |
| `daemon.RunningPID()` là liveness oracle | Nó chỉ trả > 0 khi `claim.owner()` qua; trả 0 khi unverifiable. **0 ≠ "không chạy"** |
| `Info.Active` là liveness oracle khi `ActiveUnknown` | `ActiveUnknown` đặt thì `Active` **không mang thông tin** (`internal/service/service.go:76-80`) |
| Thư viện GUI cụ thể build được | §4.1 — khảo sát knowledge-based, không có lần build nào |
| Tray chạy được trên nền tảng X | §8 — chưa có acceptance nào chạy |
| Control channel tồn tại | §9.3 — không tồn tại hôm nay |
| macOS/Windows có L2 | Không có. §5.2, §5.4 |
| `daemon.Stop()` an toàn để tái dùng | Không — nó tự escalate `proc.Terminate` → `ForceKill` (`daemon.go:197`, `proc.go:25-39`); Task 7 C2 cấm |
| Xung đột port được giải quyết tự động | Không — tray báo và đứng yên |
| `--force` từ tray | Không có trong G27; force là invocation riêng có xác nhận (Task 7 §6) |

**Ba trạng thái render phải trung thực:** `Running`, `Stopped`, `Unknown`. Một tray nói `Stopped` khi thực chất là `Unknown` đang nói dối, và §7.1 chặn cả hành vi Start trong trạng thái đó.

---

## 11. STOP Gate Declaration

| Kiểm tra | Kết quả |
| --- | --- |
| Tệp source Go được tạo | **0** — không có `cmd/9router-tray/`, không có `internal/tray/` |
| Tệp `.go` bị sửa | **0** |
| `go.mod` / `go.sum` bị thay đổi | **0** |
| Test bị thêm/sửa | **0** |
| Dependency được tải về | **0** — mọi lệnh offline với `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` |
| Tệp được tạo | đúng **1**: `docs/specs/tray-companion-architecture.md` |

Deliverable là **quyết định thiết kế được ghi lại**, không phải code. Mọi hành vi trong tài liệu này là hành vi **sẽ** được viết, không phải đã viết.

### 11.1 Tự đo

| Đại lượng | Đo được |
| --- | --- |
| Dòng trong tệp | 585 |
| Khối bảng GFM | **21** |
| Dòng dữ liệu bảng | **189** |
| Pipe-arity mismatch | **0** |
| Pipe escape trong ô bảng | **0** — mọi pipe trong ô đều là pipe thật, không pipe nào cần escape. Cả 3 ký tự backslash-pipe của tài liệu (cùng một dòng, trong fenced code block của §9.3) đều không thuộc bảng nào |
| Bất biến C1–C6 | **6** |
| Trích dẫn `file:line` | **98** dạng duy nhất (115 lần xuất hiện); mọi trích dẫn trong repo đã đối chiếu từng dòng; mọi trích dẫn `x/sys` đã đối chiếu trong `golang.org/x/sys@v0.48.0` |
| Tham chiếu `§` treo | **0** — mọi `§N` nội bộ đều có heading tương ứng; ba tham chiếu `§3.5`, `§3.7`, `§6.4` trỏ `service-drain-coordination.md` và đã xác nhận tồn tại ở đó |

---

## 12. Phụ lục — Nguồn đã đọc

| Nguồn | Dùng cho |
| --- | --- |
| `internal/daemon/daemon.go` | `Dir`, `PIDFile`, `RunningPID`, `Start` (spawn + Release), `Stop` (force fallback), `daemonLocalURL`, `portBusy`, `childArgs` |
| `internal/daemon/pidfile.go` | `claim` struct, `parseClaim`, `owner()` fail-closed, `resolvedPath` |
| `internal/daemon/health.go` | `EnvBackground`, `IsBackgroundProcess`, `HealthPath`, `waitHealthy`, `probeHealthy` |
| `internal/service/service.go` | `Backend` constants, `Info` (`ActiveUnknown`), `Status()` probe order, `Install`/`Uninstall`, `ErrNotInstalled` |
| `internal/service/control.go` | `Start`/`Stop`/`control`, `controlTimeout`, `controlWindowsTask` (`/end`), `ErrNoSupervisor` |
| `internal/service/systemd.go`, `launchd.go`, `windows.go` | `unitName`, `launchdLabel`, `windowsTaskName`, `Restart=always`, `RestartSec=3`, `KeepAlive` |
| `internal/auth/clitoken.go`, `session.go` | `CLIToken()` (machine-wide), `ValidCLIToken`, `CLITokenHeader` |
| `internal/middleware/dashboard_auth.go` | `IsAlwaysProtectedPath`, `RequireAdminAuth` — thứ token mở được |
| `internal/handlers/router.go`, `shutdown.go` | `/health` công khai, `/api/version/shutdown` + gate, `HandleShutdown` (500 ms rồi `RequestStop`) |
| `internal/config/config.go` | `ResolveDataDir`, `LoadConfig().Port`, `HOST`/`BIND_ADDR` |
| `internal/proc/proc.go`, `proc_windows.go` | `Alive`, `Terminate` (tự escalate), `ForceKill`, `SelfExecutable`, `ErrStopUnsupported` |
| `cmd/9router-go/main.go`, `daemon_cmd.go` | `signal.Notify`, nhánh `select` shutdown, `fxApp.Stop(20s)`, các lệnh daemon |
| `Makefile`, `AGENTS.md` | `CROSS_TARGETS`, `cross-one`, §4.F invariants và hai điều kiện bắt buộc |
| `golang.org/x/sys@v0.48.0` | peer-credential + named-pipe primitives (§9.2); xác nhận `Shell_NotifyIcon` và `GetExtendedTcpTable` **vắng mặt** |
| `docs/specs/service-drain-coordination.md` | quy tắc hai đường §3.7, C2 (cấm force-trong-cùng-lần-chạy), §6.4 (`windows-task` + `--force` từ chối), lý do L3 bị xoá |
| `docs/superpowers/specs/2026-10-18-9router-go-desktop-design.md` | §3.A luồng tray, invariants, ma trận test, yêu cầu `TestTray_QuitDecoupling`. Tệp đã được đưa vào repo đích (trước đó chỉ nằm untracked trong worktree điều phối `D:/Code/9router/.claude/worktrees/upstream-05104-merge/`), nên trích dẫn nay kiểm chứng được từ `9router-go` |

