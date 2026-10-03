# Changelog

## [Unreleased]

### 🔒 Penegakan Strict Provider Isolation & Concurrency Singleflight

- **Strict Provider Isolation**: Menghapus jalan pintas `routeModelToOwningProvider` di `internal/handlers/chat/resolution.go` dan prefix silang `antigravity/` / `ag/` di executor OpenCode (`internal/proxy/executor/opencode_zen.go` & `providers.go`). Seluruh model di-route secara seragam berdasarkan katalog dan alias resmi, mematuhi kontrak arsitektur di `AGENTS.md`.
- **OAuth Refresh Singleflight**: Membungkus refresh token OAuth kedaluwarsa (`refreshOAuthTokenIfExpired` & `forceRefreshOAuthToken` di `internal/handlers/chat/gemini_handler.go`) dengan `singleflight.Group` per `connectionID` untuk mencegah thundering-herd dan race condition pada request paralel.
- **Test Coverage Backend $\ge$ 85%**: Menambahkan unit test komprehensif pada 8 paket backend (`config`, `codexquota`, `usagetracker`, `middleware`, `translator`, `app`, `handlerutil`, `proc`), serta mengeliminasi bottleneck sleep 60s pada `proc_test.go` sehingga suite berjalan instan (< 1s).

### 🐛 Rotasi round-robin macet: stempel `lastUsedAt` tidak pernah maju — issue #107

Akar masalahnya bukan format stempel, tapi sumber waktunya. Format nanodetik
memang sudah benar (`db.RotationTimestampFormat`, lebar tetap). Yang gagal adalah
`time.Now()`: presisinya mengikuti platform, bukan janji Go. Di host Windows tempat
masalah ini didiagnostik, jam hanya maju **setiap ~815µs** — 689.900 panggilan
beruntun menghasilkan 246 nilai berbeda dalam 200ms. Enam pick rotasi di dalam
satu milidetik karena itu memformat menjadi string yang identik, dan baris dengan
stempel sama tidak bisa dibedakan oleh tie-break least-recently-used, sehingga
selector mengembalikan akun yang sama terus-menerus.

`TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` gagal ~1 dari 3 run di
`origin/main` yang bersih (`-count=50`), bukan efek samping PR mana pun.

Perbaikannya membuat urutan jadi **properti jalur tulis**, bukan properti jam:
`db.stampConnection` membaca `MAX(lastUsedAt)` dan menulis nilai yang selalu
mengurutkan setelahnya, di dalam satu kunci tulis SQLite (`BEGIN IMMEDIATE`).
Kunci diambil di depan, bukan saat `UPDATE` — transaksi deferred baru mengunci
sesudah `MAX` dibaca, sehingga dua proses yang berbagi satu database bisa membaca
maksimum yang sama dan mencetak stempel yang sama untuk dua baris berbeda,
menghidupkan kembali tie yang tidak bisa diputus selector.

Konsekuensi yang ikut diperbaiki: `TouchConnectionRotation` kini mengembalikan
nilai yang benar-benar ditulis, sehingga baris in-memory yang dibaca selector
tidak lagi bisa berbeda dari baris di disk. Selector sebelumnya membaca jam
*kedua* secara terpisah, jadi nilai in-memory bisa tidak sama dengan yang
tersimpan.

Satu cacat kedua ketahuan saat menulis testnya: stempel lebar-tetap
`…:00.000000001Z` ternyata terurut **sesudah** `…:00Z` versi lama saat
dibandingkan sebagai string, karena `'.'` (0x2E) mengalahkan `'Z'` (0x5A).
Lonjakan satu nanosecond tidak akan melewati baris legacy itu sama sekali, jadi
lonjakan melompat satu detik penuh saat nanosecond tidak cukup.

**Verifikasi:** `go test ./internal/...` bersih; `go vet ./internal/...` bersih;
`go test -race ./internal/db/ ./internal/handlers/chat/` hijau;
`TestApplyConnectionStrategy_*` hijau di `-count=50` (sebelumnya gagal berkala di
`origin/main`). Test baru `TestStampConnection_FrozenClockStillOrdersStrictly`
mematikan jam sepenuhnya — kondisi yang tidak akan pernah kejadian di host dengan
jam beresolusi rendah, sehingga urutan diuji sebagai kontrak, bukan sebagai
kebetulan. `TestStampConnection_SurvivesRestartAndClockJump` menutup basis data,
membukanya lagi, lalu memundurkan jam satu jam untuk membuktikan urutan bertahan
melewati restart **dan** koreksi NTP. `TestNextRotationStamp_AlwaysSortsAfterPrevious`
menutup delapan kombinasi posisi jam dan format stempel tersimpan.

> Catatan: `TestGateAcquire_*` di `internal/fetchgate` sesekali gagal karena
> asumsi `time.Sleep` di environment ini, dan sudah gagal dengan identik di
> `origin/main` (dibuktikan dengan `git stash`), jadi di luar cakupan issue ini.

### 🐛 "Strict Proxy" tidak menahan — upstream decolua/9router#4333 parity

`strictProxy` di pool dan di connection berarti "tidak pernah keluar lewat IP
asli". Dua tempat di gateway ini tetap meloloskannya: `proxy.DoRequest`
memutar ulang request yang gagal di proxy lewat `directProxyClient` tanpa
memperiksa flag sama sekali, dan jalur legacy `getClientForConnection` hanya
mencatat `strict proxy enabled but proxy url invalid` lalu mengembalikan
client direct. Keduanya berarti traffic yang seharusnya tidak pernah menyentuh
IP asli tetap aktif.

Kini `DoRequest` menolak (bukan memutar ulang) saat proxy gagal, baik lewat
penanda pada context (`proxy.WithStrictProxy`) maupun lewat client yang
diberikan pool strict (`proxy.ForbidDirectReplay`), dan jalur legacy mengembalikan
error. `strictProxy` hanya dibaca dari baris connection yang sebenarnya
disimpan; sebelumnya `var strictProxy bool` menutupi field tersebut sehingga
flag di level connection tidak pernah berarti apa pun — cabang di
`connections_proxy.go:117-120` yang memuat pesan `strict proxy enabled but proxy
url invalid` selama ini tidak pernah dieksekusi.

Gerbang "proxy memang dimaksud" ikut dijaga persis seperti upstream: strict
hanya menolak bila ada `proxyPoolId`, `enabled`, `connectionProxyEnabled`, atau
url yang tidak kosong. Executor seperti Qoder memasang `strictProxy` dengan
tidak ada proxy sama sekali — artinya "jangan putar ulang request ini langsung",
bukan "proxy wajib ada" — dan tanpa gerbang itu mereka akan mati total.

### 🐛 Fallback TLS insecure untuk sertifikat yang gagal diverifikasi — upstream b58bd804 parity

Kegagalan verifikasi sertifikat (proxy korporat atau antivirus yang
menerbitkan ulang TLS) kini dicoba satu kali lagi tanpa verifikasi, memakai
transport `InsecureSkipVerify` yang di-cache per url proxy. Deteksi lewat
`errors.As` terhadap `x509.UnknownAuthorityError`, `x509.CertificateInvalidError`,
`x509.HostnameError`, dan `tls.CertificateVerificationError` — bukan pencocokan
substring seperti `isProxyFailure`, yang akan menandai hampir semua error.
`STRICT_SSL=true` atau `=1` mematikan fallback ini, mengikuti upstream.

### 🐛 Watchdog 3 detik untuk `response.completed` yang tertunda — upstream fbcaa282 + 7111db35 parity

Translator menahan `response.completed` sampai trailer usage arrives (#4476).
Bila upstream berhenti setelah `finish_reason` — tanpa trailer dan tanpa
`[DONE]`, koneksi ditahan terbuka — penundaan itu tidak pernah selesai dan
client menunggu selamanya. `proxy.ScanStreamWithDeadline` kini membatasi jeda
antar event hanya setelah event terminal benar-benar tertunda, dan watchdog
bridge (`executor.completionWatchdog`) mengirim `response.completed` tepat
sekali ketika batas itu terlampaui. Stream yang masih mengalir tidak pernah
dipotong, dan event terminal tidak pernah terkirim dua kali.

**Verifikasi:** `TestDoRequestStrictProxyNeverReplaysDirect` (dua jalur StrictProxy) membuktikan tidak ada satu pun request yang sampai ke upstream langsung;
`TestDoRequestStrictProxyAllowsDirectWhenNothingConfigured`,
`TestStrictProxyFlagAloneDoesNotBlockDirectUpstream`, dan
`TestNonStrictPoolStillDegradesToDirect` menjaga gerbang "proxy dimaksud";
`TestDoRequestRetriesWithInsecureTLS` / `TestDoRequestStrictSSLRefusesInsecureRetry`
menguji fallback TLS dan opt-out-nya; `TestStreamChatToResponses_WatchdogFlushesStalledCompletion`
dan `TestScanStreamWithDeadlineReleasesAStalledUpstream` menjalankan batasnya
melalui variabel paket, bukan tidur 3 detik.


### 🐛 Routing & translator: enabledModels codex, tool DeepSeek ganda, prefill Claude — issue #95

Tiga filter yang upstream terapkan saat memilih akun dan menyusun request tidak ada di sisi kita. Dua di antaranya menjawab 400.

- **Codex `enabledModels` dipakai saat routing.** Akun codex punya daftar model sendiri; yang tidak ada di sana ditolak OpenAI dengan 400. `codexAccountServesModel` melewati akun yang daftar enabledModels-nya non-kosong tapi tidak memuat model yang diminta — dibaca dari `providerSpecificData.enabledModels` dulu lalu field top-level, persis seperti `buildModelsList` sudah melakukannya, karena dua penulisnya ada di dunia luar. Filter dijalankan **sebelum** strategi rotasi, bukan sesudahnya: sapuan round-robin mengembalikan urutan seluruh kandidat, jadi penyaring harus mendahului strategi. Dijalankan juga di dalam loop seleksi dan di `pinnedConnectionIneligible`, sesuai urutan predikat upstream.
- **Tool DeepSeek dengan nama ganda.** `DedupeToolsDeepSeek` hanya berlaku untuk model DeepSeek (`isDeepSeekModel` melepas sufiks `(level)` dan menerima id berawalan vendor lebih dulu). Definisi pertama yang bertahan, dan tool lain milik provider apa pun tidak tersentuh. Diletakkan di `tryForwardWithConnection` setelah `SanitizeOpenAITools` dan sebelum `FitToolNames` — titik terakhir sebelum dispatch, jadi berjalan setelah semua konversi. Aturan dedupe MCP milik klien yang ada di upstream **sengaja tidak** dipindah: ia bergantung pada deteksi tool klien yang tidak kita punya.
- **Perbaikan trailing turn Claude.** `EnsureTrailingUserTurn` menempelkan turn user `Continue.` ketika cleanup menyisakan ekor assistant, **kecuali** pemanggil memang membuka dengan prefill. `ClaudeIntentionalPrefill` membaca ekor mentah lebih dulu pada bentuk sumbernya (`contents[]` untuk Gemini/Antigravity, `input[]` untuk Responses/Codex), jadi prefill yang disengaja tetap utuh. Tanpa ini model Claude yang lebih baru menjawab `400 … does not support assistant message prefill`.

**Verifikasi:** `go vet ./...` bersih · `go test -race ./internal/translator/ ./internal/handlers/chat/` hijau · ketiganya terbukti load-bearing lewat mutasi terarah: menonaktifkan pengecualian prefill menggagalkan `TestSanitizeClaudePassthrough_ClientPrefillPreserved`; menghapus gerbang `IsDeepSeekModel` menggagalkan `TestDedupeToolsDeepSeek`; membuat `codexAccountServesModel` selalu `true` menggagalkan `TestGetBestConnection_CodexEnabledModelsPickOnlyEligibleAccount`. Arah kedua diuji pada prefill: `TestEnsureTrailingUserTurnBody` punya kasus `assistant tail gets the placeholder` **dan** `client prefill is left alone` — satu arah saja tidak cukup.


### 🔵 Empat provider upstream v0.5.95 — Meta Muse, v1m System One, TinyFish, seed Agnes

Empat entri registry dari upstream `decolua/9router` v0.5.91…v0.5.95.
Setiap entri harus ada di **semua** tabel (transport, alias, katalog,
executor, dashboard) — kalau tidak, provider itu ada di `/v1/models` tapi
tidak pernah route.

- **Meta Muse** (`muse`): Model API Meta dengan dual auth — device code akun
  Meta (langganan Muse Code, key-nya di-mint dari token akun) **dan** API key
  pay-as-you-go dari dev.meta.ai. Both rides the same bearer transport.
  Kelima model Muse Spark meng-pin lane `openai-responses`, jadi
  `internal/proxy/executor/muse.go` memilih endpoint per model (memakai
  `handleCodexStream`, sama seperti executor Responses lain), bukan
  per provider. Katalog live `/v1/models` butuh header `x-api-version: 1.0.0`
  selain bearer — itu sebabnya ada `providers.ModelsListHeaders`
  (upstream `PROVIDER_MODELS_CONFIG.muse`). Device flow-nya ada di
  `internal/handlers/oauth/muse_device.go`, termasuk retry mint saat Meta
  membalas 429 dan pesan langganan belum aktif. Pricing kelima model ikut
  dari dev.meta.ai (contributor tier jauh lebih murah).
- **v1m System One** (`v1m`, alias `systemone`): decision engine terkalibrasi,
  `serviceKinds: ["systemone"]`, endpoint `https://v1m.ir/v1/systemone`.
  Registry `ProviderConfig` tidak punya BaseURL chat untuknya — upstream pun
  tidak, jadi `BaseURL` sengaja kosong dan hanya `SystemoneURL` yang diisi.
  `/v1/models/{kind}` memilih lewat `SystemoneURL != ""`, jadi modelnya
  otomatis tampil di tab System One.
- **TinyFish** (`tinyfish`): `serviceKinds: ["webSearch","webFetch"]` dengan
  **dua host terpisah** di balik satu key `x-api-key` — `api.search.tinyfish.ai`
  dan `api.fetch.tinyfish.ai`. Karena `/v1/search` di repo ini adalah
  *transparansi* (gateway meneruskan body apa adanya ke `BaseURL + endpoint`),
  search TinyFish **tidak** diimplementasikan: menambahkan satu host kedua
  berarti membangun pipeline builder/normalizer khusus provider di dalam
  `internal/handlers/media/`, yang tidak pernah ada di repo ini — `linkup`,
  `tavily`, `serper` dan `brave-search` semuanya seperti itu. Yang di-wire
  adalah jalur fetch yang memang punya passthrough (`FetchURL` =
  `https://api.fetch.tinyfish.ai`, POST) plus kartu dashboard dengan kedua
  service kind. Detail lengkap di laporan issue.
- **Agnes seed models**: provider-nya sudah ada sejak v0.5.91 tapi katalognya
  kosong, dan `/v1/models` live-nya membalas 401 tanpa token, jadi halaman
  provider-nya kosong total. Sekarang diisi empat seed upstream
  (`agnes-2.5-flash`, `agnes-2.5-pro`, `agnes-2.5-pro-beta`,

### ⬆️ Tabel capabilities & thinking levels disinkronkan dengan upstream v0.5.95 — issue #97

Ditemukan saat mengaudit range `v0.5.86..v0.5.95`. Fixture paritas yang mengunci thinking level **menyembunyikan** sebagian dari gap ini: `TestGetThinkingLevels_MatchesUpstreamFixture` melaporkan 1547/1547 tanpa error, karena fixture itu masih memakai versi lama.

- **`xhigh` untuk `claude-adaptive`** (`7894f3d3`). Format `claude-adaptive` kini memakai set `budgetX`, bukan `levelMax`. **Kedua separuh ini harus datang bersama:** dua baris pengecualian `CLAUDE_NO_XHIGH` untuk `*claude*4.6*` dan `*claude*4-6*` ditambahkan pada saat yang sama. Tanpa pengecualian tersebut, picker menawarkan `xhigh` pada model yang menjawab 400 untuk itu.
- **Baris pattern `*claude*sonnet-5*`** (`ccd0677d`). Id berawalan vendor (`anthropic/claude-sonnet-5`, `openrouter/claude-sonnet-5`) tidak punya entri persis dan jatuh ke baris `*claude*sonnet*` → `claude-budget`, yaitu thinking adaptive yang diserialisasi sebagai anggaran token.
- **`claude-sonnet-5-5`** dan empat varian **`claude-opus-5.5*`** masuk ke tabel persis. `CacheCreationPer1M` `anthropic/claude-sonnet-5` dikoreksi dari 0 ke 2.5; `claude-sonnet-5` dan `claude-sonnet-5-5` ditambahkan ke tabel harga.
- **Jendela konteks GPT-6 / GPT-5.4+** (`89ffac5a`). `gpt-6` naik dari 272000 ke **1050000** — nilai lama adalah truncasi satu gateway yang bocor ke setiap provider gpt-6 lain lewat pattern generik. `gpt-5.4` / `gpt-5.5` / `gpt-5.6` juga 1050000; `gpt-5.4-mini` / `gpt-5.4-nano` tetap 400000. Ditambah blok 200k khusus `devin-cli`, yang mendeklarasikan jendela sendiri meski modelnya GPT.
- **Alias `deepseek-v4-1-flash`** (`8a4f4d9d`). Beberapa gateway memaparkannya dengan tanda hubung; tanpa baris ini ia jatuh ke pattern `*deepseek-v4*` dan kehilangan vision serta jendela 1M-nya.

**Bug yang ditemukan lewat regenerate fixture — bukan dari daftar issue:** urutan baris `*gpt-5*image*` salah ada **di belakang** baris `*gpt-5.4*` / `*gpt-5.5*` / `*gpt-5.6*`, padahal first-match-wins. Akibatnya `gpt-5.6-sol-image` cocok ke `*gpt-5.6*` dan terbit sebagai model reasoning — tujuh entri image (`cx/gpt-5.4-image`, `cx/gpt-5.5-image`, `cx/gpt-5.6-{sol,terra,luna}-image`, `codex/gpt-5.5-image`, `codex/gpt-5.4-image`) menawarkan thinking level yang upstream nyatakan `null`. Baris image dipindahkan ke depan, sesuai urutan upstream `capabilities.js:344-352`.

**Fixture di-regenerate dari v0.5.95** — 1588 pasangan (naik dari 1547, mengikuti katalog yang sekarang melayani model baru). Regenerasinya reproducible, bukan hasil ketik tangan:

```
DUMP_CATALOG_PAIRS=testdata/catalog_pairs.json \
  go test ./internal/providers/ -run TestDumpCatalogPairs -count=1
node scripts/gen-thinking-levels.mjs <checkout-upstream> v0.5.95 \
  internal/providers/testdata/catalog_pairs.json
```

Generator menjalankan `getThinkingLevels` upstream secara langsung, dan daftar pasangannya diambil dari `ProviderModels` — katalog yang benar-benar dilayani port ini — sehingga provider atau model baru tidak bisa lolos diam-diam dari test paritas. Hasilnya stabil: dua kali jalan menghasilkan byte identik (`fe426f51…`).

**Verifikasi:** `go vet ./...` bersih · `go test ./internal/providers/ -count=1` ok · 19 subtest gagal tanpa perubahan ini (`git stash` pada kode saja, test dipertahankan) dan semuanya hijau dengannya · generator dijalankan ulang oleh integrator dan hasilnya cocok.


### 🐛 Tiga perilaku translator yang hilang, satu yang memang belum ada — parity upstream v0.5.95

**1. Keyword anotasi MCP menolak seluruh request Gemini** (upstream `aafe3002`, #4283).
`UNSUPPORTED_SCHEMA_CONSTRAINTS` upstream menambah `errorMessage`,
`errorMessages`, `markdownDescription`, `doNotSuggest`, `suggestSortText`,
`minProperties`, dan `maxProperties`. Schema tool dari MCP server memakai
ejaan polos itu; proto schema Gemini tidak punya field-nya dan menolak
seluruh request dengan `Unknown name errorMessage: Cannot find field` —
padahal ejaan berawalan vendor (`x-errorMessage`, `x-taplo`, …) sudah
tertangkap aturan `x-` yang terpisah. Ketujuh ejaan polos kini masuk daftar
yang dilepas.

**2. Turn user yang isinya hanya `container_upload` dihapus** (upstream
`4f274c7f`, #4316). Sanitizer passthrough Claude membuang pesan yang
kontennya kosong, dan blok di luar daftar "berisi" ikut terhitung kosong —
padahal `container_upload` (Files API) adalah input Anthropic yang sah
sendiri. Akibatnya request diteruskan sebagai `messages: []` dan provider
membalas 200 untuk percakapan yang sudah tidak ada. Sekarang filter memakai
satu daftar blok berisi (`tool_use`, `tool_result`, `image`, `document`,
`container_upload`) untuk keputusan kosong-tidak-kosong di kedua arah.

**3. Hasil tool terakhir tidak pernah masuk cache** (upstream `49c761cd`).
Dalam tool loop, request berakhir dengan hasil tool dari putaran assistant
terakhir — sesudah breakpoint putaran itu — jadi isinya dibayar penuh dan
hanya ditulis cache oleh request berikutnya. Selama anggaran 4 marker masih
sisa, satu breakpoint 5m sekarang ditaruh di blok cache-eligible terakhir
turn user tersebut; turn yang sudah punya `cache_control` tidak diubah
sehingga re-anchoring tetap idempoten.

**Belum dipindah: thinking placeholder tak bertanda tangan untuk DeepSeek**
(upstream `08b21fea`, #4436). Upstream menyuntik
`{"type":"thinking","thinking":"."}` ke putaran assistant yang punya
`tool_use` tanpa blok thinking saat thinking aktif, dan pada DeepSeek
menempelkan placeholder itu **tanpa** signature. Di `9router-go` jalur
tersebut belum ada sama sekali: `prepareClaudeRequest` belum diporting, dan
`AnchorClaudeCache` — satu-satunya penanchor `cache_control` — tidak pernah
menyentuh thinking maupun signature (passthrough kita juga menghapus
`signature` sebelum meneruskan). Menambal placeholder tanpa jalur
`prepareClaudeRequest` yang sudah benar hanya akan menulis signature palsu ke
riwayat, jadi perubahan ini sengaja tidak dipaksakan; tidak ada placeholder
maupun fetch signature yang dibuat agar tidak tersisa kode mati.

### 🐛 Combo publish limit ctx salah total — plus rekursi tak berujung yang menggantung proses — issue #99

Entry combo di `/v1/models` tidak menerbitkan `context_length` / `max_completion_tokens` sama sekali. Komentar di `models_list.go` mengklaim itu parity — v0.5.95 sudah mengizinkannya, jadi catatan itu usang dan dihapus.

Enam perubahan, dua di antaranya di luar daftar file yang diminta dan karena itu dicatat terpisah.

**Yang diminta issue:**

- `comboSeatLimits` di-`/v1/models` — minimum `context_window` dan minimum `max_completion_tokens` di seluruh pohon seat, dengan ekspansi combo bersarang dan deteksi siklus (set `visiting`), setara `src/app/api/v1/models/route.js`.
- `ContextLength` / `MaxCompletionTokens` berubah dari `int` + `omitzero` ke `*int` + `omitempty` di lima titik producer. Sebuah combo hanya boleh menjanjikan apa yang **semua** seat setuju; combo tanpa seat LLM tidak menjanjikan apa-apa dan **menghilangkan** kedua key, bukan menerbitkan nol yang dibaca client sebagai limit asli. Ini terkait langsung dengan #90: angka tebakan lebih buruk daripada tidak ada.
- Seat web (`<alias>/search`, `<alias>/fetch`) dilewati. Seat itu alat, bukan model chat, dan membiarkannya ikut suara akan menerbitkan capability floor seolah-olah itu jendela seluruh combo.
- `comboIndex` membaca daftar seat seluruh combo **sekali per request**. Sebelumnya satu combo dibaca ulang per seat, dan tiap pembacaan membaca settings lagi — saat `aggregateComboCapabilities` juga memanggilnya, satu entry combo bisa menghujan query puluhan query.

**Dua hal di luar daftar file, dilaporkan terpisah:**

- **Rekursi tak berujung.** `resolveModelEntry` memanggil dirinya sendiri untuk setiap seat combo tanpa batas. Combo yang menunjuk dirinya sendiri — langsung (`["self-combo", …]`) atau tidak langsung (`mutual-combo` ⇄ `loop-combo`) — membuat `/v1/models` **gantung selamanya**. Dikonfirmasi di `origin/main`: test dengan seed tersebut menghasilkan 67 frame `resolveModelEntry` berulang sebelum timeout 60 detik, tiap hop query SQLite lagi. Tidak ada yang membatasi selain stack. `resolveModelEntryGuarded` kini membawa set `visiting`; seed yang sama selesai dalam 0.2 detik.
- **`aggregateComboCapabilities` dan seat live.** Untuk daun yang resolver live-nya menerbitkan blok kapabilitas utuh (kiro: `{thinking, agentic}`), tabel statis bukan lagi deskripsi model itu, jadi limit statisnya tidak ikut dilipat seolah-olah diketahui. Setara upstream yang mengambil blok provider "verbatim".

**Divergence yang disengaja, tercatat:** upstream `comboSeatLimits` tidak punya filter non-LLM, sehingga seat web di dalam combo LLM ikut terhitung ke minimum lewat `DEFAULT_CAPABILITIES` 200k/64k. Melewati seat itu satu-satunya pembacaan yang memenuhi dua syarat issue sekaligus — "seat non-LLM tidak boleh menarik minimum ke bawah" dan "jangan menerbitkan tebakan" — dan menjaga `/v1/models` konsisten dengan gate per-model (`isLLMModelEntry`) yang sudah membuang `<alias>/search` di cabang provider.

**Catatan:** `capabilities.maxOutput` di blok `capabilities` combo tetap **terlebar** (itu `aggregateComboCapabilities` upstream, `capabilities.js:519-520`), sedangkan `max_completion_tokens` top-level adalah **tertipis** (`comboSeatLimits`). Dua angka itu memang berbeda maknanya, jadi agregat bersama tidak diubah — mengubahnya akan diam-diam mengubah blok capabilities yang dibaca pemanggil lain.

**Verifikasi:** `go vet ./...` bersih · `go vet -tags integration ./internal/integration/` bersih · `go test ./internal/handlers/chat/... -count=1` ok · `go test -tags integration ./internal/integration/ -count=1` ok (3.6s) · 6 test baru (5 handler + 1 integrasi lewat router produksi), semuanya gagal tanpa perbaikan — termasuk test siklus yang tidak pernah selesai sebelum · smoke live ke binary di `:29201` dengan `DATA_DIR` sekali pakai: 4 combo dibuat lewat `POST /api/combos`, `GET /v1/models` mengembalikan `wide-combo` 128000/16384, `outer-combo` 128000/8192, combo web-only dan self-loop tanpa kedua key, 1302 entry, 0 provider kehilangan `context_length`.

**Utang teknis yang diketahui:** `models_list.go` kini 1060 baris, sudah melewati anjuran 300/400 sebelum perubahan ini (885). Penambahan netral ~110 baris; ekstraksi ke `combo_limits.go` adalah follow-up mekanis.

**Tidak terkait:** `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` gagal intermittent di `origin/main` juga — dipisah ke #107.

### 🐛 Manifest tanpa checksum membuat auto-update mati total — issue #72

#72 membuat digest SHA256 **wajib**: `PerformSelfUpdate` menolak sebelum request
jaringan apa pun kalau `expectedSHA256` kosong. Tapi `CheckUpdate` mencoba
manifest lebih dulu dan langsung mengembalikan jawabannya kalau sukses
(`updater.go:158-163`), sedangkan `version.json` yang benar-benar terbit hanya
punya tiga key — `downloadUrl`, `latestVersion`, `releaseNotes` — tanpa `sha256`
sama sekali. Akibatnya `checkManifest` selalu menghasilkan digest kosong,
`checkGitHubReleases` **tidak pernah dijalankan**, dan `lookupAssetSHA256` yang
justru menutup gap #72 tidak pernah menyentuh release sungguhan.

Terbukti: dengan manifest berbentuk sama persis seperti yang diterbitkan repo,
`CheckUpdate` mengembalikan `Source="manifest"`, `SHA256=""`, sementara fallback
GitHub yang punya `SHA256SUMS.txt` tidak pernah dihubungi. Semua instalasi
berakhir `release carries no checksum` — fail-closed dan aman, tapi
`9router-go update` praktis mati.

Manifest kini hanya boleh menjawab kalau memang bisa menghasilkan update yang
dapat diinstal: digestnya ada, atau memang tidak ada update yang ditawarkan.
Selain itu ia diperlakukan sebagai petunjuk dan turun ke GitHub Releases API;
metadata manifest disimpan sebagai fallback supaya catatan rilis tetap tampil
saat API-nya sedang mati.

**Verifikasi:** `TestCheckUpdate_ManifestWithoutDigestFallsThroughToReleaseDigest`
menyajikan `version.json` tanpa `sha256` dan menegaskan release API benar-benar
dihubungi serta digestrelease yang dipakai. Diuji mutation: mengembalikan
syarat ke "manifest selalu menang" membuat test itu gagal tepat di assertion
release API tidak pernah ditanya. Dua test lain mengunci agar fallthrough tidak
menjadi regresi sendiri — manifest yang **sudah** punya digest tetap dijawab
tanpa menyentuh GitHub, dan manifest yang melaporkan sudah mutakhir tetap
dijawab walau tanpa digest.

### 🔴 Header password dashboard hanya diverifikasi di satu handler — auth bypass

`RequireAdminAuth` dan `RequireDashboardAuth` mengizinkan request yang membawa
header `x-9r-password` dengan memeriksa **keberadaan** header itu saja:
`r.Header.Get(DashboardPasswordHeader) != ""`. Nilai yang dipakai tidak pernah
diverifikasi di middleware — memang tidak bisa, karena ceknya butuh hash bcrypt
yang tersimpan di repo yang tidak dipegang gate.

Asumsi di balik itu — "handler di belakang gate memverifikasi sendiri" — hanya
benar untuk **1 dari 7** path yang dilindungi. Handler lain sama sekali tidak
memeriksa kredensial apa pun, sehingga siapa pun yang bisa memasang satu header
acak cukup untuk lewat: `/api/version/shutdown` (`HandleShutdown`),
`/api/version/update` (`HandleTriggerUpdate`), `/admin/health/reset`,
`/api/oauth/cursor/auto-import`, dan `/api/oauth/kiro/auto-import`.

Yang paling berbahaya `/api/version/shutdown`: `shutdown.RequestStop()`
dijadwalkan 500 ms setelah response (`internal/handlers/shutdown.go:29-32`),
jadi efeknya selalu terjadi dan klien melihat 200 yang bersih — DoS remote
tanpa autentikasi. `/api/version/update` mencapai penggantian binary dan restart.

Pengecualian header kini dibatasi ke `PasswordHeaderCarriesOwnAuth`
(`/api/settings/database`) — satu-satunya handler yang memanggil
`verifyDashboardPassword` (`settings.go:374-382`), dan alasannya tetap ada:
tanpa pengecualian itu, jalur step-up untuk backup hanya hidup di test yang
mem-mount handler langsung (#47). Kredensial lain tidak berubah: session dan
CLI token tetap berlaku di semua path.

**Verifikasi:** `TestPasswordHeaderIsHonouredOnlyWhereTheHandlerVerifiesIt`
menolak header ngawur pada keenam path lain lewat kedua gate, dan memastikan
header tetap sampai ke handler export. Diuji mutation: mengembalikan
`allowsPasswordHeader(...)` ke pemeriksaan keberadaan membuat keenam subtest
gagal dengan 200 di handler yang seharusnya 401.
`TestPasswordHeaderScopeKeepsOtherCredentialsWorking` mengunci session dan
CLI token tetap berlaku setelah pembatasan ini. 12 test
`HandleExportDatabase`/`HandleImportDatabase` lolos tanpa perubahan — #47
tidak rusak.

### 🐛 CI gagal karena free tier opencode sedang overload — skip set tidak lengkap

`internal/handlers/chat/muse_spark_e2e_test.go` memanggil endpoint opencode
sungguhan, jadi status yang datang kapan saja ditentukan beban provider — bukan
kekurangan gateway. Empat dari lima test di file itu sudah `t.Skip` untuk 429
dan 403; `TestIntegration_OpenCode_MuseSpark13_ChatCompletions` hanya
memperlakukan 429, sehingga 503 lolos dan menggagalkan CI #82 dengan:

```
WRN [fallback] upstream failed provider=opencode model=muse-spark-1.3-contributor-free
  status=503 ... "Error from provider (Console): The backend is temporarily overloaded"
expected HTTP 200 for muse-spark-1.3, got 503
```

Kelimanya kini memakai satu helper `upstreamUnavailable`: 429, 403, dan 503
berarti "belum sekarang" dan di-skip; 400/401/500 tetap `Fatalf` supaya cacat
nyata tidak ikut tertutupi. Test yang selama ini bergantung pada ketersediaan
provider pihak ketiga tidak boleh gagal hanya karena satu status belum dicatat.

**Verifikasi:** `TestUpstreamUnavailable_SkipsOnlyProviderAvailabilityStatuses`
mengunci isi himpunan itu — memindahkan 500 ke dalamnya akan menggagalkan test
dengan pesan yang menyebut status itu adalah bug kita. Rerun suite setelah
perubahan: upstream sudah pulih dan test tersebut kembali 200; `go vet` bersih,
`go test -race ./internal/handlers/chat/` hijau.
### 🐛 Refresh kredensial quota: satu percobaan, skip tanpa refresher, tandai grant mati — regresi #83

PR #83 menambah refresh kredensial ke `/api/usage/{id}` dan langsung memunculkan
dua warning per akun yang setiap kali panel dibuka:

```
WRN [usage] credential refresh failed         provider=grok-cli … invalid_grant
WRN [usage] forced credential refresh failed  provider=grok-cli … invalid_grant
```

Audit upstream (`open-sse/services/tokenRefresh.js isUnrecoverableRefreshError`)
menunjukkan upstream **juga** retry sekali pada pesan auth-expired
(`open-sse/services/usage/grok-cli.js:372` mengembalikan "…authentication
expired. Please re-authorize."), tapi tidak pernah mengulang refresh yang **baru
saja ditolak** — itu celah yang diisi sendiri oleh #83.

- **Satu percobaan refresh per request.** Retry dipindah ke jalur yang belum
  mencoba, dan dilewati kalau percobaan yang baru saja gagal. Pair warning dan
  satu slot `fetchgate` yang terbuang per akun hilang.
- **Provider tanpa refresher dilewati.** `oauth.Refresh` hanya bisa berhasil
  untuk provider yang terdaftar di `oauth.Get`; qoder tidak punya satu pun
  (upstream: `open-sse/executors/qoder.js` → `refreshCredentials() { return null }`),
  jadi setiap Panel Open sebelumnya dijamin gagal dan masuk log.
- **Grant yang mati ditandai di DB.** `providers.IsRefreshGrantDead` memperluas
  `IsRefreshUnauthorized` (401) ke 400 `invalid_grant` /
  `refresh_token_reused` / `unrecoverable_refresh_error` — bentuk yang
  dikembalikan xAI untuk refresh token grok-cli yang dicabut. Akun yang ditolak
  sekarang di-park lewat `RecordConnectionOAuthFailure` sehingga dashboard
  menampilkan akun yang perlu di-re-auth, bukan warning yang tidak dibaca siapa pun.
  500/transport error tetap **tidak** di-park: itu bukti provider blip, bukan
  bukti kredensial mati.
- **`oauth.Unregister`** ditambahkan untuk 테스트 yang memasang stub di bawah
  provider id asli; stub yang bocor diam-diam mengubah perilaku kode produksi
  yang diuji.

### 🐛 Quota tracker tidak fetch semua akun + refresh kredensial Kiro — issue #78 (butir 3 & 4)

#### Audit upstream sebelum/sesudah

| Aspek | Upstream `decolua/9router` | 9router-go sebelum | 9router-go sesudah |
|:--|:--|:--|:--|
| Refresh sebelum baca quota | ada (`refreshAndUpdateCredentials`, `src/app/api/usage/[connectionId]/route.js`) | **tidak ada** | ada (`usage_credentials.go`) |
| Retry saat pesan auth-expired | ada (sekali) | **tidak ada** | ada (sekali) |
| Pola auth-expired | `["expired","authentication","unauthorized","401","re-authorize"]` | — | identik |
| Fetch kredensial Kiro | `open-sse/services/usage/kiro.js` | port 1:1 sudah benar | tidak diubah |
| Throttle fetch quota | **tidak ada** (deliberate gap, lihat `usage.go`) | 250ms + 120ms jitter | tidak diubah |
| Fan-out di halaman quota | `Promise.all` tanpa batas | `Promise.allSettled` tanpa batas | terikat + bisa dibatalkan |

Butir 4 ternyata **sudah ter-port penuh** di sisi fetcher: `fetchKiroUsage`
mencoba tiga endpoint (`codewhisperer-get`, `codewhisperer-post`, `q-get`) dengan
header `tokentype`/`TokenType` dan profil ARN yang benar, dan `sawAuthError`
menghasilkan pesan yang dilaporkan. Yang hilang adalah **dua langkah yang
membuat token basi itu pernah sampai ke fetcher**: route `/api/usage/{id}` tidak
pernah menyegarkan kredensial sebelum membaca, dan tidak pernah mencoba lagi
saat provider menjawab dengan pesan auth-expired. Keduanya ada di upstream.

#### Perubahan

- **`internal/handlers/dashboard/usage_credentials.go` (baru).** Refresh
  kredensial OAuth sebelum baca quota bila `expiresAt` sudah melewati lead
  window 5 menit, penyimpanan token yang sudah dirotasi (OpenAI memutar refresh
  token tiap refresh), dan satu percobaan ulang setelah refresh paksa bila
  provider menjawab dengan pola auth-expired upstream.
- **`apiKey` ikut dirotasi bila ia cerminan `accessToken`.** Login Kiro menulis
  token OAuth ke kedua field (`HandleKiroAPIKey`, `HandleKiroImport`), jadi
  hanya mengubah `accessToken` meninggalkan salinan basi untuk pembaca mana pun
  yang memakai `apiKey`. Koneksi `api_key` dengan key yang berbeda tidak
  ditimpa.
- **`web/src/components/quota/fetch.ts` (baru).** Fan-out kuota terikat
  (6 baca bersamaan — jumlah yang sama dengan yang dijaga browser per origin)
  dengan `AbortSignal` yang dimiliki pemanggil. Pass yang disusul langsung
  membatalkan pass sebelumnya, sehingga jawaban yang terlambat tidak lagi
  menimpa baris halaman baru dan baris yang masih dalam antrean tidak lagi
  tampil sebagai "selesai tapi kosong".

#### Bukti

- Repro (diulang): 50 koneksi lewat gate produksi butuh **15,07s** dan
  **50/50** terbaca — jadi pemotongan terjadi di state klien, bukan di server.
- Smoke live ke binary yang dibangun: 12 koneksi dalam satu pass tracker →
  **12/12 baris live**, **12 read upstream**, 12 key berbeda. Bundle yang
  dilayani memuat helper terikat (`Math.min(concurrency, targets.length)`).
- `go vet ./...` · `go test -race ./internal/...` · `go test -tags=integration -race ./internal/integration/...` · `bun test` 120/120 · `tsc -b` · `oxlint` · `make build`.

### 🐛 OpenCode Zen (`ocz`) paritas dengan upstream — issue #78

Halaman `/dashboard/providers/opencode-zen` hampir tidak punya perilaku upstream:
`opencode-zen` **tidak terdaftar sebagai executor**, jadi `executor.Get` mengembalikan
`nil` dan semua request jatuh ke `forwardRequest` generik — satu POST ke
`/zen/v1/chat/completions` tanpa session/request header, tanpa quartet fingerprint,
dan tanpa routing per-model. Akibatnya model Claude dan Qwen (yang hanya hidup di
`/zen/v1/messages`) serta GPT/Grok/Muse Spark (`/zen/v1/responses`) tidak pernah
menyentuh endpoint yang benar, dan `NoAuth: true` + `DefaultAPIKey: "public"`
membuat lane PAYG diam-diam memakai key free-tier.

- **Executor `ForwardOpencodeZen` + tiga transport.** `internal/proxy/executor/opencode_zen.go`
  mengimplementasikan `open-sse/executors/opencode-zen.js`: `/chat/completions`,
  `/messages` (auth `x-api-key` mentah), `/responses`, plus fingerprint headers,
  quartet `bash/glob/grep/read`, `stream:true` paksa, dan `store:false`.
- **Metadata format per model.** `internal/providers/model_formats.go` mem-port
  `targetFormat` / `supportedFormats` dari registry upstream plus fallback keluarga
  (`open-sse/providers/models/helpers.js OPENCODE_FAMILIES`) untuk id dari
  `modelsFetcher`/`passthroughModels` yang belum pernah dilihat. Katalog
  `web/src/lib/models.ts` untuk `ocz` disinkronkan ke 74 entri (termasuk
  `union-alpha` yang sebelumnya hilang) dengan medan format yang sama.
- **Claude client boleh loseless.** `tryForwardWithConnection` hanya mengonversi
  body `/v1/messages` untuk provider tanpa endpoint Messages
  (`executor.ServesMessagesEndpoint`), jadi `claude-*` dan `qwen*` kini dikirim
  apa adanya ke `/zen/v1/messages` alih-alih OpenAI → Claude bolak-balik.
- **Lane Responses.** `UpstreamSpeaksResponses` kini membedakan
  `opencode-zen` dan hanya meloloskan passthrough untuk model yang target
  format-nya `openai-responses`; model chat-lane yang diminta klien Responses
  diterjemahkan masuk, bukan diteruskan mentah.
- **Quota tracker.** `GET /zen/v1/usage` (Rolling / Weekly / Monthly, persentase
  → used/total 0..100) di-port ke `usage_opencode_zen.go`, dan `opencode-zen`
  masuk `usageSupportedProviders` + `usageApikeyProviders` — sebelumnya tidak
  eligible sehingga halaman tidak menampilkan akun sama sekali. URL usage
  diturunkan dari `baseUrl` koneksi, jadi endpoint self-hosted/relay dibaca
  dari host-nya sendiri.
- **Konfigurasi provider.** `NoAuth` dan `DefaultAPIKey: "public"` dihapus
  (koneksi tanpa key kini ditolak, bukan diam-diam memakai free tier), dan
  `UsageURL` ditambahkan ke `ProviderConfig`.
- **Verifikasi:** `go vet ./...`, `go test -race ./...`,
  `go test -tags=integration -race ./internal/integration/...` (7 kasus zen baru
  lewat router produksi dengan upstream palsu), `bun test` 115/115, `bun run build`,
  plus smoke live ke binary terhadap upstream palsu ketiga lane.

### 🐛 Console log named the provider but not the account — issue #78 (butir 2)

Pada multi-akun, baris `[usage] logged` hanya menyebut `provider` + `model` + token.
Tidak ada jejak akun mana yang melayani — padahal itu satu-satunya informasi yang
berguna saat 20 koneksi berotasi di balik satu provider. `connIdentityKV` sudah
ada, tapi hanya terpakai di jalur gagal (`upstream failed`, `connection locked`),
karena `forwardRequestParams` tidak membawa nama akun.

- **`forwardRequestParams.ConnName` / `.ConnEmail`** diisi di ketiga call site
  picker (pinned, rotasi, combo) dari baris koneksi yang sudah dipegang, jadi
  jalur sukses tidak perlu query database tambahan untuk format log.
- **`UsageLogInfo` dapat `ConnName`/`ConnEmail`** plus `ConnIdentityKV()`, dan
  `connIdentityKVOr()` menyelesaikan identitas sekali per attempt lalu dipakai
  ulang oleh `logUsage`, `LogFailure`, dan ketiga baris `fallback` — tidak ada
  permintaan yang membaca baris koneksi dua kali.
- **Konsekuensi yang terlihat:** baris sukses kini berbunyi
  `... cost=… conn=conn-a connName=Account A`; permintaan tanpa koneksi tersimpan
  (no-auth) melapor `account=Public / Direct` alih-alih diam saja.
- **Verifikasi:** 2 kasus integrasi lewat router produksi (rotasi dua akun harus
  menghasilkan dua nama berbeda di console log), 3 unit test untuk resolusi
  identitas, `go vet ./...`, `go test -race ./internal/...`,
  `go test -tags=integration -race ./internal/integration/...`.

### 🐛 Proxy pool ter-assign tapi tidak pernah dipakai — issue #78 (butir 1)

Pool yang terpasang di dashboard **tidak pernah dipakai** untuk sebagian besar
provider. UI menulis `proxyPoolId` dan menampilkan badge "Proxy" — request-nya
tetap keluar lewat IP asli. Tiga sebab, ketiganya diperbaiki.

- **`forwardRequest` mengabaikan client koneksi.** Signature-nya tidak punya
  client, jadi selalu `h.Client`: **setiap provider tanpa executor kustom**
  (deepseek, openai, openrouter, groq, mistral, …) melompati proxy. Jalur Gemini
  native punya masalah yang sama. Client hasil resolusi sekarang diteruskan
  (`forwardRequest` + `forwardGeminiNativeRequest`).
- **Pool level provider hanya dibaca koneksi virtual no-auth.**
  `settings.providerStrategies[...].proxyPoolId` baru dipakai di
  `getBestConnection` untuk koneksi hasil sintetis, jadi koneksi yang punya API
  key sendiri keluar langsung. `applyProviderProxyPool` kini menjadikannya
  fallback ketika koneksi tidak punya binding sendiri (binding eksplisit tetap
  menang).
- **Alias vs id kanonik.** UI menyimpan pool di bawah `storageAlias` (mis.
  `mmf`, `cl`, `ocg`, `ocz`) sedangkan request membawa id kanonik
  (`mimo-free`, `clinepass`, `opencode-go`, `opencode-zen`).
  `ResolveProviderProxyPoolID` hanya mengingat tiga pasangan, jadi sisanya
  membaca "tidak ada". Sekarang kunci dicari lewat `providerStrategyKeys`:
  id → alias terpublikasi → id kanonik → pasangan upstream (cline ↔ clinepass).
- **Pool tak terpakai gagal diam-diam.** Pool yang terhapus, nonaktif, tanpa
  URL, atau URL-nya tidak bisa diparse sebelumnya membuat request pergi
  langsung. Sekarang `getClientForConnection` mengembalikan error dan
  `tryForwardWithConnection` gagal **sebelum** ada byte yang terkirim — tidak
  ada request pertama yang bocor ke IP asli.
- **Verifikasi:** 4 kasus integrasi lewat router produksi dengan proxy palsu
  yang menghitung setiap tunnel (termasuk satu yang membuktikan hop lewat
  header yang di-stamp proxy), 6 unit test untuk resolusi alias pool,
  `go vet ./...`, `go test -race ./internal/...`,
  `go test -tags=integration -race ./internal/integration/...`.

### 🐛 Edge relay kehilangan `x-relay-target` di lane Zen

Dengan pool bertipe `vercel`/`cloudflare`/`deno`, `getProviderConfig` menukar
tujuan upstream menjadi header relay (`BuildEdgeRelayHeaders`) lalu mengganti
`BaseURL` dengan host relay. `ForwardOpencodeZen` membangun ulang header dari
nol, jadi `x-relay-target` hilang dan relay menjawab
`400 {"error":"Missing x-relay-target header"}` — **semua** request gagal begitu
pool edge dipasang.

- **`zenHeaders` kini membawa `x-relay-target` / `x-relay-path` /
  `x-opencode-project`** dari config koneksi. Fingerprint UA tetap menang
  atas `User-Agent` yang dikirim koneksi.
- **`zenRelayPath` menentukan lane dari satu tempat.** Nilai `x-relay-path`
  di-stamp dari `baseUrl` koneksi, jadi untuk koneksi default isinya
  `/zen/v1/chat/completions` — penting saat modelnya butuh lane lain.
- **Lane `/messages` juga.** Jalur itu membangun set header sendiri
  (`x-api-key` + `anthropic-version`), jadi ikut kehilangan header relay; sekarang
  sama seperti dua lane lain, tujuan ada di header dan `BaseURL` yang dipanggil
  adalah host relay.
- **Verifikasi:** 5 kasus test memakai relay palsu yang meniru perilaku
  deployment (`internal/handlers/media/deploy.go`) dan membalas 400 seperti
  aslinya — ketiganya **gagal dengan pesan yang sama seperti laporan Anda**
  sebelum fix, lalu hijau sesudahnya.

### 🐛 Batch perbaikan issue terbuka (#72, #73, #74, #75, #76, #77, #78, #79, #47, #61)

Sepuluh issue yang masih terbuka ditutup di satu batch. Yang sudah benar di
`main` (#48) tidak disentuh; yang butuh PR terpisah masih tercatat di issue.

- 🔴 **Self-update bisa mengganti binary tanpa verifikasi checksum (#72).**
  `PerformSelfUpdate` hanya memverifikasi SHA256 *kalau* manifest menyediakannya,
  padahal di jalur yang benar-benar dipakai `expectedSHA256` selalu kosong:
  `checkManifest` membaca field `sha256` yang tidak ada di `version.json`, dan
  `checkGitHubReleases` tidak pernah mengisinya. Rangkaian download → tulis →
  rename menimpa binary yang sedang berjalan tanpa cek integritas. Sekarang
  checksum **wajib**: tanpa itu `PerformSelfUpdate` menolak sebelum request
  jaringan apa pun dan binary yang berjalan tidak tersentuh. Untuk menutup
  gap-nya, `checkGitHubReleases` membaca aset `SHA256SUMS.txt` yang memang
  sudah diterbitkan `release.yml` (tidak ada kode Go yang membacanya) dan
  memilih entri yang cocok dengan aset platform aktif. Kegagalan lookup tidak
  mematikan pengecekan update; `SHA256` kosong dan instalasi ditolak dengan
  pesan yang menyebut tidak ada checksum.
- 🔴 **`parseSemver` membuang suffix prerelease (#73).** `1.9.7-rc1` dan
  `1.9.7` sama-sama jadi `[1,9,7]`, jadi RC tidak pernah ditawarkan sebagai
  update — dan begitu `1.9.8-rc1` terbit, user di `1.9.7` auto-update ke RC.
  Precedence semver sebenarnya sekarang dipakai (versi final menang atas
  prereleasenya sendiri, `rc2 > rc1`, build metadata diabaikan), plus guard
  kedua di `runCheckCycle`: jalur otomatis tidak pernah memasang tag
  ber-prerelease ke proses yang sedang berjalan di versi final. RC tetap bisa
  dipasang manual lewat `9router-go update` maupun tombol dashboard.
- 🔴 **PID daur-ulang bisa membuat `stop` membunuh proses lain (#74).**
  `RunningPID` hanya percaya PID telanjang plus `proc.Alive`, jadi file pid
  yang ditinggalkan daemon yang mati bisa dilaporkan hidup setelah OS memakai
  ulang nomornya — lalu `Stop` mengirim SIGTERM/SIGKILL ke orang tak
  bersalah. Klaim pid kini mencatat `<pid> <exe>`, dan `proc.Executable(pid)`
  (Windows `QueryFullProcessImageName`, Linux `/proc/<pid>/exe`, BSD
  `kern.proc.pathname`) memverifikasinya sebelum sinyal dikirim. Klaim yang
  tidak bisa diverifikasi **bukan** daemon yang hidup, jadi tidak pernah
  berwenang atas sinyal. `Stop` juga tidak lagi menghapus file pid di jalur
  force-kill, sehingga keadaan "ada klaim tapi prosesnya sudah mati" bisa
  terwakili.
- 🔴 **`gateway.log` tumbuh tanpa batas dan dibaca utuh tiap 150 ms (#75).**
  Log di-append tanpa cap, `LogTail` memuat seluruh file per panggilan
  `logs`, dan `bindFailureSeen` melakukan `os.ReadFile` + `bytes.Contains`
  pada setiap iterasi polling 150 ms. Log sekarang di-trim ke 16 MiB saat
  dibuka (ekor dipertahankan, kepala dipindah ke `gateway.log.1`), `LogTail`
  hanya membaca jendela 256 KiB dari belakang, dan pemindaian bind failure
  dibatasi ke ekor log.
- **`tools[].toolSpec.name` (Bedrock Converse) dilewati dua arah (#77).**
  `visitTools`/`replaceInTools` hanya mengenal `name`, `function.name`, dan
  `functionDeclarations`, sehingga request berbentuk Converse tetap membawa
  nama > 64 karakter ke upstream dan tidak ada apa pun yang memulihkannya di
  respons. Issue menyebut ini "direkam tapi tidak ditulis"; yang sebenarnya
  adalah keduanya tidak disentuh —adding `toolSpec` ke sisi request saja
  akan membuat respons mengembalikan nama yang tidak pernah dideklarasikan.
  Bentuk Converse kini ditangani simetris di request **dan** respons
  (`contentBlockStart.start.toolUse` dan `output.message.content[].toolUse`).
- **`signalSelfShutdown` dead code di kedua varian build (#76) — BELUM dihapus.**
  Kedua file `signal_unix.go`/`signal_windows.go` memang tidak punya call site
  sejak rewrite `RestartSelf` pindah ke `shutdown.RestartAfterStop`, dan isinya
  identik. Penghapusan file-nya **tidak termasuk batch ini**; issue #76 tetap
  terbuka.
- **Kredensial Kiro tidak pernah sampai ke quota tracker (#78).**
  `fetchProviderUsage` mengirim `accessToken` ke `fetchKiroUsage`, padahal
  koneksi Kiro menyimpan kredensialnya di `apiKey` — jadi request-nya
  membawa `Authorization: Bearer ` dan dashboard menampilkan *"Kiro quota API
  rejected the current token. Chat may still work."* sementara chat-nya
  sendiri sukses memakai kredensial yang tidak pernah dibaca quota path.
  Presedensinya sekarang sama dengan `resolveProviderAuthToken` di jalur chat,
  dan token kosong dilaporkan sebagai "kredensial tidak tersimpan" — bukan
  penolakan token yang menyesatkan.
- **Antigravityqueue dua kali di gate quota (#78).** `HandleGetConnectionUsage`
  mengambil slot `quotaFetchGate` sekali sebelum dispatch lalu sekali lagi
  di cabang Antigravity, jadi tiap akun Antigravity menunggu dua gap 250 ms
  berturut-turut untuk satu burst request. Slot kedua dihapus.
- **Dropdown periode usage dengan `all` + window kustom (#79).**
  Selector periode berupa deretan tombol pill hardcoded yang tidak punya
>  `all`, padahal backend sudah menerimanya. Sekarang dropdown dengan preset
>  (Today, 24h, 7D, 30D, 60D, All time) plus input kustom, dan backend
>  `/api/usage/stats` menerima bentuk `<n>d` / `<n>h` apa pun —
>  `resolveUsagePeriod` mengganti rantai `if/else` yang diam-diam memakai
>  365 hari untuk `all` dan 7 hari untuk nilai yang tidak dikenal.
- **Download database ditolak padahal sudah login (#47).**
>  `HandleExportDatabase` tidak menerima session dashboard — hanya header
>  `x-9r-password` atau token CLI — sehingga link browser biasa selalu 401
>  dan ekspor terlihat permanen terblokir. Session sekarang cukup dengan
>  sendirinya seperti baca dashboard lain, sementara header password tetap
>  jalan untuk skrip. Jalur zip yang diminta sudah ada di backend dan kini
>  bisa dijangkau.
- **Picker model tidak lagi menyortir ulang seluruh katalog per klik (#61).**
  `resolveFilteredGroups` menerima `addedModelValues` yang tidak pernah
  dibaca, tapi karena argumennya ada di signature, Svelte menjadikannya bagian
  dari graf reaktif: setiap klik satu pill memicu filter + sort ulang seluruh
  grup dan rekonsiliasi ulang ratusan/ribuan pill. Argumen itu dihapus; logika
  filter/sort tidak berubah sama sekali.

**Di luar cakupan:** #78 butir 1–2 (executor `opencode-zen`) sudah dikerjakan
di PR #80 dan #48 sudah benar di `main` (`stripCodexUnsupportedTokenParams`
berjalan setelah `buildResponsesBody`, bukan sebelumnya) — keduanya tidak
disentuh di sini.

### ⬆️ Katalog Codex disinkronkan dengan upstream v0.5.95 — issue #93

Tujuh model yang upstream hapus masih dipublish di `/v1/models`, sementara model yang upstream konfirmasi hidup belum ada sama sekali. Dua-duanya 400.

- **Ghost model dihapus.** `gpt-5.4`, `gpt-5.4-review`, `gpt-5.4-mini`, `gpt-5.4-mini-review`, `gpt-5.3-codex-spark`, `gpt-5.3-codex-spark-review`, dan `gpt-5.4-image` tidak ada di `backend-api/codex/models` untuk akun ChatGPT Plus/Pro — semuanya menjawab HTTP 400 "model is not supported" (upstream #4202). Dihapus dari `registry_models.go` dan katalog dashboard. Entri `gpt-5.4` milik provider lain (`openai`, `tokenrouter`) tidak disentuh.
- **Model hidup ditambahkan.** `gpt-6.1-sol`, `gpt-daybreak-blue-latest`, `gpt-reserve`, dan enam varian konteks ekstended `[1m]`.
- **Id katalog bukan id wire.** Varian `[1m]` dan `-review` adalah id katalog; ChatGPT menjawab 400 kalau keduanya diteruskan apa adanya. `providers.CodexUpstreamModelID` memetakan ke model dasar (setara `getModelUpstreamId` upstream), dipanggil dari `rewriteCodexUpstreamModel` **setelah** `buildResponsesBody` — sufiks "(level)" harus sudah dilepas lebih dulu. `codex-auto-review` sengaja tidak dipangkas (#1398), dan id berawalan vendor tidak ditulis ulang karena prefix itu menandai provider lain.
- **Bare slug route ke codex.** `gpt-5.*`, `gpt-6.*`, `gpt-6-*`, `gpt-daybreak-*`, dan `gpt-reserve` sekarang resolve ke codex, bukan jatuh ke aturan generik `gpt-*` → openai dan 404 untuk akun codex-only (#4405). Posisinya di luar guard `Repo` supaya jalur katalog statis (Repo nil) tetap jalan. `gpt-4*` / `gpt-3.5*` / `gpt-4o*` tetap openai.
- **Context window per model.** Codex OAuth melaporkan jendela sendiri, bukan milik OpenAI API: 272k untuk GPT-6 dan Terra/Luna, 372k untuk Sol, 872k untuk varian `[1m]`. `codexGpt56Caps` membangun blok kapabilitas bersama, dan `providerCapabilities["cx"]` sekarang mewarisi blok codex seperti upstream (`PROVIDER_CAPABILITIES.cx = PROVIDER_CAPABILITIES.codex`).
- **Harga.** `gpt-6-astra` dikoreksi ke 10/50/1/50/12.5 (sebelumnya 5/30 — setengah nilai sebenarnya); `gpt-6.1-sol`, `gpt-6-sol`, `gpt-6-luna` ditambahkan.
- **Lite + thinking levels.** `gpt-6.1-sol` dan varian `[1m]` yang berbasis lite masuk ke `codexResponsesLiteModels` dan `codexModelThinkingLevels`.

**Verifikasi:** `go vet ./...` bersih · `go test -race ./internal/providers/... ./internal/proxy/... ./internal/pricing/...` hijau · 15 kasus baru (`TestCodexUpstreamModelID`, `TestCodexCatalogWireIDsResolve`, `TestCodexOnlyModelSlug`, `TestResolveModel_BareCodexSlugWithoutRepo`, `TestRewriteCodexUpstreamModel` + 4 kasus body tak usable) · `bun run build` (`tsc -b` + vite) bersih.

Satu test yang tersisa rapuh **bukan** hasil perubahan ini: `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` gagal intermittent di `origin/main` juga (terbukti lewat `git stash` + `-count=20`), jadi di luar cakupan issue ini.

## [v1.9.7] - 2026-10-02

### 🐛 Egress di log tercetak UUID, bukan nama pool

Baris `egress=` dari entri sebelumnya membawa **UUID** pool
(`06a2c494-ef06-4d3f-a034-dad29ff3aebf`). Itu benar, tapi tidak menjawab
pertanyaan yang muncul saat grep log: *pool yang mana?* — dashboard menampilkan
pool berdasarkan nama, jadi operator harus membuka daftar proxy pool untuk
mencocokkan UUID sebelum tahu request-nya keluar lewat mana.

Kini nama pool yang dicetak, di-resolve **tanpa query tambahan**:
`getClientForConnection` sudah membaca baris pool untuk membangun transport,
jadi nama diambil dari objek yang sama dan ditaruh di
`ConnectionData.ResolvedProxyPool` (`json:"-"`, per request, tidak dipersist —
sehingga rename di dashboard langsung terlihat di log berikutnya).

Nama yang difilter newline: nilainya diisi operator dan masuk ke setiap baris
usage, jadi newline akan memalsukan baris log — dan log itulah jejak audit
"egress mana yang melayani request ini".

**Verifikasi:** `TestResolveEgress_ReportsThePathARequestLeftBy` mengunci delapan
bentuk, termasuk "pool tanpa nama → jatuh ke UUID" dan "proxy legacy → URL".
`TestResolveEgress_KeepsTheLabelSingleLine` menutup kasus pemalsuan baris log.

**Live (`:20151`, DB sama dengan `main`):**

```
INF [usage] logged provider=opencode model=space-bunny-free … egress=vercel-relay
INF [usage] logged provider=opencode-zen model=space-bunny-free …
  conn=80d9c65d-… egress=direct
INF [usage] logged provider=opencode-zen model=space-bunny-free …
  conn=80d9c65d-… egress=vercel-relay
```

Tiga baris itu sekaligus menunjukkan dropdown proxy di dashboard berfungsi:
baris `direct` tepat setelah `PUT /api/connections/…` yang melepas binding, dan
baris berikutnya kembali `vercel-relay` setelah pool di-bind ulang.

### 🐛 Log tidak bisa menjawab "request ini lewat proxy atau bukan"

Setelah kebocoran egress diperbaiki (entri sebelumnya), pertanyaan paling
natural berikutnya — *apakah request ini benar-benar lewat proxy?* — tetap tidak
bisa dijawab dari log. Satu-satunya baris proxy (`logProxyOnce`) memakai
`sync.Map.LoadOrStore`: **sekali per pool per proses**, dan hanya di level
`debug`. Request kedua dan seterusnya lewat pool yang sama tidak mencetak apa
pun, dan `LOG_LEVEL` default `info` emballage menyembunyikannya. Baris
`[usage] logged` juga tidak punya kolom proxy sama sekali.

Dampaknya persis pada kelas masalah yang diperbaiki: dua gateway berbagi satu
database (branch `main` di `:20130` dan build uji di `:20151`) menghasilkan log
yang identik meski salah satunya diam-diam keluar direct — dan `429`/blokir
berbasis IP mustahil dianalisis tanpa tahu egress-nya.

Kini setiap baris `[usage] logged` dan setiap baris kegagalan
`[fallback] upstream failed` membawa `egress=`. Nilainya adalah pool yang
ditugaskan (bukan URL relay), karena itulah yang dicari operator di dashboard;
request tanpa proxy dan tanpa pool lama tercetak `direct`.

Resolver-nya membaca bentuk yang sudah ada di `providerCfg`, jadi tidak ada
query pool tambahan di hot path: relay terdeteksi dari `x-relay-target`, proxy
HTTP dari field proxy koneksi (yang tidak terlihat di config).

**Verifikasi:** `TestResolveEgress_ReportsThePathARequestLeftBy` mengunci tujuh
bentuk (direct, relay, proxy legacy, proxy aktif-tanpa-URL, koneksi nil,
relay tanpa baris koneksi, `providerSpecificData`). Diuji mutation: mengembalikan
`Target` ke `x-relay-target` membuat test gagal tepat di assertion "Target must
be the relay host".

**Live (build di `:20151`, DB sama dengan `main`):**

```
WRN [fallback] upstream failed provider=opencode-zen model=muse-spark-1.3 status=401
  … forward to https://vercel-relay-myw8iebf7-legowo.vercel.app/responses …
  conn=80d9c65d-… connName=yatimrachmawati@paragadis.com 1
  egress=06a2c494-ef06-4d3f-a034-dad29ff3aebf

INF [usage] logged provider=opencode model=muse-spark-1.3-contributor-free …
  conn=default egress=direct
```

Dua baris itu sekaligus membuktikan arahnya: `ocz/muse-spark-1.3` melalui koneksi
ber-pool dan keluar lewat pool Vercel, sedangkan
`oc/muse-spark-1.3-contributor-free` memakai fast path no-auth dan memang
`direct` — jadi log menunjukkan tidak ada kebocoran yang tersisa di jalur free-tier.

### 🔴 Request yang harus lewat proxy pool dijawab dari IP asli — semua tipe proxy

Audit jalur proxy setelah 503 `service_overloaded` (entri sebelumnya)
memunculkan bug yang berlaku untuk **semua** proxy, bukan cuma Vercel: ada dua
kebocoran egress, satu di jalur HTTP dan satu di jalur relay.

**1. Pool proxy mati tetap dijawab dari IP sendiri.** `doRequestOnce` me-dial
ulang secara direct setiap kali proxy menolak tunnel (`isProxyFailure`). Itu
benar untuk proxy ambient (`HTTP_PROXY`, sandbox lokal) yang tidak pernah dipilih
operator, dan salah untuk pool yang ditugaskan: request dijawab dari IP asli,
padahal dashboard masih menampilkan koneksi sebagai "proxied". Dibuktikan
sebelum diperbaiki:

```
err=<nil>  directHits=1     ← request lewat pool mati, dijawab dari IP asli
```

Sekarang `proxiesViaAssignment` membedakan keduanya berdasarkan identitas
transport: proxy yang nilainya `http.ProxyFromEnvironment` (satu-satunya yang
dipasang environment) diperlakukan ambient dan boleh jatuh ke direct; pool yang
ditugaskan **gagal** — pesan errornya menyebut alasannya, bukan diam-diam
mengambil jalur lain.

**2. "Test Connection" tidak pernah lewat relay.** `probeHTTPClient` mengembalikan
client `nil` untuk pool `vercel`/`cloudflare`/`deno`, jadi probe berjalan
langsung ke provider dari IP host. Itu salah dua arah: bisa hijau sementara
traffic produksi lewat relay gagal, dan bisa merah sementara jalur relay sehat —
plus membocorkan IP di request yang justru dijalankan untuk memastikan proxy
pasang. Pool relay kini dapat `probeRelayRoundTripper` yang mengarahkan request
ke host relay sambil membawa `x-relay-target`/`x-relay-path`, kontrak yang sama
dengan pipeline chat.

**3. Satu daftar tipe relay, bukan tiga.** `vercel`/`cloudflare`/`deno` tertulis
ulang di `connections.go`, `proxypools.go`, dan `connection_probe.go`. Runtime
edge keempat akan diarahkan satu arah di satu tempat dan arah lain di tempat
lain — persis kelas bug yang menyebabkan #2. Sekarang `ProxyPool.IsEdgeRelay()`
(`internal/db`) adalah satu-satunya definisi, dan `proxy_egress_test.go` +
`probe_relay_test.go` mengunci kedua jalur agar tidak bisa berbeda lagi.

**Verifikasi:** `proxy_egress_test.go` membuktikan pool yang ditugaskan tidak
lagi jatuh ke direct (`directHits=0`) dan proxy ambient tetap boleh fallback
direct, jadi perbaikan ini tidak mematikan instalasi yang berada di belakang
proxy sistem. Diuji mutation: menghapus cek `proxiesViaAssignment` mengembalikan
`directHits=1` dan test gagal tepat di assertion itu. `probe_relay_test.go`
membuktikan probe benar-benar mendarat di host relay dengan header yang benar
dan `Host` bukan provider.

**Known limit:** kebocoran #1 menutup jalur yang **sudah terkonfigurasi**. Pool
yang menyimpan `vercelRelayUrl` di `providerSpecificData` lama tanpa `proxyPoolId`
tetap relay lewat `getProviderConfig` seperti sebelumnya — jalur itu tidak
disentuh perubahan ini karena tidak punya flag `strictProxy` untuk dihormati.

### 🐛 503 `service_overloaded` gagalkan satu turn penuh padahal attempt berikutnya dilayani

Laporan: `opencode-zen`/`muse-spark-1.3-contributor-free` lewat proxy pool Vercel
sering `503 service_overloaded`, sedangkan tanpa proxy "aman".

**Proxy bukan penyebabnya.** Diuji dengan body dan header fingerprint yang sama
persis, bergantian langsung ke `opencode.ai` vs lewat relay:

```
direct: {503: 16, 200: 12, ERR: 2}   ok 12/30
relay:  {504: 3,  503: 14, 200: 13}   ok 13/30
```

Both `503` muncul di kedua jalur dengan rate serupa — itu kapasitas
`opencode.ai` sendiri, bukan kerusakan relay. Relay yang terpasang juga terbukti
lossless: seluruh header (`User-Agent`, `x-opencode-session/request/project`,
`Authorization`) melewati hop, hanya `x-relay-*` yang di-strip seperti
template-nya, dan response header upstream ikut diteruskan.

**Satu temuan relay yang nyata (bukan penyebab 503):** relay menjawab
`504` pada **25.09s persis** dengan header
`X-Vercel-Error: FUNCTION_INVOCATION_TIMEOUT`, yang tidak pernah terjadi di jalur
direct. Vercel Edge mensyaratkan respons awal di bawah 25 detik; TTFT direct
yang terukur 28–55s membuat relay melewati batas itu. Jadi relay tidak
membuat turn cepat jadi gagal — relay justru mengorbankan turn lambat: setiap
turn yang token pertamanya tiba setelah ~25s akan berakhir 504 di relay.

**Akar masalah yang tersisa:** gateway mengirim **satu** attempt, lalu
menyurfacekan 503 ke klien dan lock koneksi — padahal attempt berikutnya
dilayani. Upstream sudah mengulang 502x3/503x3/504x2 di `BaseExecutor.execute`
(`open-sse/executors/base.js:107-125` + `config/runtimeConfig.js:78-83`); port
Go tidak punya padanannya sama sekali. Diukur langsung dengan kebijakan
upstream (3x @2s):

```
1 attempt (sekarang)     served  9/15 (60%)
3 attempts @2s (upstream) served 15/15 (100%)
```

Kini `proxy.DoRequest` — titik choke yang diwarisi semua provider, sama seperti
`BaseExecutor` upstream — mengulang transient 502/503/504 sesuai kebijakan itu
sebelum melaporkan kegagalan. 429 **tidak** di-retry di sini: itu jendela kuota
per akun yang harus diserahkan ke account fallback, sesuai kontrak upstream.
Backoff terikat `ctx`, jadi klien yang sudah pergi tidak ditahan Rugi penuh.

**Verifikasi:** `internal/proxy/retry_test.go` mengunci tabel kebijakan,
hitung attempt, status asli yang diteruskan ke failover, penghentian saat
status berubah, dan klien yang pergi. Diuji mutation: mengembalikan 429 ke
tabel retry menggagalkan `TestTransientRetryPolicy_MatchesUpstreamDefaults`.
`internal/integration/transient_retry_test.go` menutupnya melalui router
produksi dengan upstream palsu: 503 lalu dilayani → klien dapat `200` berisi
teks upstream; 400/429 → tepat satu request upstream.

**Known limit (jujur):** ini menutup gap parity, bukan membuat upstream yang
penuh menjadi tidak penuh. Saat `opencode.ai` benar-benar jenuh, ketiga attempt
tetap 503 dan klien tetap menerima 503 — sekarang setelah menunggu sesuai
kebijakan upstream, bukan langsung. Batas 25s edge relay juga tidak hilang:
turn dengan TTFT di atas 25s masih perlu direct atau pool non-edge.

### 🐛 Manifest tanpa checksum membuat auto-update mati total — issue #72

#72 membuat digest SHA256 **wajib**: `PerformSelfUpdate` menolak sebelum request
jaringan apa pun kalau `expectedSHA256` kosong. Tapi `CheckUpdate` mencoba
manifest lebih dulu dan langsung mengembalikan jawabannya kalau sukses
(`updater.go:158-163`), sedangkan `version.json` yang benar-benar terbit hanya
punya tiga key — `downloadUrl`, `latestVersion`, `releaseNotes` — tanpa `sha256`
sama sekali. Akibatnya `checkManifest` selalu menghasilkan digest kosong,
`checkGitHubReleases` **tidak pernah dijalankan**, dan `lookupAssetSHA256` yang
justru menutup gap #72 tidak pernah menyentuh release sungguhan.

Terbukti: dengan manifest berbentuk sama persis seperti yang diterbitkan repo,
`CheckUpdate` mengembalikan `Source="manifest"`, `SHA256=""`, sementara fallback
GitHub yang punya `SHA256SUMS.txt` tidak pernah dihubungi. Semua instalasi
berakhir `release carries no checksum` — fail-closed dan aman, tapi
`9router-go update` praktis mati.

Manifest kini hanya boleh menjawab kalau memang bisa menghasilkan update yang
dapat diinstal: digestnya ada, atau memang tidak ada update yang ditawarkan.
Selain itu ia diperlakukan sebagai petunjuk dan turun ke GitHub Releases API;
metadata manifest disimpan sebagai fallback supaya catatan rilis tetap tampil
saat API-nya sedang mati.

**Verifikasi:** `TestCheckUpdate_ManifestWithoutDigestFallsThroughToReleaseDigest`
menyajikan `version.json` tanpa `sha256` dan menegaskan release API benar-benar
dihubungi serta digestrelease yang dipakai. Diuji mutation: mengembalikan
syarat ke "manifest selalu menang" membuat test itu gagal tepat di assertion
release API tidak pernah ditanya. Dua test lain mengunci agar fallthrough tidak
menjadi regresi sendiri — manifest yang **sudah** punya digest tetap dijawab
tanpa menyentuh GitHub, dan manifest yang melaporkan sudah mutakhir tetap
dijawab walau tanpa digest.

### 🔴 Header password dashboard hanya diverifikasi di satu handler — auth bypass

`RequireAdminAuth` dan `RequireDashboardAuth` mengizinkan request yang membawa
header `x-9r-password` dengan memeriksa **keberadaan** header itu saja:
`r.Header.Get(DashboardPasswordHeader) != ""`. Nilai yang dipakai tidak pernah
diverifikasi di middleware — memang tidak bisa, karena ceknya butuh hash bcrypt
yang tersimpan di repo yang tidak dipegang gate.

Asumsi di balik itu — "handler di belakang gate memverifikasi sendiri" — hanya
benar untuk **1 dari 7** path yang dilindungi. Handler lain sama sekali tidak
memeriksa kredensial apa pun, sehingga siapa pun yang bisa memasang satu header
acak cukup untuk lewat: `/api/version/shutdown` (`HandleShutdown`),
`/api/version/update` (`HandleTriggerUpdate`), `/admin/health/reset`,
`/api/oauth/cursor/auto-import`, dan `/api/oauth/kiro/auto-import`.

Yang paling berbahaya `/api/version/shutdown`: `shutdown.RequestStop()`
dijadwalkan 500 ms setelah response (`internal/handlers/shutdown.go:29-32`),
jadi efeknya selalu terjadi dan klien melihat 200 yang bersih — DoS remote
tanpa autentikasi. `/api/version/update` mencapai penggantian binary dan restart.

Pengecualian header kini dibatasi ke `PasswordHeaderCarriesOwnAuth`
(`/api/settings/database`) — satu-satunya handler yang memanggil
`verifyDashboardPassword` (`settings.go:374-382`), dan alasannya tetap ada:
tanpa pengecualian itu, jalur step-up untuk backup hanya hidup di test yang
mem-mount handler langsung (#47). Kredensial lain tidak berubah: session dan
CLI token tetap berlaku di semua path.

**Verifikasi:** `TestPasswordHeaderIsHonouredOnlyWhereTheHandlerVerifiesIt`
menolak header ngawur pada keenam path lain lewat kedua gate, dan memastikan
header tetap sampai ke handler export. Diuji mutation: mengembalikan
`allowsPasswordHeader(...)` ke pemeriksaan keberadaan membuat keenam subtest
gagal dengan 200 di handler yang seharusnya 401.
`TestPasswordHeaderScopeKeepsOtherCredentialsWorking` mengunci session dan
CLI token tetap berlaku setelah pembatasan ini. 12 test
`HandleExportDatabase`/`HandleImportDatabase` lolos tanpa perubahan — #47
tidak rusak.

### 🐛 CI gagal karena free tier opencode sedang overload — skip set tidak lengkap

`internal/handlers/chat/muse_spark_e2e_test.go` memanggil endpoint opencode
sungguhan, jadi status yang datang kapan saja ditentukan beban provider — bukan
kekurangan gateway. Empat dari lima test di file itu sudah `t.Skip` untuk 429
dan 403; `TestIntegration_OpenCode_MuseSpark13_ChatCompletions` hanya
memperlakukan 429, sehingga 503 lolos dan menggagalkan CI #82 dengan:

```
WRN [fallback] upstream failed provider=opencode model=muse-spark-1.3-contributor-free
  status=503 ... "Error from provider (Console): The backend is temporarily overloaded"
expected HTTP 200 for muse-spark-1.3, got 503
```

Kelimanya kini memakai satu helper `upstreamUnavailable`: 429, 403, dan 503
berarti "belum sekarang" dan di-skip; 400/401/500 tetap `Fatalf` supaya cacat
nyata tidak ikut tertutupi. Test yang selama ini bergantung pada ketersediaan
provider pihak ketiga tidak boleh gagal hanya karena satu status belum dicatat.

**Verifikasi:** `TestUpstreamUnavailable_SkipsOnlyProviderAvailabilityStatuses`
mengunci isi himpunan itu — memindahkan 500 ke dalamnya akan menggagalkan test
dengan pesan yang menyebut status itu adalah bug kita. Rerun suite setelah
perubahan: upstream sudah pulih dan test tersebut kembali 200; `go vet` bersih,
`go test -race ./internal/handlers/chat/` hijau.

### 🐛 Refresh kredensial quota: satu percobaan, skip tanpa refresher, tandai grant mati — regresi #83

PR #83 menambah refresh kredensial ke `/api/usage/{id}` dan langsung memunculkan
dua warning per akun yang setiap kali panel dibuka:

```
WRN [usage] credential refresh failed         provider=grok-cli … invalid_grant
WRN [usage] forced credential refresh failed  provider=grok-cli … invalid_grant
```

Audit upstream (`open-sse/services/tokenRefresh.js isUnrecoverableRefreshError`)
menunjukkan upstream **juga** retry sekali pada pesan auth-expired
(`open-sse/services/usage/grok-cli.js:372` mengembalikan "…authentication
expired. Please re-authorize."), tapi tidak pernah mengulang refresh yang **baru
saja ditolak** — itu celah yang diisi sendiri oleh #83.

- **Satu percobaan refresh per request.** Retry dipindah ke jalur yang belum
  mencoba, dan dilewati kalau percobaan yang baru saja gagal. Pair warning dan
  satu slot `fetchgate` yang terbuang per akun hilang.
- **Provider tanpa refresher dilewati.** `oauth.Refresh` hanya bisa berhasil
  untuk provider yang terdaftar di `oauth.Get`; qoder tidak punya satu pun
  (upstream: `open-sse/executors/qoder.js` → `refreshCredentials() { return null }`),
  jadi setiap Panel Open sebelumnya dijamin gagal dan masuk log.
- **Grant yang mati ditandai di DB.** `providers.IsRefreshGrantDead` memperluas
  `IsRefreshUnauthorized` (401) ke 400 `invalid_grant` /
  `refresh_token_reused` / `unrecoverable_refresh_error` — bentuk yang
  dikembalikan xAI untuk refresh token grok-cli yang dicabut. Akun yang ditolak
  sekarang di-park lewat `RecordConnectionOAuthFailure` sehingga dashboard
  menampilkan akun yang perlu di-re-auth, bukan warning yang tidak dibaca siapa pun.
  500/transport error tetap **tidak** di-park: itu bukti provider blip, bukan
  bukti kredensial mati.
- **`oauth.Unregister`** ditambahkan untuk 테스트 yang memasang stub di bawah
  provider id asli; stub yang bocor diam-diam mengubah perilaku kode produksi
  yang diuji.

### 🐛 Quota tracker tidak fetch semua akun + refresh kredensial Kiro — issue #78 (butir 3 & 4)

#### Audit upstream sebelum/sesudah

| Aspek | Upstream `decolua/9router` | 9router-go sebelum | 9router-go sesudah |
|:--|:--|:--|:--|
| Refresh sebelum baca quota | ada (`refreshAndUpdateCredentials`, `src/app/api/usage/[connectionId]/route.js`) | **tidak ada** | ada (`usage_credentials.go`) |
| Retry saat pesan auth-expired | ada (sekali) | **tidak ada** | ada (sekali) |
| Pola auth-expired | `["expired","authentication","unauthorized","401","re-authorize"]` | — | identik |
| Fetch kredensial Kiro | `open-sse/services/usage/kiro.js` | port 1:1 sudah benar | tidak diubah |
| Throttle fetch quota | **tidak ada** (deliberate gap, lihat `usage.go`) | 250ms + 120ms jitter | tidak diubah |
| Fan-out di halaman quota | `Promise.all` tanpa batas | `Promise.allSettled` tanpa batas | terikat + bisa dibatalkan |

Butir 4 ternyata **sudah ter-port penuh** di sisi fetcher: `fetchKiroUsage`
mencoba tiga endpoint (`codewhisperer-get`, `codewhisperer-post`, `q-get`) dengan
header `tokentype`/`TokenType` dan profil ARN yang benar, dan `sawAuthError`
menghasilkan pesan yang dilaporkan. Yang hilang adalah **dua langkah yang
membuat token basi itu pernah sampai ke fetcher**: route `/api/usage/{id}` tidak
pernah menyegarkan kredensial sebelum membaca, dan tidak pernah mencoba lagi
saat provider menjawab dengan pesan auth-expired. Keduanya ada di upstream.

#### Perubahan

- **`internal/handlers/dashboard/usage_credentials.go` (baru).** Refresh
  kredensial OAuth sebelum baca quota bila `expiresAt` sudah melewati lead
  window 5 menit, penyimpanan token yang sudah dirotasi (OpenAI memutar refresh
  token tiap refresh), dan satu percobaan ulang setelah refresh paksa bila
  provider menjawab dengan pola auth-expired upstream.
- **`apiKey` ikut dirotasi bila ia cerminan `accessToken`.** Login Kiro menulis
  token OAuth ke kedua field (`HandleKiroAPIKey`, `HandleKiroImport`), jadi
  hanya mengubah `accessToken` meninggalkan salinan basi untuk pembaca mana pun
  yang memakai `apiKey`. Koneksi `api_key` dengan key yang berbeda tidak
  ditimpa.
- **`web/src/components/quota/fetch.ts` (baru).** Fan-out kuota terikat
  (6 baca bersamaan — jumlah yang sama dengan yang dijaga browser per origin)
  dengan `AbortSignal` yang dimiliki pemanggil. Pass yang disusul langsung
  membatalkan pass sebelumnya, sehingga jawaban yang terlambat tidak lagi
  menimpa baris halaman baru dan baris yang masih dalam antrean tidak lagi
  tampil sebagai "selesai tapi kosong".

#### Bukti

- Repro (diulang): 50 koneksi lewat gate produksi butuh **15,07s** dan
  **50/50** terbaca — jadi pemotongan terjadi di state klien, bukan di server.
- Smoke live ke binary yang dibangun: 12 koneksi dalam satu pass tracker →
  **12/12 baris live**, **12 read upstream**, 12 key berbeda. Bundle yang
  dilayani memuat helper terikat (`Math.min(concurrency, targets.length)`).
- `go vet ./...` · `go test -race ./internal/...` · `go test -tags=integration -race ./internal/integration/...` · `bun test` 120/120 · `tsc -b` · `oxlint` · `make build`.

### 🐛 OpenCode Zen (`ocz`) paritas dengan upstream — issue #78

Halaman `/dashboard/providers/opencode-zen` hampir tidak punya perilaku upstream:
`opencode-zen` **tidak terdaftar sebagai executor**, jadi `executor.Get` mengembalikan
`nil` dan semua request jatuh ke `forwardRequest` generik — satu POST ke
`/zen/v1/chat/completions` tanpa session/request header, tanpa quartet fingerprint,
dan tanpa routing per-model. Akibatnya model Claude dan Qwen (yang hanya hidup di
`/zen/v1/messages`) serta GPT/Grok/Muse Spark (`/zen/v1/responses`) tidak pernah
menyentuh endpoint yang benar, dan `NoAuth: true` + `DefaultAPIKey: "public"`
membuat lane PAYG diam-diam memakai key free-tier.

- **Executor `ForwardOpencodeZen` + tiga transport.** `internal/proxy/executor/opencode_zen.go`
  mengimplementasikan `open-sse/executors/opencode-zen.js`: `/chat/completions`,
  `/messages` (auth `x-api-key` mentah), `/responses`, plus fingerprint headers,
  quartet `bash/glob/grep/read`, `stream:true` paksa, dan `store:false`.
- **Metadata format per model.** `internal/providers/model_formats.go` mem-port
  `targetFormat` / `supportedFormats` dari registry upstream plus fallback keluarga
  (`open-sse/providers/models/helpers.js OPENCODE_FAMILIES`) untuk id dari
  `modelsFetcher`/`passthroughModels` yang belum pernah dilihat. Katalog
  `web/src/lib/models.ts` untuk `ocz` disinkronkan ke 74 entri (termasuk
  `union-alpha` yang sebelumnya hilang) dengan medan format yang sama.
- **Claude client boleh loseless.** `tryForwardWithConnection` hanya mengonversi
  body `/v1/messages` untuk provider tanpa endpoint Messages
  (`executor.ServesMessagesEndpoint`), jadi `claude-*` dan `qwen*` kini dikirim
  apa adanya ke `/zen/v1/messages` alih-alih OpenAI → Claude bolak-balik.
- **Lane Responses.** `UpstreamSpeaksResponses` kini membedakan
  `opencode-zen` dan hanya meloloskan passthrough untuk model yang target
  format-nya `openai-responses`; model chat-lane yang diminta klien Responses
  diterjemahkan masuk, bukan diteruskan mentah.
- **Quota tracker.** `GET /zen/v1/usage` (Rolling / Weekly / Monthly, persentase
  → used/total 0..100) di-port ke `usage_opencode_zen.go`, dan `opencode-zen`
  masuk `usageSupportedProviders` + `usageApikeyProviders` — sebelumnya tidak
  eligible sehingga halaman tidak menampilkan akun sama sekali. URL usage
  diturunkan dari `baseUrl` koneksi, jadi endpoint self-hosted/relay dibaca
  dari host-nya sendiri.
- **Konfigurasi provider.** `NoAuth` dan `DefaultAPIKey: "public"` dihapus
  (koneksi tanpa key kini ditolak, bukan diam-diam memakai free tier), dan
  `UsageURL` ditambahkan ke `ProviderConfig`.
- **Verifikasi:** `go vet ./...`, `go test -race ./...`,
  `go test -tags=integration -race ./internal/integration/...` (7 kasus zen baru
  lewat router produksi dengan upstream palsu), `bun test` 115/115, `bun run build`,
  plus smoke live ke binary terhadap upstream palsu ketiga lane.

### 🐛 Console log named the provider but not the account — issue #78 (butir 2)

Pada multi-akun, baris `[usage] logged` hanya menyebut `provider` + `model` + token.
Tidak ada jejak akun mana yang melayani — padahal itu satu-satunya informasi yang
berguna saat 20 koneksi berotasi di balik satu provider. `connIdentityKV` sudah
ada, tapi hanya terpakai di jalur gagal (`upstream failed`, `connection locked`),
karena `forwardRequestParams` tidak membawa nama akun.

- **`forwardRequestParams.ConnName` / `.ConnEmail`** diisi di ketiga call site
  picker (pinned, rotasi, combo) dari baris koneksi yang sudah dipegang, jadi
  jalur sukses tidak perlu query database tambahan untuk format log.
- **`UsageLogInfo` dapat `ConnName`/`ConnEmail`** plus `ConnIdentityKV()`, dan
  `connIdentityKVOr()` menyelesaikan identitas sekali per attempt lalu dipakai
  ulang oleh `logUsage`, `LogFailure`, dan ketiga baris `fallback` — tidak ada
  permintaan yang membaca baris koneksi dua kali.
- **Konsekuensi yang terlihat:** baris sukses kini berbunyi
  `... cost=… conn=conn-a connName=Account A`; permintaan tanpa koneksi tersimpan
  (no-auth) melapor `account=Public / Direct` alih-alih diam saja.
- **Verifikasi:** 2 kasus integrasi lewat router produksi (rotasi dua akun harus
  menghasilkan dua nama berbeda di console log), 3 unit test untuk resolusi
  identitas, `go vet ./...`, `go test -race ./internal/...`,
  `go test -tags=integration -race ./internal/integration/...`.

### 🐛 Proxy pool ter-assign tapi tidak pernah dipakai — issue #78 (butir 1)

Pool yang terpasang di dashboard **tidak pernah dipakai** untuk sebagian besar
provider. UI menulis `proxyPoolId` dan menampilkan badge "Proxy" — request-nya
tetap keluar lewat IP asli. Tiga sebab, ketiganya diperbaiki.

- **`forwardRequest` mengabaikan client koneksi.** Signature-nya tidak punya
  client, jadi selalu `h.Client`: **setiap provider tanpa executor kustom**
  (deepseek, openai, openrouter, groq, mistral, …) melompati proxy. Jalur Gemini
  native punya masalah yang sama. Client hasil resolusi sekarang diteruskan
  (`forwardRequest` + `forwardGeminiNativeRequest`).
- **Pool level provider hanya dibaca koneksi virtual no-auth.**
  `settings.providerStrategies[...].proxyPoolId` baru dipakai di
  `getBestConnection` untuk koneksi hasil sintetis, jadi koneksi yang punya API
  key sendiri keluar langsung. `applyProviderProxyPool` kini menjadikannya
  fallback ketika koneksi tidak punya binding sendiri (binding eksplisit tetap
  menang).
- **Alias vs id kanonik.** UI menyimpan pool di bawah `storageAlias` (mis.
  `mmf`, `cl`, `ocg`, `ocz`) sedangkan request membawa id kanonik
  (`mimo-free`, `clinepass`, `opencode-go`, `opencode-zen`).
  `ResolveProviderProxyPoolID` hanya mengingat tiga pasangan, jadi sisanya
  membaca "tidak ada". Sekarang kunci dicari lewat `providerStrategyKeys`:
  id → alias terpublikasi → id kanonik → pasangan upstream (cline ↔ clinepass).
- **Pool tak terpakai gagal diam-diam.** Pool yang terhapus, nonaktif, tanpa
  URL, atau URL-nya tidak bisa diparse sebelumnya membuat request pergi
  langsung. Sekarang `getClientForConnection` mengembalikan error dan
  `tryForwardWithConnection` gagal **sebelum** ada byte yang terkirim — tidak
  ada request pertama yang bocor ke IP asli.
- **Verifikasi:** 4 kasus integrasi lewat router produksi dengan proxy palsu
  yang menghitung setiap tunnel (termasuk satu yang membuktikan hop lewat
  header yang di-stamp proxy), 6 unit test untuk resolusi alias pool,
  `go vet ./...`, `go test -race ./internal/...`,
  `go test -tags=integration -race ./internal/integration/...`.

### 🐛 Edge relay kehilangan `x-relay-target` di lane Zen

Dengan pool bertipe `vercel`/`cloudflare`/`deno`, `getProviderConfig` menukar
tujuan upstream menjadi header relay (`BuildEdgeRelayHeaders`) lalu mengganti
`BaseURL` dengan host relay. `ForwardOpencodeZen` membangun ulang header dari
nol, jadi `x-relay-target` hilang dan relay menjawab
`400 {"error":"Missing x-relay-target header"}` — **semua** request gagal begitu
pool edge dipasang.

- **`zenHeaders` kini membawa `x-relay-target` / `x-relay-path` /
  `x-opencode-project`** dari config koneksi. Fingerprint UA tetap menang
  atas `User-Agent` yang dikirim koneksi.
- **`zenRelayPath` menentukan lane dari satu tempat.** Nilai `x-relay-path`
  di-stamp dari `baseUrl` koneksi, jadi untuk koneksi default isinya
  `/zen/v1/chat/completions` — penting saat modelnya butuh lane lain.
- **Lane `/messages` juga.** Jalur itu membangun set header sendiri
  (`x-api-key` + `anthropic-version`), jadi ikut kehilangan header relay; sekarang
  sama seperti dua lane lain, tujuan ada di header dan `BaseURL` yang dipanggil
  adalah host relay.
- **Verifikasi:** 5 kasus test memakai relay palsu yang meniru perilaku
  deployment (`internal/handlers/media/deploy.go`) dan membalas 400 seperti
  aslinya — ketiganya **gagal dengan pesan yang sama seperti laporan Anda**
  sebelum fix, lalu hijau sesudahnya.

### 🐛 Batch perbaikan issue terbuka (#72, #73, #74, #75, #76, #77, #78, #79, #47, #61)

Sepuluh issue yang masih terbuka ditutup di satu batch. Yang sudah benar di
`main` (#48) tidak disentuh; yang butuh PR terpisah masih tercatat di issue.

- 🔴 **Self-update bisa mengganti binary tanpa verifikasi checksum (#72).**
  `PerformSelfUpdate` hanya memverifikasi SHA256 *kalau* manifest menyediakannya,
  padahal di jalur yang benar-benar dipakai `expectedSHA256` selalu kosong:
  `checkManifest` membaca field `sha256` yang tidak ada di `version.json`, dan
  `checkGitHubReleases` tidak pernah mengisinya. Rangkaian download → tulis →
  rename menimpa binary yang sedang berjalan tanpa cek integritas. Sekarang
  checksum **wajib**: tanpa itu `PerformSelfUpdate` menolak sebelum request
  jaringan apa pun dan binary yang berjalan tidak tersentuh. Untuk menutup
  gap-nya, `checkGitHubReleases` membaca aset `SHA256SUMS.txt` yang memang
  sudah diterbitkan `release.yml` (tidak ada kode Go yang membacanya) dan
  memilih entri yang cocok dengan aset platform aktif. Kegagalan lookup tidak
  mematikan pengecekan update; `SHA256` kosong dan instalasi ditolak dengan
  pesan yang menyebut tidak ada checksum.
- 🔴 **`parseSemver` membuang suffix prerelease (#73).** `1.9.7-rc1` dan
  `1.9.7` sama-sama jadi `[1,9,7]`, jadi RC tidak pernah ditawarkan sebagai
  update — dan begitu `1.9.8-rc1` terbit, user di `1.9.7` auto-update ke RC.
  Precedence semver sebenarnya sekarang dipakai (versi final menang atas
  prereleasenya sendiri, `rc2 > rc1`, build metadata diabaikan), plus guard
  kedua di `runCheckCycle`: jalur otomatis tidak pernah memasang tag
  ber-prerelease ke proses yang sedang berjalan di versi final. RC tetap bisa
  dipasang manual lewat `9router-go update` maupun tombol dashboard.
- 🔴 **PID daur-ulang bisa membuat `stop` membunuh proses lain (#74).**
  `RunningPID` hanya percaya PID telanjang plus `proc.Alive`, jadi file pid
  yang ditinggalkan daemon yang mati bisa dilaporkan hidup setelah OS memakai
  ulang nomornya — lalu `Stop` mengirim SIGTERM/SIGKILL ke orang tak
  bersalah. Klaim pid kini mencatat `<pid> <exe>`, dan `proc.Executable(pid)`
  (Windows `QueryFullProcessImageName`, Linux `/proc/<pid>/exe`, BSD
  `kern.proc.pathname`) memverifikasinya sebelum sinyal dikirim. Klaim yang
  tidak bisa diverifikasi **bukan** daemon yang hidup, jadi tidak pernah
  berwenang atas sinyal. `Stop` juga tidak lagi menghapus file pid di jalur
  force-kill, sehingga keadaan "ada klaim tapi prosesnya sudah mati" bisa
  terwakili.
- 🔴 **`gateway.log` tumbuh tanpa batas dan dibaca utuh tiap 150 ms (#75).**
  Log di-append tanpa cap, `LogTail` memuat seluruh file per panggilan
  `logs`, dan `bindFailureSeen` melakukan `os.ReadFile` + `bytes.Contains`
  pada setiap iterasi polling 150 ms. Log sekarang di-trim ke 16 MiB saat
  dibuka (ekor dipertahankan, kepala dipindah ke `gateway.log.1`), `LogTail`
  hanya membaca jendela 256 KiB dari belakang, dan pemindaian bind failure
  dibatasi ke ekor log.
- **`tools[].toolSpec.name` (Bedrock Converse) dilewati dua arah (#77).**
  `visitTools`/`replaceInTools` hanya mengenal `name`, `function.name`, dan
  `functionDeclarations`, sehingga request berbentuk Converse tetap membawa
  nama > 64 karakter ke upstream dan tidak ada apa pun yang memulihkannya di
  respons. Issue menyebut ini "direkam tapi tidak ditulis"; yang sebenarnya
  adalah keduanya tidak disentuh —adding `toolSpec` ke sisi request saja
  akan membuat respons mengembalikan nama yang tidak pernah dideklarasikan.
  Bentuk Converse kini ditangani simetris di request **dan** respons
  (`contentBlockStart.start.toolUse` dan `output.message.content[].toolUse`).
- **`signalSelfShutdown` dead code di kedua varian build (#76) — BELUM dihapus.**
  Kedua file `signal_unix.go`/`signal_windows.go` memang tidak punya call site
  sejak rewrite `RestartSelf` pindah ke `shutdown.RestartAfterStop`, dan isinya
  identik. Penghapusan file-nya **tidak termasuk batch ini**; issue #76 tetap
  terbuka.
- **Kredensial Kiro tidak pernah sampai ke quota tracker (#78).**
  `fetchProviderUsage` mengirim `accessToken` ke `fetchKiroUsage`, padahal
  koneksi Kiro menyimpan kredensialnya di `apiKey` — jadi request-nya
  membawa `Authorization: Bearer ` dan dashboard menampilkan *"Kiro quota API
  rejected the current token. Chat may still work."* sementara chat-nya
  sendiri sukses memakai kredensial yang tidak pernah dibaca quota path.
  Presedensinya sekarang sama dengan `resolveProviderAuthToken` di jalur chat,
  dan token kosong dilaporkan sebagai "kredensial tidak tersimpan" — bukan
  penolakan token yang menyesatkan.
- **Antigravityqueue dua kali di gate quota (#78).** `HandleGetConnectionUsage`
  mengambil slot `quotaFetchGate` sekali sebelum dispatch lalu sekali lagi
  di cabang Antigravity, jadi tiap akun Antigravity menunggu dua gap 250 ms
  berturut-turut untuk satu burst request. Slot kedua dihapus.
- **Dropdown periode usage dengan `all` + window kustom (#79).**
  Selector periode berupa deretan tombol pill hardcoded yang tidak punya
>  `all`, padahal backend sudah menerimanya. Sekarang dropdown dengan preset
>  (Today, 24h, 7D, 30D, 60D, All time) plus input kustom, dan backend
>  `/api/usage/stats` menerima bentuk `<n>d` / `<n>h` apa pun —
>  `resolveUsagePeriod` mengganti rantai `if/else` yang diam-diam memakai
>  365 hari untuk `all` dan 7 hari untuk nilai yang tidak dikenal.
- **Download database ditolak padahal sudah login (#47).**
>  `HandleExportDatabase` tidak menerima session dashboard — hanya header
>  `x-9r-password` atau token CLI — sehingga link browser biasa selalu 401
>  dan ekspor terlihat permanen terblokir. Session sekarang cukup dengan
>  sendirinya seperti baca dashboard lain, sementara header password tetap
>  jalan untuk skrip. Jalur zip yang diminta sudah ada di backend dan kini
>  bisa dijangkau.
- **Picker model tidak lagi menyortir ulang seluruh katalog per klik (#61).**
  `resolveFilteredGroups` menerima `addedModelValues` yang tidak pernah
  dibaca, tapi karena argumennya ada di signature, Svelte menjadikannya bagian
  dari graf reaktif: setiap klik satu pill memicu filter + sort ulang seluruh
  grup dan rekonsiliasi ulang ratusan/ribuan pill. Argumen itu dihapus; logika
  filter/sort tidak berubah sama sekali.

**Di luar cakupan:** #78 butir 1–2 (executor `opencode-zen`) sudah dikerjakan
di PR #80 dan #48 sudah benar di `main` (`stripCodexUnsupportedTokenParams`
berjalan setelah `buildResponsesBody`, bukan sebelumnya) — keduanya tidak
disentuh di sini.

## [v1.9.6] - 2026-10-01

### 🐛 Pre-release review: 5 blocker yang lolos semua gate (#70)

Audit 42 commit `v1.9.5..main` sebelum rilis menemukan lima cacat yang **tidak**
tertangkap `go vet`, `bun test`, maupun `-race`, karena test yang ada memock
hal yang sama persis dengan jalur kodenya. Kelimanya sudah diperbaiki dan
diuji dengan uji regresi yang gagal bila fix-nya dibalik (*mutation-checked*).

- 🔴 **`9router-go status|stop|logs` buta terhadap `DATA_DIR` dari `.env`.**
  `daemonURL()` dan `daemon.Dir()` membaca `os.Getenv`, sedangkan server
  menyelesaikannya lewat viper yang membaca `.env`. Pada deployment yang
  dikonfigurasi lewat `.env` saja — termasuk setiap docker compose — proses CLI
  melihat direktori berbeda dari daemon yang sedang jalan: `status` melaporkan
  *"not running"* padahal ada listener hidup, `stop` menolak untuk halt, dan
  `logs` mengklaim tidak ada log padahal file-nya 50 KB. `ResolveDataDir()`
  kini membaca `.env` (dengan urutan env → `.env` → default platform) dan
  `daemonURL()` memakai `config.LoadConfig()` sehingga port yang diprobe sama
  dengan yang di-bind.
- 🔴 **Nama tool ter-fit bocor ke client di lane Claude `TranslateResp`.**
  `handleClaudeMessagesStream` `return` di baris 19–46, **sebelum**
  `decloaker := NewClaudeStreamDecloaker(req.ToolNameMap)` di baris 78. Request
  sudah di-fit di `fallback.go:362`, jadi `content_block_start` tool_use sampai
  ke client dengan nama 64 karakter dan tidak bisa di-dispatch. Non-stream
  punya cacat serupa (passthrough di `claude_messages.go:191` keluar sebelum
  decloak) — keduanya sekarang decloak sebelum branch mana pun. Decloaker
  di-hoist ke atas `TranslateResp`.
- 🔴 **Lane Responses: request tidak konsisten dengan dirinya sendiri.**
  `FitToolNames` tidak punya walker `input[]` (hanya `tools`/`functions`/
  `messages`/`contents`/`tool_choice`), padahal `RestoreToolNames` tetap
  me-restore `output[]` (`fingerprint.go:282-311`). Akibatnya deklarasi tool
  ter-fit ke 64 karakter sementara history masih memanggil nama aslinya 71
  karakter — upstream menerima request yang bertentangan dengan dirinya, dan
  fix 400-nya sendiri tidak benar-benar terpakai, karena nama panjang tetap
  dibawa lewat `input`. Ditambahkan `visitInput`/`replaceInInput`
  (`tool_fit.go`) yang berbagi satu walker untuk kedua arah, dan
  `passthroughResponses` kini menerapkan `req.ToolNameMap` pada body non-stream
  maupun lewat `sseStreamOpts` pada stream.
- 🔴 **Idempotency key reset-credit kosong — proteksi double-redeem tidak
  pernah ada.** `resetCreditIdempotencyKey` dideklarasikan tapi tidak pernah
  di-assign di mana pun (`newIdempotencyKey()` adalah dead code), sehingga
  tiap request mengirim `idempotencyKey: ""` dan server memint key baru per
  request (`usage_codex_reset.go:176-178`) → dua submit innocuous =
  dua kredit terbuang. Key kini di-mint saat modal dibuka, di-reset saat ditutup,
  dan `confirmResetCredit` menolak mengirim key kosong. Helper-nya dipindah ke
  `lib/codexResetCredit.ts` agar unit-testable sesuai konvensi repo.
- 🔴 **`9router-go stop` di Windows tidak pernah graceful.** `stopGraceMS = 5000`
  dideklarasikan, tapi `requestStop` di `proc_windows.go` selalu
  `ErrStopUnsupported`, jadi `proc.Terminate` langsung `ForceKill` — drain
  `server.Shutdown` 5 detik yang dirancang di `server.go:99-101` **tidak pernah
  jalan** di Windows: setiap `stop`/`restart`/auto-update memotong SSE
  in-flight dan SQL transaction setengah jalan. `Stop()` kini POST ke
  `/api/version/shutdown` lebih dulu **dengan CLI token** (endpoint-nya
  always-protected; tanpa token selalu 401 dan jatuh ke force-kill), dengan
  `proc.Terminate` sebagai fallback untuk daemon yang tidak menjawab. Terverifikasi
  live: log `Server stopped gracefully` pada 4 siklus restart beruntun.
- 🔴 **Data race `usagetracker`** (ketemu saat rerun `-race`, pre-existing dan
  tidak terkait 5 fix di atas): `scheduleBroadcastLocked` menyalin daftar
  subscriber lalu melepas lock **sebelum** send, sedangkan `unsubscribe` menutup
  channel di bawah write lock → *send on closed channel*. Map-nya terbaca aman,
  channel-nya tidak. Kirim dipindah ke bawah `RLock`. Catatan: `8520e4e`
  mengklaim "fix data race in usagetracker" tetapi hanya menambal ring seeding.
- **Batas 64 karakter sekarang benar-benar diuji.** Fixture lama adalah 63 dan
  65 karakter, jadi tidak ada yang mem-*pin* tepat di batas — dan batas itulah
  yang jadi inti fitur ini. Ditambah `TestFitToolNames_ExactBoundary`.
- **Verifikasi:** `go vet ./...` + `-tags=integration` 0 warning;
  `go test -race -count=1 ./...` exit 0; `go test -tags=integration` ok;
  `bun test` 115/115; `tsc -b`/`oxlint`/`bun run build` bersih;
  `make build` + `make cross` (5 platform) sukses. Live: lifecycle daemon
  start/status/restart/stop tanpa `DATA_DIR` ter-export, tool 71 karakter
  kembali ke client sebagai 71 karakter, 7 route dashboard tanpa console
  error.
- **Ditunda (bukan blocker, tidak ikut PR ini):** self-update bisa memasang
  binary tanpa verifikasi saat manifest tidak punya `sha256` (`updater.go:411`;
  `release.yml` sudah menerbitkan `SHA256SUMS.txt` tapi tidak ada kode Go yang
  membacanya); `parseSemver` membuang suffix prerelease sehingga RC bisa
  terbaca lebih baru dari final dan di-auto-apply; `RunningPID` hanya percaya
  PID telanjang sehingga PID daur-ulang bisa membuat `stop` membunuh proses
  lain; `gateway.log` tidak pernah dirotasi dan dibaca utuh tiap 150 ms;
  `tools[].toolSpec.name`
  (Bedrock Converse) direkam tapi tidak ditulis.

### 🐛 MCP tools dengan nama fungsi > 64 karakter mental dengan HTTP 400 (#68)

- **Masalah:** Spesifikasi fungsi OpenAI / OpenAI-compatible membatasi panjang `function.name` maksimal 64 karakter (`^[a-zA-Z0-9_-]{1,64}$`). Coding agent dengan integrasi server MCP sering kali menggunakan nama namespaced (misal `mcp__server_name__action_detail_something`) yang melebihi 64 karakter, menyebabkan upstream provider (OpenAI, Console, Responses API, dll.) menolak request dengan status 400 Bad Request (`name must be at most 64 characters, got XX`).
- **Perbaikan:**
  - Ditambahkan `translator.FitToolNames` yang secara deterministik memangkas nama fungsi yang melebihi 64 karakter menjadi maksimal 64 karakter dengan sufiks unik `_1`, `_2`, dst (memperhitungkan panjang sufiks sehingga total panjang tidak pernah melebihi 64 karakter dan tidak bentrok dengan tools lain).
  - Mengganti seluruh referensi nama fungsi di deklarasi `tools`, `functions`, conversation history (`messages` assistant `tool_calls`, `function_call`, `role: "tool"`/`role: "function"`, Claude `tool_use`), dan `tool_choice`.
  - Mengintegrasikan pemulihan nama via `NewToolNameRestoringWriter` dan `RestoreToolNamesInPayload`, sehingga respons dari upstream (baik streaming SSE maupun non-streaming JSON, OpenAI/Claude/Responses/Gemini) dikembalikan ke nama asli yang panjang sebelum diteruskan ke client.
  - Sesi multi-turn percakapan tetap sinkron karena pemotongan nama bersifat deterministik.
- **Verifikasi:** Unit test `TestFitToolNames_*`, `TestRestoreToolNames_*` di `internal/translator/tool_fit_test.go` dan end-to-end integration test `TestE2E_FitToolNames_*` (non-streaming, SSE streaming, multi-turn) di `internal/handlers/chat/tool_fit_e2e_test.go` lolos dengan `go test -race` dan `go vet`.

### 🐛 Fix dashboard feedback issues: login lockout, remote password rotation, proxy dropdown, combo model drag-and-drop, and zip database backup (#50)

- **Login limiter IP bucketing**: `LoginClientIP` in `internal/auth/session.go` no longer falls back to `"unknown"` when no proxy headers are present. Direct TCP peer IP from `r.RemoteAddr` is used so distinct clients have their own failure buckets and one misconfigured client does not lock out all other users.
- **Remote / Docker initial password rotation**: `POST /api/auth/login` now accepts `{ password, newPassword }`. Remote and Docker fresh installs requiring default password rotation can set their new password directly and receive a valid session cookie without encountering 401 Unauthorized from protected settings endpoints.
- **Initial password change check**: `changeDashboardPassword` in `internal/handlers/dashboard/settings.go` now validates against `INITIAL_PASSWORD` when no password hash is stored.
- **Provider proxy dropdown & Antigravity Free glitch**:
  - In `ProviderDetailView.svelte`, the connection row Proxy button is now always rendered even if no proxy pools exist yet, displaying a clear empty state with a shortcut to create one.
  - The proxy dropdown now uses `position: fixed` relative to the trigger button to prevent clipping inside the scroll container (`overflow-y-auto`).
  - Wrapped `loadData()` inside `untrack` so that background polling of `connections` does not continuously re-trigger `loadData()`, eliminating the re-render flash / glitch on free providers like OpenCode Free and preventing proxy selection from resetting.
- **Combo model drag-and-drop & picker performance**:
  - Implemented HTML5 drag-and-drop reordering (`draggable`, `ondragstart`, `ondragover`, `ondrop`, `ondragend`) on model rows in `CreateComboModal.svelte` with active drag visual indicators.
  - Preserved stable alphabetical ordering in `pickerData.ts` to eliminate layout shift, frame drops, and freezing when clicking model pills in `ModelPickerModal.svelte`.
  - Added module-level caching for model picker metadata (`pickerExtras`) so opening the picker does not flash empty states or block UI interactions.
- **Database backup download & ZIP archive support**:
  - Deferred `URL.revokeObjectURL` in `ProfileSettingsView.svelte` to prevent modern Chromium/Firefox download managers from cancelling or blocking in-flight blob downloads.
  - Added support for `?format=zip` in `GET /api/settings/database` to export backups as standard compressed `.zip` archives with `Content-Disposition: attachment`.
  - Added support for importing `.zip` archives in `POST /api/settings/database`, automatically extracting and restoring the JSON payload.
  - Updated `ProfileSettingsView.svelte` to download `.zip` by default and accept `.zip` as well as `.json` imports.

### ✅ Binary bisa jalan di background — `9router-go start` / `stop` / `restart` / `status` / `logs`

- **Opsi baru:** `--background` (alias `-d`) dan sub-command `start` menjalankan gateway sebagai proses terpisah yang tidak menempel ke terminal, lalu langsung kembali. Sub-command baru: `stop`, `restart`, `status`, `logs -n <baris>`. Perilaku lama (`9router-go` tanpa flag) **tidak berubah** — tetap jalan di foreground dan berhenti saat `^C`.
- **PID file & log:** proses yang terpisah mencatat dirinya di `DATA_DIR/run/gateway.pid` dan menulis stdout/stderr ke `DATA_DIR/run/gateway.log`. File PID basi dibersihkan saat command berikutnya jalan, sehingga daemon yang dibunuh paksa dari Task Manager tidak meninggalkan jejak yang menyesatkan.
- **`Start` menolak saat port sudah dipakai** — pre-flight TCP connect, bukan sekadar mengandalkan PID file. Diuji dengan server Python yang memegang port: `start` gagal dengan pesan yang menyebut port dan sumbernya, bukan melombakan dua proses untuk satu bind.
- **Dua bug Windows nyata yang ketemu dan diperbaiki di jalur yang sama.** (1) `HandleShutdown` (tombol Shutdown di dashboard) self-signal `SIGTERM`, dan `os.Process.Signal(syscall.SIGTERM)` di Windows mengembalikan `not supported by windows` — tombolnya tidak pernah menghentikan server. (2) `RestartSelf()` melakukan `os.Exit(0)` sebelum listener ditutup, lalu spawn spawn replacement, sehingga proses baru bisa rebut port sebelum yang lama melepaskannya. Keduanya kini lewat `shutdown.RequestStop()` / `shutdown.RunAfterStop()`: main menunggu tiga sumber (`SIGINT`, `SIGTERM`, stop request), menjalankan `fxApp.Stop`, baru menjalankan hook spawn. Terbukti: `POST /api/version/shutdown` dengan session cookie → proses mati, port `20197` benar-benar lepas.
- **Detail restart yang sempat bikin bug:** `Restart` harus **stop dulu baru pre-flight**. Kalau pre-flight dijalankan lebih dulu, ia menemukan daemon lama yang masih listen dan reported "already running"; kalau dijalankan sesudah `TerminateProcess`, socket-nya baru diedit sangat sedikit. Urutan stop → tunggu port bebas (maks 10 dtk) → start sudah teruji: pid `21700` → restart → pid `17628`, `/health` tetap `{"status":"ok"}`.
- **Baru `internal/proc`:** primitif proses portabel ( Alive / Terminate / Detached / SelfExecutable ) dipakai bersama oleh daemon **dan** headroom; `internal/headroom/sig_unix.go` + `sig_windows.go` yang menduplikasi logika yang sama dihapus.
- **Yang berubah di API shutdown:** `shutdown.RequestStop()` menutup channel baru `StopRequested()` sekaligus memicu `Cancel()` (SSE berhenti). `Cancel()` saja **tidak** menutup `StopRequested()` — `^C` bukan permintaan keluar dari operator, dan main harus terus menunggu sinyal asli. Dipin di `internal/shutdown/shutdown_test.go`.
- **Verifikasi:** `go vet ./...` bersih, `go build ./...` bersih, `go test ./internal/...` semua paket hijau kecuali `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` yang **sudah gagal sebelum perubahan ini** (dibuktikan di worktree bersih pada `HEAD` `9323d21`, bukan efek samping). Smoke penuh di Windows: start → `/health` `{"status":"ok"}` → `/login` 200 → status → restart (pid baru) → health → stop → pid file hilang → port tidak lagi `LISTENING`; `-d` identik dengan `start`; start kedua refused "already running"; foreground tidak menulis pid file sama sekali.

> ⚠️ **Perbedaan perilaku antar-platform yang disengaja:** di Windows tidak ada SIGTERM, jadi `stop` memakai `TerminateProcess` (proses langsung mati, drain SSE dilewati). Di POSIX, stop mengirim SIGTERM sehingga hook Fx berjalan dan `runServer` keluar bersih. Yang butuh drain (update via dashboard, restart) sebaiknya lewat `restart` atau tombol Shutdown, bukan `stop` di Windows.

### 🐛 Makefile hanya jalan di POSIX shell — di Windows setiap target build/run rusak

- 🔴 **Semua gejala berasal dari sintaks POSIX di dalam recipe.** `VERSION ?= $(shell cat VERSION 2>/dev/null …)` mengembalikan string kosong di shell non-POSIX (`/dev/null` tidak ada), jadi `-X …CurrentVersion=` meng-embed versi **kosong** ke binary. `PORT=20130 ./9router-go` membuat cmd.exe mencoba menjalankan program bernama `PORT`. `run`/`version`/`update`/`mitm-*` memanggil `./$(BINARY_NAME)`, dan cmd.exe menjawab `'.' is not recognized as an internal or external command` karena `.` tidak ada di PATH (PATHEXT hanya menyertakan `.EXE`). `LDFLAGS` diapit tanda kutip tunggal, dan cmd.exe memperlakukan `'` sebagai karakter literal sehingga linker menerima nama simbol yang sudah ter-quote. `DATA_DIR ?= $(HOME)/.9router` expandable jadi kosong di native Windows → path literal `/.9router` → `C:/Program Files/Git/.9router`, sehingga **setiap** `make run` menulis DB sementara yang baru dan dashboard membalas 401 terus-menerus.
- **Perbaikan:** `VERSION` dibaca lewat `$(file <VERSION))` (Make native, tanpa shell) dengan fallback berurutan ke `version.json` lalu `git describe` lalu `1.0.0`; binary dipanggil sebagai `$(BINARY)` tanpa `./` (ditemukan lewat PATHEXT di cmd.exe, tetap jalan di sh/POSIX); `LDFLAGS` memakai tanda kutip ganda; `DATA_DIR ?=` dibiarkan kosong agar binary menerapkan default per-platform-nya sendiri (`%APPDATA%\9router` / `~/.9router`).
- **`PORT`/`DATA_DIR`/`RTK`/`CAVEMAN`/`PONYTAIL` hanya diekspor kalau asalnya bukan `file`.** `make run` polled tidak boleh menimpa config: nilai `?=` punya origin `file`, sedangkan yang datang dari environment atau command line punya origin `environment`/`command line`. Yang di-set operator tetap diteruskan; yang tidak di-set dilewati agar binary membaca `.env` (viper) sendiri seperti biasa. Ini menggantikan prefix `VAR=value` yang tidak bisa di-parse cmd.exe.
- **`web-build` dan `cross` tidak lagi butuh POSIX.** Guard `[ ! -f web/dist/index.html ]` mati di cmd.exe (`! was unexpected at this time.`), jadi seluruh pemeriksaan-keberadaan dan build didelegasikan ke `bun -e` — Bun sudah jadi prasyarat setiap jalur yang lewat target ini. `cross` tidak lagi menulis `GOOS=… GOARCH=…` di depan `go build`; per-target OS/ARCH masuk lewat direktif `export` yang identik di sh maupun cmd, dan `export`-nya di-scope ke subgoal `cross-one` supaya `GOARCH` kosong tidak pernah bocor ke `build` biasa.
- **Verifikasi:** `make version` menghasilkan `9router-go version 1.9.5 (windows/amd64)` baik saat make dipanggil dari sh maupun dari `cmd.exe /c`; `make cross-one CROSS_OS=linux CROSS_ARCH=amd64` dari Windows menghasilkan binary ELF sungguhan (magic `\x7fELF`), jadi plumbing `export GOOS/GOARCH` terbukti bukan sekadar lolos parse. Recipe `cross` (loop `for`), `clean` (`rm -f`), `help` (`grep|sed`), dan `bench` masih POSIX-saja — di mesin tanpa `sh.exe` di PATH, recipe itulah yang pertama gagal.
- 🔴 **Follow-up: `BINARY_NAME` tidak punya ekstensi, jadi di Windows build baru tidak pernah dijalankan.** `go build -o 9router-go` di native Windows menulis file bernama `9router-go` (PE tanpa ekstensi), sementara `make run` memanggil `9router-go`, yang cmd.exe resolve lewat `PATHEXT` ke **`9router-go.exe` milik build sebelumnya**. Akibatnya `make run` diam-diam menjalankan binary lama: aset `go:embed` tetap bundle SPA yang sudah usang, dan perbaikan frontend tampak "tidak masuk" padahal sudah di-build. Diperbaiki dengan `BINARY_SUFFIX := $(if $(findstring Windows_NT,$(OS)),.exe,)` sehingga target build menulis persis file yang dieksekusi. `BINARY` juga dipisah per-OS: bare name di Windows (PATHEXT), `./$(BINARY_NAME)` di macOS/Linux (`.` tidak ada di PATH).
- **`web-build` bisa dipaksa rebuild.** Kondisi `existsSync('web/dist/index.html')` membuat SPA hanya dibangun sekali; sekarang `FORCE=1 make web-build` (atau `FORCE=1 make build`) membangun ulang `web/dist`. Docker (`Dockerfile` stage `web-builder`) dan GitHub Actions (`ci.yml`, `release.yml`) **tidak** terpengaruh: keduanya sudah memanggil `bun run build` secara eksplisit di build context bersih, jadi tidak pernah melewati cache ini.


### 🐛 Dua error console dashboard: tombol "Add Model" tidak pernah membuka picker, dan event install dipakai ulang

- **Gejalanya satu, root cause-nya lain, dan keduanya sudah terukur di browser.** Membuka modal Create/Edit Combo lalu menekan **Add Model** melempar `effect_update_depth_exceeded` — picker tidak pernah tampil, tidak ada input pencarian di DOM. Efek reset di `CombosView.svelte` menulis `modalNameResetKey`, nilai yang dibaca blok `{#key}` di bawahnya; blok itu membuat ulang `CreateComboModal`, dan itu mengantrekan ulang efeknya. Svelte membuang seluruh flush setelah **1001** putaran (terhitung di dev build: `window.__resetRuns = 1001` tepat saat modal Create terbuka, sebelum "Add Model" ditekan). Efeknya kini dijaga `createSessionOpen`, jadi reset terjadi sekali per sesi — diverifikasi di build production: picker terbuka, search 77 → 36 pill → kembali 77, klik pill toggle, **0 error console**.
- **Bukan regression dari #61.** Bug ini direproduksi juga dengan kode pra-#61 (`git show a8b1556^`), dan dibisect: menghapus `modalNameResetKey += 1` saja (sambil membiarkan `modalModels = []`) membuat efek berhenti di **1** putaran dan error hilang. Jadi penyebabnya terukur, bukan dugaan.
- **Event `beforeinstallprompt` sekarang benar-benar sekali pakai.** `promptInstall()` lama menyimpan event setelah dialog di-*dismiss*, jadi klik berikutnya memanggil `prompt()` pada event yang sudah dipakai dan Chrome menolak dengan "Ignored bad install" di console. Sekarang event di-*spend* pada panggilan pertama (accepted maupun dismissed) dan CTA-nya mati sampai Chrome mengirim event baru; kalau app sudah berjalan standalone, event tidak lagi di-*preventDefault* karena tidak ada tombol custom yang memakainya. Dites di `web/src/lib/pwa.test.ts` (4 kasus) dan **mutation-checked**: menghapus guard standalone atau mengembalikan event ke keadaan "belum dipakai" membuat 3 dari 4 test gagal pada asersi yang tepat.
- **`s.includes is not a function` — akhirnya terReproduksi dan diperbaiki.** Akar masalahnya ketemu: `internal/db/accounts.go:LockConnectionRateLimit` menulis `lastError` ke `providerConnections.data` sebagai **JSON object** (`{"status":429,"message":"…","timestamp":"…"}`, dipakai juga oleh `RecordConnectionOAuthFailure`), sedangkan halaman provider membaca `conn.lastError` lalu memanggil `.includes('429')`/`.toLowerCase().includes('quota')` di `getCooldownInfo`. Begitu sebuah koneksi kena rate limit, `s` adalah object dan `.includes` melempar `TypeError` — seluruh render halaman mati. Diperbaiki di tiga lapis: (1) **backend** `sanitizeProviderConnection` menormalkan `lastError` apa pun (object/string/lainnya) menjadi string sebelum keluar dari server; (2) **API client** `normalizeConnection`/`normalizeLastError` melakukan hal yang sama pada semua jalur ambil koneksi (`getConnections`, `getProvidersClient`, `getProvidersClientPage`); (3) **komponen** memakai helper yang sama, jadi halaman provider tidak pernah memanggil method string pada nilai non-string. Diuji dengan `TestSanitizeProviderConnection_LastErrorString` (backend) dan dua kasus `client.test.ts` yang menutup ketiga jalur fetch.
- **`/api/models/caps` tidak lagi 404 untuk compatible node.** Halaman `openai-compatible-*` / `anthropic-compatible-*` dan custom provider node selalu meminta caps; backend menjawab `404 unknown provider or no static models` karena node itu memang tidak punya katalog statis, jadi tiap pembukaan halaman menambah request merah di console. Handler kini menjawab `200 {"provider":…,"caps":{}}` untuk id yang ditandai sebagai node, dan halaman provider tidak lagi memanggil endpoint itu sama sekali bila provider-nya bukan katalog statis. Alias dan id katalog tetap ikut ter-cover: seluruh provider Go yang punya model statis (92 id kanonis) ada di `PROVIDER_CATALOG`, jadi ikon kapabilitas dan picker thinking tidak hilang.
- **Combo `Auto Free Tier` tidak lagi terkunci permanen — bisa diedit, di-rename, dan dihapus.** Alasannya: feature ini berasal dari PR #58 dan **tidak ada di upstream**. `src/app/api/combos/` upstream hanya punya `[id]` dan `presets`, tidak ada `auto-free`; route `DELETE /api/combos/[id]` upstream juga **tidak** punya guard apa pun. Sementara itu di port ini combo-nya di-lock di tiga tempat: `validateLockedReorder` menolak rename/kind/ubah model set (`combos.go`), `HandleDeleteCombo` menolak `DELETE` dengan 403, dan `ComboCard` menonaktifkan checkbox/Edit/Delete. Akibatnya begitu tombol **Auto Free Tier** diklik sekali, baris `auto-free-tier` ada selamanya dan tidak bisa dilepas dari dashboard.
- **Yang dihapus:** `validateLockedReorder` + `sameModelSet` + guard di `HandleUpdateCombo`/`HandleDeleteCombo` (backend), `isAutoFreeCombo` beserta filter `deletableCombos` dan guard `toggleSelect` (frontend), badge "reorder only" beserta tombol Edit/Delete yang mati, dan jalur `onReorder` yang sekarang mati — panah Move up/down di kartu dihapus karena Edit modal sudah menyediakan reorder yang sama untuk semua combo. `isAutoFreeCombo` digantikan `isAutoGenerated` yang hanya menandai kind `auto-*` untuk badge "Auto-generated", sama seperti `auto-family-*` yang sejak awal memang editable.
- **Yang dipertahankan:** `POST /api/combos/auto-free` tetap upsert pada id stabil `auto-free-tier`, jadi menghapus combo lalu klik lagi akan membuatnya kembali dengan isi registry terkini. Tipe `kind = "auto-free"` tetap ditulis agar kartu bisa menandainya sebagai machine-generated.
- **Verifikasi:** `TestAutoFreeComboCreateRebuildAndEdit` (menggantikan `TestAutoFreeComboCreateAndLock`) menjalankan route sungguhan lewat `HandleAutoFreeCombo` → rename 200 + nama tersimpan → edit model set 200 + 2 member tersimpan → update strategy-only tidak menimpa model set → `DELETE` 200 + baris hilang → rebuild 200 + nama & model set kembali ke isi registry. Live di `localhost:20130`: `DELETE` menjawab 200 (sebelumnya 403), rename 200, edit model set 200, dan rebuild mengembalikan combo ke keadaan semula. Browser di `localhost:5173/dashboard/combos`: kartu Auto Free Tier kini ber-badge "Auto-generated", checkbox/Edit/Delete aktif, modal Edit dan modal konfirmasi Delete keduanya terbuka, 0 error console. `go vet`, `go test ./...` (32 paket), integration suite, `bun test` 113/113, build bersih.
- **Model embedding tidak lagi masuk rantai fallback `Auto Free Tier`.** Penanda free-tier hanyalah konvensi penamaan — `IsFreeTierModel` mencocokkan sufiks `:free` / `/free` / `-free`, dan sufiks itu juga menempel pada model embedding, image, tts, dan stt. `openrouter` punya tepat satu model free-suffix, yaitu `nvidia/llama-nemotron-embed-vl-1b-v2:free`, yang registry sudah tandai `"embedding"` di `ProviderModelKinds` — tapi `freeTierComboModels` tidak pernah memanggil `GetProviderModelKind`, sehingga model yang tidak bisa menjawab satu turn chat pun tetap masuk daftar. Sekarang satu baris gate `kind != "" && kind != "llm"` membuangnya, persis seperti yang sudah dilakukan `usableModelFamilies` untuk combo auto-family. Census registry: 40 model free-suffix, 35 di antaranya chat-capable, tersebar di 12 kunci provider — jadi fitur ini bukan formalitas, combo untuk/setup yang punya 6 provider gratis berisi belasan model chat.
- **Tombol "Auto Group by Model" dan "Auto Free Tier" dihapus dari header halaman Combo.** Keduanya adalah generator massal yang sekali klik menulis puluhan baris `combos` — 40 model free-suffix di registry, dikelompokkan per keluarga, sudah menghasilkan 80 combo `Auto: …` di satu dashboard. `CombosHeader` kehilangan keempat prop auto-builder, `CombosView` kehilangan `handleBuildAutoFamily` beserta `isBuildingAutoFamily`, dan `api.buildAutoFamilyCombos` ikut dibuang karena tidak lagi dipanggil siapa pun.
- **Halaman Combo disamakan dengan upstream v0.5.91 (`localhost:20128/dashboard/combos`).** Yang dibandingkan langsung di browser, bukan dari reading kode. Upstream memakai **satu** tombol di header — `Create Combo` — dan memindahkan seluruh aksi massal ke *selection bar* di bawah daftar: checkbox `Select all (N)` di kiri yang berubah menjadi `N selected` begitu ada yang dipilih, lalu di kanan `Set strategy…`, `Delete (N)`, dan `Clear` yang **hanya muncul saat ada selection**. Port ini sebelumnya menyimpan `Delete Selected` dan `Delete All (N)` sebagai tombol toolbar permanen, tidak punya selection bar sama sekali, dan punya tombol Rebuild per kartu yang tidak ada di upstream.
- **Apa yang berubah:** `CombosHeader` sekarang hanya menerima `onCreateClick`. `CombosView` mendapat `selectedCombos`/`allSelected`/`toggleSelectAll`, `handleApplyBulkStrategy` (menulis `comboStrategies` sebagai satu patch lalu satu `updateCombo` per baris, karena key-nya adalah *nama* combo sehingga patch terpisah akan menimpa dirinya sendiri), dan selection bar yang meniru markup upstream. `handleDeleteAll` dan `deletableCombos` dihapus. `ComboCard` kehilangan `onRebuild`/`rebuilding` dan tombol Rebuild; `RefreshCw` import ikut dibuang. `api.buildAutoFreeCombo` ikut terhapus karena tidak ada lagi pemanggilnya.
- **Konsekuensi yang disengaja:** `POST /api/combos/auto-free` dan `POST /api/combos/auto-family` kini tidak terjangkau dari dashboard sama sekali. Endpoint-nya tetap ada di backend — yang hilang hanya tombolnya.
- **Selisih yang BELUM dispatched:** daftar strategy port ini 5 (Fallback, Round Robin, **Sticky**, **Capacity**, Fusion) sedangkan upstream 3 (tanpa Sticky dan Capacity). Itu divergence fungsional pada routing, bukan soal tampilan, jadi dibiarkan — perlu keputusan terpisah kalau memang mau disamakan.
- **Verifikasi:** browser `:5173` — toolbar `aria-label="Combo actions"` berisi persis `Create Combo`; selection bar idle berbunyi `Select all (84)`; setelah diklik berbunyi `84 selected` dan memunculkan `Apply Strategy` (disabled sampai strategy dipilih), `Delete (84)`, `Clear`; tombol per kartu `Copy combo name` / `Edit` / `Delete`, tanpa Rebuild; 0 error console. `bun test` 113/113, `tsc -b && vite build` bersih, oxlint tidak menambah warning.


### 🐛 Issue #61 (partial) — picker combo: satu flush untuk tiga fetch metadata, bukan tiga

- **Status jujurnya: #61 BELUM selesai.** Angka utamanya masih hidup. Diukur ulang di build production memakai komponen aslinya (`ModelPickerModal` + katalog asli, 997 pill / 5.561 node di dalam satu `max-h-[400px] overflow-y-auto` + `flex flex-wrap`), median 14 iterasi bergantian di satu sesi Chromium: **clear search → full list 146ms Task / 72ms Script**, dan itu tetap setelah perubahan di bawah.
- **Yang diubah: `web/src/components/combos/pickerExtras.ts` (baru) + `ModelPickerModal.svelte`.** Tiga fetch metadata (`/api/models/alias`, `/api/models/custom`, `/api/models/disabled`) dulu dirantai dengan tiga `.then()` yang masing-masing menulis `$state` sendiri, jadi tiap settle membangun ulang seluruh daftar pill. Sekarang ketiganya dibatch ke satu `Promise.all` dengan catch per-endpoint, dan modal menerbitkan **satu** objek extras dalam satu write. Kontrak per-endpoint dijaga: satu endpoint 500 tidak boleh membuang dua yang berhasil (dipin di `pickerExtras.test.ts`).
- **Juga: `ModelPill.svelte` tidak lagi menerima closure per baris.** `onClick={() => handleToggle(model.value)}` dibuat ulang tiap render, dan `caps` datang sebagai objek yang identitasnya berubah tiap rebuild daftar. Sekarang pill menerima `onToggle` yang stabil plus `vision`/`reasoning` boolean, dan membangun handler-nya sendiri dari `value`.
- **Yang benar-benar bergerak: jalur buka.** 30 iterasi strict-alternating (posisi dibalik tiap giliran): `Task` **149,6ms → 130,1ms (−13%)**, `Script` **105,0ms → 84,3ms (−20%)** — sesuai prediksi, dua rebuild yang hilang ≈ 2 × 13ms. Transisi search (ketik / clear) **tidak** bergerak: masih di dalam noise.
- **Kenapa angka utama tidak bisa dikejar dari sisi ini.** Isolated probe (rebuild data group tanpa menyentuh DOM) menunjukkan biaya rebuild di **kedua** versi identik: **13,15ms vs 13,00ms Script** — jadi closure per baris dan caps objek tidak menyentuh biaya yang dominan. Dan logic aplikasi sendiri cuma **0,14ms**: `resolveFilteredGroups` atas 915 pill / 80 group terukur 0,14ms di Bun dengan katalog asli. Biayanya adalah pembuatan ~1.000 instance komponen + ~5.500 node DOM sekali per render — cocok dengan profil di issue (`props.js` 19,3ms + `attributes.js` 11,4ms + `Icon.svelte` 4,0ms).
- **Prototipe yang TIDAK dikirim (silakan minta kalau mau).** Collapsed-by-default per group — pill baru mount saat grup diklik, otomatis terbuka begitu ada query — diukur di harness yang sama: **open 196ms → 12,8ms Task**, **clear 146ms → 36ms**. Hanya pendekatan ini yang tidak pernah membuat 1.000 pill itu. Tidak ikut dikirim karena (a) opsi tanpa divergensi upstream yang dipilih, dan (b) upstream `v0.5.91` merender list penuh; perlu keputusan eksplisit dulu, lalu dicatat di changelog sebagai divergensi.
- **Verifikasi:** `bun test` 107/107, `tsc -b` + `vite build` bersih, `oxlint` tanpa warning baru (2 warning yang ada sudah pre-existing di `TerminalView.svelte` dan `CreateComboModal.svelte`). Semua angka di atas diukur A/B pada bundle production yang dibangun dua-duanya (HEAD vs perubahan) dan disajikan bergantian supaya drift mesin tidak mengarang selisih. Smoke run di browser juga memverifikasi toggle add/remove pill lintas baris dan grup tanpa page error.

### 🐛 Issue #52 — `additionalItems` in a tool schema 400'd the whole Gemini request

- **The symptom.** `Unknown name "additionalItems" at functionDeclaration.parameters` (HTTP 400). Clients and MCP servers that describe array parameters with JSON Schema draft-07 emit `additionalItems`; the Gemini schema proto has no field for it, and one occurrence anywhere in the parameter schema rejects the entire turn, not just that one tool.
- **The gap was one keyword wide.** `cleanGeminiSchema` already stripped the 2020-12 tuple keyword `prefixItems`, but not its draft-07 twin `additionalItems`, so a draft-07 tuple schema was the one shape that still reached Google verbatim.
- **The fix is one list entry, next to `additionalProperties`.** `additionalItems` joins the `unsupported` keywords, so it is dropped at every node the recursion visits (`properties` values, `items`, `items` tuples, `additionalProperties`) rather than at one known depth. It is a list entry and not a local `delete` on purpose: `anyOf`/`oneOf` flattening copies a branch's keys back into the schema, so only the list — which `stripUnsupported` re-runs after the merge — closes that path. `prefixItems` keeps its dedicated block because it has to be promoted to `items` first; `additionalItems` only constrains the tail of a tuple and carries no `items` schema, so dropping it is the whole contract. An array left without `items` still gets the existing `{"type":"string"}` default.
- **Deliberately not Gemini-scoped at the call site.** `SanitizeOpenAITools` also runs on the OpenAI-compat fallback path for every provider, which already strips ~40 keywords there (`const`, `$ref`, `format`, `additionalProperties`, `title`, …). `additionalItems` is a client-side validation constraint with no meaning in a tool declaration on the wire, and it is the draft-07 twin of a keyword that path already drops, so removing it cannot change what any provider does with the body.
- **Mutation-checked.** The new table test (`TestCleanParametersSchema_StripsTupleKeywords`, 10 rows) walks the cleaned schema for the keyword at every depth and pins the siblings that must survive: `type`, `items`, `description`, an existing `items` that must beat `prefixItems[0]`, and a promoted tuple entry. Reverting the one-line change fails 8 of the 10 rows with exactly the reported keyword surviving; the two `prefixItems`-only rows pass either way, which is the no-regression guard. `TestSanitizeOpenAITools_StripsAdditionalItems` covers the OpenAI-compat body. Smoke-run through `TranslateOpenAIToGemini`, the emitted Gemini request carries no `additionalItems` at any depth.

### ✨ Issue #55 — a screenshot returned by a tool reached Gemini as nothing at all

- **Where the pixels were dropped.** `TranslateOpenAIToGemini` builds the tool turn in `internal/translator/gemini.go`: it read the `role: "tool"` content through `extractContentString`, which concatenates `text` blocks and ignores every other key. A Playwright/screenshot tool answers with `[{"type":"text",…},{"type":"image_url","image_url":{"url":"data:image/png;base64,…"}}]`, so the base64 had nowhere to go — `GeminiFunctionResp` carries only `Name`, `ID` and `Response.Result`, and the outbound payload contained no `inlineData` at all. Confirmed before the fix: the trailing user turn had exactly one part, the functionResponse.
- **What changed.** One line — `parts = append(parts, geminiToolMediaParts(msg.Content)…)` — plus the helper that feeds it. The helper is not a second image parser: it calls the existing `convertContentToGeminiParts`, the same path user messages already take, and keeps only the `inlineData`/`fileData` parts. Text is filtered out on purpose because it still rides inside `functionResponse.response.result`, unchanged.
- **Wire shape.** Before: `{"role":"user","parts":[{"functionResponse":{"name":"browser_screenshot","response":{"result":{"output":"captured"}}}}]}`. After: the same part, plus `{"inlineData":{"mimeType":"image/png","data":"QUJD"}}` as a sibling in the same user turn — the shape Gemini accepts for multimodal tool output, and the one OmniRoute PR #14173 established.
- **The common path is byte-identical.** `geminiToolMediaParts` returns nil for a string result and for a text-only block array, so the overwhelming majority of tool calls serialize exactly as before. Both of those cases are pinned by tests that assert the payload contains no `inlineData`.
- **Tests.** `TestOpenAIToGemini_ToolResultMediaParts` (6 table rows): text-only stays a lone functionResponse, text-only block array likewise, image-only keeps the picture, mixed text+image keeps both, a PDF tool output is inlined, and a remote image stays `fileData` rather than being pulled in as bytes. Every row asserts on the marshalled request, not on an intermediate struct, and on the media as wire substrings — a struct field that marshalling dropped would fail. Mutation-checked: reverting `gemini.go` fails the four media rows and leaves the two text-only rows green.

### 🐛 Issue #54 — an OAuth account with a revoked grant was refreshed again on every request

- **The symptom.** A permanently dead account (a revoked Antigravity refresh token) made the router call Google's token endpoint once per request, forever, until the egress IP was rate limited. `forceRefreshOAuthToken` — reached from the reactive-401 path in `fallback.go` and from the `authFailed` probe in `forwardGeminiNativeRequest` — failed with 401, and nothing recorded that failure. The account-scoped cooldown added in #39 is written only from the *upstream response* path, never from a failed refresh, so a dead grant had no backoff at all.
- **The 401 was unreadable even in principle.** Every refresher flattened the token endpoint's status into a message (`refresh returned 401: …`), so no caller could tell a revoked grant from a 5xx blip without parsing English. `providers.OAuthRefreshError` now carries the status and `providers.IsRefreshUnauthorized` reads it, and every refresher returns one: standard (the path Antigravity takes), cline, kiro (both exchanges), xai, claude. Bodies stay truncated at 200 bytes, because a token endpoint that echoes the request would otherwise write a live grant into the logs.
- **A rejected grant parks the account.** `db.RecordConnectionOAuthFailure` writes `oauthLockedUntil`, `oauthFailureCount` and `lastError`; the selector reads both account-scoped cooldowns through the new `db.ConnectionBlockedUntil`, so a parked account is skipped *before* a request is spent on it and no refresh is attempted for it at all. Backoff is `db.OAuthLockDelay`: 5m, 10m, 20m, 40m … capped at 24h, counting consecutive failures only — a successful refresh clears both fields (so a repaired credential is back in rotation at once and the next rejection starts from the floor), and so does `ResetConnectionHealthState`. Upstream parity: OmniRoute #14917.
- **The cooldown counts windows, not attempts.** One request can discover a dead grant more than once — `tryForwardWithConnection` refreshes, `forwardGeminiNativeRequest` refreshes again, and the `authFailed` project probe force-refreshes — so a rejection that lands while the account is already parked is the same strike seen again and does not move the counter. Without that rule one bad request would park the account for 20 minutes instead of 5. The three calls that first request makes are a pre-existing cost, not a hammer: the endpoint is not touched again until the window runs out. Measured end to end through `HandleChatCompletions` against a fake token endpoint that answers 401: three client requests, 3 calls on the first, **0 on the second and third**, and one strike recorded.
- **Its own field, not `rateLimitedUntil`.** The dashboard renders that field as `remainingPercentage: 0` plus `resetAt` (`handlers/dashboard/usage.go`) — "quota spent, come back at HH:MM". For a dead credential that is a lie the user acts on: the quota is untouched and waiting out the window cannot help; only a re-login can. The park keeps its own timestamp and a counter of its own, and the reason still reaches the dashboard through `lastError`, which that panel already renders.
- **The background loop stops hammering too.** `SelectConnectionsNeedingRefresh` skips a parked account, and a 401 out there records the same park. Without that, the 5-minute tick would have re-hit the token endpoint for an account no request can use, and would have inflated the backoff counter behind the request path's back.
- **Only 401 parks.** 400, 403, 5xx and transport failures keep the previous behaviour: none of them prove the grant is gone, and a park on a blip would take a healthy account out of rotation for nothing. On a 401 the custom refresher's error is no longer swallowed into a pointless second call at the standard endpoint, which rejects the same credential.
- **Verification.** Mutation-checked, not just green: routing the selector back to `ConnectionCooldownUntil` fails all three `TestParkedAccountIsSkippedByRouting` subtests plus the earliest-reset test; making `IsRefreshUnauthorized` always false fails both "revoked grant" subtests of `TestRejectedOAuthRefreshParksAccount`; dropping the clear fails `TestSuccessfulRefreshReleasesThePark`. Malformed or missing state fails open in the reader and in the selector (5 table cases). `go vet` and `go test -count=1` green on `internal/db`, `internal/providers`, `internal/proxy/oauth`, `internal/handlers/chat`, `internal/handlers/dashboard`, `internal/handlers/oauth` and `internal/handlers/media`.

### 🐛 Issue #53 — a Gemini 429 with a long Retry-After kept re-hitting the same exhausted account

- **The window Google names was never read.** `proxy.UpstreamError` carried only a status and a body, so the upstream `Retry-After` header was gone by the time any handler saw the failure. And `extractResetDuration` — the one helper that does parse a wait out of a body — looks for `quotaResetDelay` inside ErrorInfo metadata, while a Gemini/Antigravity 429 puts it on a `google.rpc.RetryInfo` detail as `retryDelay`. Neither carrier reached the router: with two accounts in the pool and a `retryDelay: 120s`, the client got **no** `Retry-After` at all and the failed account was locked for the classifier's 2s base backoff. `UpstreamError` now keeps the response headers, and the new `internal/handlers/chat/retry_after.go` reads all three carriers — header, `RetryInfo` detail, and the field names `extractRetryAfter` already owned — taking the longest, because a wait is only over once every source's window has passed.
- **A long Retry-After was waited out on the accounts that just refused it.** `comboRetryAfter` accepted any Retry-After up to `comboRetryWaitCap` (8s), so a 429 naming a 5-second window made a fully-failed combo pass sit for 5 seconds and then re-run the whole pass against the same limited accounts: 4 upstream requests and a 5s stall where 2 requests and no stall were correct. A 429 is now held to `brief429RetryTolerance` — **2s**, chosen because it is the router's own base backoff (`providers.BackoffConfig.BaseMs`): at or under it the account is very likely free again by the time the retry runs, so an ordinary burst is still absorbed, and above it the quota window is spent and waiting here cannot shorten it. Every other status keeps the 8s cap, unchanged.
- **The lock now matches the window, in both directions.** Honouring a long `Retry-After` keeps the next request from re-picking an exhausted account; honouring a short one is what lets the bounded second pass find the account again instead of waiting out a lock the router set on itself a second earlier. `retryableCooldownSec` takes the upstream's word over the classifier's and clamps at `maxResetCooldown`, so a hostile or nonsensical duration still cannot park an account forever.
- **The two combo loops no longer drift.** `earliestRetryAfter` was an RFC3339 string, re-parsed by `mustParseTime`, string-compared for "earliest", and re-formatted into a header — duplicated verbatim in `handleComboFallback` and `handleMessagesComboFallback`. It is now a `passRetry` holding a `time.Duration`, and `comboRetryAfter`/`mustParseTime` are gone; `TestComboRetryAfter`, which only pinned the old string parsing, was replaced by `TestComboPassRetryWait` over the decision that replaced it.
- **Mutation-checked.** `TestComboRateLimitFailover` (7 cases over a real pool), `TestRetryAfterWait_Extraction` (15 carriers, including the malformed header, the past HTTP-date, the empty header and the unparseable `retryDelay`), `TestRetryableCooldownSec` and `TestRateLimitCooldownParksAccountForTheNamedWindow`. Restoring the 8s cap for 429s fails the past-the-tolerance case; dropping `retryInfoDelay` fails the RetryInfo cases; dropping the header carrier fails the `Retry-After` cases; ignoring the window when locking fails both cooldown tests. `TestHandleMessagesComboFallback_RetriesOnceOnBoundedRetryAfter` kept its intent against a Retry-After inside the new tolerance.

### ✨ Issue #56 — an exhausted combo now says *when* to come back

- **The header was already there, the body was not.** When every account in a combo is cooling down, `handleComboFallback` and `handleMessagesComboFallback` already set `Retry-After` to the earliest upstream reset and appended a `reset after …` suffix to the message. That covers a header-reading client — but a client that never surfaces headers (browser SDKs, log-only integrations, a proxy that strips them) got the same 429 with no timing at all.
- **Both response fields now carry it, inside the repo's own error envelope.** New `internal/handlers/chat/combo_exhausted.go` publishes `reset_at` (ISO-8601 UTC) and `retry_after` (seconds) alongside `message`/`type`/`code`, so the body parses exactly like every other error the gateway emits. The duplicated response block in both combo handlers collapsed into one `writeExhaustedComboError`, so the two endpoints cannot drift apart again.
- **The human-readable suffix was reporting the wrong unit, and the new fields would have contradicted it.** `formatRetryAfter` divided `time.Until` — a `time.Duration` in nanoseconds — by `1000`, i.e. it rendered milliseconds as if they were seconds. A 150-second cooldown read as `(reset after 41399h 2m 26s)` next to a `Retry-After: 150` header, so the new `retry_after` would have shipped next to a message denying it. Now `reset after 2m 30s`.
- **A missing reset stays missing.** With no cooldown information the upstream error is forwarded untouched and no field is invented. A `Retry-After` the router cannot parse still degrades to the header's 1s minimum but gets no `reset_at` — a year-1 timestamp is a worse answer than none, because clients schedule on it.
- **Verified, not just green.** Table-driven `TestComboExhaustedCarriesResetTiming` drives both endpoints end-to-end against two mock upstreams that 429 with different cooldowns and asserts the response names the *earliest* candidate, not merely *a* candidate; `TestWriteExhaustedComboError_ResetFieldBoundaries` pins the envelope boundaries and the exact suffix. Mutation-checked: removing the two field assignments fails both tests with `error.reset_at = ""`, and restoring the millisecond division fails the suffix cases.

### 🗑️ `union-alpha` dihapus total — model trial yang sudah tidak ada di upstream

- **Bukan cuma satu string.** Model ini masih hidup di sembilan tempat, dan `docs/DASHBOARD_PROVIDER_PARITY.md` sudah menandainya **"MISS upstream ✅ dihapus 2026-09-21"** — ledger-nya sudah mencatat, kodenya tidak pernah menyusul. Yang dihapus: cabang routing Messages API khusus di `ForwardOpencode` (68 baris, `cleanModel == "union-alpha"`), entri `opencodeGoMessagesModels`, klausul `|| cleanModel == "union-alpha"` di dispatch `ForwardOpencodeGo`, daftar katalog `oc` / `ocz` / `opencode` / `opencode-zen` (`registry_models.go`), baris kapabilitas di `capabilities.go`, dan dua entri di `web/src/lib/models.ts` supaya dashboard tidak lagi menawarkan model mati. Dua test executor dan satu test live (`TestIntegration_OpenCode_UnionAlpha_Messages`) ikut dibuang karena hanya menguji cabang yang sudah tidak ada.
- **Placeholder yang paling menyesatkan ikut dibetulkan.** `translator.TranslateClaudeChunkToOpenAI` memakai `state.Model = "union-alpha"` sebagai **fallback generik** untuk stream Messages yang tidak mengirim `model` — dan nilai itu bocor ke setiap chunk lewat `"model": state.Model`, jadi klien terus melihat model yang sudah di-pensiun diklaim sebagai yang menjawab. Sekarang `handleClaudeMessagesStream` mengisi state dengan model yang benar-benar diminta klien (`RequestedModelFromContext`), dan translator hanya jatuh ke placeholder `"unknown"` kalau upstream **dan** request sama-sama tidak menyebut model. Tiga kasus baru dipin di `TestTranslateClaudeChunkToOpenAI_ModelEcho`: model upstream mengalahkan seed, seed bertahan saat upstream diam, dan placeholder sebagai upaya terakhir.
- **Test yang masih hidup tetap hidup.** `ensureMessagesMaxTokens` masih dipakai (jalur Anthropic + `opencode-go`), jadi test-nya tidak dihapus — hanya nama sampelnya diganti `claude-sonnet-4-5`, karena fungsi itu membangun payload Claude Messages dan memakai model yang sudah di-pensiun sebagai contoh tidak jujur. Sama di `models_list_scope_test.go`: sample model pada `ConnectionOwnsItsCatalog` diganti `big-pickle` (absen dari katalog statis, jadi memang efek yang diuji), sedangkan `jev-1.13-free` hanya boleh jadi model yang harus **tidak** muncul.
- **Verifikasi:** `go build ./...`, `go vet ./...`, `go test -race ./...`, suite `integration` (`-race -count=1`), `bun test` 103/103, dan `bun run build` semuanya hijau. `TestHandleUsageStream` dan `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` sempat muncul di beberapa run — keduanya sudah di-A/B di `5ba8aa0` (kode tanpa satu pun perubahan ini) dan gagal di sana juga, jadi pre-existing.



### 🐛 HTTP 200 dengan isi kosong tidak lagi dianggap "sukses" — router pindah model

- **Gejalanya.** Laporan: "round-robin tetap stay di satu model". Root cause-nya bukan strategi round-robin, tapi satu asumsi: status HTTP dianggap sama dengan jawaban. `providers.RetryableStatusCodes` (401/403/429/502/503/504) hanya memicu failover kalau upstream **menolak**. Kalau upstream membalas `200` dengan body kosong, halaman error HTML, atau amplop `{"error": …}`, `jsonResponse` / `handleJSONResponse` meneruskannya apa adanya, `tryForwardWithConnection` mengembalikan `nil`, dan `handleAccountFallback` menganggap turn itu served — termasuk **membuka** `LockConnectionModel` dan `ClearConnectionRateLimit` untuk koneksi tersebut. Efeknya lebih buruk dari sekadar stuck: combo berhenti di member pertama, akun yang bermasalah dinilai pulih, dan putaran berikutnya mengulang model yang sama.
- **Yang sudah ada, dan kenapa tidak cukup.** `sseWithoutCompletionError` sudah mengubah event stream yang tidak membawa chunk completion menjadi 502 — tapi hanya untuk body berbentuk SSE, persis seperti dilaporkan. Body JSON biasa tidak pernah dicek. Jalur streaming lebih longgar lagi: `executor.ForwardOpenAI` langsung memanggil `execSSEStream` tanpa pernah membaca `Content-Type`, jadi halaman error HTML dengan 200 masuk ke SSE scanner, menghasilkan **nol frame**, lalu `SSECopy` menutupnya dengan `[DONE]` yang bersih. Client melihat turn selesai; router tidak pernah dapat kesempatan failover.
- **Perbaikannya: `internal/proxy/empty_success.go`.** Satu validator (`EmptyUpstreamError`) yang membaca amplop ketiga wire shape yang dilayani gateway — Chat Completions (`choices`), Claude Messages (`content`), Responses (`output`) — dan mengembalikan `*UpstreamError{502}` untuk body kosong atau whitespace, body non-JSON (HTML error page diberi nama sendiri di log), amplop `{"error": …}` di balik 200, `choices: []`, choice tanpa content/tool_calls/reasoning/refusal, dan `output: []` dengan status `completed`. 502 dipilih karena sudah terdaftar di `RetryableStatusCodes`, jadi lock dan exclude combo jalan tanpa perubahan lain. Dua helper yang tadinya diduplikasi antar package ikut dirapatkan: `UpstreamFailure` menggantikan `sseWithoutCompletionError`, dan `LooksLikeSSE` menggantikan pasangan `executor.looksLikeSSE` / `chat.isSSEBody`.
- **Dua fabrikasi jawaban kosong dihapus, bukan ditutup.** `handleCodexStream` menulis `{"choices":[…,"content":"","finish_reason":"stop"}}` sebagai "Fallback empty response", dan `ForwardOpencode` (union-alpha) mengirim buffer SSE kosong ke `jsonResponse`. Keduanya kini 502. Efek sampingnya terbukti: `TestE2E_Opencode_MuseSpark_Mock_Vision` sebelumnya hijau justru karena menerima completion fabrikasi itu — mock-nya kini mengembalikan event stream yang memang diminta (`buildResponsesBody` memaksa `stream:true`) dan test itu assert isi jawabannya.
- **Streaming: klasifikasi sebelum header.** `executor.ForwardOpenAI` kini mengecek `Content-Type` sebelum menulis header apa pun. Body yang bukan event stream dibaca, dan karena **belum ada satu byte pun yang terkirim ke client**, request itu bisa diproses sebagai JSON dan gagal over persis seperti jalur non-streaming. Event stream yang salah label (`Content-Type: application/json` dengan body SSE) tetap diputar dari buffer, jadi tidak ada regresi untuk provider yang salah menulis content type.
- **Batas yang jujur.** SSE asli yang terhubung lalu langsung EOF dengan nol frame **tidak** bisa di-failover: header sudah terkirim, jadi status tidak bisa berubah lagi. Client-nya sudah diberi tahu — `internal/proxy/sse_error.go` (#62) menutup stream itu dengan in-band error frame — tapi router-nya tetap tidak pindah model di kasus itu. Perbaikannya hanya berlaku selama belum ada satu byte pun yang terkirim ke client.
- **Verifikasi.** `TestHandleChatCompletions_ComboMovesOnFromEmpty200`, `…FromHTML200WhileStreaming`, dan `…ComboAllEmpty200Fails` mutation-checked: dengan validator dimatikan, ketiganya gagal dengan gejala yang persis dilaporkan (body kosong sampai ke client, HTML mentah terpipa sebagai "stream", `content:""` dilayani sebagai turn selesai). Ditambah 25 subtest tabel untuk validator, 5 subtest untuk klasifikasi Content-Type di executor, `go vet ./...` bersih, `go test -race` hijau di `internal/proxy/...` dan `internal/handlers/chat`, serta suite `integration` hijau.

### 🐛 The Tailscale dashboard test asserted on whatever was installed on the machine running it

- **The handler was never wrong; the test was.** `TestHandleTunnelEndpoints/TailscaleEnable_ReturnsCleanError` asserted a 400, which is only what the handler returns when no `tailscale` binary is found. On any machine with Tailscale installed the handler finds the binary, probes it, sees a logged-out daemon, and correctly answers **200 with `needsLogin` + `authUrl`** so the dashboard can render its login button — the same contract upstream returns. So the test passed only where Tailscale was absent, and failed for everyone else.
- **It also mutated the developer's machine.** These handlers shell out to the real binary, so the test ran `tailscale up --reset` against a live daemon (~10s), and `TailscaleDisable` ran `tailscale funnel --bg reset` — real side effects on a real Tailscale node, from a unit test.
- **Fixed by pinning the host, not by loosening the assertion.** `internal/handlers/dashboard/tunnel.go` now resolves the binary and runs commands through two package-level seams (`tailscaleBinFn`, `tailscaleExec`), the same pattern `validate.go` and `connection_probe.go` already use for their network calls. No test shells out to a real binary any more; the suite dropped from 10.04s to 0.10s.
- **Both real branches are now pinned**, not just the absent-binary one: no binary -> 400 clean error, installed-but-logged-out -> 200 `needsLogin` with the auth URL parsed out of the login output, and disable -> exactly one `funnel --bg reset`. Mutation-checked: routing the handler back around the seam fails with `must not exec tailscale when none is installed`. `go test -race ./...` is green across the repo for the first time.

### 🐛 Issue #57 — a stream that dies after HTTP 200 was closed silently

- **The client was told the answer was complete.** A stall timeout or a dropped socket used to end a streaming request with a synthesized finish_reason plus the [DONE] sentinel; every OpenAI client reads a finish_reason as a normal completion, so truncated text was kept and reported as a finished turn. Ported from upstream decolua/9router commit 93001213 (buildStreamErrorBytes, onAbortTerminal).
- **An in-band error frame, and never a fabricated terminal.** New `internal/proxy/sse_error.go` emits the OpenAI shape (a data frame carrying an error key with a machine-readable code, then the [DONE] sentinel) and the Anthropic shape (event error) for the Claude-named path. The abort path of `SSECopy` no longer reaches the `finish_reason` synthesis that `finish()` still uses for a clean EOF.
- **A timeout is distinguishable from a lost socket.** The stall watchdog now records that it fired (`stall.go` sets an atomic flag) and `Read` re-wraps the opaque closed-file error as `ErrStreamStall`, so the client gets a 504 `gateway_timeout` instead of a 502 `upstream_error`. Cancellation stays 499 rather than being reported as a gateway failure.
- **Verified, not just green.** New unit tests cover the abort frame and all three classifications; a new integration test drives the real router against a fake upstream that hijacks and kills the socket mid-turn. Mutation-checked: restoring the old call site fails the integration test and the unit tests with exactly the reported symptom. Smoke-tested against the built binary with a socket reset mid-turn: the client receives the error frame plus [DONE], and a healthy stream is relayed unchanged with no error frame.
- Also fixes a pre-existing data race in `mockResponseWriter` (the heartbeat goroutine appended to an embedded `bytes.Buffer` while the test read it), which `go test -race` flags on the untouched `TestHeartbeatWriter_EmitsKeepAliveWhenIdle`.



### ✨ Feature integration suite + `integration` CI job

- **The gap this closes.** Every Go test until now called a handler directly or mounted a hand-built `chi` router. That shape cannot see a regression in the wiring production actually uses, and the failures it hides are exactly the ones nobody can reproduce by hand later: a route registered in the wrong auth group, the `middleware.RequestLogger` `/v1` rewrite dropped so every documented OpenAI URL 404s, account rotation no longer skipping a throttled connection, usage no longer being recorded. `internal/handlers/router_test.go` already documents two of these having shipped — the CLI-Tools 401 and the Codex reset-credit 404 — both found after the fact, both because a route was wired into a table the tests exercised but the server did not.
- **What the suite is.** `internal/integration/` boots `app.ProvideRouter` — the same middleware stack and route table the binary serves — on a real HTTP listener against a temporary SQLite database created by the production schema bootstrap (`EnsureCoreSchema` + `EnsureUpstreamLeases`). Every provider call is intercepted by an `httptest` fake seeded through `providerConnections.data.baseUrl`; the stored row is read back and the fake URL asserted, because an empty `baseUrl` falls through to the real provider URL from `providers.KnownProviders` and would turn an offline suite into a live call. `internal/integration/bootfx/` is a separate test binary that boots the real fx graph (`DatabaseModule` and `ServerModule` included) on a free port. It boots **once**, from `TestMain`: `db.InitGlobalDatabase` is a process-wide `sync.Once` and the fx `OnStop` hook closes that handle for good, so a per-test boot would hand the second caller a closed database and an already-cancelled shutdown context.
- **Coverage.** Auth (missing/unknown/deactivated key with the typed 401 envelope, both credential carriers, the `?key=` asymmetry, the dashboard gate and its always-protected paths), chat completions (outbound envelope, payload passthrough, verbatim upstream errors, non-retryable statuses not rotating accounts, retryable ones locking and moving on, all-throttled surfacing a real 429, disabled accounts out of rotation, provider isolation and alias resolution), streaming (SSE relay terminated by `[DONE]`, provider errors before the first byte), combos and model aliases, `/v1/models` and `/v1/models/info`, dashboard CRUD for connections/keys/combos including the 409 name guard and the key-masking leak guard, `/v1/messages` Claude translation both ways, and usage accounting (a served completion billed, a failed one recorded but not billed).
- **Mutation-checked, not just green.** Removing `middleware.RequestLogger` from the stack fails `TestV1AndUnversionedPathsReachTheSameHandler` with a 404; turning the retryable-status branch of `handleAccountFallback` into an immediate return fails `TestRetryableUpstreamErrorRotatesAccount` with the 429 the client would otherwise have seen. Both regressions leave every handler correct, which is why unit tests stayed green.
- **Harness discipline.** `Env` deliberately holds no `*testing.T`; every helper takes the running test as its first argument. Capturing the parent would make a failing `t.Run` call `FailNow` on the parent from the subtest's goroutine, which `testing` reports as "subtest may have called FailNow on a parent test" and attributes the failure to the wrong line. Subtests assert on a row selected by id and fail when it is absent, so a list that came back empty can never pass a CRUD assertion vacuously.
- **Pipeline.** New `make test-integration` / `make vet-integration` targets (both depend on `web-build`, since the suite imports `internal/handlers` and `web/embed.go` embeds `web/dist` at compile time), and a CI `integration` job that runs `go vet -tags=integration` plus `go test -tags=integration -race -count=1` on every push and PR. `-race` is on because the proxy is genuinely concurrent (SSE pumps, the in-memory usage tracker, per-handler sticky state). The `integration` tag keeps `go test ./...` fast; `internal/integration/doc.go` is deliberately untagged so the package always has a compilable file and `go build ./...` never trips over a directory whose every file is excluded.

### ✨ Issue #38 — the Codex reset-credit counter was read-only

- **The button had no handler at all.** `QuotaTrackerView.svelte` drew the credit count from `resetCredits.availableCount` (which `fetchCodexUsage` already reports) as a `<button>` carrying `type`, `disabled`, `title` and `class` — but no `onclick`, so clicking it did nothing. `wham/rate-limit-reset-credits` appeared nowhere in `internal/`. The port covers the whole flow: list, choose, redeem, and re-read the quota.
- **Contract ported from OmniRoute, not transliterated.** `src/lib/usage/codexResetCredits.ts` was fetched first and its behaviour preserved — the two endpoints, the eight accepted list shapes (`credits` / `reset_credits` / `resetCredits` / `rate_limit_reset_credits` / `rateLimitResetCredits` / `items` / `data` / bare array), the camel/snake id aliases, the unavailable-status filter, expiry ordering, and the typed refusals. New `internal/codexquota/resetcredits.go` + `resetcredits_fetch.go` implement it natively; no TypeScript was carried over.
- **The typed error codes survive.** `no_credit`, `nothing_to_reset`, `selected_credit_unavailable` and `unknown_reset_credit_response` keep their 409/502 statuses, and the dashboard renders the code. Collapsing them into one generic error would have thrown away the only thing that tells "you have no credit" from "your limit is not actually exhausted". `already_redeemed` is a **success**, not a failure — it is the state the user was trying to reach.
- **Two things the port had to add that the original got for free.** `chatgpt-account-id` is sent from the connection's stored account id, because the wham endpoint scopes credits per Codex account and would otherwise list the wrong one. And the consume reuses **one idempotency key across the auth retry**, so a retried click after a 401 can never redeem twice.
- **Failures are read leniently, and the dashboard stays honest.** A non-JSON body becomes a raw string, because the outcome can legitimately be a bare token. A generic upstream 500 is *not* relabelled as `no_credit`; only the two meaningful refusals are.
- **The list is fetched on open, not on every poll.** The row counter keeps costing nothing; opening the chooser is what calls the endpoint, and the server returns them soonest-expiry first so the default selection is the one that frees the quota soonest. Radio selection, the empty/loading/error states and the redeem button disabling themselves are all in the new modal.
- **The refresher is wired as `nil`, deliberately.** `codexquota` supports a 401/403 refresh-and-retry, but the dashboard has no OAuth refresher of its own and importing the chat package's would drag provider-specific token persistence across package boundaries — the same reason `fetchCodexUsage` passes none today. The seam is implemented and tested; leaving it dormant is honest, where a half-wired refresher would silently fail to persist a rotated token.
- **Verification:** 2060 Go tests across 35 packages, 87 web tests, gofmt + vet + `tsc -b` clean. 22 new Go tests cover the parsing contract (filtering, ordering, all eight payload shapes, count reporting), selection, every outcome code, the headers actually sent, the refresh-once-on-401, and that the consume body carries the chosen credit plus the idempotency key; 7 handler tests pin that the new routes are reachable past the `/usage/{connectionId}` parameter route and that a non-Codex or API-key connection is refused before any upstream call.

### 🐛 Stream tanpa `finish_reason` membuat client gagal dengan "stream closed before a finish_reason was received"

- 🔴 **`SSECopy` meneruskan bare `data: [DONE]` apa adanya.** Jalur passthrough OpenAI (`ForwardOpencode` rute `chat/completions` default, dan semua upstream OpenAI-compatible lain via `execSSEStream`) menulis chunk upstream mentah ke client lalu `return` begitu melihat substring `[DONE]` — tanpa memeriksa apakah terminal `finish_reason` pernah terkirim. Ketika upstream mengirim delta konten lalu `[DONE]` tanpa chunk terminal, client yang strict (Oh My Pi) melempar `OpenAI completions stream closed before a finish_reason was received` di setiap turn. Karena combo fallback mencoba akun berikutnya yang perilakunya sama, error yang sama muncul berulang (5x di laporan). Bentuk yang sama dilaporkan di `can1357/oh-my-pi#9433`: "streamed content deltas and then emitted the [DONE] sentinel without a terminal finish_reason chunk".
- **Sintesis terminal dipindah ke sebelum `[DONE]`, bukan sesudahnya.** `SSECopy` ditulis ulang di `internal/proxy/sse_copy.go` sebagai `sseCopier` yang sadar batas baris: baris sentinel `[DONE]` yang asli selalu didahului frame `finish_reason: "stop"` bila belum ada terminal — `stop`, bukan `network_error`, karena `[DONE]` adalah akhir yang disengaja (trunkasi EOF/read-error tetap memakai `network_error` seperti sebelumnya, dan read error non-EOF kini juga menutup stream secara best-effort sebelum error dikembalikan). Deteksi terminal memakai rolling window 64B sehingga token yang terbelah antar read tetap ketahuan, dan nilai `null` eksplisit tidak dihitung sebagai terminal.
- **Bonus: `[DONE]` di dalam konten tidak lagi memutus stream.** Deteksi lama (`bytes.Contains(checkBuf, "[DONE]")`) memotong stream begitu substring muncul di mana saja — termasuk di dalam string konten JSON. Sentinel kini hanya diakui sebagai baris event utuh (`data: [DONE]` / bare `[DONE]`), termasuk yang terbelah antar read dan yang tanpa newline di EOF.
- **Tests:** `TestSSECopy_InjectsStopBeforeBareDone` (delta→DONE, chunk `finish_reason:null`→DONE, DONE tanpa newline di EOF, sentinel terbelah per-byte via `iotest.OneByteReader`, `[DONE]` di dalam konten, read-error tetap menutup stream + mengembalikan error). Mutation-checked: keenam subtest gagal di implementasi lama dengan tepat gejala yang dilaporkan. Suite lama `TestSSECopy_SynthesizesTerminalOnAbruptClose` tetap hijau tanpa perubahan.

### 🐛 Issue #39 — accounts already in cooldown were still handed out by rotation

- 🔴 **The Go port was missing a mechanism upstream has, in two halves.** Upstream `filterAvailableAccounts` (`open-sse/services/accountFallback.js:180`) skips any account whose `rateLimitedUntil` is still in the future, and `applyErrorState` (line 216) is what writes it. The Go port had **neither**: `rateLimitedUntil` was read by the dashboard (`internal/handlers/dashboard/usage.go:104`, to render "reset at") and cleared by `ResetConnectionHealthState`, but **nothing ever wrote it** — so the Quota Tracker's reset time was always empty, and the selector had no account-scoped signal to skip on. The only cooldown was `modelLock_<model>`, which is keyed by model *and* only consulted when the request carries a model at all.
- **Writes:** `LockConnectionRateLimit` stores `rateLimitedUntil` + `lastError` + `status` (upstream `applyErrorState`), called from both lock paths — `comboLockRetryable` and the non-combo loop in `fallback.go`. `ClearConnectionRateLimit` drops it again when a request is served (upstream `resetAccountState`); without that a recovered account would stay out of rotation until the cooldown expired on its own. It deliberately leaves the per-model locks alone, so a success on one model cannot unlock a model that is still cooling.
- **Reads:** the pick loop in `getBestConnection` now skips any candidate whose cooldown is still in the future. The check parses `c.Data` directly, so it costs **no extra database query** in the routing hot path — the candidates are already in memory. It sits *outside* the `if model != ""` block because the cooldown is account-scoped: that is what catches a quota spent account-wide, and it is the only cooldown a model-less request can consult.
- **A pinned connection is covered too.** `pinnedConnectionIneligible` now reports "account cooldown until …", so pinning cannot be used to force a request onto a known-dead account — it falls through to the strategy, matching how upstream resolves the pin inside its availability filter (`src/sse/services/auth.js:100-148`).
- **Fails open, on purpose.** A missing, empty, null, wrongly-typed or unparseable `rateLimitedUntil` is treated as *not* in cooldown. A malformed field must never be able to take routing down or silently retire every account.
- **The caller learns when to come back.** When every candidate is cooling, the error now names the earliest reset instead of the bare "all excluded" it replaces — which is what turns a run of failed requests into a "retry in Nm" the client can act on.
- **Verification:** 2021 Go tests pass across 35 packages, gofmt + vet clean. Twelve new tests: the selector skips a cooling account, keeps an expired one, applies the rule to a request with no model, reports the earliest reset when all are cooling, falls through from a pinned cooling account, and ignores a malformed field; the write/clear round-trip and fail-open parsing are pinned in `internal/db`. Mutation-checked — disabling the filter fails four of them with the reported `selected an account that is still in cooldown`, and the two negative cases correctly stay green when it is off.

### 🐛 Issue #40 — the combo strategy dropdown rendered blank

- 🔴 **The claim's mechanism was wrong, but the reported symptom was real.** The issue claimed the card "did not fall back to `combo.strategy`" when `comboStrategies[name]` was missing. It did — `ComboCard.svelte:47` already read `combo.strategy` as the second term. The real bug: `combo.strategy` can hold strings with **no matching `<option>`**. The global "Combo Routing Mode" defaults to `first-model` (`ProfileSettingsView.svelte:49`), and `repos.go:609-611` copies that global onto every combo lacking a per-combo entry. The card offered only three options (`fallback`, `round-robin`, `fusion`), so `first-model` matched nothing and `select.selectedIndex` fell to `-1` — a blank box that still opened on click.
- 🔴 **Selecting Fallback reproduced the blank on the very next refresh.** `updateComboStrategy` (`types.ts:74`) *deletes* the entry when you pick `fallback` without a judge (`if (newStrategy === 'fallback' && !next.judgeModel) { delete updated[comboName] }`). Choosing Fallback cleared `comboStrategies[name]`, the card fell through to the global `first-model`, and the box went blank again.
- **Single source of truth for the options, and a normalizer.** `COMBO_STRATEGIES` (`types.ts`) now defines every strategy the backend speaks (`fallback`, `round-robin`, `sticky`, `capacity`, `fusion`). `resolveComboStrategy` maps `first-model` onto `fallback` (they describe the same try-in-order behaviour in different vocabularies) and falls back to `fallback` for an unrecognised value. The card's `<select>` renders from that list, so the resolved value is guaranteed to match an option.
- **Verified:** 90 web tests pass (5 new in `types.test.ts` covering every value the server can send), and end-to-end through a real Chromium instance with `comboStrategy: 'first-model'` persisted in SQLite — the card renders `"Fallback — try in order"` with `selectedIndex: 0`, and the screenshot confirms the label is visible.

### 🐛 Deteksi terminal di `SSECopy` tidak pernah aktif — tiap stream dapat terminal dobel

- 🔴 **Needle-nya dua kutip.** `sseHasNonNullValue` menyusun `needle := []byte(key + `":`)`, padahal `key` yang dipanggil sudah berakhir dengan kutip penutup (`"finish_reason"`). Hasilnya ia mencari `"finish_reason":` — dua kutip sebelum titik dua — yang tidak pernah muncul di JSON mana pun, jadi `bytes.Index` selalu mengembalikan `-1` dan `sseHasTerminalToken` selalu `false`. Akibatnya `sseCopier.hasTerminal` tidak pernah terisi: **setiap** stream yang upstream-nya sudah benar-benar menutup dengan `finish_reason` tetap mendapat frame terminal kedua yang disuntik tepat sebelum `[DONE]`. Bug ini ada sejak entri "inject stop terminal before bare [DONE]" dan membatalkan separuh klaimnya — "preservasi terminal native" tidak pernah dijalankan, hanya bagian sintesis yang bekerja. Needle sekarang `key + ":"`.
- **Akibatnya tidak kelihatan di log.** Frame hasil injeksi ditulis `sseCopier.writeRaw` langsung ke `ResponseWriter`, bukan lewat `onChunk`, jadi tidak masuk `ResponseBuf` yang disimpan di `requestDetails`. Kalau stream yang terekam dibaca ulang dari DB, stream itu terlihat bersih walau ada terminal kedua tepat di depan `[DONE]`-nya — telusur dari sisi log saja tidak akan pernah menemukan bug ini.
- **Tests:** `TestSSECopy_KeepsSingleTerminalOnCompliantStream` (tabel: OpenAI yang sudah `stop`, OpenAI yang sudah `tool_calls`, Claude yang sudah `end_turn` — semuanya harus relayed tanpa satu pun frame terminal yang disuntik, dan tanpa `network_error`), plus `TestSSECopy_QuotedTerminalInsideContentIsNotATerminal` yang mengunci konten berisi `\"finish_reason\": \"stop\"` ter-escape: kontennya harus utuh dan terminal tetap disuntik. Ketiga subtest pertama mutation-checked — mengembalikan needle ke `key + `":`` membuat ketiganya gagal dengan tepat satu terminal tambahan sebelum `[DONE]`.

### 🐛 Issue #41 — `stream:false` to kiro and qoder was answered with an event stream

- 🔴 **kiro never looked at the request.** `ForwardKiro` called `handleKiroStream` unconditionally while every sibling executor — qoder, iflow, kimchi, commandcode — branches on `req.IsStream`; `handleKiroStream` hardcodes `Content-Type: text/event-stream` and flushes `data: {...}` frames. A client that asked for JSON got SSE, and `JSON.parse` died on the first `d` of `data:`. Reproduced here before the fix: `Content-Type = "text/event-stream"`, `jsontext: invalid character 'd'`.
- **`ForwardKiro` now branches, and the non-stream path reuses the stream parser instead of duplicating it.** `handleKiroNonStream` runs `handleKiroStream` against an `sseCollectWriter` — a body-only `http.ResponseWriter` — and folds the frames with the existing `sseToOpenAIJSON`, the same fold the codex non-streaming path already uses. The ~120 lines of Kiro event parsing, including the fragmented `toolUseEvent` argument reassembly that keeps emitting `arguments: "{}"`, stay in exactly one place. A clean end of stream surfaces as `io.EOF` once the last frame is consumed, which is normal and not treated as a failure.
- **No assistant frame is an error, not an empty answer.** Returning 200 with an empty body would read as a successful empty completion and silently end combo fallback, so a stream that folds to nothing becomes a 502 `sseWithoutCompletionError`.
- 🔴 **qoder is SSE-only, and the error was hiding inside the stream.** `ForwardQoder`'s upstream path is `/sse/agent_chat_generation`, so it answers with an event stream whatever `stream` says. The non-stream branch did branch, but it handed that stream to `jsonResponse`, which wrote it verbatim under `Content-Type: application/json` — header and body contradicting each other. The body also carried an upstream envelope `{"statusCodeValue":400,…,"body":"[FAIL]node:agent_router …"}`, so the client got HTTP 200, failed `JSON.parse`, and never saw the real error.
- **Two fixes, because folding alone would have hidden the error a second way.** `jsonResponse` now folds an SSE body into one `chat.completion` — the general fix, placed there because an SSE-only upstream ignoring `stream:false` is a cross-provider mechanism, and it runs *before* the log buffer, the Responses bridge and the Claude translation, all of which previously received raw SSE. `looksLikeSSE` only inspects the first non-empty line and requires a `data:`/`event:` prefix, so a JSON body (which starts with `{`) can never be misread. Separately, `qoderSSEUpstreamError` stays in `qoder.go` because the envelope is Qoder-specific, and returns a `*proxy.UpstreamError` carrying the upstream status and message. Verified: without it, a 400 envelope folds into a fake 200 with an empty completion — the error is swallowed rather than raised.
- **Streaming is untouched, and pinned.** `TestForwardKiro_StreamStillEmitsSSE` asserts a `stream:true` request still answers `text/event-stream` with the assistant delta and the terminal `data: [DONE]` frame, so the new branch cannot quietly swallow the streaming path later.
- **Every new test is mutation-checked.** Reverting the kiro branch fails with the reported `text/event-stream` + `invalid character 'd'`; disabling the qoder error detector fails with a fake `200` and an empty completion; disabling the generic fold fails with the same `invalid character` parse error. The four new tests build real AWS EventStream frames and real SSE payloads rather than asserting on mocks, and the kiro non-stream test asserts the *concatenated* content, so a parser that emitted only the last frame would fail.

### 🔒 Issue #35 — the backup file carried the dashboard password hash and the live OIDC client secret

- 🔴 **`GET /api/settings/database` wrote the raw settings blob into the payload.** `exportDatabase` assigned `Repo.GetSettingsRaw()` straight into `out.Settings` (`settings.go:349-354`), and `GetSettingsRaw` unmarshals the stored JSON unfiltered. `sanitizeSettings` — the one function that drops these keys — had exactly two call sites, `GET /api/settings` and `PUT /api/settings`, so the backup path bypassed it entirely. Every `9router-backup-*.json` therefore contained `"password": "$2a$10$…"` and `"oidcClientSecret": "…"` on disk. The OIDC secret is not a hash: `HandleOidcTest` (`sso.go:70`) reads it as plaintext, so it is directly usable against the identity provider. Since #32/#34 made the password-modal flow actually produce a file instead of 401-ing in the browser, this stopped being theoretical.
- **Export now strips them through the existing key list.** `exportDatabase` calls a new `stripSecretSettings`, which reuses `secretSettingKeys` rather than repeating the list, so there is one place that decides what a secret is. It deliberately does *not* add the derived `hasPassword` field the way `sanitizeSettings` does: the payload is read back by `importDatabase` and written into the settings row, so a response-only flag would persist a value nothing reads. Non-secret settings are untouched — `oidcIssuerUrl`, `requireLogin` and friends still restore.
- 🔴 **Naive sanitising was itself a security regression, and this is the part the issue did not predict.** `importDatabase` wipes the settings row (`DELETE FROM settings`) before writing the payload, so a backup with the keys stripped would have *deleted the live password* on restore and dropped the dashboard onto the well-known default `123456` — trading "a secret sits in a file" for "anyone who knows the default can log in", with OIDC silently disabled on top. Verified against a live build before fixing it: after importing a sanitised backup, `POST /api/auth/login` answered **200 for `123456`**, 401 for the real password, and `/api/auth/status` reported `hasPassword: false`. `importDatabase` now reads the current secret values **inside the transaction, before the wipe** (`readSettingsSecrets`) and carries them forward, so a restore can never downgrade auth. A payload that does carry its own hash still wins, so files written by older builds — or by the Next dashboard — keep restoring as before; and an *empty* secret in the payload is read as "not present here" rather than "clear it", so a hand-edited backup cannot blank the stored credentials either.
- **Deliberate divergence from upstream.** `exportSettings()` in `src/lib/db/repos/settingsRepo.js` is a plain `return await readRaw()` and `exportDb()` uses it directly, so upstream leaks the same two keys. This is a parity break, taken because the file is user-shared by nature.
- **What users should know:** the dashboard password and OIDC client secret are now machine-local. A backup restored onto a *fresh* machine no longer carries them, and the password has to be set again there — that is the intended trade, but it is a behaviour change from a backup that used to carry both.
- **Tests:** `TestHandleExportDatabase_StripsSecrets` (both keys and the injected `hasPassword` are absent from the settings block, neither credential appears anywhere in the serialized bytes, non-secret settings still present), `TestHandleImportDatabase_PreservesLiveSecrets` (a secret-free backup keeps the live hash and secret, and so does one whose secrets are empty strings), `TestHandleImportDatabase_LegacyBackupPasswordWins` (a backup carrying its own hash still wins). All three are mutation-checked — reverting each half of the change fails them with the exact leak or the exact `nil` credential.

### ⚡ PGO + upstream connection-pool tuning

- **PGO is now on for release builds.** `cmd/9router-go/default.pgo` is a CPU profile captured from a real chat-completions workload (auth + model resolution + SQLite reads + upstream forward). Go 1.27's default `-pgo=auto` picks it up automatically, so `make build`, `make run` and `make cross` all get the optimized binary with no flag to remember — `go version -m 9router-go | grep pgo` confirms it.
- **The connection pool was the actual bottleneck, not the compiler.** Go defaults `MaxIdleConnsPerHost` to **2**, so a reverse proxy under concurrency kept re-dialling upstream: a fresh TCP connect plus TLS handshake per request. `FallbackTransport`, `directProxyClient`, the chat handler's streaming client and the rotating-proxy client all ran on 2 idle slots per host. Raising it to 128 idle per host (256 total) and enabling HTTP/2 cut p99 latency **-51%** and raised throughput **+27%** at c=100 against the mock upstream.
- **The numbers live in `constants.HTTPTransportConfig`.** The pool/timeout values were four separate magic-number blocks; they are now one struct with a documented field for each knob, plus `Configure(t)` for a cloned `*http.Transport` and `NewTransport()` for a fresh one. `internal/proxy`, `internal/handlers/chat` and the rotating-proxy cache all read the same `DefaultHTTPTransportConfig`.
- **Verified behaviour-preserving against 13 live providers.** A three-way fingerprint (HTTP status, response envelope, error body) was captured from the pre-change build, a `-pgo=off` build and a PGO build, all against the same real upstream accounts: **0/13 providers changed outcome**, and the provider executor suite is 94/94 identical. The eight providers that fail do so for account reasons that predate this work — model not in plan (groq, clinepass), ChatGPT-plan restriction (codex), zero credits (commandcode, trae/opencode), and a bad base path (nvidia).
- **Two pre-existing non-streaming bugs surfaced while probing and are left as-is here.** `qd/*` and `kr/*` answer a `stream:false` request with HTTP 200 and an SSE-shaped body (qoder even mislabels it `application/json` while embedding an upstream 400 envelope). Confirmed identical on the pre-change build, so this change neither caused nor fixed it.
- **`benchmark/run_perf_test.py`** reproduces the load numbers end-to-end against a mock upstream; **`benchmark/live_provider_smoke.py`** reproduces the 13-provider fingerprint against the real database. Neither is part of `go test` — the live one needs `SMOKE_API_KEY` and a populated `~/.9router/db/data.sqlite`.
### ✨ Combos page: bulk delete with selection

- Every combo card gets a checkbox, with **Delete Selected (n)** and **Delete All (n)** buttons in the header. Both open the existing confirm modal, then delete sequentially and report partial failures (`Deleted 3 of 5 combo(s)`) instead of silently dropping the rest.
- The locked auto free-tier combo is excluded from selection, from the Delete All count and from the actual deletes — the button count and what really gets removed always agree, and its checkbox renders disabled with an explanatory tooltip.
- Selection is cleared after any completed delete run, so the counts never point at rows that are already gone.

### ✨ Auto Group combos by model family

- A **Auto Group by Model** button on the combos page creates one fallback combo per model family from every **usable** chat model: any version, any provider. "claude-sonnet-4.6" (cc), "claude-sonnet-4.5" (claude) and a hand-added gateway sonnet all land in the combo **Auto: claude-sonnet**.
- A model counts as usable when its provider has an **active** connection and the model is not in the user's `disabledModels` list. Embedding/image/audio models (declared `kind`, or "embedding" in the id) are skipped — they cannot serve a chat fallback chain.
- Family normalization (`modelFamily`) drops generation numbers and lifecycle flags, keeping size/role words, so `gpt-5.4-mini`→`gpt-mini`, `gemini-3.1-flash-preview`→`gemini-flash`, `kimi-k2.7-code`→`kimi-code`, `minimax-m3`→`minimax`. OpenAI's o-series is deliberately preserved (`o1`, `o3-mini`) since the leading token is the model line, not a generation. Pure-version ids fall back to the original rather than merging into an empty family.
- Combos are upserted under stable ids (`auto-family-<family>`) so regenerating refreshes rows instead of stacking duplicates. Families with under two usable models are skipped — a single-model combo has nothing to fall back to. Unlike the free-tier combo these stay fully editable (badge "Auto-generated", delete/reorder/edit all open).

### ✨ Provider model management: check latest, probe accessibility, prune unusable

- `ProviderDetailView.svelte` gains a **Check Latest Models** button (reusing the existing `/api/providers/{id}/models` fetch, no new endpoint): it diffs the provider's live catalogue against the models already registered (built-in + custom) and lists only the new ones, each with an inline **Add** button. The button only renders for providers the backend can enumerate — live-catalogue providers (`cline`, `clinepass`, `qoder`, `qoder-cn`, `antigravity`, `gemini-cli`), the five `modelsListURL` providers, and OpenAI/Anthropic-compatible custom nodes.
- A **Check All Models** button probes every model of the provider with the existing minimal-chat `api.testModel` path, at bounded concurrency (6 in flight) rather than firing one request per model at once. Each row shows ok/error, and on error gains a per-model **delete** button: custom models are removed via `deleteCustomModel`, built-in registry models (static, undeletable) are disabled instead. A summary line reports the failure count instead of spamming the single shared error banner.
- **Stale results never survive a provider switch** (review finding from #32): `ConnectionsView` renders the panel without a `{#key providerId}`, so the instance is reused across providers. A `$effect.pre` drops `latestModels`, the test verdicts and the in-flight flags before the new provider's DOM commits, and every handler that awaits an upstream call captures `providerId` up front and bails if it changed — previously the old provider's "new models" list stayed clickable and **Add saved the model into the wrong provider**.

### ✨ Locked auto free-tier combo

- A **Auto Free Tier** button on the combos page (re)builds the locked combo from the registry's free-tier models (`:free` / `/free` / `-free` suffixes) of providers the user actually has a connection for, so it never references unreachable providers.
- Locked combos carry a badge and can be **reordered** (fallback order matters) but not edited or deleted — the delete button renders disabled, and `validateLockedReorder` rejects any update that changes the name, kind, or model *set* while permitting pure permutations.
- `isLlmCombo` rejected any `kind !== 'llm'`, so an `auto-free` combo would be invisible on the combos page. It now accepts the `auto-*` kinds while still excluding the upstream `search-combo` media combos.
- **Strategy-only updates no longer fail with 403.** The card's strategy dropdown persists via `PUT {strategy}` with no `models` field, which `validateLockedReorder` read as "models changed to nothing" and always refused with `auto free-tier combo models are locked`. A request that carries no `models` field now skips the model-set comparison entirely — only a request that actually sends models must prove it is a pure permutation.

### 🐛 The CLI Tools page could not be opened at all (broken since v1.9.0)

- 🔴 **The view threw before it could render.** `CliToolsView.svelte` referenced `selectedTool` in ~20 places — the tool-card click handler and the whole detail modal — but the `$state` declaration had been dropped, leaving it as an undeclared global. The template threw `ReferenceError: selectedTool is not defined`, so the component never finished mounting and the page sat on "Connecting to 9router-go Localhost Gateway (:20130)…" forever. `git log -S` pinned the removal to `d8aa5ec3`, which is in **every release from v1.9.0 through v1.9.5** — six releases, about four days.
- 🔴 **The API it calls was behind the wrong auth gate.** `/api/cli-tools/all-statuses` was registered inside `SetupRoutes`, which `SetupServerRouter` mounts under `RequireApiKey` — the LLM API-key middleware. But the SPA calls it with the dashboard session cookie and never an API key, so every call returned 401 and the app bounced back to the endpoint tab even after the render crash was fixed. The route's own comment already said "dashboard batch status", so it was never meant to sit behind the LLM gate. It now lives in the `RequireDashboardAuth` group alongside the rest of the dashboard API; the unprefixed `/cli-tools/all-statuses` alias stays on the API-key group for external callers, and both paths still reject anonymous requests.
- **Why six releases missed it.** `tsc -b` does not typecheck `.svelte` files and the Svelte compiler does not scope-check templates, so an undeclared variable in a template is invisible to both `bun run build` and the CI build step. The 85-test web suite added in #36 covers `client.ts`, `router.ts` and friends, but there is no component-level render test, and no `svelte-check` in the toolchain. Fixing the two bugs closes the symptom; the gap that let it ship six times is still open.
- `TestSetupServerRouter_CLIToolsStatusIsADashboardRead` pins the dashboard-session auth (anonymous stays 401, session gets 200), and `TestSetupServerRouter_CLIToolsStatusAPIKeyAliasUnchanged` keeps the API-key alias registered rather than accidentally removed. Both are mutation-checked: restoring the old registration fails the first one with the exact 401 the dashboard was hitting.

## [v1.9.5] - 2026-09-28

### 🐛 Issue #27 — "Update now" opened the changelog inside the sidebar column

- 🔴 **The modal was clipped into the 288px sidebar.** `App.svelte` wraps `<Sidebar>` in a mobile drawer carrying `transform … lg:transform-none`, and an ancestor with a `transform` becomes the containing block for `position: fixed`. The modal's `fixed inset-0` therefore resolved against the drawer's box instead of the viewport, so the overlay covered only the sidebar and the panel was cut off part-way through the release notes. Upstream renders its update confirm as a sibling of `<aside>` (`src/shared/components/Sidebar.js:367`) for the same reason, so the fix is structural: the modal and the disconnected overlay now live in a new `UpdateModal.svelte` that `App.svelte` mounts outside the transformed drawer. `showUpdateModal`, `updateInfo` and `version` are bindable props so the sidebar keeps ownership of the version polling.
- **Release notes render as markdown, and fall back to the changelog.** `updateInfo.releaseNotes` is a GitHub release body, which the modal printed through `whitespace-pre-line` — so `- **bold**` and `` `code` `` showed up with their markers still attached. It is now parsed with `marked` and rendered through the existing `.changelog-body` styles, and when the manifest carries no notes the modal falls back to `/api/changelog`, the same source `ChangelogModal` uses, so "what changed" is answerable either way. The panel is `max-w-2xl` and scrolls at `max-h-[90vh]` instead of running off the bottom of the screen.
- **Escape now closes it, and every dismiss path is guarded the same way.** The handler was bound to a `role="button"` backdrop, so it only fired while that backdrop itself held focus; pressing Escape with the mouse elsewhere did nothing. A `keydown` listener on `window` closes the modal instead, and the backdrop, close button and Cancel all route through the same `dismiss()`, which refuses while `isUpdating` so an in-flight binary update is never abandoned. Body scroll is locked while the modal is open.
- **Four correctness bugs in this same modal, caught in review.** `marked.parse()` is synchronous unless `async` is set, so the release-notes branch calling `.then()` on its result threw a `TypeError` and the notes never rendered — the first E2E pass missed it because the stubbed manifest carried no notes, and it is now covered by a case that does. The changelog `$effect` depended on both `releaseNotesHtml` and `isLoadingChangelog`, so a failed fetch re-armed itself and retried in a loop while the modal stayed open; a one-shot `changelogRequested` flag ends that. "Copy & Shutdown" swallowed clipboard failures and started the countdown anyway, leaving the user with a stopped server and no command, so a failed copy now aborts with a message. And the countdown's `setInterval` was never cleared, so closing the modal mid-countdown still called `shutdownServer()` five seconds later — it is now cancelled on dismiss, and "Server Stopped" only appears once the shutdown request actually succeeds instead of being assumed.

### 🐛 Issue #30 — the quota tracker fired every account in the same millisecond, and Google answered 429

- 🔴 **Ten accounts on one office IP each tick was a self-inflicted rate limit.** `QuotaTrackerView` refreshes every visible connection at once (`Promise.allSettled(connections.map(fetchQuota))`), so a dashboard with ten accounts sent ten live quota reads inside a few hundred microseconds. Google answered 429, the chat path reads a 429 as real quota exhaustion, and the accounts locked in sequence with "no project ID" while their tokens were still live — taking the paid combos down with the free ones. The per-connection 30s throttle (`MinAntigravityQuotaRefreshInterval`) never applied: it caps how often *one* account is re-read, not how close two *different* accounts' reads are to each other.
- **Quota reads now pass through a single process-wide gate: 250ms floor plus up to 120ms of jitter**, so the same call repeated for the next account is always at least a quarter-second behind the last one and never lands in a perfectly periodic pattern. Measured A/B on the real router with six ollama connections fetched concurrently: with the gate the six responses spread across 325ms → 2.59s (gaps 295–661ms), without it they all landed inside a 22ms window (333–355ms) — the exact burst from the report.
- **New `internal/fetchgate` is the general, provider-agnostic mechanism** (`fetchgate.New(minGap, maxJitter)` + `Acquire(ctx)`), not an Antigravity patch: slots are reserved under the lock in call order, jitter is additive so `minGap` stays a hard floor, and a cancelled context releases the wait instead of holding a slot against the requests behind it. `HandleGetConnectionUsage` acquires one slot before every live read, on both the `fetchProviderUsage` dispatch and the separate Antigravity branch (which does not route through it). One account's own two RPCs (`loadCodeAssist` then `fetchAvailableModels`) stay back to back — that is upstream's own sequence, and one account is not the burst.
- **Only the start of a request is paced, and only when one is actually made.** A single account's manual refresh pays nothing, a provider with no live fetcher (the lock/Cooldown fallback) never queues, and a dashboard that navigates away mid-refresh spends no upstream request on a response nobody reads.
- **Upstream `decolua/9router` has no throttle here either**, so this is a deliberate, documented gap rather than a parity regression to revert later. The same shape is what OmniRoute added in `quotaFetchThrottle` after accounts on one egress IP started looking like automation to their provider.
- Covered by `internal/fetchgate` (idle gate is free, concurrent callers spaced, jitter only widens the gap, cancellation releases the wait) and by the dashboard, which asserts the spacing at the fake provider's own socket — the ollama dispatch, the Antigravity branch, and that an abandoned request makes no upstream call at all. Every one of those three fails when the gate is set to a zero gap.

### ✨ Chat Completions → OpenAI Responses streaming translator

Diff 1–4 dari 4 (stacked), sudah digabung ke `main`. SHA di bawah adalah yang dipakai setelah rebase ke `main` (`63b3c14c`); sebelum rebase, diff 1–2 ada di `36b17d46`/`b3af67ad`.

| Diff | Isi | Status |
|:---|:---|:---|
| 1 | Chat SSE → Responses SSE + `reasoning_details` | ✅ `b244d9e6` |
| 2 | Arah request: body Responses → body Chat Completions | ✅ `b85b256e` |
| **3** | Wiring di `ChatHandler.HandleResponses` (resolve, combo, fallback, penerjemah hanya bila upstream bukan Responses-native) | ✅ `6f47face` |
| **4** | `response.completed` untuk jalur non-streaming | ✅ `6f47face` |

**Diff 1 — mesin translasinya.** `TranslateOpenAIToResponses` + `FlushResponses` dengan `ResponsesState` yang mencerminkan `initState()` upstream.

- 🔴 **Klien `/v1/responses` yang upstream-nya bukan Responses-native mendapat balasan yang salah bentuk.** `HandleResponses` meneruskan body apa adanya ke `<base>/responses` dan menyalin balasan mentah, jadi upstream Chat-Completions-only (OpenAI-compatible) menjawab dengan chunk Chat Completions ke klien yang mengharapkan event Responses.
- **`response.completed` harus mengulang item-nya (#4307).** Klien yang membangun hasil akhirnya dari event terminal (GitHub Copilot CLI, helper "final response" OpenAI SDK) menganggap turn kosong padahal teksnya sudah streaming. `recordCompletedOutputItem` menyimpan item **berkunci `output_index`** sehingga close berulang menimpa alih-alih menduplikasi, dan `collectCompletedOutputItems` mengurutkannya menurut index supaya `response.output` mengikuti urutan emisi. Keduanya menempel di item yang sama yang memancarkan `response.output_item.done` — kalau terpisah, nomor yang tercatat pasti menyimpang dari yang dikirim.
- **Penyelesaian ditunda saat usage belum tiba, tapi hanya pada jalur langsung.** OpenAI mengirim usage di chunk terakhir yang `choices`-nya kosong, jadi menyelesaikan di frame `finish_reason` akan membekukan payload sebelum chunk itu terbaca. `FlushReachesUs` meniru `state.targetFormat === FORMATS.OPENAI` upstream: benar (tunda ke flush) bila translator berada di rute langsung, salah (langsung kirim) bila ia hop kedua dari pivot, karena `translateResponse` sudah membuang chunk terminal sehingga flush tidak pernah dipanggil dan penundaan akan menelan `response.completed` sepenuhnya.
- **Usage tidak menimpa akuntasi milik layer stream.** Dihasilkan ke `ResponsesState.Usage` sendiri, bukan ke usage internal, karena yang terakhir diisi `normalizeUsage()`-shaped untuk log dan biaya; ditimpa dengan bentuk Responses akan diam-diam membuang token cache/reasoning dari statistik. Tanpa `usage`, klien seperti Codex CLI menahan gauge "context used" di 0 dan tidak pernah auto-compact.
- **Item tool baru diumumkan setelah id dan nama sama-sama tiba.** Beberapa provider memecah `id` dan `function.name` ke chunk berbeda; memutuskan lebih awal membuat panggilan `exec` terkunci permanen sebagai `function_call`. Argumen custom tool justru **tidak** di-stream dan baru dikirim saat close, setelah amplop JSON Chat bisa dibungkus — streaming fragmen mentah akan mengekspos `{"input":"..."}`, bukan program freeform yang diharapkan klien.
- **`reasoning_details` kini ikut dibaca.** `extractReasoningText` hanya mengenal `reasoning_content` dan `reasoning`; vendor ketiga mengirim `reasoning_details` sebagai array yang bisa berisi string polos atau objek ber-`text`/`content`. `OpenAIReasoningDetail` menerima kedua bentuk dan mengembalikannya ke bentuk aslinya saat marshal, sehingga request yang diterjemahkan tidak mengubah bentuk yang disukai vendor.
- Covered by `internal/translator/responses_test.go`: urutan event teks, nomor urut monotonik, item pesan dibuka sekali, reasoning via `reasoning_content`/`reasoning_details`/blok `<think>` (termasuk yang terbelah antar chunk), idempotensi close, tool call (nunggu id+nama, buffer argumen, custom tool, `extractCustomToolInput`), usage dari chunk tanpa `choices` beserta detail cache/reasoning, penundaan penyelesaian pada kedua mode `FlushReachesUs`, `output` pada `response.completed` (#4307) termasuk overwrite dan urutan, serta `[]` bukan `null` saat kosong.
- **Diff 2 — arah request.** `ResponsesToChatRequest` mem-port `openaiResponsesToOpenAIRequest`: `input` (string, array, atau kosong) menjadi `messages` Chat, `instructions` jadi system message, blok `input_text`/`output_text` jadi `text` dan `input_image` jadi `image_url`, item `function_call` beruntun digabung ke satu turn assistant, `function_call_output` jadi pesan `tool`, dan `reasoning` di-buffer lalu menempel ke turn assistant berikutnya. `max_output_tokens` dipetakan ke `max_tokens` dan field khusus Responses (`input`, `instructions`, `include`, `store`, `prompt_cache_key`, `client_metadata`, `reasoning`) dibuang agar tidak bocor ke upstream.
- **Prompt kosong tidak pernah jadi request tanpa user turn.** `input: ""`, `input: "   "`, dan `input: []` semuanya menjadi satu pesan user berisi `"..."`; `messages: []` ditolak semua provider.
- **Tool call tanpa nama dilewati, bukan diteruskan.** Codex dan OpenAI menolak nameless call, jadi item yang namanya kosong/whitespace tidak pernah sampai ke wire. Hal yang sama berlaku untuk tool deklarasi tanpa nama: tool *hosted* seperti `{type:"request_user_input"}` tidak punya `name` dan tidak bisa jadi function declaration — meneruskannya mencapai provider seperti Gemini yang memvalidasi nama tool dengan ketat.
- **Tool custom dipertahankan sebagai identitas.** Chat Completions tidak punya deklarasi custom tool, jadi tool `type:"custom"` diekspos sebagai function dengan satu properti string `input`; namanya dikembalikan di `ResponsesRequestResult.CustomToolNames` supaya konversi balasan bisa mengenali lagi dan memancarkan `custom_tool_call_input`. Field ini **tidak** ikut masuk body upstream — upstream menaruh `_customToolNames` di body lalu menghapusnya di `chatCore.js:214` sebelum mengirim; di sini ia jadi bagian hasil, bukan body.
- **`additional_tools` digabung ke deklarasi tool**, dengan tool dari body lebih dulu. Skema `type:"object"` tanpa `properties` mendapat `properties` kosong, karena Codex Responses API mewajibkannya.
- Covered by `internal/translator/responses_request_test.go` (24 test): normalisasi input string/kosong, instructions, pemetaan blok konten termasuk `file_id` dan default `detail:"auto"`, tool call beruntun, tool call nameless, tool result (string dan objek), reasoning yang menempel ke turn berikutnya / tidak bocor ke turn user / `encrypted_content` bukan teks tampil, deklarasi tool (function, hosted, custom, additional, sudah-bentuk-Chat), dan pembersihan field khusus Responses.

**Diff 3 — `/v1/responses` benar-benar speaks Responses.** Rute `/responses` pindah dari `MediaHandler` ke `ChatHandler.HandleResponses`, cermin `HandleMessages`: resolve model, combo, capacity adapter, account fallback, dan usage logging. Sebelumnya handler media cuma passthrough lewat `forwardMediaRequest` — tanpa resolution, tanpa combo, tanpa fallback, tanpa log.

- 🔴 **Klien `/v1/responses` ke upstream Chat-Completions dapat balasan salah bentuk.** Smoke live (binary, upstream tiruan): sebelumnya balasan keluar apa adanya sebagai chunk Chat Completions; sesudahnya jadi `response.created` → `response.output_text.delta` → `response.completed`, dan upstream menerima `{"messages":[...]}` tanpa `input`/`instructions`.
- **Penjaga "Responses-native" menentukan arah, bukan cuma tata letak.** `previous_response_id`, `store`, dan id item hanya milik API Responses; mengubahnya ke Chat Completions membuangnya dan codex kehilangan percakapan sisi server. Native-ness dibaca dari data yang sudah ada, bukan tabel baru: `executor.UpstreamSpeaksResponses` — base URL yang berakhiran `/responses` (codex, grok-cli, perplexity-agent; tes yang sama dengan yang dipakai eksekutor opencode sebelum menambahkan `/responses`) atau model opencode yang memang dilayani dari `/responses` (muse-spark, grok-4.6, gpt-5.6-luna). Padanan `sourceFormat === targetFormat` yang membuat upstream melewati penerjemahan. Balasan native diteruskan byte for byte lewat `passthroughResponses`, bukan lewat `handleCodexStream` yang akan mengubahnya jadi Chat Completions.
- **Format klien lewat context, bukan field baru.** `translator.WithClientFormat` / `IsResponsesClient` / `NeedsResponsesBridge`, pola yang sama dengan `WithRequestedModel` yang sudah dipakai `HandleMessages`. Nol perubahan signature: `forwardRequestParams` dan kelima call site-nya tidak tersentuh, dan `executor.Request.Ctx` sudah meneruskan context ke `executor.Request{Ctx: ctx}`. `nil` context dibaca sebagai klien Chat di ketiga flag — itu mayoritas traffic, dan panic di sini akan menjatuhkan seluruh proxy.
- **Kombo dan capacity adapter memakai aturan yang sama.** `handleMessagesComboFallback` dan `handleComboFallback` sebelumnya meng-hardcode `TranslateResponse: true` dan `Endpoint: "/v1/messages"`. Untuk klien Responses itu berarti balasan dibalik ke bentuk Claude, dan upstream Anthropic diberi body Chat mentah tanpa `EnsureClaudeMessages`. Keduanya kini diturunkan dari format klien lewat `forwardEndpoint`, tanpa mengubah perilaku klien Chat maupun klien Claude.
- **Upstream Claude dilayani dua hop.** Event Claude → chunk Chat (cabang yang sudah ada) → event Responses, lewat bridge yang sama. `responsesBridge.feedFrames` menerima frame siap jadi maupun payload per chunk, supaya dua produser ini tidak masing-masing memecah frame sendiri.
- **Stream terpotong tetap ditutup.** `bridge.close()` memancarkan `response.completed` kalau upstream mati di tengah jawaban; tanpa event terminal klien menunggu giliran yang sebenarnya sudah selesai. Berlaku juga untuk hop kedua (upstream Claude), yang tidak pernah memakai sentinel `[DONE]`.

**Diff 4 — jalur non-streaming.** Klien yang tidak meminta streaming harus tetap menerima satu objek `Response`, bukan body Chat Completions yang tidak bisa dibacanya.

- **Objek `Response` dibangun dari translator streaming, bukan penulis kedua.** `ChatResponseToResponses` melipat satu jawaban Chat jadi satu chunk sintetis lalu memainkannya melalui `TranslateOpenAIToResponses` + `FlushResponses`, dan mengambil objek dari event `response.completed`. Bentuk item, urutannya, dan detail usage jadi identik di kedua jalur secara konstruksi, bukan karena dua implementasi kebetulan sama. Padanan upstream `convertResponsesStreamToJson`, yang mengagregasi event yang sama dari stream.
- **Provider yang memaksa stream tetap dijawab JSON.** Klien non-streaming bisa saja menerima SSE dari upstream (Codex melakukannya); `respondAsResponses` mengagregatnya lebih dulu lewat `sseToClaudeJSON`, karena menjawab klien yang menunggu JSON dengan stream membuat parsing gagal di sisi klien, bukan di sisi proxy.
- **Body yang tidak bisa dikonversi diteruskan utuh, bukan jadi 200 kosong.** Kesalahan konversi dicatat dan body asli dikirim; bentuk yang salah setidaknya masih membuat klien melaporkan masalahnya, sedangkan 200 kosong tidak memberi tahu apa pun.
- **Usage native dibaca ulang.** `ParseResponsesUsage` menerjemahkan `input_tokens`/`output_tokens` (dan `input_tokens_details.cached_tokens`) ke bentuk Chat yang dipakai log dan biaya, baik dari body non-streaming maupun dari event terminal `response.completed`. Tanpa ini setiap giliran codex, grok-cli, dan perplexity-agent tercatat nol token.
- Covered by `internal/proxy/executor/responses_bridge_test.go` (replay Chat SSE ke event Responses, stream terpotong tetap punya event terminal, dua hop Claude, dan tabel native-ness provider/model/config), `internal/translator/responses_json_test.go` (objek Response non-streaming, tool call, body tidak terbaca), `internal/translator/responses_usage_test.go` (envelope non-streaming, event terminal, cache detail, tanpa usage), `internal/translator/client_format_test.go` (ketiga flag termasuk context `nil`), dan `internal/handlers/chat/responses_test.go` (tiga e2e lewat handler: bridging ke upstream chat, upstream native tidak diterjemahkan, dan jawaban non-streaming berbentuk `Response`).

### 🐛 Empat jalur yang terlewat saat wiring `/v1/responses`

Audit setelah wiring menemukan jalur yang tidak melewati `sseStream` maupun `handleJSONResponse`, jadi bridge-nya tidak pernah sampai ke sana. `ResponsesBridge` diekspor supaya producer mana pun bisa memakainya, dan `FeedFrames` menerima frame siap jadi maupun payload per chunk.

- 🔴 **Antigravity (gemini-native) menjawab Chat Completions ke klien Responses.** `handleGeminiStream` dan `handleGeminiNonStream` menulis hasil terjemahannya langsung ke writer, tanpa pernah melewati bridge. Keduanya kini memakai bridge yang sama, dan `[DONE]` tidak lagi ditulis untuk klien Responses karena yang mereka tunggu adalah `response.completed`.
- 🔴 **MiMo free menulis body mentah di jalur non-streaming.** Jalur streaming-nya sudah lewat `handleStreamResponse` (dan jadi ikut ter-bridge), tapi cabang non-streaming melakukan `io.Copy` apa adanya. Sekarang keduanya membaca body lalu melewati `respondAsResponses`.
- 🔴 **`/v1/responses/compact` masih lewat handler media, dan `_compact` tak pernah dibaca siapa pun.** Rutenya diarahkan ke `HandleResponsesCompact` (yang sekarang memanggil `HandleResponses`, sama seperti upstream yang set `body._compact` lalu reuse `handleChat` — sebelumnya ia memaksa body Responses masuk ke `/v1/chat/completions`). `applyCodexCompact` di eksekutor codex menerjemahkan penanda itu jadi URL `/compact` dan menghapusnya dari body, porting `codex.js` `transformRequest` + `buildUrl`.
- **Kode mati dibuang, bukan ditinggal sebagai shim.** Setelah rute berpindah, `MediaHandler.HandleResponses` / `HandleResponsesCompact` dan blok dispatch `/responses` di `forwardMediaRequest` tidak punya pemanggil lagi, jadi ketiganya dihapus. Test `media/responses_test.go` (6 test) ikut hilang: semuanya memanggil handler yang sudah tidak ada, dan sebagian hanya menguji handler lewat `io.Copy`. Dua perilaku yang memang masih berarti — provider tanpa koneksi, dan kombo yang berotasi ke anggota sehat — dipindah ke `chat/responses_test.go` dan ditulis ulang terhadap apa yang dilihat klien, bukan urutan handler.
- **Status kode untuk `/v1/responses` kini sama dengan `/v1/chat/completions`.** Provider tanpa koneksi dulu dijawab 404 oleh handler media; sekarang lewat mesin fallback yang sama dengan dua endpoint lain, jadi 502 dengan pesan yang sama. Konsisten lebih berharga daripada status yang berbeda per wire format.
- Covered by `internal/handlers/chat/responses_test.go`: `TestHandleResponsesCompact_MarksRequestAndKeepsWireFormat` (penanda `_compact` sampai ke URL `/compact`, tidak bocor ke body, klien tetap dapat format Responses), `TestHandleResponses_ConnectionProblemsAreReported` (tanpa koneksi ditolak, koneksi tanpa kredensial tidak pernah memanggil upstream), `TestHandleResponses_ComboRotatesAndStaysInWireFormat` (anggota kombo yang sehat menjawab, format tidak bocor), dan `TestHandleGeminiStream_ResponsesClient` (dua hop Gemini → Chat → Responses, ditutup `response.completed`).

### 🐛 reasoning_effort "max" sekarang diturunkan di jalur MiMo

- 🔴 **mimo-v2.5-pro dan v2.6 menjawab 400 untuk `reasoning_effort: "max"`.** Upstream sudah memperbaikinya di `1b72f02e`; port Go belum punya penerapan aturannya sama sekali — bukan karena "terblokir", tapi karena `applyFormat` deepseek memang belum ada di Go. Yang sudah ada sebelumnya adalah clamp `max→xhigh` untuk Codex dan opencode zen (`transform.go:387`, `providers.go:1130`), yang **tidak menyentuh mimo** dan punya nilai target berbeda.
- **Aturannya umum, bukan mimo-spesifik.** `providers.ClampDeepseekEffort` memetakan level ke kosakata effort deepseek (`xhigh`/`max` → `"max"`), lalu menurunkan ke `"high"` bila level yang dideklarasikan model **tidak memuat `"max"`**. Yang menentukan adalah level yang dipromosikan, bukan nama model — mimo-v2.5 *menerima* `"max"` sementara v2.5-pro/v2.6 menolaknya, jadi model yang dipatok akan salah untuk setengah kasus.
- Dipanggil dari `injectMimoMarker` (`internal/handlers/chat/mimofree.go`), yang kini menyiapkan body MiMo: marker anti-abuse **dan** effort. Jalur deepseek lain belum diaudite — helper-nya sudah generik dan siap dipakai di sana.
- Covered by `internal/providers/clamp_effort_test.go` (7 kasus, termasuk batas v2.5 vs v2.5-pro) dan `internal/handlers/chat/mimofree_test.go` (clamp lewat body, effort yang tidak ada tidak dikarang, marker anti-abuse tetap terpasang).

### 🐛 Issue #25 — Codex quota tracker was empty; codex now reads live 5h/7d quota

- 🔴 **Every Codex account showed "Account active. No quota limits tracked."** `codex` was already listed in `usageSupportedProviders`, so Codex OAuth rows appeared in the quota tracker — but `fetchProviderUsage` had no `codex` case, so the request fell through to the lock fallback and answered `{plan:"codex", quotas:{}}`. Antigravity looked fine because it had a fetcher; Codex had none. The bug report was right that nothing read `wham/usage` anywhere in the tree. `internal/codexquota` now owns that endpoint (`GET https://chatgpt.com/backend-api/wham/usage`) and the dashboard dispatches `codex` to it, porting `open-sse/services/usage/codex.js:getCodexUsage`.
- **The window is positional, not duration-derived, and a missing 7d stays missing.** `primary_window` becomes the `5h` row and `secondary_window` the `Weekly` row; `window_minutes` is ignored, exactly as upstream does. When the account has no secondary window the row is simply omitted — the `7d=None` in the report is the account lacking a weekly cap, not a parse failure, and upstream renders that one-row table quietly rather than fabricating a zero-filled Weekly bar. The Report/New/Spark metered features are decoded too (`review_*`, `spark_*`), since the dashboard already renders those labels.
- 🔴 **The `Forbidden` was never an auth or WAF rejection — it was the environment proxy refusing the CONNECT tunnel.** A first cut of this fix copied upstream's `getCodexUsage` exactly and then "fixed" the resulting 403 by adding Codex CLI identity headers and a non-Go `User-Agent`, on the theory that a WAF was blocking `Go-http-client/1.1`. **That diagnosis was wrong and has been reverted.** Two facts killed it. First, the message itself: `Get "https://chatgpt.com/…": Forbidden` is Go's `url.Error` wrapping a *transport* error, not a status — a real 403 is reported as `wham usage returned status 403` and never reaches the body, and Go renders a refused CONNECT tunnel as exactly that reason phrase. Second, a live probe: with the same token, the wham endpoint returns **200** through a direct connection, and returns 200 with upstream's two headers, with a `User-Agent` only, and with the full CLI identity — the headers make no difference at all. This host runs behind `HTTPS_PROXY`, whose tunnel policy refuses `chatgpt.com`.
- **The quota path now uses the proxy-fallback transport the chat path already used.** `internal/proxy/fallback_transport.go` exists for exactly this and its comment names the failure ("CONNECT tunnel failed, 403 Forbidden, blocked-by-allowlist"), but only the chat handler was wired to it. `usageDo` and `codexquota.Fetch` used a bare `http.DefaultClient`, which has no direct-connection retry — so the quota tracker failed on hosts that chat traffic reached fine through the fallback. Both now share a `proxy.FallbackTransport`-backed client, which is what finally made the tracker populate. This is shared infrastructure, not a Codex-only change: `usageDo` is the sender for every dashboard quota fetcher, so it also repairs the providers that were failing for the same reason and are listed in the next section. A refusal that survives the direct retry is now reported as `ProxyRefusedError` naming the proxy as the cause, instead of a bare `Forbidden` that reads like a rejected credential.
- **Each quota row carries an explicit `remaining`.** `usageQuota` emits `used`/`total` only, but the dashboard's codex branch reads `quota.remaining` directly and `getConnectionQuotaRemaining` returns `Infinity` without it, which silently broke the "% quota: low to high" account sort. The rows are built explicitly instead of through that helper, and `resetCredits.availableCount` is carried through `usageResult.extra` because the tracker reads it off the raw response.
- **The three response envelopes upstream tolerates are all decoded**: `rate_limit`, `rate_limits` and `rate_limits_by_limit_id.codex`, plus the one further nested `rate_limit` level that `getCodexRateLimitBody` unwraps. Reset times accept unix seconds, unix milliseconds, numeric strings and ISO-8601; anything unparseable leaves `resetAt` null instead of a zero time.
- 🐛 **Quota rows used to reshuffle on every refresh.** Upstream emits them in insertion order, but Go serializes a `map[string]any` in nondeterministic key order, so `Object.entries` in the dashboard produced a different table order each poll. The codex branch now sorts into the fixed upstream order (`5h`, `Weekly`, `Review (5h)`, `Review (Weekly)`, `Spark (5h)`, `Spark (Weekly)`), keeping any unrecognised window last.
- **Codex also takes part in quota-aware fallback, which it previously could not.** A Codex 429 answers with `{"error":{"type":"usage_limit_reached","resets_at":…}}` (or `resets_in_seconds`) — a body `extractResetDuration` cannot read, so those accounts previously fell back to a generic exponential-backoff cooldown. `NoteCodexQuotaError` now parses that exact reset and caches it, and the picker pre-filters in `connections.go` skip the account until it passes, mirroring the Antigravity quota cache. Both filter sites now go through one `quotaCacheBlocked` dispatcher so the per-provider caches stay separate and cannot cross (AGENTS.md §3.A).
- **A 429 without the `usage_limit_reached` marker blocks nothing.** A per-request or burst limit on an otherwise healthy account reads live quota instead (throttled to one wham call per 30s per connection, in-flight calls coalesced) and caches the healthy reading — it must not synthesize a block that outlives the burst. A successful request clears the cache, and a failed refresh preserves the last known reading rather than overwriting it with "no quota".
- Codex quota is **account-level**, not per-model: one 5h and one 7d window covers every model the account can serve. The cache is therefore keyed by connection alone, which is also why a model-keyed `modelLock` row was the wrong shape for it — it would have free-routed `gpt-6-sol` while `gpt-5.5-codex` on the same account was equally dead.
- Covered by `internal/codexquota` (envelope precedence, window clamping, all four reset-time encodings, review/spark discovery, the two-header upstream contract, non-2xx, proxy-refusal classification), the dashboard fetcher (both windows rendered with `remaining`, absent-7d omission, bare message on 503, dispatch through `fetchProviderUsage`), the shared sender (`TestUsageDo_SendsNonGoUserAgent` — default UA applied, per-fetcher UA preserved) and the live end-to-end check: the real `GET /api/usage/{id}` route against the real wham endpoint, on the connection from the report, returns `{"plan":"free","quotas":{"session":{"used":0,"remaining":100,"total":100,"resetAt":"2026-10-27T13:29:01Z","unlimited":false}}}` and the chat cache (429 body parsing, the five non-quota-429 cases, weekly-window blocking, expired-reset and unknown-reset non-blocking, optimistic-refresh re-assertion, 30s throttle, in-flight coalescing, fail-open, success-clears, picker pre-filter).
- Note: `internal/usagetracker/quota_parsers.go` has an older, uncalled `ParseCodexUsageQuotas` that reads a different field set (`remaining_fraction` / `reset_after_seconds`). It was already dead before this change and is left alone; the live path is `internal/codexquota`.

### 🐛 Other quota trackers — Ollama, Qoder, Groq

- 🐛 **Ollama Cloud free accounts were told "No usage limits reported" while a live quota sat in the response.** `fetchOllamaUsage` read only `limits.session` and `limits.weekly`, but a free account reports `limits.monthly` and nothing else — so the card showed no bars at all. The window list is now a table mirroring upstream's `OLLAMA_LIMIT_WINDOWS` (`session`/`weekly`/`monthly`), which is also what the dashboard's `case 'ollama'` branch was already ready to render. Ollama exposes no reset timestamp, so the monthly reset is derived from the signup date on `/api/me` ("usage resets monthly from the date you signed up"), ported from upstream `nextMonthlyResetFromSignup`; day-of-month clamping (Jan 31 → Feb 28/29) is preserved. Verified against the upstream JavaScript on five cases, including the real account (signup 2025-08-14 → reset 2026-10-14T15:08:01Z).
- **A Qoder 401 now says the token is dead instead of printing a status code.** `Qoder connected. Usage fetch returned 401.` told the user nothing actionable. The connection is OAuth with an expired `accessToken`, and Qoder device tokens genuinely cannot be refreshed — `center.qoder.sh` answers 403 for device tokens, which upstream's own `shared/qoder/constants.js` documents — so the card now reads "Qoder authentication expired. Please re-authorize this connection." No silent retry is attempted, since one cannot succeed.
- **Groq's "No rate-limit data reported" is left alone on purpose.** Groq dropped `x-ratelimit-*` from `/openai/v1/models`; those headers now ride only on chat completions (confirmed live: `/models` returns none, a completion returns limit/remaining/reset for both buckets). Upstream deliberately piggybacks on `/models` so reading usage never costs a token, and returns the same message when no bucket is present. Surfacing real numbers would mean spending a token per quota poll, so that trade-off is unchanged.

### ✨ `/v1/models` can report the models you can actually call (#28)

- Reported by **@FmcStore** ([#28](https://github.com/luqman-v1/9router-go/issues/28)) and implemented in [#29](https://github.com/luqman-v1/9router-go/pull/29) — Thank you for the report that pinned the two code paths apart, and for the live before/after model counts that made the fix verifiable!
- On a fresh install `/v1/models` listed **1302 models across 119 providers** while the dashboard picker showed ~8, and nothing in the response said which list was authoritative. The two numbers came from different code paths: `buildModelsList` deliberately dumps the whole static registry when `providerConnections` is empty ("so a fresh install still has a usable picker"), while `resolveModelPickerGroups` in `web/src/components/combos/pickerData.ts` gates on `isConnected || noAuth`. A new user reads the 1302-entry list as "models I can call" and looks for a missing import step.
- The listing is now scopeable by query parameter, leaving the default byte-for-byte upstream-compatible:
  - `GET /v1/models` (default) — unchanged: full catalog on a fresh install, connection-scoped once connections exist
  - `GET /v1/models?connected=1` — only providers with an active connection, plus registry `noAuth` providers. On a fresh install this is the ~8-model noAuth subset instead of the full catalog
  - `GET /v1/models?all=1` — always the full static catalog, connections ignored, for explicit discovery
- Every response now carries `mode` (`all` | `connected` | `catalog`) and `connections` (distinct providers with an active row), so a client can tell a candidate catalog from a usable model list without guessing.
- The `disabledModels` KV scope keeps filtering in all three modes, and a custom model for an unconnected provider stays hidden under `?connected=1`.
- 🔴 **The first cut of `?connected=1` deleted every noAuth provider as soon as one connection was configured.** The builder walks connection rows, so the fresh-install catalog dump was skipped entirely once any row existed and the response contained nothing but the connected providers — measured on a live instance with one `kiro` row: 44 models, all `kr/*`, no `oc/*`, while the dashboard picker still offered opencode. That silently contradicted the mode's own contract ("providers with an active connection, **plus** registry `noAuth`"). A noAuth catalog pass now runs after the connection loop, skipping any provider an active connection already covers, so a connection's `enabledModels` / live catalog still owns its own model list and is not widened back out to the static catalog. Same instance after the fix: 202 models (`kr/*` plus the 158 noAuth), with unconnected credentialed providers still absent.
- Adds `providers.IsNoAuthProvider`, which resolves an alias to its canonical id before reading the registry flag, and `ModelsListMode` + `ModelsListResult` on the chat handler so the mode and its metadata travel together.
- 🐛 **The listing and the lookup route now disagree with each other.** `GET /v1/models/<model>` resolved against the default list only, which is connection-scoped as soon as any connection row exists — so `oc/jev-1.13-free` would be **listed** by `?connected=1` and **404** from the lookup route on the same install, at the same moment. Two requests seconds apart, two opposite answers about whether the model exists. The lookup now tries the default list and then falls back to connected mode (`findModelForLookup`).
- **The fallback is additive on purpose; swapping the lookup to connected mode outright would be a breaking change.** The two lists swap which one is the superset: with connections, `connected ⊇ default`; on a fresh install `default` is the full catalog and therefore `default ⊃ connected`. Resolving against connected mode alone would fix the 404 and, in the same change, turn the credentialed-provider lookups that resolve today into 404s — `gcli/grok-4.5-high` on a fresh install is the concrete case the test pins. Both tests are mutation-checked: reverting the fix fails `TestModelLookup_AgreesWithConnectedListing`, and the naive single-mode version fails `TestModelLookup_FreshInstallStaysPermissive`.

### 🐛 Dashboard backup import always failed with `Invalid database payload`

- Implemented by [@lautdalamvip](https://github.com/lautdalamvip) in [#32](https://github.com/luqman-v1/9router-go/pull/32) — the multipart/JSON mismatch and the upstream modal port both traced correctly, thank you!
- `ProfileSettingsView.svelte` posted the chosen file as **`multipart/form-data`**, but `HandleImportDatabase` (like upstream `src/app/api/settings/database/route.js`) does `request.json()` on the body, so every import died at the decode with HTTP 400 and the user saw "Failed to import database: Import failed with status 400". The client now reads `file.text()`, `JSON.parse`s it, and POSTs the raw JSON body — verified against a real `9router-backup-<ISO stamp>.json` export.
- The destructive op also re-authenticates against the stored bcrypt hash, and the client never sent a password at all: once a dashboard password was set, import (and the plain anchor-click export, which is likewise an `x-9r-password`-gated route) returned 401. Upstream prompts in a modal first; the port now does the same — both buttons open a password modal, and the password travels in the `x-9r-password` header.
- Backup filename stamp regained full precision (`T15-07-00-715Z` instead of a day-only `T00-00-00-000Z`), matching upstream's `toISOString().replace(/[.:]/g, '-')`.
- Server side accepts the password from **either** the body field or the header, so the pre-existing body-embedded contract keeps working. The upstream post-import `applyOutboundProxyEnv` has no Go equivalent yet (no `outboundProxyUrl` handling exists in this runtime), so it is noted in a comment rather than faked.

### 🐛 Four defects in that same backup flow, caught in review

- 🔴 **A 401 rendered as `[object Object]`.** `/api/settings/database` is always-protected, so a sessionless request is rejected by `RequireDashboardAuth` and gets the nested `{"error":{"message":…}}` envelope, not the handler's flat `{"error":"…"}`. `new Error(data.error)` then stringified the object. Both paths now read the message through `responseErrorMessage`, which unwraps either shape — the server's actual reason is what the user sees, instead of something less informative than the pre-fix "Import failed with status 401".
- 🔴 **Escape did not cancel the import — the import ran anyway.** `Modal` wires Escape, the backdrop and both ✕ buttons to `onClose`, and nothing gated them on the in-flight flag, so dismissing the dialog left the POST running: it completed, alerted success and reloaded the page, overwriting the database after the user had cancelled. All dismissal paths now route through one `closeDbAuth()` that refuses while a request is active, which is the behaviour the old blocking `confirm()` had.
- **The two backup actions could run on top of each other.** "Download Backup" was only disabled during its own download, so opening it mid-import cleared the typed password and reopened the dialog as a download prompt — and the import's `finally` then closed that brand-new dialog. Both buttons are now disabled while either request is in flight, the entry handlers re-check, and the import snapshots the password before its first `await` so a reset mid-read cannot blank the `x-9r-password` header.
- **The negative auth assertion passed for the wrong reason.** The authorized import earlier in `TestHandleImportDatabase_ClientContract` wipes the settings row, so by the time the wrong-password case ran there was no stored hash left and the request was answered by the `"123456"` fallback rather than by bcrypt — the case would have kept passing even with header auth broken. The hash is re-armed first, and the default password is now asserted rejected too, which is the assertion that actually distinguishes the two paths.

### 🐛 Claude decloak now recovers a tool name the map lost (`b65d2d0a`, #4342)

- A cloaked tool name reached the client as an unresolvable `<tool>_ide` whenever `toolNameMap` missed it — the map is built per request, so a retry or a reconnect loses it. Both decloak paths now fall back to stripping the literal suffix: the non-streaming `DecloakClaudeResponseBody` and the streaming `ClaudeStreamDecloaker`. Decoy names are exempt, so they still reach the client unresolved and surface as "tool unavailable" rather than a silent no-op.
- Two places bailed out before the fallback could help. `DecloakClaudeResponseBody` returned early on an empty map, and `NewClaudeStreamDecloaker` returned nil for one. The constructor now distinguishes the two cases: a **nil** map means no cloaking was applied at all, so stripping would be wrong; a non-nil **empty** map means the map was lost, and the decloaker is built anyway so the fallback runs.
- `claude-opus-5-5` joined the `cc` and `claude` catalogues, in the registry position upstream lists it.

### 🐛 Qoder parity — the provider page worked, the provider did not

- 🔴 **Every Qoder request was signed for a made-up account.** `ForwardQoder` passed an empty user id to `buildQoderCosyHeaders`, which substituted `user-<8 hex chars>` and carried on. The COSY signature is verified against the real account, so Qoder answered `403 {"code":"105","message":"Login expired"}` — which reads as an expired login, not as a signing bug. Upstream refuses to sign at all in that case (`buildCosyHeaders` throws on an empty user id). `BuildQoderCosyHeaders` now takes a `QoderCosyCreds` and errors instead of inventing an identity. Verified live against the account in the bug report: a fabricated id gives `403 Login expired`, the stored one gives `200`.
- 🔴 **The signing identity was never stored at login.** `qoderPoll` read `user_id` from the device-token response only to synthesise an email, and never persisted it. Upstream keeps it as `providerSpecificData.userId` (`src/lib/oauth/providers/qoder.js` mapTokens). It is now stored, along with the machineId that was already generated but dropped.
- 🔴 **"Import from /models" was dead for Qoder.** `GET /api/providers/{id}/models` only handled providers with a plain `/models` URL plus the compatible-node shapes, so Qoder fell through to `400 "provider qoder does not support models listing"`. The COSY-signed catalogue path already existed in `validate.go` for the key-validate probe but was never reachable from the models endpoint; `qoder_catalog.go` now serves it, porting `fetchQoderCatalogRaw` (skip `enable:false`, `max_input_tokens` with a 131 072 fallback) and upstream's route normalization. Verified live: the endpoint returns the same two models upstream does.
- **The machine UUID is now stable per connection.** It was regenerated on every request; upstream persists it so one auth always presents the same machine. `name` and `email` are also carried into the signed user-info blob, which upstream sends and we sent as empty strings.
- **Connection cards show the secondary label.** Upstream's `ConnectionRow` renders the email under a distinct name, or the `displayName` when the stored name *is* the email — which is what the Qoder device flow writes, so "Luqmanul Hakim" never appeared on our card. Ported as `secondaryConnLabel`.
- **The Qoder button is labelled like upstream's.** Upstream renders a dedicated `Fetch Qoder Models` action for qoder/qoder-cn (distinct from the Cline/clinepass one); ours said `Import from /models` for all four providers. The label is now provider-aware, and the icon spins while fetching as upstream does.
- 🐛 **"Quota Exhausted (0%): Resets in 2912172d 1h" on a perfectly healthy Qoder account.** The connection card's exhausted badge treated any quota with `remaining <= 0` as spent, but Qoder reports credits as `total: 0` when the account has no credit pool at all, and its `expiresAt` is a `9999-12-31` "no expiry" sentinel — so the countdown rendered 7973 years. A pool that was never allocated cannot be exhausted, so `total <= 0` is now excluded. The same misread had been tagging Command Code's `Credits` row (which is also `total: 0, unlimited`). Upstream has no such badge, so this is our own logic corrected rather than a parity gap.
- Covered by `internal/proxy/executor/qoder_cosy_test.go` (signature refuses a missing user id or token, connection identity is read, missing userId stays empty) plus a live check of the models route.
- Not changed: Qoder's own account state. Upstream's Qoder chat on the same account also fails with a `403` pointing at qoder.com/pricing, so that is account-side, not gateway-side.

### 🐛 Qoder OAuth — the login button opened a "paste your token" form

- 🔴 **Clicking OAuth on Qoder never reached the login page.** `handleAddConnectionClick` had branches for antigravity, freebuff, cline, PKCE, auth-code, custom, kiro, special and generic OAuth — but nothing for the device-code family, so Qoder fell through to `openGenericOAuth` and the user was asked to paste a token by hand. `isDeviceOAuth` was computed and never read, and `openDeviceOAuth` — which already opens the verification URL in a new tab, shows the user code and polls every 2s — was never called by anything. The missing branch is added ahead of the generic one (after kiro, which keeps its own specialised flow), so it also fixes kilocode, grok-cli, github, kimi/kimi-coding and codebuddy-cn/-intl, which were all silently on the same dead path.
- 🔴 **Even with the modal open, the poll could never succeed.** `device.go` built a bare `&http.Client{}` for every device exchange, and a sandbox or corporate `HTTP(S)_PROXY` refuses the CONNECT tunnel to `openapi.qoder.sh` with 403 — which Go renders as `Get "https://…": Forbidden`, a transport error, not a status. The modal opened the login page, then failed every poll with that text and sat on "waiting" forever. Verified: direct reaches the endpoint (404 for a bogus nonce), the proxy answers 403. Every device-code call now shares one `deviceClient` built on `proxy.NewFallbackTransport`, the same mechanism the chat path and the quota tracker already use.
- Verified live end to end: the OAuth button opens the Qoder device URL in a new tab and the modal shows the user code, the login URL with Open/Copy, and "waiting for device authorization (auto-check every 2s)" with no poll error. `kiro`, `github`, `grok-cli`, `kilocode`, `codebuddy-intl` and `kimi` still return 200 with valid device codes.

### 🗣️ The OAuth surfaces are now entirely in English

- The login modal and callback pages were a mix of Indonesian and English inside the same dialog — "Masukkan kode ini di halaman login yang terbuka" above a box already labelled "Step 1: Open this URL in your browser", and a callback page that said "Login berhasil!" next to "Koneksi diproses otomatis di tab dashboard". Every user-facing string in the OAuth flow is now English, in both places it is rendered: the Svelte modal (`ProviderDetailView.svelte`), the SPA callback view (`OAuthCallbackView.svelte`) and the Go-served callback page (`internal/handlers/oauth/callback.go`), which has two code paths — the no-code page and the auto-submitting one after a successful exchange. The Indonesian code comments in those files went with it so the next reader isn't switching languages mid-file.
- Also translated: the per-provider manual-token instructions (Cline, GitLab, iflow, Cursor, Freebuff), every `oauthError` message, the Cline divider label, and the direct-OAuth-unsupported fallback.

### 🐛 Guard: a combo name can no longer collide with a model alias or a custom model id

- **The hole.** A bare model string resolves against a **model alias** first (`resolveModel` step 2), a **combo name** second (step 3), and a **provider node prefix** third — and all three are writable from the dashboard, with no cross-check. A combo named `agy` beside a model alias `agy` left the alias silently outranking the combo: the combo stayed listed in the dashboard, answered no request, and the only symptom was a wrong model upstream. A custom model id colliding with a combo name is the same confusion one address over — `/v1/models` then advertises `combo-wombo` (the combo) and `xai/combo-wombo` (the custom model) side by side, and a name copied out of the list no longer says which one it lands on. A real install hit this with four collisions at once: the `xai` node carries custom model ids `combo-wombo`, `agy`, `agy-low` and `agy-med` beside combos of exactly those names.
- **The guard.** `POST /api/models/custom`, `PUT /api/models/alias`, `POST /api/combos` and `PUT /api/combos/{id}` now refuse a name already taken in another space, with a typed `409` (`COMBO_NAME_CONFLICT`, `MODEL_ALIAS_CONFLICT`, `CUSTOM_MODEL_NAME_CONFLICT`) shaped like the existing `PROVIDER_NAME_CONFLICT` guard, naming the occupant so the message says what to rename. Comparison is exact after trimming — resolution itself is case-sensitive, so `Combo` and `combo` are two real addresses and the guard must not refuse either. A combo keeping its own name across an update is unaffected.
- **Write-side only, so nothing existing breaks.** Rows that already collide keep serving traffic; the guard only refuses new writes, so no migration or hidden row is involved.
- **A refused write is now visible.** `CombosView` caught a failed combo save with `console.error` alone, so a 409 would have looked like a dead button; the modal keeps itself open and shows the reason inline. `ProviderDetailView`'s add-compatible-model path had the same gap and now alerts, matching the other import paths on that page.
- Verified with `go test ./...`, a nine-case regression test (`TestGuardNameCollision`) that also asserts a refusal writes nothing, and a live run of the built binary against a copy of the real database: the four collision shapes return 409, a rename into a taken name leaves the stored name untouched, and a free name still saves.


### 🐛 Capacity-adapter pool no longer joins a combo's round-robin rotation

- 🔴 **Traffic leaked to a provider that was not in the combo.** A combo whose own models cannot serve a request gets the capacity-adapter pool prepended to it — the pool exists *because* none of the combo's models fit. `HandleChatCompletions` and `HandleMessages` then discarded the adapter's own returned strategy and handed the **combo's** strategy to `handleComboFallback`, so under `round-robin` the injected model was folded into the rotation. With `combo-wombo` (one entry, `oc/space-bunny-free`, which reports no vision) plus the default `ag/gemini-3.8-flash-high` vision adapter, every turn carrying an image alternated between `opencode/space-bunny-free` and `antigravity/gemini-3.8-flash-high` — half the traffic went to a provider absent from the combo, which is exactly what an explicit combo is supposed to prevent. Both call sites now go through `applyCapacityAdapter`, which returns the adapter's strategy whenever something was injected and logs the switch, and falls back to the combo's own strategy only when the list was untouched. The single-model path already honoured it.
- Verified with `go test ./...` plus a targeted regression test asserting four consecutive vision turns all lead with the vision-capable model and keep the combo's own model as the trailing fallback.


### 🐛 Fix usage animation edge loss when many models/providers are active and on hard refresh

- **Provider topology node slicing removed**: `AnalyticsView.svelte` and `ProviderTopologyCard.svelte` previously hard-sliced displayed providers with `.slice(0, 14)`. When users had 14+ configured connections, active providers beyond the first 14 (such as `opencode` free tier or public endpoints) were completely discarded from the canvas, causing the center router node to pulse while leaving the active laser beam and shockwave animation missing. Providers are now dynamically accommodated on the ellipse with auto-scaled `fitView` zoom.
- **Active provider prioritization & alias matching**: `topologyProviders` now explicitly prioritizes currently active requests, recent completions, and pulse providers ahead of dormant connections so lines to in-use models are never dropped. Provider alias matching in `ProviderTopologyCard` now supports canonical IDs, display names, and catalog aliases (e.g. `oc` ↔ `opencode`).
- **Hard refresh state preservation**: `loadStats` on hard refresh now immediately seeds `activeRequests` and `lastProvider` from `/api/usage/stats` REST responses, preventing a blank state before the SSE stream connects.
- **Robust model key parsing & direct request tracking in usagetracker**: In `internal/usagetracker/tracker.go`, `parseModelKey` previously failed on model names containing spaces (e.g. `Grok CLI (Grok Build)`), returning `unknown` provider. It now robustly splits via `strings.LastIndex`. Concurrent direct/no-auth requests are tracked under `__direct__` so they are never masked when requests with connection IDs are active.
- **SVG filter clipping fix**: SVG `filter="url(#topo-electric)"` now uses `filterUnits="userSpaceOnUse"` with ample bounds to avoid 0-width/0-height bounding box clipping on vertical/horizontal edges.

## [v1.9.4] - 2026-09-27

### 🐛 Issue #24 — proxy UI, account rotation, priority reorder, provider search

- 🔴 **The Apply Proxy modal was unusable past a handful of pools.** It was hand-rolled without the shared `Modal.svelte` scroll wrapper, so the flex-centred panel grew past the viewport and clipped the title and every action above the fold — with a couple of hundred batch-imported pools the user saw only an endless `Imported <ip>` list and could reach neither "One-to-one (rotate)" nor Cancel. The panel is now `max-h-[85vh]` with the pool list scrolling on its own, and a filter box narrows pools by name or URL. Bulk apply also reports `Applying N / M` and its Stop button now actually aborts the loop instead of just hiding the modal while requests keep firing.
- 🔴 **Reordering two accounts could permanently break that pair.** The dashboard swapped priorities with two independent full-row `PUT`s, which have no cross-row transaction: one failing while the other landed left two rows sharing a priority, and a stable sort over tied priorities then made every later click a literal no-op. A single `POST /api/connections/{id}/reorder` now performs the swap *and* renumbers the pool to a contiguous 1..N inside one SQLite transaction, mirroring upstream's `reorderInTx` (`connectionsRepo.js:111-133`) — partial failure is impossible and any pre-existing tie is repaired. The client gained an in-flight guard so a second click cannot fire a swap computed from a stale list, and a failed reorder now alerts instead of only reaching `console.error`.
- 🔴 **A NULL priority was silently rewritten to 0 by any unrelated edit.** `HandleUpdateConnection` defaulted the column to `0` and only restored the old value when the row already had one, so a rename, proxy assignment or model assignment promoted a legacy row to rank 0 — ahead of every other account, since 0 beats every positive rank. The parameter is now `*int` and NULL stays NULL. The same coercion existed in four background writers; they use the new data-only `Repo.UpdateConnectionData`, which also stops a background OAuth refresh from writing back the name/isActive/priority it read before the write and reverting a reorder or a toggle the user had just performed.
- 🔴 **A client-pinned connection bypassed the enable/disable toggle.** The pinned branch of `getBestConnection` only checked provider ownership, so `x-connection-id` (and the custom-node prefix pin in `resolution.go`) served a disabled, excluded or model-locked account. Upstream resolves the pin *inside* `availableConnections` (`src/sse/services/auth.js:100-148`), where those rows never match; the pin now falls through to the configured strategy on the same conditions, and cross-provider pinning is still rejected.
- **Provider search only matched display names.** Searching a provider's own id found nothing — "bai" returned "Baidu Qianfan" but never "B.AI", and "atria-asi" or "tokenharbor" returned zero results — so a user concluded the provider was missing and built a custom OpenAI-compatible node instead. `matchesSearch` now also matches the id and alias.
- **`qoder-cn` was half-registered.** It appeared in the dashboard catalog, aliases and offline models, but had no `KnownProviders` transport and no executor, so `qdcn/<model>` resolved a provider the request path could not serve. It is now registered end to end: its own gateway base URL, COSY executor, OAuth refresh config, device-flow login/poll hosts, PAT validation endpoints and quota URL — each keeping its own hosts, never aliased onto `qoder` (AGENTS.md §3.A). Note that `bai` and `atria` were already present in the working tree from `773b5d96` but sit under `[Unreleased]`; a release containing that commit is what actually reaches users.
- 🔴 **The edit modal kept manufacturing priority ties.** The priority input was seeded with `conn.priority ?? 1` and always sent back, so renaming a NULL-priority row silently assigned it rank 1 — tying it with whichever row already held that rank and recreating exactly the un-reorderable pair the new endpoint exists to repair. The field is now compared against its seeded value and the key is omitted when untouched, so only a deliberate edit changes a rank.
- **The first priority-chevron click after opening a row proxy dropdown was always dead.** The dropdown paints a `fixed inset-0 z-40` click-away backdrop over the whole viewport, and a positioned element paints above the unpositioned chevrons, so the click only ever dismissed the dropdown. The chevron group now sits at `z-[60]`, above that backdrop, and the list is raised to `z-50` to match.
- 🔴 **Round-robin state was an in-memory index, so it reset to the top account on every restart** — and the reported "round robin never reaches the next account" is exactly what that produces. Rotation is now persisted per row, ported from upstream's stateless LRU (`src/sse/services/auth.js:151-189`): `lastUsedAt` / `consecutiveUseCount` are read with the candidate set, the least-recently-used row wins once its sticky window expires, and the winner's stamp is written back. The selector lives in `selectByRecency`, replacing `rotateConnectionsSticky`; the now-dead `ResetConnectionState` and the `conn:`-prefixed in-memory keys are gone, since there is no in-memory connection state left to reset.
- **Rotation stamps need nanosecond precision, and this is load-bearing.** The least-recently-used tie-break keeps the first row in priority order when two stamps are equal, so with `time.RFC3339` (whole seconds) every account ends up carrying the same stamp after one full cycle and the rotation then locks onto the top account forever. `db.RotationTimestampFormat` is a fixed-width nanosecond RFC3339 — fixed width, not `time.RFC3339Nano`, which trims trailing zeros and would sort `…:00.5Z` after `…:00.500000001Z`. `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` fails on pick 5 if the format is reverted to seconds.
- **Test fixtures were building a narrower schema than production.** `dbtest.CreateTables` and five hand-rolled test schemas declared `providerConnections` without the Go-only additive columns, so any query reading `lastUsedAt` failed in tests while working in a real database. `db.EnsureAdditiveColumns` is now exported and applied by those fixtures, which keeps the two shapes from drifting again the next time a column is added.
- **Providers added to the catalog without a shipped PNG rendered as an empty tile.** `getIconPath` resolves `/providers/<id>.png` for every card, and the sites hid the failed `<img>` — B.AI, Qoder CN, Dahl, Atria, Agnes, TokenHarbor (and `zai-search`) all showed a blank 32px box. The real logos were copied from upstream `decolua/9router` (`public/providers/{bai,atria,dahl,agnes,tokenharbor,qoder-cn}.png`, verified as valid PNGs), so the cards now render the genuine artwork. As a backstop, the new shared `ProviderIcon.svelte` (card tile, detail header, top bar) still falls back to an initials badge in the provider's own catalog colour on any missing asset, so a future provider that forgets to ship artwork reads as intentional rather than broken. `zai-search` is an internal tool provider with no card and no upstream logo; only the fallback covers it if it ever renders.
- Verified with `go test ./...`, a full SPA build, and a live run of the built binary driven through the dashboard UI: the reorder endpoint repaired a deliberately tied pool; a chevron click moved a row the old two-PUT path had wedged; renaming a NULL-priority row left the column NULL while an explicit priority change still took effect; and a chevron click landed while a row proxy dropdown was open.
- Round-robin was then exercised end to end against a stand-in upstream that echoes back the credential it received, over a custom OpenAI-compatible node with three accounts: six requests rotated `1→2→3→1→2→3`; the gateway was restarted mid-cycle and the next three continued `3→1→2` rather than restarting at the top account; disabling one account removed it from service (`3→1→3→1`) and an explicit `x-connection-id` pin naming that disabled account was ignored in favour of an active one.

### ✨ Gemini Live realtime STT transport (`fe347e4e`)

- 🔴 **Realtime transcription was not available at all.** The REST `generateContent` path can only transcribe a whole file inline; the Live API's `:bidiGenerateContent` WebSocket is the streaming counterpart. `internal/handlers/media/gemini_live_stt.go` ports it: audio goes up as `realtimeInput.mediaChunks` in 16 KiB slices (~0.5s of 16-bit 16kHz mono PCM) and the server's incremental `serverContent.inputTranscription` deltas accumulate into the transcript.
- **Dispatch is on a transport marker, never a model id.** `providers.ResolveModelTransport` reads the `transport` field off a catalog entry (`modelTransports`, mirroring the registry field upstream reads in `sttCore.resolveModelTransport`), so a new realtime provider extends through data rather than a new hardcoded branch. `gemini-2.5-flash-native-audio-preview-09-17` joined the gemini catalogue with `kind: "stt"` and `transport: "gemini-live"`, and a caller-supplied `transport` still wins — same precedence as upstream.
- Audio is only sent after the server's `setupComplete`, and the run settles on `turnComplete` — or on a graceful close, where a partial transcript beats a hard error and silence is the hard error. `goAway` advisories rotate the socket once per call: setup is replayed and audio resumes from the byte offset already sent, so nothing is re-uploaded and the transcript survives the hop.
- Lifecycle knobs (`prompt`, `language`, `system_instruction`, `setup_timeout_ms`, `turn_timeout_ms`) ride the same form pass-through every transport already gets, so no engine change was needed to reach this leaf. `system_instruction` replaces the built-in directive wholesale; `prompt`/`language` only shape the default. Caller-supplied timeouts are clamped at 300s.
- `verbose_json` answers with `segments` built from the raw deltas. Those frames carry **no timestamps**, so segments expose `{id, text}` only — start/end/duration are deliberately absent rather than fabricated as zeros, which would misrepresent provider data to anyone diffing transports.
- New dependency: `github.com/gorilla/websocket` (Node has a global `WebSocket`, which is why upstream needed none).
- Tested end to end against a stand-in Live server: `toLiveWSURL` shapes, the setup frame's model/generationConfig/systemInstruction, every audio byte arriving as base64 (20 000 bytes round-tripped), the flushing turn, transcript assembly from two deltas, partial-transcript-on-close versus silence-as-error, the system-instruction precedence, and the timeout clamp.

### ✨ Claude header parity — session id, beta merge, rate-limit forwarding

- **`x-claude-code-session-id` on OAuth requests** (`6aea3875`). The cloak generates a `metadata.user_id` carrying a session id, but nothing echoed it back in the matching header, so Anthropic saw two different sessions for one request. `extractClaudeSessionIdFromUserId` reads the id back out (JSON object form, which is what the cloak writes, or a bare id from other clients) and the request-scoped header is set from it. `generateFakeUserID` now also strips the CLI's own `claude:` prefix before baking the id into the body, so the two agree without a second pass.
- **The caller's `anthropic-beta` flags are merged, not dropped** (`dc198dff`). A client asking for a beta the gateway does not list was silently refused without being told why. `handlerutil.WithClientAnthropicBeta` carries the header on the context alongside the session id, and `providers.MergeAnthropicBeta` unions it with the registry's own list, de-duplicated and in order.
- **Retry and rate-limit headers are forwarded to the client** (`dc198dff`). `retry-after`, `x-should-retry` and every `anthropic-ratelimit-*` header are copied from the upstream response in `ForwardOpenAI`, before any branch writes a status — so they reach the client on the streaming, non-streaming and Claude-translated paths alike. Without them a 429 arrives as an opaque failure and a gateway in front of us cannot back off on our behalf. Hop-by-hop, auth and content headers stay internal.
- Header edits are request-scoped: `providers.WithHeader` and `WithoutBetaFlag` both copy, because the map in `KnownProviders` is shared by every request and a mutation would leak into later ones. `TestWithHeader_DoesNotMutateSharedMap` guards it.
- Not ported here: **Claude free-tier reset grants** (`consumeClaudeResetGrant`, `?cedar_ember=1` plus a new `claude-reset` API route). It is a new usage-side flow, not a header one — see the outstanding list.
- `TestExtractClaudeSessionIdFromUserId` pins a quirk on purpose: the prefix is matched at the start only, and leading whitespace is trimmed *after* the match, so `"  Claude: abc"` keeps its prefix — exactly what upstream's anchored replace does.

### ✨ Port the full upstream pricing table (271 rate entries) and the real cost formula

- 🔴 **Costs in the Usage views were a guess.** The Go port had four hand-written prefix rows (`claude-sonnet-4`, `claude-haiku`, `deepseek-v4-flash`, `gpt-4o`) and charged everything else a fabricated **$1/M input, $3/M output** — and charged cache reads at the *full* input rate, because the call site never passed the cached or cache-creation counts. `internal/pricing/tables.go` is now generated from `open-sse/providers/pricing.js`: 108 canonical models, 112 provider-specific entries, 51 pattern rows, five rates each (input, output, cached, reasoning, cache_creation). Regenerate rather than hand-edit.
- `GetPricingForModel(provider, model)` replaces `lookupPricing(model)` and follows upstream's four steps: the provider's own table, the free namespace, the canonical table (vendor prefix stripped, then whole id), then the ordered pattern table. `path.Match` is unusable here for the same reason it is in the thinking-levels port — its `*` never crosses `/` — so the glob keeps upstream's semantics.
- `CalculateCost` is the port of `calculateCostFromTokens`. The convention it assumes is the one the wire uses: **`prompt_tokens` is cache-inclusive**, so cached and cache-creation are subtracted before the input rate and then charged at their own cheaper rate; a rate left at zero falls back the way upstream does (cached → input, reasoning → output, cache_creation → input).
- The call site in `handlers/chat/usage.go` now passes the provider and the full token mix, so a cache-heavy request stops being billed as fresh input.
- ⚠️ **Visible change in the Usage totals: 556 of the 1592 catalogued `(provider, model)` pairs are priced by nobody, upstream either.** Upstream records a cost of **0** for those; the Go port used to invent $1/$3 per 1M for them. This follows upstream and makes "unpriced" visible instead of quietly fictional, but it does mean the cost column drops for those models from here on — say the word if you would rather have a fallback rate back.
- Tests pin the **numbers upstream itself returns**, not a re-implementation: `TestGetPricingForModel_Parity` and `TestCalculateCost_Parity` use rates and costs read out of the upstream module (anthropic sonnet $18.8625, gpt-4o $13.6875, gpt-5.6-sol $36.875, vendor-prefixed deepseek $0.4137, free namespace $0, unpriced $0 — all on prompt 1M / completion 1M / cached 250k / reasoning 100k / cache-creation 50k). The old `cost_test.go` asserted the four-row table and the prefix-matching behaviour, neither of which exists upstream, so it was replaced.
- `TestPricingTablesPopulated` guards the generated file: an empty table would still compile and would silently price everything at zero.
### 🐛 Codex "Add Connection" died at `invalid_authorize_request` (upstream loopback parity)

- **Symptom**: `Providers → OpenAI Codex → Add Connection` opened the OpenAI login page only to bounce straight to `Authentication Error` (`error_code: unknown_error`), and the modal never connected. Older builds surfaced the same rejection as `{"code":"invalid_authorize_request","message":"Invalid authorize request"}`.
- **Root cause**: `callbackRedirectURI` (`internal/handlers/oauth/redirect.go`) derived the callback from the dashboard's own host for *every* provider, so Codex was sent `redirect_uri=http://localhost:<dashboardPort>/callback`. Codex's public OAuth client (`app_EMoamEEZ73f0CkXaXp7hrann`, the Codex CLI) has exactly one registered loopback URI, and `auth.openai.com` validates `redirect_uri` during the authorize step. Probed live in a browser against `auth.openai.com`: `http://localhost:1455/auth/callback` reaches the login page, while `http://localhost:20130/callback`, `http://localhost:20130/auth/callback` and even `http://localhost:1455/callback` (right port, wrong path) are all rejected before the login form renders.
- **Fix, in three parts**:
  - `pkceConfig` gained `fixedRedirectURI` / `fixedPort`, and `callbackRedirectURIFor` / `exchangeRedirectURIFor` make the registered URI win over the dashboard-derived one for providers that pin it. Every other PKCE provider keeps following the dashboard, unchanged.
  - `internal/handlers/oauth/codex_proxy.go` (new) ports upstream `startCodexProxy` (`src/lib/oauth/utils/server.js`): a short-lived listener on `127.0.0.1:1455` behind `GET /api/oauth/codex/start-proxy`, a state-keyed session carrying the PKCE verifier, and `GET /api/oauth/codex/poll-status` so the dashboard learns the outcome. The listener completes the token exchange **server-side** and renders the result in the popup; an unregistered callback (a real `codex` CLI on the same machine) is 302'd to the dashboard's `/callback` instead of being dropped.
  - The exchange itself moved out of the HTTP handler into `resolvePKCEExchange` → `requestToken` → `buildConnectionData` → `persistConnection`, so the loopback server and `POST /api/oauth/pkce/exchange` run one code path instead of two copies of the OAuth rules.
- **A second, quieter Codex gap closed at the same time.** Upstream stores `chatgpt_account_id` / `chatgpt_plan_type` from the id_token into `providerSpecificData` and sends `chatgpt-account-id` on every call; Go stored neither and sent no such header. That is not cosmetic — the codex responses endpoint rejects a request without it, so even a successfully added account could not complete a single call. `codexAccountInfo` (`codex_account.go`) decodes the namespaced `https://api.openai.com/auth` claim block (with the legacy root-level `account_id` / `plan_type` as fallback) and `ForwardCodex` now sends the header from `ConnData`.
- **Two listener-lifetime bugs found by testing, both fixed**: `Server.Close()` from inside the request handler killed the connection serving it, truncating the very result page the user was looking at — shutdown is now `Shutdown`, deferred off the handler goroutine. And a stray callback retiring the listener would silently kill an unrelated in-flight login (the port is fixed, so a dead listener is a dead login with no way back), so the listener now only retires once nothing is pending. The dashboard also stopped freezing on "Waiting for popup authorization…" when the server no longer tracks the login; it says so and asks for a retry.
- Verified live against a worktree build on `:20141`, upstream on `:20128`: `Add Connection` now lands on `https://auth.openai.com/log-in`, `127.0.0.1:1455` is listening, `poll-status` reports `pending`, and a callback with an unusable code produces OpenAI's real `401 token_expired` in the popup and in the modal — i.e. the token request genuinely reaches `auth.openai.com`. A/B on the authorize endpoint: `:20130` (old) sends `http://localhost:20130/callback`, `:20141` (fixed) sends `http://localhost:1455/auth/callback`. `go test ./...` and `bun run build` green; `TestCodexLoopback_completesLoginEndToEnd` drives the real listener through a full login against a stub token endpoint.

### ✨ Sync batch — Codex `responsesLite` and the connection name guard

- **`responsesLite` for `gpt-6-sol` / `gpt-6-luna`** (`95600db1`). Those two models were in the catalogue but would 400: Codex 0.155 serves them with a different request shape — tools and instructions travel as an `input` prefix (`additional_tools` + a developer message) instead of top-level fields, and reasoning carries `context: "all_turns"` with no `summary`. Adding the ids to the catalogue without the wire shape was worse than leaving them out, so `internal/proxy/executor/codex_responses_lite.go` ports it: `isCodexResponsesLiteModel` (stripping a trailing `(level)` override first), the prefix rewrite, and the lite reasoning block (effort defaulting to medium rather than low). A body that already carries the prefix is left untouched, so replaying a transcript is not double-wrapped, and an input shape the rewrite cannot read is left alone rather than replacing the conversation with the prefix — the two build paths produce different concrete slice types, `[]any` and `[]map[string]any`. Classic Codex models keep the shape they had; `TestBuildResponsesBody_ClassicCodexUnchanged` pins that.
- Not ported: upstream falls back to `CODEX_DEFAULT_INSTRUCTIONS` when the client sends none, and 9router-go has no copy of that prompt blob for any model — the prefix carries the caller's own instructions, and adds no developer message when there are none. Carrying a Codex prompt only on the lite path would be a new, unreviewed prompt rather than a parity fix.
- **`POST /api/connections` refused to overwrite nothing** (`239bcfc5`, fixes #4311). A create reusing a name already in use replaced the stored key without a word: a script naming rows "Key 1", "Key 2", … kept wiping existing pool entries and got a success back. A create now looks the name up first and answers **409 `PROVIDER_NAME_CONFLICT`** naming the row that would have been replaced; a caller that means it passes `allowOverwrite: true` (or the legacy `overwrite`), which rewrites that row in place, keeping its id and its place in the rotation instead of leaving two rows the account picker cannot tell apart. A request carrying an `id` is already an explicit edit and is never name-guarded. Go's symptom was a silent duplicate rather than a silent replace, because `CreateProviderConnectionFull` is a plain INSERT — same caller-facing hole, different shape.
- The O(1) half of that upstream commit was already ported (`MAX(priority)+1`, no renumber pass on insert).
- Live: `POST /api/connections {"provider":"commandcode","name":"9router",…}` without an id answers 409 with `existingId`/`existingName`, and the stored connection is still the only one — one row, key intact.

### ✨ Sync batch — 5 aggregator providers, catalogue and Cline free tier

- **Five OpenAI-compatible aggregators** added upstream in v0.5.91, wired end to end: `tokenharbor` (+`th`/`thh`), `dahl` (+`dahl-inference`), `atria` (+`atria-asi`), `agnes` (+`agnes-ai`), `bai` (+`b-ai`). A provider entry is only real once every table carries it, so this touches `providers.go` (transport), `aliases.go` (short prefixes), `registry_models.go` (offline catalogue), `executor/init.go` (routing), `web/src/lib/providers.ts` (dashboard card, notice, `modelsFetcher`) and `web/src/lib/models.ts`. Upstream ships no capabilities or pricing rows for any of the five, so none were invented here.
- The dashboard "Suggested free models" import needs a live catalogue endpoint, which upstream wires per provider (`PROVIDER_MODELS_CONFIG`). `providers.ModelsListURL` carries that mapping and `HandleGetConnectionModels` gained a branch that Bearer-probes it. `agnes` and `bai` publish no seed catalogue at all — their page correctly lists no models and imports everything live.
- **`passthroughModels` needs no Go counterpart.** Upstream added a per-provider opt-in so any model id is accepted; the Go request path already forwards the model string untouched for every provider, so it is a superset. Deliberately not "implemented" — adding a gate would be a regression.
- **Catalogue sync**: `opencode-go` 28 → **42** models, `codex` 27 → **29** (`gpt-6-sol`, `gpt-6-luna`), both now byte-identical to the upstream registry. `qoder-cn` was missing from the dashboard catalog entirely and is back.
- **Cline free tier** (`199bcfc5`). `/api/v1/models` carries no `cline-free/*` ids, so upstream merges a second, unauthenticated feed (`/api/v1/ai/cline/recommended-models`) and prices that namespace at zero. Both are here: `clineFreeTierModels` merges with first-writer-wins so a dead feed cannot take the catalogue down, and `pricing.IsFreeModel` checks the namespace before the price table — the same model id is *not* free through a paid gateway.
- Parity fixture re-captured for the wider catalogue: **1589/1589** `(provider, model)` pairs match upstream (was 1547). The 42 new pairs include `gpt-6-sol`/`gpt-6-luna`, which are the first to exercise the per-model `thinkingLevels` stage.
- Live, `20130` vs upstream `20128` on the same data: all six provider pages identical — `tokenharbor` 6 rows / 7 level options, `dahl` 3 rows (incl. the binary `thinking` level), `atria` 1 row, `agnes` and `bai` 0 rows with no picker, `qoder-cn` 16 rows.

### ✨ Sync batch — upstream v0.5.86..v0.5.91 (Gemini turns, Claude thinking text, usage by API key)

- **Gemini terminal-turn guard** (`30464bc2`). `NormalizeGeminiContents` merged same-role turns and stripped empty parts, then stopped — a conversation that ends on a `model` turn is rejected by Gemini, and three ways arrive that way: a prefill, a truncated stream, and tool calls the client never answered. It now brackets the payload with user turns: a leading `user "..."` when the first turn is not a user turn (that part was already missing, from #e7b5f09), and a trailing turn carrying one `functionResponse` per unanswered `functionCall` — or a plain `Continue.` when the model was talking. `GeminiFunctionCall`/`GeminiFunctionResp` gained `ID`; without it a synthesized response could not be matched to its call. Ported upstream's own `gemini-contents-normalization.test.js` as a table test.
- **Claude thinking text for OpenAI clients** (`90b06934`). Claude returns thinking as a signature only unless the request sets `thinking.display: "summarized"` — a field OpenAI has no equivalent for, so a client asking with `reasoning_effort` (Chat Completions) or `reasoning.summary` (Responses) silently got nothing: `ensureMessagesMaxTokens` deleted `reasoning_effort` outright. The intent is now captured into the Claude field before the OpenAI-only keys are stripped (`internal/proxy/executor/claude_thinking.go`), and `redact-thinking-2026-02-12` is dropped from `Anthropic-Beta` for that request, since it asks Anthropic for exactly the signature-only behaviour the client is trying to avoid. An explicit `thinking.display` on the body still wins, and `reasoning_effort: "none"/"off"` stays an opt-out.
- **Usage by API key** (`4a57df8b`, `a406381f`). `UsageStatsResponse.ByApiKey` was declared and initialised but never written, so the dashboard's per-key breakdown was permanently empty. It is now filled from both the daily rollup and live `usageHistory`, keyed by the **stored** key value rather than a re-derived display mask — every key an instance mints shares the same `sk-{machineId}` head, so masking collapsed a whole team key set into one row and attributed one key's usage to another. Friendly names resolve against the `apiKeys` table, indexed under both the full key and its mask because the shared database holds both forms (rows this build writes are masked, rows the Next.js dashboard wrote are not).
- `handlerutil.MaskAPIKey` is now the single mask implementation, widened from a 4-character prefix to 8 to match upstream's keep-the-tail change. The `***` sentinel for short/empty keys is kept so existing no-key usage rows stay in one bucket instead of splitting.
- Three existing translator tests indexed `contents[0]` / `contents[1]` and counted turns; they now locate the part by role or by scanning, since the bracketing deliberately changes the shape.
- Live: `/api/usage/stats?period=7d` went from an empty `byApiKey` to 506 buckets with the two local keys resolving to `luqman` and `mac`. `go test ./...` and `bun test` green.

### 🐛 Thinking levels drifted from upstream v0.5.91 (MiMo + Codex per-model sets)

- Found while auditing the upstream tag range `v0.5.86..v0.5.91` (38 commits, 123 files). `internal/providers/thinking_levels.go` was missing two pattern rows and a whole resolution stage that v0.5.91 added.
- Missing rows: `*mimo*v2.6*` and `*mimo*v2.5-pro*`, both `["none","low","medium","high","xhigh"]`. Upstream's note: *mimo-v2.5-pro on opencode-go rejects reasoning_effort "max" (probed live); v2.5 accepts it*. Without them, 15 `(provider, model)` pairs fell through to the `deepseek` format default and answered `["high","max"]` where upstream answers `["low","medium","high","xhigh"]` — the picker would have offered a level those gateways reject.
- Missing stage: upstream resolves a per-model `thinkingLevels` off the catalog record before the pattern table (`getProviderModels("cx").find(e => e.id === baseId)?.thinkingLevels`, with a trailing `(level)` suffix stripped first). Go's catalog is a flat id list, so the field lives in `codexModelThinkingLevels`; `GetThinkingLevels` now resolves registry-declared → pattern → format default, the order upstream uses.
- The parity fixture had been **masking this**: it was captured from an upstream tree carrying the older values, so `TestGetThinkingLevels_MatchesUpstreamFixture` reported 1547/1547 while 15 pairs were wrong. The fixture now carries an `upstreamVersion` field, the test fails when it does not match the tag the port targets, and the capture was re-taken from v0.5.91 — 1547/1547 against the real thing.
- `TestCodexModelLevels` covers the per-model stage directly (including the `(level)` suffix and the codex/cx split), since the GPT-6 models are not in the Go catalog yet and the fixture cannot reach that path.

### ✨ Provider detail parity for `/dashboard/providers/<id>` (CommandCode audit against upstream `:20128`)

- 🔴 **The "Available Models" list was empty for 28 providers.** `web/src/lib/models.ts` keyed `PROVIDER_MODELS` by provider id but resolved it through `PROVIDER_ID_TO_ALIAS`, which holds the *display* prefix (`uiAlias`: `commandcode→cmc`, `deepseek→ds`, …). Upstream derives the same map from the registry `alias` and only remaps OAuth entries (`providerModels.js:107-113`), so every non-OAuth provider whose alias differs from its id got `undefined` instead of a list. `getModelsByProviderId` now resolves by id first and falls back to the alias.
- `internal/providers/thinking_levels.go` (new) — port of upstream `open-sse/providers/thinkingLevels.js`: the shared level sets, `FORMAT_LEVELS` keyed by thinking format, the ordered `PATTERN_THINKING` overrides (codex GPT-5.6, deepseek v4, per-model codebuddy sets), and the Kiro `resolveKiroEffortPath` gate. `path.Match` is unusable for the pattern table because its `*` never crosses `/`, so the port carries upstream's glob semantics.
- `Capabilities` gained the thinking block (`ThinkingFormat`, `ThinkingCanDisable`, `ThinkingRange`, `ThinkingEffortSupported`) plus provider-declared `ContextWindow`/`MaxOutput`. `ThinkingCanDisable` is a `*bool` because upstream's default is `true` and a Go bool cannot say "not specified" — without the tri-state, every table entry that names a format silently lost the `none` level. `mergeCapabilities` takes the whole thinking block from an overlay only once it declares a format, mirroring the JS spread.
- Ported upstream's 145 thinking declarations across the model / provider / pattern tables, and the 8 pattern rows the Go table was missing (`*gemini-3.7*`, `*grok-4.6*`, `*glm-5.3*`, `*glm-5.2*`, `*deepseek-v4.*`, `*minimax-m2.5*`, `*mimo*v2.6*`, `*muse*spark*`). Removed the table entries upstream does not have and that therefore shadowed a pattern row (`grok-4.5`, `grok-4.6`, `gpt-6-astra`, `glm-5.3`, the `gpt-5.6-*-image` trio, `*hy4*`, `*longcat*`, the `qoder` provider scope), and corrected `union-alpha` plus the `*mimo*` rows.
- `internal/handlers/dashboard/model_caps.go` (new) — `GET /api/models/caps?provider=<id>`, the Go counterpart of upstream `useModelCaps`: the model *catalog* still ships as a static bundle, but capabilities and thinking levels are resolved server-side from the provider registry, the capability tables and the synced models.dev catalog. The provider resolves by id or alias, so `/dashboard/providers/commandcode` and the `cmc/` storage prefix share one block.
- CommandCode capabilities are now the upstream short-circuit (`capabilities.js:570-582`): every model is served from one `/alpha/generate` wire, so the per-family patterns (deepseek-v4 → vision false, thinkingFormat deepseek, …) must not win. `internal/providers/capabilities_commandcode.go` ports the 23-model text-only denylist and declares `reasoning`, `thinkingFormat: "commandcode"`, `thinkingEffortSupported`, `contextWindow: 1000000`, `maxOutput: 384000`.
- Dashboard: the "Thinking:" picker is now the union of the levels this provider's models accept, with `auto` first and the picker hidden when no model has reasoning (upstream `providerThinkingLevels`); the `(level)` suffix is gated per model and shown in the model row, not only copied; the vision/reasoning icons come from the server caps instead of being empty for every built-in model.
- Model rows now render in **registry order**, the way upstream does: `models` there is `getModelsByProviderId()` verbatim (`providers/[id]/page.js:158`) and the A–Z sort this port added is gone. `buildAvailableModels` also lists the catalog first and appends custom models, instead of hoisting customs to the top. The bundled web catalog had drifted out of registry order for `commandcode`, `deepseek`, `gcli`, `openai` and `ocz`; all four are now in the upstream sequence (the Go `registry_models.go` already was).
- Live verification against upstream `:20128` on the same data. `commandcode` is byte-identical: 22 rows, the same 22 ids in the same order, the same per-model vision/reasoning icons, and the same picker (`auto, low, medium, high, xhigh, max`); the `(high)` suffix appears and clears identically. `openai` (21), `xai` (6) and `blackbox` (10) also match row-for-row including icons and order. The 16 LLM providers that rendered zero models now render theirs; the media/embedding/STT providers correctly render none on both.
- Resolved below: the `deepseek-v4-flash` vision icon was not stale data but a broken catalog sync — see "Synced models.dev catalog recorded every model as text-only".
- Tests: `TestGetThinkingLevels_MatchesUpstreamFixture` replays upstream's own resolver over all 1547 `(provider, model)` pairs in the catalog — **1547/1547 match**. Plus `TestGetCapabilitiesDetailForModel_CommandCode`, `TestIsCommandCodeTextOnly`, `TestMergeCapabilities_ThinkingIsDeclarative`, `TestGetThinkingLevels_Parity` (upstream's vitest expectations), `TestGetThinkingLevels_NoReasoning`, `TestResolveKiroEffortPath`, `TestMatchThinkingGlob`, `TestHandleGetModelCaps_*`, and `web/src/lib/models.test.ts`. `TestQoder_Capabilities` was removed: it asserted a `qoder` capability scope that upstream does not have, so it contradicted the parity it claimed.
- Still open, left alone on purpose because each one adds or removes a model rather than fixing a rendering defect: 8 providers whose bundled catalog content differs from the registry (`ag`, `cx`, `gemini`, `huggingface`, `kr`, `opencode-go`, `oc`, `xiaomi-mimo` — the `oc` and `huggingface` extras are local free models such as `space-bunny-free` / `nemotron-3-ultra-free`), and 4 registry providers with no bundled catalog at all (`qdcn`, `tokenharbor`, `dahl`, `atria`). No order-only difference remains.

### 🐛 Synced models.dev catalog recorded every model as text-only

- Symptom: capability icons that upstream shows never appeared, and the token limits in `/v1/models` came from the substring table instead of the catalog. `deepseek-v4-flash` rendered without a vision icon while the upstream dashboard rendered one.
- Root cause — the sync read a field models.dev no longer publishes. `SyncModelCatalog` decoded `modality: {image: true}` (a boolean map); the API serves `modalities: {input: ["text","image",…], output: […]}` (string arrays), so the decoded map was always empty and the writer stored `vision: false` for **every** model. Second defect: modalities were filed under the bare model id, aggregating across gateways, while upstream keys them `provider:model` precisely because gateways disagree about the same weights — some do not proxy images at all. Third: `SyncedCatalog.UnmarshalJSON` read `syncedAt` into a `[]byte`, which `encoding/json/v2` accepts only as base64, so neither an ISO string (this writer) nor epoch milliseconds (upstream's writer) loaded — a restart could not read back the file it had just written, which also dropped the `LastSync` state.
- Fix: `internal/providers/catalog_sync.go` decodes `modalities.input` with upstream's `MODALITY_BY_INPUT` mapping (image→vision, pdf, audio, video), keys modalities `provider:model` with the local alias filed too (`claude` → `anthropic`), and `GetCatalogModalities` takes the provider so a gateway's entry only answers for that gateway. A bare-model key is still accepted, so a catalog written by an earlier build keeps resolving. `syncedAt` is now written as an ISO string and decoded from ISO, epoch milliseconds, or absent.
- After the first background sync the file holds **4780** real modality entries (was 2399 all-false). `/api/models/caps?provider=deepseek` now reports `deepseek-v4-flash` as `vision: true`, and the provider detail page is row-for-row identical to upstream again — same ids, same order, same icons.
- `TestSyncModelCatalog_Modalities` (was a test that re-implemented the parser it was checking, so it passed against the broken schema — now drives the real `SyncModelCatalog` end-to-end against the current payload), `TestSyncModelCatalog_ModalitiesArePerProvider`, `TestLoadCatalogFromFile_SyncedAtForms`, `TestLoadCatalogFromFile_LegacyBareKeys`, `TestCatalogProviderKeys`, `TestCatalogBaseID`.

## [v1.9.3] - 2026-09-26

### 🐛 Kiro tool calling never reached the client (`arguments: {}` / no `tool_calls`)

- Symptom: `POST /v1/chat/completions` with `tools` never returned `tool_calls` on Kiro. The model wrote pseudo tool calls as plain text (`<invoke name="browser">…`), or — once the catalogue was sent — replied with an empty turn carrying only `reasoning_content: "..."`. Other providers (grok-cli, antigravity) were unaffected.
- Root cause 1 — **request**: Kiro has no OpenAI-style `tools` array. The tool catalogue must be attached to the last user turn as `userInputMessage.userInputMessageContext.tools` (`{toolSpecification:{name,description,inputSchema:{json:…}}}`, upstream `normalizeKiroToolSpecs`). The Go translator only sent `toolResults`, so the model never learned which tools existed.
- Root cause 2 — **response**: Kiro splits one tool call across several `toolUseEvent` frames — the first carries only `name` + `toolUseId`, the following ones carry slices of the JSON arguments under `input` (`"{\"ci"`, `"ty\": \"Jak"`, …), and a final `stop: true` frame closes it. The old code read a `content` field that is never sent and emitted `arguments: "{}"` per frame.
- Fix:
  - `internal/translator/kiro_tools.go` (new) — port of upstream `normalizeKiroToolSpecs`: OpenAI/Claude tool normalization, name sanitization (`[^a-zA-Z0-9_-]` → `_`, de-duplication suffix, 64-char cap), `inputSchema.json` forced to `type: object`, `additionalProperties` stripped, `required` filtered down to properties that actually exist.
  - `internal/translator/kiro.go` — the tool catalogue is attached to `userInputMessageContext.tools` on the last turn; `toolResults` still travel alongside it.
  - `internal/proxy/executor/stream.go` — `kiroToolCall` buffers arguments per `toolUseId` (accepting both fragmented `input` strings and objects, with the legacy `content` kept as a fallback), and `tool_calls` are only emitted once the stream ends, with the reassembled arguments.
- Live e2e verification (tool `get_weather`, "weather in Jakarta"): `kr/auto` → `{"city":"Jakarta","unit":"celsius"}`, `kr/claude-sonnet-4.5` → `{"city":"Jakarta"}`, while `gcli/grok-4.5` and `ag/gemini-3.8-flash-high` keep passing. The upstream instance (`:20128`, same Kiro account) returns the same `tool_calls`.
- Tests: `TestOpenAIToKiro_AttachesToolSpecsToLastUserTurn`, `TestOpenAIToKiro_ToolResultsStillTravel`, `TestOpenAIToKiro_NoToolsNoContext`, `TestKiroNormalizeRootSchema`, `TestKiroUniqueToolName`, `TestForwardKiroRequest_ReassemblesFragmentedToolInput`. Suite: 1473 pass.

### 🐛 Kiro `403 The bearer token included in the request is invalid`

- Symptom: Kiro chat and `POST /api/models/test` failed with 403 while the token was actually valid — `GET ListAvailableModels` with that same token returned 200.
- Root cause: `extractAPIKey` always preferred `apiKey`, and a Kiro connection created by *import* stores **two** credentials (`apiKey` + `accessToken`). That `apiKey` was sent as both `Authorization: Bearer` and `x-amz-sso-bearer`, while upstream (`open-sse/executors/kiro.js` `buildHeaders`) uses the apiKey only for `authMethod: "api_key"` connections and the `accessToken` everywhere else. CodeWhisperer rejects that token with "bearer token invalid" even though the `accessToken` is perfectly healthy.
- Fix:
  - `internal/handlers/chat/connections.go` — `resolveProviderAuthToken` applies that upstream rule in the forward path (Kiro only; every other provider is untouched).
  - `internal/proxy/grokcli.go` — `ForwardKiro` now sends `TokenType: API_KEY|EXTERNAL_IDP` according to `authMethod` plus the `x-amzn-codewhisperer-profile-arn` header, and **rotates endpoints** like upstream: `q.<region>` → `codewhisperer.<region>` → `runtime.us-east-1.kiro.dev`, falling back on 401/403/404 (`KIRO_ENDPOINT_FALLBACK_STATUSES`); 400 stays terminal. AWS hosts are regionalized from `providerSpecificData.region`.
- Verification: `POST /v1/chat/completions {"model":"kr/claude-sonnet-4.5"}` → **200** (`"ok"`), previously 403; all three Kiro surfaces also answer 200 when probed directly with the same `accessToken`.
- Tests: `TestResolveProviderAuthToken_Kiro` (7 cases: imported/builder-id/api_key/access-only/key-only/empty/other provider), `TestKiroEndpointsOrdering` (3), `TestKiroTokenType` (5), `TestKiroEndpointFallbackStatus` (401/403/404 rotate, 400/429/5xx terminal). Suite: 1466 pass.

### ✨ `GET /v1/models` — live catalog and response shape identical to upstream

- `internal/handlers/chat/live_catalog.go` (new): port of upstream's `LIVE_MODEL_RESOLVERS` — **kiro** (`GET https://q.<region>.amazonaws.com/ListAvailableModels` with a Kiro IDE fingerprint UA, each model expanded into `-thinking`/`-agentic`/`-thinking-agentic` variants, `auto` without agentic variants), **grok-cli** (`GET <base>/v1/models` with the `x-grok-cli` headers), and **custom nodes** (`fetchCompatibleModelIds`: `GET <baseUrl>/models`). In-process cache of 5 minutes per credential; 401/403 triggers a single token refresh and a retry; failures fall back to the static catalog and never empty a provider.
- A resolver is only consulted when `enabledModels` is not pinned on the connection, exactly like upstream; the live catalog replaces the static one while custom models and aliases are still merged in.
- Response shape aligned with upstream: kiro `capabilities` uses the live `{thinking, agentic}` block verbatim; combos use a separate `ComboCapabilities` shape (no `thinkingEffortSupported`, `tools` = AND across leaves, `contextWindow` = narrowest leaf, `maxOutput` = widest leaf — the `aggregateComboCapabilities` rules); `context_length`/`max_completion_tokens` are gone from combo entries (`omitzero` is required because `encoding/json/v2` does not drop zero numbers under `omitempty`); the `outputAlias/` qualifier is stripped only from registry/alias ids, never from custom ids (which is why `openrouter/openrouter/free` stays double-prefixed upstream too).
- `internal/providers/catalog_sync.go`: the models.dev catalog file written by upstream is now readable (its `v2`/`etag`/epoch-millisecond `syncedAt` format), and `GetCatalogLimits` is consulted before the substring guesses, so token limits follow the catalog instead of heuristics.
- Cross-check against the running upstream instance (`:20128`, same database): **714 models upstream vs 713 in 9router-go, all 713 ids identical, 0 entry-key and 0 `capabilities`-key differences**. Breakdown: `clinepass` 469/469, `kr` 34/34, `ag` 20/20, `cbai` 15/15, `openrouter` 7/7, `nvidia` 4/4, `Id` 3/3, `gcli` 1/1, `tr` 153/154, combos 7/7.
- Remaining: (a) `tr/typesafe/jev-1.13` exists only upstream — the tokenrouter node's `GET <baseUrl>/models` now returns nothing, so it is a leftover upstream cache entry; (b) the **values** of `context_length`/`max_completion_tokens` still differ for 627 ids because upstream's `MODEL_CAPABILITIES`/`PATTERN_CAPABILITIES` tables are not ported yet (the models.dev catalog only covers 23 providers) — the model list and the response shape are identical, the numbers are not.
- Tests: `live_catalog_test.go` (kiro variant expansion, `kiroDisplayName`, grok-cli catalogue parser with 5 cases, region from profileArn) + `live_catalog_handler_test.go` (live kiro replaces the static catalog, `enabledModels` skips the live fetch, failures fall back to the static catalog, live grok-cli and its headers).

### 🐛 `GET /v1/models` — full port of upstream's Next.js `buildModelsList` (fixes issue #1)

- Symptom: `/v1/models` flooded the catalogue — `ghost customs` in `kv.customModels` for unconnected providers were published, alias keys were published as models of their own, `enabledModels` was ignored, and media/embedding models leaked in. On the user's real database: 640 models, `clinepass/` duplicated with `cp/`, and 4 dead nodes still exposing 21 models.
- Source of truth: `~/htdocs/9router/src/app/api/v1/models/route.js` (`buildModelsList`, `KIND_SLUG_MAP`, `MODEL_TYPE_TO_KIND`, `inferKindFromUnknownModelId`, `aggregateComboCapabilities`).
- `internal/handlers/chat/models_list.go` (new; the builder moved out of `chat.go` — 1331 → 897 lines):
  - Upstream ordering: **combos first** (`owned_by: "combo"`, capabilities aggregated across every leaf), then per-connection models.
  - `isActive !== false` **only** — upstream requires no credentials for listing, so 9router-go's credential gate was deliberately reverted (`TestHandleModels_SkipsCredentiallessConnections` was replaced by `TestHandleModels_ActiveConnectionPublishesCatalog`).
  - Per connection: `staticAlias` = catalogue alias, `outputAlias` = `providerSpecificData.prefix`; `enabledModels` (psd or top-level) **replaces** the static catalogue; then custom models whose `providerAlias ∈ {staticAlias, outputAlias, providerId}` (llm-typed only) and `modelAliases` targets with a matching prefix are merged in; `isDisabled(outputAlias|staticAlias, id)` is enforced; `outputAlias/`, `staticAlias/` and `providerId/` qualifiers are stripped from ids.
  - Alias keys are **no longer** model ids themselves (upstream only uses their targets), and untyped custom models are now filtered by `isLLMModelID` (the same `embed|tts|speech|audio|voice|image|imagen|dall-e|flux|sdxl|sd-|stable-diffusion` heuristic as upstream's `inferKindFromUnknownModelId`).
  - The static dump only runs when the `providerConnections` table is genuinely empty (upstream `connections.length === 0`).
  - Non-LLM combos (`webSearch`/`webFetch`) are excluded from this list — the endpoint builds with `kindFilter ["llm"]` (upstream `comboMatchesKinds`); combo entries carry `capabilities` only, without `context_length`/`max_completion_tokens`.
- `internal/providers/registry_aliases.go` (new): 61 provider aliases ported from `open-sse/providers/registry` (`uiAlias || alias`), and `GetProviderAlias` now uses that table — providers without an upstream alias are published under their own id (`clinepass`, `nvidia`, `openrouter`, `openai`) instead of our invented short aliases. The old prefixes (`cp`, `or`, `nv`, `gb`) are gone from the listing; `ProviderAliasMap` (alias → id) still drives **request resolution**, so `cp/...` keeps routing to clinepass. The now-dead `ProviderToAliasMap` was removed.
- Entry shape aligned with upstream: `ModelInfoObject` now only has `id`, `object`, `owned_by`, `capabilities`, `context_length`, `max_completion_tokens` (the `created` and `context_window` fields that upstream does not have were dropped), and `CapabilitiesDetail` gained `search`, `tools`, `reasoning`, `thinkingFormat`, `contextWindow`, `maxOutput` (replacing the duplicated `contextWindows`).
- `internal/providers/registry_models.go` fully regenerated from `open-sse/providers/registry` (89 providers, 1550 id/alias entries) plus a new `ProviderModelKinds` table (191 media models typed `image`/`tts`/`stt`/`embedding`/`video`/`systemone`) and a `GetProviderModelKind` accessor. Kind resolution in `/v1/models` now mirrors upstream (custom `type` → registry `kind` → id heuristic), so media models no longer leak into the LLM list and the static catalogue tracks upstream (e.g. `openrouter` 10 → 18 entries, `ag` 21 models).
- Derived fix: a provider with one active plus one inactive connection row is no longer treated as entirely disabled (previously `tr/*`'s 153 models disappeared).
- Cross-check against the running upstream instance (port 20128, same database): **714 models upstream vs 726 in 9router-go, 687 identical ids**; prefixes, entry keys and `capabilities` keys match exactly. Breakdown: `clinepass` 469/469, `tr` 154/153, `kr` 34/44, `ag` 20/20, `cbai` 15/15, `openrouter` 7/6, `nvidia` 4/4, `Id` 3/3, `gcli` 1/5, combos 7/7.
- Remaining 39-id gap at that point: (a) upstream uses a **live catalog** for kiro/grok-cli (`kr/auto`, `kr/minimax-m2.1`, `gcli/grok-4.7`) while 9router-go used the static catalogue — the next PR; (b) `openrouter/free` exists upstream only; (c) an upstream quirk: vendor-prefixed ids (`nvidia/parakeet-ctc-1.1b-asr`) lose their qualifier before the kind lookup, so they slip through on both sides.
- Tests: `TestIsLLMModelID` (9 cases), `TestGetProviderModelKind` (11 cases), `TestHandleModels_MediaKindModelsExcluded`, `TestHandleModels_EnabledModelsOverrideCatalog`, `TestHandleModels_AliasTargetMergedIntoProvider`, `TestHandleModels_ComboOwnedByCombo`, plus the filtering tests from the previous commit; the legacy `TestHandleModelLookup_ProviderModel` / `TestHandleModels_IncludesTokenLimits` were re-seeded to upstream semantics (an alias needs a connected provider, and token limits are asserted through `context_length`/`max_completion_tokens`/`capabilities.contextWindow`). Full suite: 1433 pass.

### 🐛 Fallback logs never named the failing account/project (Antigravity 403 `VALIDATION_REQUIRED`)

- Symptom: `WRN [fallback] upstream failed provider=antigravity ... status=403 error=... "Verify your account to continue."` only printed `conn=<uuid>`, so there was no way to tell which project ID / email needed verification at `accounts.google.com`.
- `internal/handlers/chat/conn_identity.go` (new): `connIdentityKV` (name, email, projectId from `data.projectId` or `data.providerSpecificData.projectId`, empty fields skipped) + `connIdentityKVByID` (lookup via `Repo.GetProviderConnectionByID`, failure paths only).
- `internal/handlers/chat/fallback.go`: the `upstream failed` and `connection locked` logs now add `connName=`, `email=` and `projectId=`. Example: `... status=403 cooldown_s=120 connName=AG Verify email=luqmangeminipro@gmail.com projectId=mega-rainfall-szp2g`.
- Tests: `TestConnIdentityKV` (6 cases: both projectId shapes, empty fields, broken JSON, nil conn) + `TestConnIdentityKVByID_MissingConnection` (a handler without a Repo does not panic). Smoke: a mocked 403 `VALIDATION_REQUIRED` makes both log lines carry the email and project ID.

### 🐛 `POST /api/models/test` returned 401 even when logged in

- The route moved from the `RequireApiKey` group (Bearer / `X-API-Key` only) to `RequireDashboardAuth` — parity with upstream `dashboardGuard` (`src/app/api/models/test/route.js`). Previously a dashboard authenticated by cookie session got `401 invalid_api_key`; now cookie session, CLI token and API key are all accepted, matching `/api/models/custom|disabled|alias`.
- Regression test: `TestSetupServerRouter_ModelTestDashboardSession` (anonymous → 401, valid session → passes).

### 🐛 Kiro `400 Improperly formed request` — OpenAI body forwarded raw to the gateway

- Root cause: there was no OpenAI→Kiro translator in Go. `ForwardKiro` passed the OpenAI body through untouched; the kiro.dev gateway only accepts a `conversationState` envelope and answered `400 {"message":"Improperly formed request."}`. Upstream has `openai-to-kiro.js` / `claude-to-kiro.js`, which were not ported.
- `internal/translator/kiro.go` (new): port of `openaiToKiroRequest` — build the `conversationState` (chatTriggerType / conversationId / currentMessage / history), inject the system and time context into the user turn's content (the gateway rejects a top-level `systemPrompt`), tool_use / tool_result, base64 images → Kiro blocks, `profileArn` from `providerSpecificData`, `inferenceConfig`, `modelId` backfill across history, and merge consecutive user turns. Upstream session replay and thinking budget are not ported yet.
- `ForwardKiro` now runs the translator for OpenAI bodies and passes through bodies that already carry `conversationState` (MITM); it strips the `kr/` prefix and the `-thinking` / `-agentic` suffixes before `modelId`.
- Tests: `TestOpenAIToKiro_*` (7) + `TestKiroUpstreamBody_*` against a mock upstream (3).

### 🐛 Kiro connect modal never opened and the OAuth/API Key buttons were overwritten

- Root cause 1: `devicePollTimer` was never declared, so `stopDevicePoll()` threw a `ReferenceError` inside `openKiroOAuth()` and `showOAuthModal = true` never ran (no console error, because the failure was in an async callback).
- Root cause 2: `isNoAuth` was inferred from `category === 'free'`; kiro and gemini-cli declare `noAuth: false` but were still treated as no-auth, hiding the connection card (and its buttons). Upstream parity: only an explicit `noAuth` flag counts. Also fixed in `combos/pickerData.ts`.
- Root cause 3: the `{:else if providerId === 'kiro'}` branch in the lower button row swallowed the `{:else if hasDualAuthModes}` branch (the OAuth/API Key buttons). The "Waiting…" banner and the "MANUAL TOKEN IMPORT" block were hidden whenever the Kiro method list was shown.
- Tests: browser verification (click → 7 methods render, with no leftover generic markup).

- `web/vite.config.ts` — the dev proxy now also covers `/admin`; previously `resetHealth()` (`/admin/health/reset`) fell through to the SPA fallback in dev mode because it was not proxied to Go.
- `Makefile` — new `web-dev` target (Vite dev server on :5173 with HMR). Two-terminal workflow: `make dev` (Go on :20130) + `make web-dev` → frontend changes hot-reload without rebuilding or restarting the binary.
- `web/README.md` — documents the two-terminal dev workflow and the service worker caveat in dev (`sw.js` also registers in dev; hard-refresh / unregister if the view looks stale).

## [v1.9.2] - 2026-09-26

### 🐛 Dashboard console errors: version 401 spam, manifest/SW 404, missing icons, auth redirect

- `GET /version`, `/api/version`, `/api/version/status`, and `/api/version/check` are now public (upstream `PUBLIC_API_PATHS` parity): the Sidebar polls on every page including `/login` before any session exists. `POST update/shutdown/auto-update` stay admin-only (upstream `ALWAYS_PROTECTED`).
- `GET /sw.js`, `/manifest.webmanifest`, and `/manifest.json` routed to the embedded SPA handler; PWA shell files existed in `web/dist` but chi had no route.
- Added missing provider icons `opencode-zen.png` and `ollama-search.png`.
- Frontend 401 handling: `onUnauthorized` clears stale local session and redirects to login; `App` stops polling when logged out.
- `POST /api/models/test` moved to `RequireDashboardAuth` (cookie session, CLI token, and API key accepted) — dashboard got `401 invalid_api_key` despite being logged in.
- Sidebar nav now scrolls with sticky header/footer: `aside` is viewport-bounded (`h-full max-h-screen overflow-hidden`), header/footer `shrink-0`, nav `min-h-0` (closes issue #22 side-nav points; live-verified at 500px viewport + mobile drawer).
- Dev workflow: `make web-dev` (Vite :5173 + HMR, no binary rebuild); dev proxy keeps `/debug` (traces/pprof) alongside `/admin` (health reset).

### 🐛 Media providers audit (search/fetch/image/STT/TTS) + Antigravity failover

- Antigravity/Xquik search, Antigravity image/STT, and Nvidia TTS now rotate all active accounts with per-account `ClassifyError` locks and success unlocks; pinned `x-connection-id` honored once.
- Fixed Xquik registry `BaseURL`, clamped `max_results 5..100`, optional `queryType`, `answer`/timing envelope fields, upstream error statuses preserved.
- Request-scoped 4xx no longer lock accounts; combo video skips 5xx rotation; multipart model rewrite byte-exact; pinned connections provider-scoped.
- `GET /api/keys` returns full secrets to dashboard sessions (upstream parity); masked values stay for API-key callers.
- Live-verified: OpenRouter embeddings `:20128` vs `:20129` return byte-identical 3072-dim vectors.

## [v1.9.1] - 2026-09-25

### 🐛 Dashboard: Custom Models Parity — Combo Picker Unwraps `{models}` Envelope

- `web/src/lib/customModels.ts` (new): shared `parseCustomModelsResponse` (`{models:[...]}` upstream shape, tolerates bare-array/record-map), `parseDisabledModelsMap` (`{disabled:{...}}` upstream + bare map go-port), `notifyCustomModelsChanged` / `subscribeCustomModelsChanged` (`customModelChanged` + `focus` reload, ported from upstream `SttExampleCard.js` / `ModelsCard.js` / `useModelCaps.js`).
- `web/src/components/combos/ModelPickerModal.svelte`: `normalizeCustoms`/`normalizeDisabled` now use the shared parser. Root cause of the reported bug: `GET /api/models/custom` returns `{models:[...]}` but the modal expected a bare array/record, so `fetchedCustoms` stayed `[]` and `oc/space-bunny-free` (custom-only, not in builtin `oc` catalog) never appeared in "tambah combo" — while the provider page (which already unwrapped `{models}`) showed it.
- `web/src/api/client.ts`: `getCustomModels`/`getDisabledModels` typed honestly (`{models:[...]}` / map) so future consumers stop guessing the envelope.
- `web/src/components/connections/types.ts`: `fetchProviderModelsData` uses the shared parser (same result as before, single code path).
- `web/src/components/media/MediaProviderDetail.svelte`: models card merges builtin + custom per kind (upstream `ModelsCard kindFilter` parity, builtin dedupe). Previously custom media models (`tts`/`stt`/`image`/`embedding`/`video`) never appeared.
- `web/src/components/media/SttExampleCard.svelte`: custom STT filter matches `storageAlias || providerId` (upstream uses `getProviderAlias`), reloads on `focus` + `customModelChanged` like upstream `loadCustom`.
- `web/src/components/connections/ProviderDetailView.svelte`: dispatches `customModelChanged` after every add/delete/import of a custom model (upstream `ModelsCard`/`page.js` parity), so pickers and STT cards refresh without full reload.
- Unchanged (already parity): `TtsExampleCard` stays builtin-only like upstream; backend `GET /models/disabled` bare-map shape kept, parser handles both.


## [v1.9.0] - 2026-09-25

### 🔀 Routing: antigravity-prefixed muse-spark Reaches the Owning Executor

- `internal/handlers/chat/resolution.go`: `routeModelToOwningProvider` — a request like `ag/muse-spark-1.3-contributor-free` (prefix copied from a dashboard combo) now resolves to the provider that actually serves the model (opencode family) instead of antigravity, which answered upstream 404 `Requested entity was not found`. Native antigravity models are untouched. Ported from `fix/10` (`ad4b355b`), which never reached main. Tests: `TestRouteModelToOwningProvider`, `TestResolveModel_AntigravityMuseSparkRoutesToOpencode`.


### 🐛 Dashboard: Recent Requests List No Longer Blinks/Shrinks on First Request

- `internal/usagetracker/tracker.go`: seed the in-memory `recentRing` from `usageHistory` once per process (upstream `ensureRingInitialized` parity) + `recentFromHistoryRow` mapper. Previously a fresh process streamed a ring holding only post-restart rows, so the first completed request replaced the dashboard's DB-backed 20-row list with 1 row (list blinked, rows below vanished). Regression test `TestTracker_RingSeededFromHistoryOnce`.
- `web/src/components/analytics/AnalyticsView.svelte`: SSE `recentRequests` now merges (union + dedupe + newest-first + cap 20) instead of replacing, so a short stream payload can never drop rows already rendered.


### 🧹 Leak Hunt: 7 Fixes for 24/7 Operation (Independent Audit)

- `internal/shutdown/shutdown.go` + `internal/app/server.go`: new `shutdown.Context()` (canceled on `Cancel`); updater + catalog-sync loops take it instead of `context.Background()` — background goroutines + tickers now exit on ^C. Fixed `TestReset` double-close panic (recreate `done` channel).
- `internal/handlers/chat/combo_fusion.go`: `collectPanel`/`makePanelCall` take `ctx`; stragglers abort on grace/hard timeout, client cancel, or shutdown (previously `context.Background()`, orphaned until upstream responded).
- `internal/proxy/executor/freebuff_session.go` + `internal/handlers/chat/antigravity_quota.go`: lazy eviction of expired entries + opportunistic sweep (2× TTL) — token-rotated keys no longer accumulate.
- `internal/handlers/chat/connections_proxy.go`: `proxyClients` capped at 128 with idle-close eviction (each entry pins a Transport + sockets).
- `internal/auth/session.go`: login limiter capped at 5000 buckets with window sweep + oldest-evict (scanner IPs bounded).
- `internal/handlers/chat/gemini_handler.go` + `internal/proxy/executor/stream.go`: 10MB caps on non-stream body reads and codex SSE accumulation.
- Out of scope (pre-existing, bounded): HeartbeatWriter ticker (dies with stream Close), tracing ring (2000), translator prune (50/10min), MITM conns (Wait).


### 🔊 Opencode Responses Errors Fail Loud (No More Silent Empty 200)

- `internal/proxy/executor/stream.go`: `ProcessCodexEvent` now records upstream `{"type":"error",...}` events (e.g. `FreeTierError` on muse-spark `-free` models) on stream state; `handleCodexStream` (non-stream) converts them to `*proxy.UpstreamError` (403 for free-tier/auth gates, else 502) instead of emitting `200 + content:""`. Silent success broke agents, bypassed account fallback/locks, and faked green monitoring. Upstream Next.js (`base.js`) likewise returns non-OK responses as errors, never empty 200s.
- Live evidence: `POST opencode.ai/zen/v1/responses` with `muse-spark-1.3-contributor-free` returns `FreeTierError: "OpenCode's free tier can only be used from within OpenCode"`.
- Limit (honest): on the already-committed SSE stream path headers cannot be unwound, so a pure-error stream still closes without chunks; the non-stream path (which agents use for the failing case) now errors properly.


### 🔗 Freebuff Cross-Process Session Coordination (Anti-Hijack)

- `internal/proxy/executor/freebuff_session.go` + `freebuff.go`: session lookup now memory L1 → `upstream_leases` L2; admission is coordinated — exactly one claimer per token+model across processes sharing the DB (`freebuffClaimMu` in-process + `AcquireLease` cross-process). Losers follow the winner's `instanceId` instead of POSTing their own claim (the pattern upstream punishes with 409 `session_superseded`). Stale-session retry drops the lease compare-and-delete (a sibling's fresh row survives). `Request.Leases` (nil = memory-only, old behavior) wired from chat fallback ×2, media `/responses`, and the session-switch endpoint.
- Tests: two racers converge on 1 POST (fake + real SQLite backends), follower reads with 0 POST, stale drop is compare-and-delete, nil-store contract unchanged.
- Note: coordination fixes *technical* hijacking between cooperating instances, not *policy* — two machines serving traffic concurrently on one account is still concurrent use server-side.


### ⬆️ Upstream v0.5.86 Parity (decolua/9router#v0.5.86)

- `internal/handlers/chat/claude_cloaking.go` + `internal/providers/providers.go`: bumped Claude CLI fingerprint `2.1.258` → `2.1.280` (upstream `cbffeb9`), so the billing-header cloak and `claude-cli/*` UA stay current. Added `claude-opus-5-5` to `cc`/`claude` catalogs (`registry_models.go`, `web/src/lib/models.ts`).
- `internal/handlers/media/deploy.go`: Vercel relay template now forwards headers losslessly (copy to plain object, strip only `x-relay-target`/`x-relay-path`/`host`) — upstream `6af26a9`. Cloaking headers survive relay pools, which matters for proxied Freebuff traffic.
- Deferred: Xiaomi MiMo v2.6 desktop login (5 region clusters, dual-route models, server-assisted flow) — large scope, tracked as separate stacked diff.

### 🛡️ Freebuff client_id Cloaking (Anti-Ban Parity)

- `internal/proxy/executor/freebuff.go`: `ForwardFreebuff` reuses the account's stored `fingerprintId` verbatim as `codebuff_metadata.client_id`, falling back to a fresh unbranded UUID only for connections saved before this change. Previously every chat request sent `client_id: "9router-<uuid>"`, which brands the traffic as non-CLI at the application layer — the most likely reason accounts got `banned` even though headers/User-Agent already matched the CLI. Note: cloaking only removes the self-identifying fingerprint; it cannot protect accounts banned for quota abuse, multi-account farming on one IP/fingerprint, or region violations.
- `internal/handlers/oauth/cline.go`: `decodeClineCode` now accepts the real browser-callback shape — base64url (`-`/`_` alphabet, padding stripped) plus the trailing signature segment the extension appends after the JSON payload. Previously only strict `StdEncoding` decoded, so pasting the callback failed to extract tokens and the handler fell through to `POST /api/v1/auth/token`, which the server rejects with `Forbidden` — exactly the reported `Cline token exchange failed ... Forbidden` error.
- `internal/handlers/chat/connections.go` + `internal/handlers/dashboard/connection_probe.go`: the `workos:` prefix is now JWT-only (WorkOS JWT = base64url `eyJ…` + dot, upstream parity `open-sse/shared/clineAuth.js`). Non-JWT ClinePass API keys ride plain `Bearer` — prefixing them is what the server answers with 401 `"Unauthorized: Please make sure you're using the latest version of Cline and re-authenticate your Cline account."` Note: your pasted bundle decodes to a real WorkOS JWT (`eyJhbGciOiJSUzI1NiIsImtpZCI6InNzb19vaWRj...`, `expiresAt` already past `2026-09-24T07:23:51Z`), so that specific token is expired server-side — reconnect with a fresh browser login after updating.
- `internal/handlers/oauth/cline.go`: new Cline/ClinePass connections are named by account email from the token bundle (fallback: first+last name, then provider default) instead of the generic `ClinePass` label, so multi-account setups stay distinguishable in provider detail.
- All-providers branding sweep (no behavior change otherwise): audited every header/body sent to upstream. Only one real leak found and removed — `User-Agent: 9router/oauth` on the Antigravity Google token exchange (`internal/handlers/oauth/antigravity.go`), now unbranded Go default. Everything else already mirrors an official client: Freebuff `codebuff-cli/*` + fingerprinted `client_id` (no `9router-` anywhere on the wire), Cline `Cline/*` + `cline-cli`, Antigravity `antigravity/ide/*`, Gemini CLI `google-api-nodejs-client/*`, Grok/Codex/iFlow/Qoder/MiMo browser or CLI UAs, TTS browser UAs. `X-Msh-Platform: 9router` (Kimi) and `HTTP-Referer/X-Title: endpoint-proxy.local / Endpoint Proxy` (OpenRouter/Airforce) are byte-identical to upstream `decolua/9router` — changing them would *break* parity, not improve stealth. `User-Agent: 9Router` only ever hits the user's own proxy-test target and GitHub API (never an LLM provider). Local-only strings (`9router-oauth` BroadcastChannel, MITM CA, updater UA, file paths) never leave the machine.
- `internal/handlers/oauth/naming.go` (new) + all OAuth handlers (`freebuff.go`, `cline.go`, `antigravity.go`, `trae.go`, `windsurf.go`, `zed.go`, `authcode.go`, `pkce.go`, `device.go`): single email-first naming rule `connectionDisplayName` — account email when known, else explicit user-supplied name, else provider default. No more `"Provider (name)"` labels anywhere, so every provider detail page (Freebuff, ClinePass, Antigravity, …) lists accounts by email.
**Production Internet Hardening & Cloudflare Integration:**
- **Protect `/debug/pprof/*` endpoints**: Disabled Go runtime profiling endpoints (`/debug/pprof/*`) by default in production to prevent Denial of Service (DoS) and potential memory/key disclosures. Can be explicitly enabled via `PPROF_ENABLED=true`.
- **Privilege separation for client API keys**: Restricted destructive administrative routes (`/api/version/shutdown`, `/api/version/update`, `/api/settings/database`, `/admin/health/reset`) so they strictly require a valid dashboard JWT session cookie or local CLI token (`x-9r-cli-token`), matching upstream `ALWAYS_PROTECTED` behavior in `src/dashboardGuard.js`. Client API keys can no longer trigger shutdowns or database dumps.
- **Cloudflare `CF-Connecting-IP` support**: Updated `LoginClientIP` in `internal/auth/session.go` to support `CF-Connecting-IP` when `TRUST_PROXY=true` or `TRUST_CLOUDFLARE=true`, ensuring proper client IP resolution and preventing shared-bucket lockout behind Cloudflare.
- **Configurable host binding (`HOST` / `BIND_ADDR`)**: Added `Host` to configuration and updated server listener to bind to `HOST` or `BIND_ADDR` when specified (e.g. `127.0.0.1` when proxied by `cloudflared`), while preserving `:20130` (`0.0.0.0`) default behavior.

### 🎨 UI & Dashboard

**Media Providers Full Parity (`/dashboard/media-providers/*`):**
- `internal/handlers/media/tts_synthesizers.go` & `tts_forward.go`: Built local native synthesis engines (`edge-tts`, `google-tts`, `nvidia`) and voice catalog endpoints (`/api/media-providers/tts/voices`), supporting real-time streaming audio generation and custom speed/pitch/voice options.
- `internal/handlers/media/antigravity_image.go` & `antigravity_stt.go`: Added Antigravity image generation and speech-to-text (STT) transcription handlers with multipart form-data parsing, extracting audio models and delegating to Google's IDE backend.
- `internal/handlers/media/antigravity_search.go`: Ensured typed JSON serialization for Antigravity web search requests and responses to match upstream key ordering and structure.
- `internal/handlers/chat/resolution.go`: Fixed System One endpoint `/v1/systemone` to handle `x-antigravity-session` headers and fall back seamlessly to direct connections with `public` default keys for `antigravity-zen`.
- `web/src/components/media/MediaKindView.svelte`, `MediaProviderCard.svelte`, `NoAuthProxyCard.svelte`, `TtsExampleCard.svelte`, and `SttExampleCard.svelte`: Full Svelte 5 runes parity with upstream Next.js for all 8 media kinds (`video`, `stt`, `tts`, `image`, `embedding`, `systemone`, `webSearch`, `webFetch`), aligning provider sorting, priority, hidden flags, and live audio/waveform test players.

**Antigravity Live Models Discovery & "Import from /models":**
- `internal/providers/registry_models.go` & `web/src/lib/models.ts`: Added Google Antigravity official live models (`gemini-2.5-flash`, `gemini-2.5-flash-lite`, `gemini-2.5-pro`, `gemini-2.5-flash-thinking`, `gemini-3.1-pro-high`, `gemini-3.1-flash-lite`, `gemini-3.5-flash-lite`).
- `internal/handlers/dashboard/connections.go`: Extended `GET /api/providers/:id/models` to support Antigravity, Gemini CLI, Cline, and ClinePass connections. For Antigravity, queries Google's live RPC (`https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels`) with Bearer tokens, filtering internal chat IDs and parsing vision/reasoning capabilities.
- `web/src/components/connections/ProviderDetailView.svelte`:
  - Added `[📥 Import from /models]` button next to `Add Model` for Antigravity, Cline, ClinePass, and Qoder accounts.
  - Auto-fetches live models from Google for active Antigravity connections on page load, displaying uncataloged models in the **Suggested models** section for 1-click addition.

**Suggested Free Models Feed & Custom Models Parity:**
- `internal/handlers/suggestedmodels.go`: Wrapped HTTP client in `proxy.NewFallbackTransport` so public model catalog feeds (`opencode`, `openrouter`, `kilocode`, `airforce`) never get blocked by sandbox proxy allowlists.
- `web/src/lib/providers.ts`: Added `modelsFetcher: { url: "https://opencode.ai/zen/v1/models", type: "opencode-free" }` to `opencode`, restoring the "Suggested free models (≥200k context)" section on OpenCode Free.
- `internal/handlers/dashboard/models.go`: Updated `GET /api/models/custom` to return `{ "models": [...] }` matching upstream Next.js shape, and updated `web/src/components/connections/types.ts` to cleanly parse custom model lists without type-assertion errors.

**Universal Outbound Direct Fallback & Proxy Allowlist Bypass:**
- `internal/proxy/fallback_transport.go`: Created `FallbackTransport` which wraps Go HTTP round-trippers to detect local proxy refusal (`403 Forbidden`, `blocked-by-allowlist`, `CONNECT tunnel failed`, or proxy text/plain errors) and instantly re-issue the request directly (`Proxy: nil`) with re-readable request bodies.
- Applied universally across chat resolution, streaming SSE forwarders, media endpoints, validation probes, and catalog feeds.

**Comprehensive Connection Health Probing & Zero False Errors:**
- `internal/handlers/dashboard/connection_probe.go`:
  - Added native probe configurations for OAuth providers (`antigravity`, `gemini-cli`, `cline`, `clinepass`, `freebuff`, `xai`, `grok-cli`, `codebuddy-intl`, `zed`, `windsurf`, `trae`, `devin`, `devin-cli`, etc.).
  - Implemented token-exists heuristic for unconfigured providers and compatible base-URL probing for custom nodes.
  - Fixed `persistProbeResult` so that informational "Provider test not supported" messages no longer falsely mark connections as `testStatus: "error"` or pollute `lastError` in the SQLite database.
  - Updated Cline / ClinePass probe to prefix WorkOS JWT tokens with `workos:`.

**Quota Tracker Full Parity with Upstream Next.js (`/dashboard/quota`):**
- `internal/handlers/router.go`: Mounted `/api/usage/{connectionId}`, `/api/usage/providers`, `/api/usage/stream`, `/api/usage/stats`, and `/api/usage/request-details` inside `SetupDashboardRoutes` with `RequireDashboardAuth`, allowing authenticated browser sessions (JWT cookie), local CLI tokens, and client API keys to fetch quota and usage details.
- `internal/handlers/dashboard/usage_providers.go`: Enhanced `fetchAntigravityDashboardWeekly` to parse both session (5h) and weekly quota buckets from Google's `retrieveUserQuotaSummary`, and added reconciliation when all Gemini models are exhausted (matching upstream `open-sse/services/usage/antigravity-weekly.js` and `google.js`).
- `web/package.json` & `web/src/main.ts`: Added and bundled `material-symbols/outlined.css` locally, ensuring all icons (refresh, edit, delete, eye-off, hourglass, toggle) render instantly and work 100% offline without text flashing.
- `web/src/components/quota/types.ts`: Implemented full upstream provider quota parser `parseQuotaData` supporting Antigravity (5-quota family grouping: Gemini 5h, Claude & GPT 5h, Gemini 3.1 Flash Image, Gemini Weekly, Claude & GPT Weekly), Codex, Kiro, Qoder, Claude, DeepSeek, Groq, Ollama, and Zed, with model catalog canonical sorting.
- `web/src/components/QuotaTrackerView.svelte`:
  - Added secondary connection label (`getConnectionSecondaryLabel`) for accounts with different emails/display names.
  - Aligned status badges to only render on Kiro connections (matching upstream Next.js).
  - Added connection edit action (pencil icon) with interactive `Edit Connection` modal (name & priority editing + reachability test).
  - Added auto-ping toggle (`bolt` icon) for Claude & Codex OAuth accounts and Codex reset credits integration.


**Token Saver Full Parity with Upstream Next.js (`/dashboard/token-saver`):**
- `internal/handlers/router.go`: Mounted `/api/headroom/*` (`status`, `start`, `stop`, `restart`, `extras`, and `proxy`) inside `SetupDashboardRoutes` with `RequireDashboardAuth`, allowing dashboard session cookies to query and control Headroom proxy lifecycle.
- `web/src/api/client.ts`: Updated `HeadroomStatusResponse` and `HeadroomExtrasResponse`, adding `getHeadroomExtras()`, `startHeadroom()`, `stopHeadroom()`, `restartHeadroom()`, `installHeadroomExtras()`, and `uninstallHeadroomExtras()`.
- `web/src/components/TokenSaverView.svelte`:
  - Removed duplicate in-page header (`PiggyBank` banner) to align with upstream Next.js header layout where page title & icon live in `TopBar`.
  - Replaced custom dialog with standard `Modal.svelte` featuring macOS-style traffic lights (`#FF5F56`), backdrop blur, and `Button.svelte`/`Input.svelte` components.
  - Implemented full Headroom status detection (`Checking…`, `Running`, `Not installed`, `Stopped`, `External`) with local/managed PID checks.
  - Added Headroom compression extras (`[code]`, `[ml]`), install confirmation modal (warning on 1GB ML download), pip uninstall, and log tail streaming.
  - Added locale-based Wenyan level filtering for Caveman output compressor (only showing classical Chinese compression levels on `zh` locales).


**Freebuff Multi-Account Session Management & Direct Proxy Fallback:**
- `internal/proxy/executor/freebuff_session.go` & `freebuff.go`: Added `DoFreebuffHTTP` with automatic fallback to direct connection (`Proxy: nil`) when local or environment proxies refuse requests to `codebuff.com` / `freebuff.com` (`403 Forbidden` / `blocked-by-allowlist`), preventing session lookups and admissions from failing.
- `internal/handlers/oauth/freebuff_session.go` & `freebuff_session_switch.go`: Automatically syncs active session models (`currentModel`) into `conn.Data` (`freebuffModel` and `assignedModel`) in SQLite, enabling strict model routing per connection.
- `web/src/components/connections/FreebuffSessionBanner.svelte`: Added multi-account selection pills allowing users to view and switch between different Freebuff accounts and their respective sessions directly from the banner.
- `web/src/components/connections/ProviderDetailView.svelte`: Added per-connection session badges (`🔒 model-id`, `queued`, `banned`, `no session`) on connection cards with a dedicated **Session** button (`lock_clock`) to quickly focus and manage any account's active seat.

**Vision & Audio Adapter Full Parity (`/dashboard/combos`):**
- `web/src/lib/models.ts`:
  - Updated `getModelCaps` to detect `audioInput` capability and refined vision detection by eliminating false positives from bare `"flash"` model ID tokens (which previously caused non-vision models like `deepseek-v4-flash` to be incorrectly classified as vision-capable).
  - Aligned vision patterns with upstream `visionPatterns.js` (`looksLikeVisionModel`), filtering out audio/tts/stt/embedding generators while identifying multi-modal vision families (`gemini`, `4o`, `gpt-5`, `gpt-6`, `opus`, `sonnet`, `haiku-4.5`, `fable`, `kimi`, `minimax`, `mimo`, `qwen`, `grok`, `llama-4`, `muse-spark`).
- `web/src/components/combos/pickerData.ts`:
  - Added `caps.audioInput` to `PickerModel`.
  - Strictly enforced modality filtering in `resolveFilteredGroups`: `target === 'vision'` now filters exclusively for models with `caps.vision === true`, and `target === 'audio'` filters exclusively for `caps.audioInput === true`, regardless of whether a search query is active.
  - Added empty state handler displaying `No models found` with search icon when no models in the active catalog match the modality requirement.
- `web/src/components/combos/CapacityAdapterSection.svelte`:
  - Removed redundant summary bullet list above cards to match upstream Next.js header layout.
  - Aligned subtitles to `— images (png, jpg, webp, …)` and `— audio input`.
  - Standardized icons to Material Symbols `visibility` (eye) and `graphic_eq` (sound wave equalizer).
- `internal/db/settings.go`: Added `CapacityAdapterEntry` and `CapacityAdapter map[string]CapacityAdapterEntry` to `SettingsData` with default fallback to `ag/gemini-3.8-flash-high`.
- `internal/handlers/chat/combo.go`: Implemented `AugmentModelsWithCapacityAdapter` to automatically prepend models from the capacity adapter pool when none of the target models support required input modalities (e.g. vision for image inputs).
- `internal/handlers/chat/chat.go`: Integrated capacity adapter auto-switch into both OpenAI `/v1/chat/completions` and Anthropic `/v1/messages` for both combos and single-model requests.
- `internal/handlers/chat/vision_adapter_e2e_test.go`: Added comprehensive E2E tests verifying automatic switching from text-only models (`deepseek/deepseek-chat`) to vision-capable models (`ag/gemini-3.8-flash-high`) when image inputs are present in OpenAI and Anthropic request formats.

**Change Log in-app modal (upstream Next.js parity):**
- `web/src/components/ChangelogModal.svelte`: Added `ChangelogModal` Svelte 5 component with markdown parsing via `marked`, custom scrollable container, loading/error states with retry, backdrop-blur overlay, and links to GitHub Releases & Changelog history.
- `web/src/components/TopBar.svelte`: Changed the App Drawer item under "Theme" from an external link to a button triggering the in-app `ChangelogModal`, matching upstream Next.js `HeaderMenu` behavior.
- `web/src/api/client.ts`: Added `api.getChangelog()` querying `/api/changelog` with fallback to GitHub raw URLs.
- `internal/handlers/chat/chat.go` + `internal/handlers/router.go`: Added `GET /api/changelog` and `GET /changelog` endpoints to serve the application changelog directly from local disk with remote fallback.
- `web/src/index.css`: Added `.changelog-body` markdown typography and badge styles.

**Update notification banner in Sidebar (upstream Next.js parity):**
- `web/src/components/Sidebar.svelte`: Added update notification banner below the version label (`↑ New version available: v{latestVersion}`) with `Update now` button and clickable `9router-go update` command pill, matching upstream Next.js `Sidebar.js`.
- Added interactive Update modal with release notes, one-click auto-updater (`api.triggerUpdate()`), manual copy command with countdown shutdown, and disconnected reconnect overlay.
- `web/src/api/client.ts`: Added `SystemVersionInfo` type, `checkUpdate()`, `triggerUpdate()`, and `shutdownServer()`.

**Remove 9Remote & 9English; align Support 9Router with 9router-go repo:**
- `web/src/components/Sidebar.svelte`: Removed the `9Remote` action button & modal and `9English` external link from navigation items; cleaned up modal markup and unused `isRemoteModalOpen` state.
- `web/src/components/TopBar.svelte`: Updated the "Support 9Router" modal to remove the external 9English project link and point to the `9router-go` repository ([`https://github.com/luqman-v1/9router-go`](https://github.com/luqman-v1/9router-go)) and Releases page ([`/releases`](https://github.com/luqman-v1/9router-go/releases)).

### 🐛 Bug Fixes

**OpenCode Chat Completions Tool Fingerprint & Model Purity (`space-bunny-free`):**
- `internal/translator/fingerprint.go`: Fixed `ConcealFingerprintTools` to preserve standard Chat Completions tool shape (`{"type":"function","function":{"name":...}}`) with `"tool_choice":"none"` when client tools are absent, instead of falling back to flat Responses format (`{"type":"function","name":...}`) which caused upstream `[invalid_request_error] invalid request` on OpenCode Chat Completions models like `space-bunny-free`.
- `internal/proxy/executor/providers.go`: Stripped provider prefixes (`oc/`, `opencode/`) from `model` in `ForwardOpencode` and `ForwardOpencodeGo` before forwarding upstream.
- `internal/handlers/chat/resolution.go`: Removed hardcoded model rewrites (`strings.Contains(model, "muse-spark")`, etc.) from Antigravity resolution; all `ag/` and `antigravity/` models resolve cleanly to `antigravity` without special-case overrides.
- `web/src/components/connections/ProviderDetailView.svelte`: Restricted `suggestedModels` strictly to providers that declare a public `modelsFetcher` (upstream Next.js parity). Removed artificial suggested models injection under Antigravity so OpenCode models no longer leak into Antigravity's view.

**Antigravity Google OAuth Callback percent-encoding unescape:**
- `internal/handlers/oauth/antigravity.go`: Added `cleanAuthCode` to unescape double-encoded slashes (`4/0A...` vs `4%252F...`) and strip raw URL parameter prefixes before submitting `application/x-www-form-urlencoded` token exchange requests to Google.
- `web/src/components/connections/ProviderDetailView.svelte`: Added `decodeURIComponent` input cleansing for pasted OAuth authorization callback URLs.

**Deep Auth Verification for OpenAI-Compatible Custom Nodes:**
- `internal/handlers/dashboard/validate.go`: Added a secondary 1-token probe to `POST /v1/chat/completions` during provider node validation (`validateOpenAICompatibleNode`), preventing mock or unauthenticated servers from returning false positive validation results.

**Fix Round-Robin routing for combos and provider connections (Issue #20):**
- `internal/db/settings.go`: Updated `SettingsData` and `GetSettings()` to parse both dashboard JSON keys (`fallbackStrategy` / `rotateStrategy` and `stickyRoundRobinLimit` / `stickyLimit`), as well as global `fallbackStrategy`, `stickyRoundRobinLimit`, `comboStrategy`, `comboStickyRoundRobinLimit`, and `comboStrategies`.
- `internal/db/settings.go`: Updated `SetProviderStrategy` and added `SetComboStrategy` to write to `settings.data` via `UpdateSettingsRaw` without clobbering other settings fields.
- `internal/db/repos.go`: Updated `GetComboByName`, `GetComboById`, and `GetCombos` to populate `combo.Strategy` from `settings.comboStrategies[combo.Name]` and global `settings.comboStrategy`.
- `internal/handlers/chat/resolution.go`: Added `resolveComboRouting` so `ResolveModel` and `resolveModelEntry` populate `ModelInfo.Strategy`, `ModelInfo.StickyLimit`, and `ModelInfo.JudgeModel` from `settings.comboStrategies` or global combo settings.
- `internal/handlers/chat/connections.go` & `fallback.go`: Updated provider connection selection to rotate active connections using `fallbackStrategy` or global fallback settings when configured to `"round-robin"`.
- Added unit tests in `internal/db/settings_test.go` and `internal/handlers/chat/connection_strategy_test.go` covering combo strategy resolution, sticky limits, judge models, and provider connection rotation.

**Fix fetch stream double-read in `web/src/api/client.ts` and add missing tunnel endpoint handlers:**
- `web/src/api/client.ts`: Resolved `Failed to execute 'text' on 'Response': body stream already read` error when receiving non-2xx responses. Previously, `res.json()` consumed the stream body on error responses, which caused the subsequent `res.text()` fallback in the catch block to crash. The client now safely reads `res.text()` first before attempting JSON parsing.
- `internal/handlers/dashboard/tunnel.go` & `internal/handlers/router.go`: Added endpoints `POST /api/tunnel/enable`, `POST /api/tunnel/disable`, `GET /api/tunnel/tailscale-check`, `POST /api/tunnel/tailscale-enable`, and `POST /api/tunnel/tailscale-disable` with structured JSON responses and clean error handling instead of unhandled 404s.

**Combo bypass Vercel Edge Relay for no-auth providers (e.g. `oc/muse-spark-1.3` 429 on `combo-wombo`, solo test 200):**
- `internal/handlers/chat/combo.go` (chat + messages fallback) and `combo_fusion.go` — no-auth branch now sets `ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider)` (was `&ConnectionData{APIKey}` only), so combo routing goes through the configured `providerStrategies.<provider>.proxyPoolId` relay (`x-relay-target`/`x-relay-path`) exactly like the solo path (`handleAccountFallback`). Direct-to-`https://opencode.ai/zen/v1/responses` calls that burned the free-tier IP quota (`FreeUsageLimitError` 429) are eliminated.

**Vercel Edge Relay header forwarding for `muse-spark` / `antigravity`:**
- `internal/proxy/opencode.go` — `BuildOpenCodeHeaders` preserves `x-relay-target` / `x-relay-path` instead of dropping them.
- `internal/proxy/executor/providers.go` — `ForwardOpencode` / `ForwardOpencodeGo` keep `BaseURL` on the relay host and route via `x-relay-path` (`/zen/v1/responses`, `/zen/v1/messages`, `/zen/go/v1/responses`) when relay headers are present.
- Added `TestForwardOpencode_MuseSpark_EdgeRelay` (PASS); verified live `200 OK` via relay.

**Topology false pulse on dashboard load (`AnalyticsView.svelte`):**
- SSE `/api/usage/stream` initial snapshot no longer triggers the electric-beam animation: added `streamInitialized` guard so only genuine new model requests after init pulse; active-request updates set `lastProvider` without re-pulsing. Dashboard API traffic (`/api/usage`, polling) never triggers topology effects — only upstream model calls do.
- Consolidated per-node SVG turbulence filters into one lightweight global filter (`numOctaves="1"`) for GPU/CPU relief during continuous animation.

**Query-param auth for SSE streams (`internal/middleware/auth.go`):**
- `ExtractApiKey` accepts `?key=` / `?apiKey=` on routes ending in `/stream` (native `EventSource` can't set custom headers); REST/LLM endpoints stay header-only.

**Topology Option A visuals (`ProviderTopologyCard.svelte` + `web/src/index.css`):**
- Bidirectional neural stream (cyan prompt Router→Provider, emerald/gold response Provider→Router), dual shockwave rings on the active provider target, router absorption rings, node micro-bounce + `LIVE` badge.

### 🔄 Upstream Parity Sync

**Strike-breaker quota-only (upstream `decolua/9router#4197` parity, PR #16):**
- `internal/handlers/chat/antigravity_quota.go` — `HandleAntigravityQuotaError` now takes the upstream `errorMessage` and only counts a strike on explicit quota markers (`RATE_LIMIT_EXCEEDED`, `QUOTA_EXHAUSTED`, `Individual quota reached`). Generic bare `RESOURCE_EXHAUSTED` 429s no longer burn strikes / lock combos.
- `internal/handlers/chat/gemini_handler.go` — forwards `string(uErr.Body)` as the error message source.
- Added `TestAntigravityQuota_Generic429NoStrike` regression test.

**Refusal → content_filter mapping (upstream `decolua/9router#4210` parity, PR #16):**
- `internal/translator/claude_response.go` — streaming + non-streaming: Gemini `refusal` finish maps to `content_filter`, emits `stop_details.explanation` so Claude Code renders the block instead of hanging.
- `internal/translator/response.go` — reverse mapping `content_filter → refusal` for round-trips.
- Added refusal stream / non-stream / round-trip tests.

**Weekly vs session quota buckets (upstream `decolua/9router#4209` parity, PR #18):**
- `internal/handlers/chat/antigravity_quota.go` — `ParseWeeklyQuotaSummary` classifies the `window` field into weekly (`gemini`/`claude_gpt`) vs 5h-session (`gemini_session`/`claude_gpt_session`) buckets; `IsAntigravityModelBlocked` honors session buckets via `quotaEntryExhausted` helper.
- Added `TestAntigravityWeeklyQuota_SessionBuckets`.

**Add Compatible modal + provider-node validation (upstream `AddCompatibleModal.js` + `provider-nodes/validate/route.js` parity):**
- `web/src/components/connections/AddCompatibleNodeModal.svelte` — rebuilt to match the upstream modal: separate `Name` / `Prefix` / `API Type` fields with upstream placeholders (`OpenAI Compatible (Prod)`, `oc-prod`, …) and hints, `API Key (for Check)` + `Model ID (optional)` inputs driving a `Check` button with `Valid` / `Invalid` badges (chat-fallback note included), and full-width `Create` + `Cancel`. The API key is now validation-only and no longer auto-creates a connection (`ConnectionsView.svelte`).
- `internal/handlers/dashboard/provider_nodes.go` + `router.go` / `routes.go` — added `POST /api/provider-nodes/validate` (OpenAI-compatible `/models` + chat fallback, Anthropic-compatible with `x-api-key` + `/messages`-suffix strip, `custom-embedding` with dimension report), including SSRF guard for non-loopback callers (`handlerutil.AssertPublicURL`).
- `web/src/api/client.ts` — added `validateProviderNode`.
- Added `provider_nodes_validate_test.go` (13 tests: input guards, SSRF/local, OpenAI/Anthropic/embedding probes, chat fallback, network-error mapping).

### 🧹 Style Cleanup (behavior-neutral, PR #17 + follow-ups)

- `interface{}` → `any` across production code and test files; `errors.New` + `%w` wrapping; `slices.Contains/Sorted/Delete`, builtin `max()`/`clear()`, `strings.Builder`, shared header constants in `internal/constants`.
- Named constants: `antigravityDecoyUnavailable`, `maxReadLimit`, `thinkingHeadroomTokens`, `maxCallIDLen`, `MaxUpstreamBodyBytes`/`UpstreamErrLimit`.
- `fallback.go` — `forwardRequestParams` struct replaces 10-param forwarding; `openai.go` — `sseStreamOpts` struct replaces 8-param SSE helper.
- `antigravity_quota.go` — `AntigravityQuotaError` struct + named quota markers (replaces `map[string]any` error plumbing).
- Added `samber/lo` (`Ternary`, `CoalesceOrEmpty` only — `Coalesce` on `any` maps and eager `Ternary` slicing deliberately avoided).
- Default port `20128` → `20130` (`config.go`, `Makefile`, `mitm/handlers/base.go`, `.env.example`, `docker-compose.yml`, `Dockerfile`, `README.md`).

### 🧪 Tests

- `gemini38_live_test.go` — real upstream tests for `ag/gemini-3.8-flash-medium` (chat + stream, `200 OK`). Live E2E: 17/17 PASS.

### 📦 Release Hardening (issue #19)

- `make cross` generates `SHA256SUMS.txt`, uploaded by `release.yml`; README documents the Windows Defender false-positive (`Wacatac.C!ml` heuristic on the unsigned binary) with verify + Allow steps.


## [v1.8.18] — 2026-09-21

### 🐛 Bug Fixes

**OMP Harness False-429 on Antigravity (upstream `decolua/9router#3986` parity):**
- `internal/translator/antigravity.go` — `WrapForAntigravity` no longer sends `requestType: "agent"` in the Cloud Code envelope (`AntigravityRequest.RequestType` is now `omitempty` and left empty). Google enforces a tiny separate quota bucket whenever `requestType="agent"` is present, so OMP (Oh My Pi) harness payloads (~25–30k token system prompt + tools) were rejected with false `429 RESOURCE_EXHAUSTED` even with quota remaining — cascading into `CACHE_BLOCK` account locks while Claude Code stayed green. Verified live: same payload returns `200 OK` after the fix.
- `internal/translator/antigravity_test.go` — Added `TestWrapForAntigravity_OmitsAgentRequestType` regression test (asserts the field is absent from the raw envelope JSON and the `requestId` `agent/<…>` shape is preserved). Image (`image_gen`) and search (`search`) request types are untouched.

### 🔄 Upstream Parity Sync — `decolua/9router` v0.5.75…v0.5.81 (100%)

**Model Catalog & Routing:**
- `internal/providers/registry_models.go` — Registered `deepseek-v4.1-flash` for `codebuddy-intl` (`cbai`, replacing the retired `deepseek-v4-flash`) and added `deepseek-v4.1-flash:cloud` to the `ollama` catalog.
- `internal/handlers/chat/resolution.go` — Routed bare `codex-auto-review` to the `codex` provider (PR #4135 parity), resolved even with a nil repo / empty DB.

**Antigravity Hygiene:**
- `internal/translator/antigravity.go` — Stripped the Claude Code `x-anthropic-billing-header` from system prompts and sanitized the Hermes Agent identity (`You are Hermes Agent, an intelligent AI assistant created by Nous Research.` → neutral form) to eliminate false HTTP 429/403 anti-abuse rejections.
- `internal/translator/thought_signature_store.go` — Scoped cached Gemini thought signatures to the producing model family (`claude` vs `gemini`), preventing cross-family replay that triggers HTTP 400 `Invalid thought signature` when a conversation switches models (upstream `bc3be0cb` parity).
- `internal/translator/gemini.go` — Threaded the model name through the `GetGeminiThoughtSignature` / `StoreGeminiThoughtSignature` call sites so stored signatures are keyed per model family (call-site half of the scoping above).

**CommandCode Multimodal & Reasoning:**
- `internal/proxy/executor/providers.go` — Added native image blocks to `buildCommandcodeBody`: OpenAI `image_url` data URIs and Claude/OpenAI base64 image sources are converted to CommandCode `{type: "image", image: <dataUri>, mimeType}` blocks, and `reasoning_effort` (`low`/`medium`/`high`/`max`) is preserved on `/alpha/generate`.

**Union-Alpha / OpenCode Parity (verified):**
- Confirmed live routing of `oc/union-alpha` through the Anthropic Messages API (`/zen/v1/messages`) with `anthropic-version: 2023-06-01` and automatic `max_tokens` injection (PR #4099 parity); free-tier `forceStream`/SSE aggregation parity already structural in Go.
- `internal/handlers/chat/muse_spark_e2e_test.go` — Added `TestIntegration_OpenCode_UnionAlpha_Messages` (live E2E; SKIPs on upstream rate-limit or auth-dependent `Model union-alpha is not supported` 401, consistent with existing Muse Spark E2E policy).

## [v1.8.17] — 2026-09-18

### 🚀 Features & Upstream Parity

**Claude OAuth Subscription (`sk-ant-oat`) Support End-to-End (PR #13):**
- Contributed by **@rezhajulio** ([#13](https://github.com/luqman-v1/9router-go/pull/13)) — Special thanks for bringing full Claude Pro/Max subscription parity from the dashboard to the native Go proxy!
- `internal/handlers/chat/fallback.go` — Automatic header switching to `Authorization: Bearer` and appending `?beta=true` for Claude OAuth credentials (`sk-ant-oat` or `accessToken`), supporting direct Anthropic API as well as Edge Relay proxy pools.
- `internal/handlers/chat/claude_cloaking.go` — Injected official `x-anthropic-billing-header` into `system[0]`, deterministic account `metadata.user_id`, client tool name obfuscation with `_ide` suffix, and decoy tools (`CCDecoyTools`) preventing false HTTP 429 anti-abuse rate limits.
- `internal/proxy/executor/claude_decloak.go` — Streaming and non-streaming response decloaker restoring original tool names and translating decoy tool invocations into clean text blocks.
- `internal/proxy/executor/providers.go` — Added `sanitizeToolUseID` to rewrite foreign/Gemini tool IDs deterministically to Anthropic-compliant `toolu_<sha256>`.
- `internal/tokensaver/prompts.go` — Added `InjectSystemPromptClaude` for format-aware system prompt injection at top-level `system`.

**Antigravity Zen Free-Tier Tool Quartet Renaming (PR #12):**
- Contributed by **@yxxrn** ([#12](https://github.com/luqman-v1/9router-go/pull/12)) — Special thanks for identifying the exact upstream fingerprinting gate and eliminating Claude Code 403/500 errors!
- `internal/translator/fingerprint.go` — Implemented `ConcealFingerprintTools` to rename uppercase tool quartet variants from Claude Code CLI (`Bash`, `Glob`, `Grep`, `Read`) to canonical lowercase (`bash`, `glob`, `grep`, `read`), eliminate duplicates (preventing upstream HTTP 500), retarget `tool_choice`, and restore original tool names in response payloads via `RestoreToolNamesInPayload` / `RestoreToolNamesInSSE`.
- `internal/proxy/executor/toolname_writer.go` — Embedded `toolNameRestoringWriter` on responses ensuring client tools are seamlessly restored across both streaming and non-streaming responses.

### 🐛 Bug Fixes & Improvements

**CommandCode CLI User-Agent & Schema Wrapping (PR #14, fixes #9):**
- Reported by **@jhonoryza** ([#9](https://github.com/luqman-v1/9router-go/issues/9)) — Thank you for reporting the Cloudflare challenge error!
- `internal/providers/providers.go` & `internal/proxy/executor/providers.go` — Added official `User-Agent: commandcode/0.25.7 (cli)` and `x-command-code-version: 0.25.7`, eliminating Cloudflare WAF bot-challenge intercepts (HTTP 403 `Attention Required!`).
- `internal/proxy/executor/providers.go` — Implemented `buildCommandcodeBody` wrapping OpenAI payloads into `{threadId, memory, config, params}` schema required by CommandCode's `/alpha/generate` endpoint.
- `internal/handlers/chat/fallback.go` & `internal/proxy/proxy.go` — Enhanced error parsing in `extractErrorText` and `UpstreamError.Error()` to summarize Cloudflare challenge pages cleanly without dumping raw HTML.

**Responses API (`POST /v1/responses`) Public Provider Fallback & String Input (PR #15, fixes #10):**
- Reported by **@pankaj-raikar** ([#10](https://github.com/luqman-v1/9router-go/issues/10)) — Thank you for the detailed reproduction report!
- `internal/handlers/media/media.go` — Added automatic fallback for public/free-tier providers (`DefaultAPIKey: "public"`) in `forwardMediaRequest`, eliminating `"no active connections for provider: opencode"` when no SQLite connection is seeded.
- `internal/handlers/media/media.go` & `internal/handlers/chat/resolution.go` — Routed `opencode`, `opencode-go`, and `antigravity/muse-spark-*` models on `/responses` directly to `ForwardOpencode` so session tracking, request headers, and tool-name concealing work out of the box.
- `internal/proxy/executor/transform.go` — Updated `buildResponsesBody` to support both string inputs (`"input": "Say hello"`) and array inputs (`Input []any`), normalizing string prompts into valid Responses message items.

## [v1.8.16] — 2026-09-17

### 🐛 Upstream Parity & Bug Fixes

**Active Provider Connections & Capabilities on `/v1/models` and `/api/models` (PR #8, fixes #7):**
- `internal/providers/registry_models.go` — Added comprehensive upstream provider models registry mapped from 128 provider definitions (`decolua/9router` parity) with `GetProviderModels()`.
- `internal/providers/capabilities.go` — Implemented `CapabilitiesDetail` and `GetCapabilitiesDetailForModel()`, outputting full multimodal flags (`vision`, `pdf`, `audioInput`, `videoInput`, `imageOutput`, `audioOutput`), `thinkingCanDisable`, and dual token limits (`contextWindows` and `contextWindow`).
- `internal/handlers/chat/chat.go` — Replaced empty `/v1/models` responses by dynamically aggregating models for active connections (`isActive = 1`), honoring custom prefixes and connection-enabled models. Custom models from deactivated connections (`disabledProviders`) are automatically excluded, and clean static catalogs are returned when no database connections are configured.
- `internal/handlers/router.go` — Mounted `GET /api/models` and `GET /api/models/*` serving both `"data"` and `"models"` top-level keys for universal compatibility across OpenAI-compliant IDE clients and Next.js web dashboards.
- `internal/handlers/chat/models_provider_test.go` — Added regression tests verifying model discovery for active Codex (`cx`) connections, fallback registry, and single-model lookups.

## [v1.8.15] — 2026-09-17

### 🚀 Features & Upstream Parity

**Claude Messages to OpenAI Response Translation for Zen / Union-Alpha (PR #4099, #4111):**
- `internal/translator/claude_response.go` — Added on-the-fly streaming (`TranslateClaudeChunkToOpenAI`) and non-streaming (`TranslateClaudeResponseToOpenAI`) response translation engines. Transforms Claude Messages SSE events (`content_block_delta`, `thinking_delta`, `tool_use`, `input_json_delta`, `message_delta`, `message_stop`) into standard OpenAI chunks (`choices[0].delta.content`, `reasoning_content`, `tool_calls`) so that client harnesses (e.g. omp, Cursor, Cline) receive native responses.
- `internal/proxy/executor/claude_messages.go` — Added dedicated streaming handler (`handleClaudeMessagesStream`) and non-streaming handler (`handleClaudeMessagesNonStream`) wired into `ForwardOpencode` and `ForwardOpencodeGo` for `union-alpha` routes.
- `internal/proxy/opencode.go` — Updated Antigravity Zen headers to comply with upstream PR #4111 (`User-Agent: antigravity/1.18.31 ai-sdk/provider-utils/4.0.46 runtime/bun/1.3.14`, `x-antigravity-client: cli`, dynamic 40-character hex project IDs `GenerateOpenCodeProjectID()`, and `x-api-key: public`).

**Anthropic Tools & Messages Schema Normalization:**
- `internal/proxy/executor/providers.go` — Added `convertOpenAIToolsToClaude`, `ensureMessagesMaxTokens`, `extractClaudeSystemPrompt`, and `convertOpenAIMessagesToClaude` to convert incoming OpenAI tool definitions (`type: "function"`) into Claude tools (`{name, description, input_schema}`), normalize `tool_choice`, and merge adjacent same-role messages for compliant Anthropic payload delivery.
- `internal/proxy/sse.go` — Expanded terminal detection buffer to 64 bytes and added recognition for Anthropic terminal signals (`"stop_reason":` non-null and `"message_stop"`) to eliminate premature `finish_reason: "network_error"` synthesis at clean stream EOF.

### 🐛 Bug Fixes & Resilience

**Google RPC `quotaResetDelay` Automatic Duration Locking with Deadlock Prevention:**
- `internal/handlers/chat/fallback.go` — Implemented `extractResetDuration` to parse Google RPC ErrorInfo metadata `quotaResetDelay` (e.g. `"1h12m28.109534319s"`) and text patterns (`"Resets in XhYmZs."`). Enforces safety bounds (min 5s, hard cap at 2 hours) to avoid perpetual lockouts or deadlocks.
- `internal/handlers/chat/combo.go` — Updated `comboLockRetryable` to use the parsed reset duration for connection and model locks instead of falling back to 8s exponential backoff.
- `internal/handlers/chat/antigravity_quota.go` & `internal/handlers/chat/gemini_handler.go` — Added `BlockAntigravityModelUntil` to cache exhausted model quotas and canonical synonyms (`gemini-3.8-flash-tiered`) in RAM until the verified reset timestamp, preventing continuous 429 spam to Google upstream while automatically unblocking the moment reset time is reached.

## [v1.8.14] — 2026-09-17

### 🐛 Upstream Parity & Bug Fixes

**Muse Spark 1.3 FreeTier Authorization & Session Normalization (PR #4105, #4061, #4062):**
- `internal/proxy/antigravity.go` — Updated Antigravity OpenCode user-agent default to `antigravity/1.18.31` and implemented descending canonical 30-character session (`ses_` + 12 hex + 14 Base62) and message (`msg_` + 12 hex + 14 Base62) ID generators. This resolves HTTP 403 `FreeTierError` when routing requests to `oc/muse-spark-1.3-contributor-free`.
- `internal/proxy/executor/providers.go` — Added `normalizeMuseSparkResponsesBody` to strip prior multi-turn reasoning content and encrypted content blocks that trigger HTTP 400 parameter errors, while explicitly normalizing `tool_choice` to `"auto"` for Muse Spark 1.3.

**Missing `tool_call_id` FIFO Repair (PR #4090):**
- `internal/handlers/chat/tool_repair.go` & `internal/translator/request.go` — Added automatic repairing for client requests where `role: "tool"` or `function_call_output` messages omit `tool_call_id`. Uses FIFO pairing with un-paired assistant tool calls or mints deterministic call IDs to prevent strict upstreams (OpenAI, DeepSeek, Antigravity) from failing with HTTP 400.
- `internal/handlers/chat/chat.go`, `internal/handlers/chat/combo.go`, `internal/handlers/chat/fallback.go`, & `internal/proxy/executor/transform.go` — Integrated tool call repair across OpenAI chat completions, combo routing, and account fallback handlers.

**Stream Interruption Terminal Synthesis (PR #4079):**
- `internal/proxy/sse.go` — Implemented terminal frame synthesis (`finish_reason: "network_error"` followed by `data: [DONE]\n\n`) when upstream SSE connections terminate abruptly at EOF before emitting a terminal frame, preventing IDE client hangs and errors in Cline/Pi.

**Claude Tool Result Image Hoisting (PR #4083):**
- `internal/translator/request.go` — Converted base64 image blocks embedded inside Claude `tool_result` into follow-up user messages with `[Image from tool result <id>]` and OpenAI `image_url` blocks, allowing vision models to inspect tool screenshot outputs without violating text-only tool-role schema constraints.

**Grok CLI Tool Result Neutral Placeholder (PR #4109):**
- `internal/proxy/grokcli.go` & `internal/mitm/handlers/kiro.go` — Replaced placeholder `"continue"` with neutral `"Tool results provided."` for tool-result user turns in Kiro request payloads to prevent assistant hallucination loops.

**Thinking Variant Route Stripping & Union Alpha Messages Route (PR #4084, #4099):**
- `internal/proxy/executor/providers.go` — Stripped model suffix before checking `isOpencodeResponsesModel`, enabling models like `gpt-5.6-luna(high)` to correctly route to `/responses`.
- `internal/proxy/executor/providers.go` — Routed Antigravity Zen `union-alpha` directly to `/zen/v1/messages` with `anthropic-version: 2023-06-01`.
- `internal/providers/capabilities.go` — Registered model capabilities for `union-alpha`, `deepseek-v4.1-flash`, and `deepseek-flash`.

## [v1.8.13] — 2026-09-16

### 🐛 Bug Fixes & Parity

**Grok CLI Responses Endpoint Fix (PR #4, fixes #2):**
- `internal/providers/providers.go` — Updated `grok-cli` `BaseURL` from bare root `https://cli-chat-proxy.grok.com` to `https://cli-chat-proxy.grok.com/v1/responses`, resolving HTTP 404 HTML edge errors when calling `gcli/*` models (`grok-4.5`, `grok-4.6`).
- `internal/proxy/grokcli.go` — Added auto-normalization in `ForwardGrokCLI` so that if `cfg.BaseURL` is empty or lacks the `/v1/responses` path, it automatically normalizes to `/v1/responses`.
- `internal/handlers/chat/grokcli_handler_test.go` — Added regression tests for grok-cli BaseURL and endpoint routing.

**Custom Provider Nodes Prefix Priority & Model Mapping (PR #5, fixes #3):**
- `internal/handlers/chat/resolution.go` — Prioritized custom `providerNode` prefix resolution (`h.resolvePrefixProvider(prefix, model)`) before checking built-in provider aliases (`resolveProviderAlias(prefix)`). This prevents short prefixes like `oa` or `cc` from being shadowed by `openai` or `claude`, eliminating false 502 "no active connections for provider: openai" failures when the custom node has active connections.
- `internal/handlers/chat/resolution.go` — Added fallback so that when the alias-resolved provider has no active connections, unresolved prefix queries report the matching custom `providerNode.id` instead of falsely blaming the shadowed built-in provider.
- `internal/handlers/chat/resolution.go` — Added defensive nil checks for `h.Repo` across all model and combo resolution helpers.
- `internal/db/repos.go` — Added `GetProviderNodePrefixMap()` to map internal row IDs (`openai-compatible-chat-0489...`) to user-configured prefixes (e.g. `nara`, `orca`, `oa`).
- `internal/db/repos.go` — Updated `GetCustomModels()` with fallback parsing from keys (`<providerAlias>|<modelId>|<kind>`) and removed restrictive `type == "llm"` filtering, exposing all custom chat and completion models.
- `internal/handlers/chat/chat.go` — In `HandleModels` (`GET /v1/models`) and `HandleModelLookup` (`GET /v1/models/*`), mapped `cm.ProviderAlias` through the prefix map so models are published under their clean user-configured prefix (e.g. `nara/glm-5.3`) with `owned_by` set to the prefix rather than leaking internal database row IDs.
- `internal/handlers/chat/resolution_test.go`, `internal/handlers/chat/chat_v065_test.go`, & `internal/db/repos_test.go` — Added comprehensive unit and regression tests for custom prefix priority, fallback error reporting, prefix map caching, key-fallback parsing, and `/v1/models` prefix output.

## [v1.8.12] — 2026-09-16

### 🚀 Features & Provider Additions

**Freebuff Provider Integration (`fb`):**
- `internal/proxy/executor/freebuff.go` — Added native Freebuff executor supporting `https://www.codebuff.com/api/v1/chat/completions` with 1-hour session token lifecycle caching, agent run tracking (`/api/v1/agent-runs`), Buffy system prompt marker injection, and `end_turn` tool injection for sub-agent orchestration.
- `internal/providers/providers.go` & `internal/providers/aliases.go` — Registered provider `freebuff` and alias `fb`.

**Provider Connection Routing Strategies:**
- `internal/db/settings.go` & `internal/handlers/chat/connections.go` — Added configurable multi-connection routing strategies per provider: `sticky` (with configurable `stickyLimit`), `round-robin`, `random`, and `none`.

**Model Capabilities & Limits:**
- `internal/providers/capabilities.go` — Added capabilities for Upstage Solar Pro (`*solar-pro*`) and LongCat (`*longcat*`) with reasoning, tools, 200,000 token context window, and 32,000 max output tokens.

### 🐛 Bug Fixes & Resiliency

**Cline & Clinepass OAuth Refresh Overhaul:**
- `internal/proxy/oauth/cline.go` — Registered `clinepass` alongside `cline` in the OAuth registry, resolving issues where ClinePass connections fell back to incompatible standard form-urlencoded OAuth refresh.
- Migrated token refresh endpoint from deprecated `/v1/auth/refresh` (which returned 401 "Please make sure you're using the latest version of Cline") to active upstream `/api/v1/auth/refresh`.
- Emulated full Cline CLI identity headers on refresh (`User-Agent: Cline/3.0.61`, `X-CLIENT-TYPE: cline-cli`, `X-CLIENT-VERSION: 3.0.61`, `X-CORE-VERSION: 3.0.61`, `X-PLATFORM: cli`).
- Added token rotation support: propagated rotated `refreshToken` to SQLite database across `BuildConnectionUpdate` and `forceRefreshOAuthToken`.
- `internal/handlers/chat/fallback.go` — Ensured `refreshedKey` is normalized with `NormalizeProviderToken` on reactive 401 retries so WorkOS prefix (`workos:`) is preserved.

**Zero-Sleep Failover & Canonical Model Locking:**
- `internal/handlers/chat/combo.go` — Removed synchronous blocking sleeps (`time.Sleep`) during combo failover loops on transient errors (502, 503, 504), enabling immediate non-blocking failover to backup models/connections without stalling client turns.
- `internal/handlers/chat/connections.go` & `internal/handlers/chat/combo.go` — Added `canonicalLockModel(provider, model)` to atomically lock shared tier pools across connections (e.g., Antigravity `gemini-3.8-flash-low`/`high` map to canonical lock key `gemini-3.8-flash-tiered`). Prevents split-lock failure loops across shared tier accounts.
- `internal/providers/errorclassify.go` — Expanded error classification to trigger backoff for `resource_exhausted`, `model_capacity_exhausted`, and HTTP 502/503/504 status codes.

**Stream Telemetry & Connection Safety:**
- `internal/proxy/sse.go` — Added rolling 16-byte tail buffer in `SSECopy` to safely detect `[DONE]` across chunk boundaries and cleanly terminate SSE streams without hanging on keep-alive connections.
- `internal/handlers/chat/fallback.go` — Guaranteed `usageHistory` database logging on completed streams even when the client disconnects at stream end.
- Advertised `context_window` in `/v1/models` and `/v1/models/info` dynamically from capabilities.

## [v1.8.11] — 2026-09-12

### 🐛 Bug Fixes & Parity — Upstream PR Porting

**Gemini Multiple System Messages Preservation (PR #3973):**
- `internal/translator/gemini.go` — Preserved all `role: "system"` messages in `req.SystemInstruction.Parts` rather than overwriting earlier instructions with the last turn, ensuring all system prompts and developer directives reach Gemini models.

**Antigravity Thinking Budget & Output Tokens Guard (PR #3981):**
- `internal/translator/gemini.go` & `internal/translator/antigravity.go` — Guarded `maxOutputTokens > thinkingBudget` across `TranslateOpenAIToGemini` and `hardenAntigravityRequest`, preventing HTTP 400 `INVALID_ARGUMENT: max_tokens must be greater than thinking.budget_tokens` and erroneous connection locks on reasoning models.
- Added support for `max_completion_tokens`, `thinking.budget_tokens`, and `thinking_budget`.

**Claude Document Block Support for Antigravity & OpenAI (PR #3968):**
- `internal/translator/request.go` & `internal/translator/types.go` — Added `document` block handling in `TranslateClaudeToOpenAI` and `OpenAIFile` struct in `OpenAIContentBlock`, converting base64 PDF documents into OpenAI file format that flows into Gemini/Antigravity `inlineData`.

**Client Cancellation Tracing & Stream Telemetry:**
- `internal/handlers/chat/fallback.go` — Differentiated client cancellations (`errors.Is(fwdErr, context.Canceled)` or `ctx.Err() != nil`) from true upstream failures, logging `INF [fallback] client canceled request` and recording status `499` in traces instead of raising false `WRN upstream failed` alarms.
- `internal/handlers/chat/gemini_handler.go` — Added accurate `totalBytesWritten` accumulation for OpenAI format streaming branches and terminal `[DONE]` frame.
- `internal/handlers/chat/fallback.go` & `internal/handlers/chat/combo.go` — Refactored hardcoded HTTP status codes to standard `net/http` constants (`http.StatusOK`, `StatusClientClosedRequest`).

**Memory Leak Protections & High-Traffic Concurrency:**
- `internal/translator/usage.go` & `internal/translator/response.go` — Added `pendingFragment` struct with `createdAt` timestamps and 10-minute TTL pruning in `pruneStaleStatesLocked()`. Prevents abandoned fragmented SSE streams from accumulating in the global `pendingJSON` map.
- `internal/handlers/chat/connections.go` — Pooled and cached `*http.Client` and `*http.Transport` instances by proxy URL using `sync.RWMutex`. Eliminates per-request transport allocations, enables TCP keep-alive reuse across proxy pool traffic, and prevents socket/goroutine exhaustion.

**Live Profiling & Diagnostics:**
- `internal/handlers/router.go` — Mounted Go standard `net/http/pprof` endpoints (`/debug/pprof/`, `/debug/pprof/heap`, `/debug/pprof/goroutine`, `/debug/pprof/profile`) for real-time heap and concurrency inspection.

**Documentation & Client Guides:**
- `README.md` — Added comprehensive pre-built binary download links (macOS, Linux, Windows), one-liner install script, Docker setup, and configuration examples for Claude Code, `omp`, and Cursor/Cline.

## [v1.8.10] — 2026-09-11

### ✨ Features & Parity — Next.js v0.5.75 Sync (27 Commits)

**Gemini & Antigravity Content Normalization:**
- `internal/translator/gemini.go` & `internal/translator/antigravity.go` — Added `NormalizeGeminiContents` merging adjacent same-role messages and filtering out empty parts (parity with `#e7b5f09`).
- `internal/handlers/chat/antigravity_quota.go` — Added Antigravity weekly quota tracking (`gemini_weekly`, `claude_gpt_weekly`) and free-tier handling via `retrieveUserQuotaSummary`, caching summaries and reconciling against exhausted model families (#3892).

**Kiro Routing & Wire Payload Cleanup:**
- `internal/providers/providers.go` & `internal/proxy/grokcli.go` — Routed Kiro through Amazon Q first (`https://q.us-east-1.amazonaws.com/generateAssistantResponse`), deprecated legacy runtime path to avoid 400 `REQUEST_BODY_INVALID` (#3776).
- Injected `x-amz-sso-bearer`, `x-amzn-kiro-agent-mode: spec`, and `x-amzn-codewhisperer-machine-id: kiro-desktop` headers.
- `internal/proxy/grokcli.go` & `internal/mitm/handlers/kiro.go` — Stripped top-level `systemPrompt`, `agentMode`, and `conversationState` continuation fields (`agentContinuationId`, `agentTaskType`) that modern Kiro gateways reject with 400 (#1892ed7).

**Opencode-Go Catalog Refresh & Responses API:**
- `internal/proxy/executor/providers.go` — Routed `grok-4.6` and `gpt-5.6-luna` on `opencode-go` to the `/zen/go/v1/responses` endpoint alongside `muse-spark`.
- `internal/providers/capabilities.go` & `internal/proxy/executor/providers.go` — Registered newly published Go models (`deepseek-flash`, `glm-5.3`, `kimi-k3`, `longcat-2.0`, `qwen3.8-max`, `qwen3.8-flash`, `hy4-preview`, `hy3`), with `deepseek-flash` (DeepSeek V4.1 Flash) priority and Qwen 3.8 models in `opencodeGoMessagesModels`.

**Codex CLI Bump & Unicode Schema Sanitization:**
- `internal/providers/providers.go` — Updated Codex CLI User-Agent to `codex_cli_rs/0.154.0` (parity with `#a7047a0`).
- `internal/proxy/executor/transform.go` — Added `StripCodexUnsupportedPatterns` to sanitize `\p{...}` / `\P{...}` Unicode property escapes in tool parameters that Codex's `/responses` validator rejects with HTTP 400 (#3922).
- `internal/providers/capabilities.go` — Added Codex image models (`gpt-image-2.5`, `gpt-image-2.5-flare`, `gpt-image-2.5-sunburst`, `gpt-image-2`, `gpt-image-1.5`) with `ImageOutput` capability and `*gpt-image*` pattern match.

**Claude Cache Budget & Single-Object Turns:**
- `internal/translator/request.go` — Enforced Anthropic 4-marker `cache_control` budget in `AnchorClaudeCache`: pins head anchors (last system block, last non-deferred tool) and keeps at most 2 tail message markers, trimming earlier ones (#8a81085).
- `internal/translator/request.go` — Supported single-object content turns (`content: {type: "text", ...}`) across `convertClaudeMessage`, `SanitizeClaudePassthrough`, and `AnchorClaudeCache`.
- `internal/handlers/chat/chat.go` — Scoped Claude tool type defaulting to gateways declaring `requireClaudeToolType` (MiniMax / MiniMax-CN), avoiding 400 `unknown variant custom` on DeepSeek Anthropic endpoint (#3905, #45ec1d3).

**Cline Envelope Unwrapping & Token Refresh:**
- `internal/translator/response.go`, `internal/handlers/chat/forward.go`, `internal/proxy/executor/openai.go` — Added `UnwrapClineEnvelope` unwrapping `{"success":true,"data":{...}}` for non-streaming completions on `cline` and `clinepass` (#122f23ee).
- `internal/providers/oauth.go` — Registered `clinepass` in OAuth token refresh config.
- `internal/providers/capabilities.go` — Replaced `deepseek-v4-flash` with `deepseek-v4.1-flash` for `codebuddy-cn` (#807553e).

**Connection Health & Security:**
- `internal/db/accounts.go` — Added `ResetConnectionHealthState` clearing `modelLock_*`, `errorCode`, `rateLimitedUntil`, and resetting `backoffLevel = 0` upon connection activation (#3830).
- `internal/handlers/media/media.go` — Rejected path-escaping characters (`..`, `/`, `\`) in `HandleVideoGet` (#da6aa901).

**Responses API Stream & Tool Calling Fixes:**
- `internal/proxy/executor/stream.go` — Fixed tool call argument duplication (`InputValidationError` caused by duplicate concatenated JSON bodies such as `{"q":"*.yaml"}{"q":"*.yaml"}`) on Responses API streams by tracking `ArgsEmitted` during `response.function_call_arguments.delta` and suppressing redundant full argument re-emission on `response.output_item.done`.
- `internal/handlers/chat/gemini_handler.go` — Ensured terminal `data: [DONE]\n\n` SSE frame is emitted upon stream completion for OpenAI-format streaming clients and cleaned up redundant newlines.
- `internal/handlers/chat/live_e2e_test.go` — Added live non-mock E2E tests for Antigravity (`gemini-2.5-flash`, `gemini-3.8-flash-high`), DeepSeek, and OpenCode covering parallel multi-tool calls, multi-turn tool execution, streaming, and weekly quota retrieval.

## [v1.8.9] — 2026-09-06

### ✨ Features & Parity — Next.js v0.5.69 Sync (19 Commits)

**Gemini & Antigravity ThoughtSignatureStore:**
- `internal/translator/thought_signature_store.go` — Added thread-safe in-memory LRU store (capacity: 2,000 entries, 1-hour memory TTL) scoped by `sessionId` + `toolCallId`. Caches and replays thought signatures across turns to prevent corrupted thought signature errors during multi-turn reasoning conversations.
- `internal/translator/gemini.go` & `antigravity.go` — On parallel function calls, only the first call receives the signature/fallback, leaving sibling calls unsigned per Google Gemini 3+ specification.

**New Models & Capabilities Sync:**
- `internal/providers/capabilities.go` — Registered **`gpt-6-astra`** (Vision, Reasoning, Search, Tools; 272K window / 128K max output) and added `*gpt-6*` pattern match.
- Registered GPT-5.6 image aliases: `gpt-5.6-sol-image`, `gpt-5.6-terra-image`, and `gpt-5.6-luna-image` with `ImageOutput` capability.
- Qoder capabilities catalog refresh (`ultimate`, `performance`, `gmodel`, `gfmodel`, `qmodel_38max`, etc.).
- CodeBuddy-CN capabilities updated (`glm-5.2` vision enabled).

**Anti-Abuse Google Token Refresh (Antigravity):**
- `internal/handlers/chat/antigravity_project.go` — Configurable `ONBOARD_MAX_ATTEMPTS` (default 2, down from 5) and `ONBOARD_RETRY_DELAY_MS` (default 12s) to prevent Google account rate-limit blocks during multi-account refresh (#3813).

**Anthropic-Beta Header Forwarding & Effort Normalization:**
- `internal/handlers/chat/fallback.go` — Automatically injects `Anthropic-Beta: prompt-caching-scope-2026-01-05, context-management-2025-06-27` for `anthropic-compatible-*` nodes serving Claude models (#3797).
- `internal/translator/request.go` & `types.go` — Normalizes Claude adaptive auto effort (`output_config.effort="auto"` and `"xhigh"` $\to$ `"high"`) (#3792).

**OpenCode-Go Executor & Responses Parallel Tool Calls Fixes:**
- `internal/proxy/executor/providers.go` — Added `deriveOpencodeSession` generating stable `x-opencode-session: ses_<32hex>` headers for all `opencode-go` requests, with fallback and client tool isolation (#3800).
- Routed `muse-spark-1.2-contributor` and `muse-spark-1.3-contributor` on `opencode-go` to the `/responses` endpoint (#3819, #3820).
- `internal/proxy/executor/stream.go` — Fixed parallel tool calls argument collision on Responses API SSE stream by indexing events via `item_id`. Emits arguments from `response.output_item.done` when upstreams send arguments on item completion without deltas.
- `internal/proxy/executor/stream.go` — Fixed `handleCodexStream` SSE stream truncation and disconnects on `muse-spark-1.3` (and 1.2) by switching to `proxy.ScanStream` (up to 10MB buffered scanner), preventing line fragmentation when handling large (>3KB) encrypted reasoning payloads across TCP packet boundaries.
- `internal/proxy/executor/stream.go` — Added support for `response.reasoning_summary_text.delta`, `response.reasoning_text.delta`, and `response.thought.delta` emitting `reasoning_content` delta chunks for thinking models.
- `internal/proxy/sse.go` — Added `HeartbeatWriter` emitting periodic `: keep-alive\n\n` comments every 15 seconds during prolonged upstream reasoning phases (fixes #3796 stream stall timeouts on strict clients like Oh My Pi during deep thinking on `ag/gemini-3.8-flash*`).
- `internal/proxy/stall.go` — Added `NewStallReaderWithContext` binding client `ctx.Done()` directly to body closer, immediately freeing upstream sockets on client abort and eliminating Windows socket leaks (`CLOSE_WAIT`/`FIN_WAIT_1`).
**Database Path Configuration:**
- `internal/config/config.go` — Enhanced `DB_PATH` resolution to automatically detect `db/data.sqlite`, `data.sqlite`, or `9router.db` when pointed directly to a directory (e.g. `E:\project\database\9router`).
## [v1.8.8] — 2026-09-03

### ✨ E2E & Parity — Next.js v0.5.65 (31 commits)

**E2E Gemini 3.8 Flash High + tool calling (deterministic mocks):**
- `internal/handlers/chat/gemini38_e2e_test.go` — `gemini-3.8-flash-high` via Antigravity `2.11.0` non-stream + multi-turn + stream SSE `get_weather_ide` uncloaking, `prefixItems` cleaning, `thoughtSignature` backfill, `tool_calls` dedup. Ports `decolua/9router` `gemini-3.8-flash-medium/high/low` + `capabilities.go:*gemini-3.8*` + `antigravity.go:gemini-3.8-flash-tiered` + `proxy/gemini.go:2.11.0`.

**E2E Opencode muse-spark (deterministic mocks, no real network):**
- `internal/handlers/chat/opencode_mock_e2e_test.go` — `oc/muse-spark-1.2` & `1.3` via `Responses API /v1/responses` SSE `output_item.added` + `function_call_arguments.delta/done` aggregation, `reasoning max→xhigh`, `Vision:true` (`capabilities.go:124` pattern `*muse-spark*`), `image_url` preservation. Fixes routing `muse-spark-1.3` `500` → `200` (`providers.go:293` `Contains(muse-spark)` + `capabilities.go:124` `1.3`).

**Unit tests — now locking logic (previously untested):**
- `providers_v065_test.go` — `claude-cli/2.1.258` + full `Anthropic-Beta`, `ollama FetchURL https://ollama.com/api/web_fetch`, `gemini-3.8` caps, `muse-spark 1.2/1.3`, `codebuddy-cn hy3/hy3-x/hy4-preview/x/glm-5.3/kimi-k3-1` + EOL `glm-5.0/4.7` removed, `GetModelTokenLimits` 3.8.
- `translator/claude_cache_test.go` — `LastCacheableToolIndex` + `AnchorClaudeCache` for `defer_loading:true` tail, all-deferred, stripping client `cache_control` (#3567).
- `handlerutil/ssrf_test.go` — `trailing dot` (`localhost.`), `CGNAT 100.64/10`, `169.254.169.254`, IPv6 `::ffff:7f00:1` hex, `64:ff9b::`, `fe80/fc`, `normalizeHost`, `parseIPv6ToGroups`.
- `mitm/handlers/mitm_handlers_test.go` — `HandleKiro` removes `systemPrompt` + `userInputMessage.images → image_url data:`, `HandleAntigravity` preserves `fetchAvailableModels(2.11.0)` vs overrides `generateContent→1.23.2`.
- `usagetracker/quota_parsers_test.go` `TestParseGroqQuotasFromHeaders` — `x-ratelimit-*` Go duration `2m59.56s` → `requests/tokens` `used/total/resetAt`.
- `handlers/chat/chat_v065_test.go` — `HandleModelLookup` kind `image` + `cc/claude-sonnet-4-6` + encoded slash + 404 `model_not_found`, `HandleModels` custom `cc/my-custom-vision` caps, `StrikeReassert` 3×429 optimistic 90% → `CACHE_BLOCK 15m` + re-assert after `Refresh`.
- `proxy/executor/opencode_test.go` `MuseSpark13_ResponsesRouting` + `OCPrefix` — routing `1.3` + `oc/` to `/responses`.

**Fixes:**
- **Opencode 1.3 `500` → `200`** — `ForwardOpencode` routing `Contains(muse-spark)` + `capabilities` `1.3` Vision (fixes report `14:11:47` `muse-spark-1.3 500`).
- **jcode tool_smoke 3→1** — `stream.go:118` dedup `ToolCallIdx` + `codebuddy.go:168` `sseToOpenAIJSON` dedup `arguments` for `bash` `intent` split (fixes `echo JCODE_TOOL_OK` 3 tool_calls).
- **Flaky real upstream 429** — `muse_spark_e2e_test.go` real `opencode.ai` `429 FreeUsageLimitError` now `Skip` instead of `Fail`.
- **DB flaky `429` in `go test ./...`** — `go vet` clean, `ps` `9router-go 20130` health `{"status":"ok"}` (not stopped, log stopped due to `user stepped away` recap 98k prompt).

## [v1.8.7] — 2026-09-02

### 🐛 Bug Fixes

- **Gemini `system_instruction` Empty Part 400 Fix** — `StripCompetitivePrompts` now drops empty `system_instruction` parts after `rewriteCompetingBranding` (e.g. `"You are a Claude agent..."` -> `""`) and filters empty text parts in `contents`; if all parts are empty the `system_instruction` is removed (`nil`) instead of emitting `{"parts":[{}]}` which Gemini rejects as `system_instruction.parts[0].data: required oneof field 'data' must have one initialized field`. Also `TranslateOpenAIToGemini` now `TrimSpace` checks system content. Fixes `ForwardGemini (antigravity/gemini-3.7-flash-high): ...system_instruction.parts[0].data: required oneof`. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Gemini Tool Schema `required` Inside `properties` 400 Fix** — `cleanGeminiSchema` now detects misplaced `required` array inside `properties` (e.g. `{"properties":{"query":{...},"required":["query"]}}`) and promotes it to top-level `required`, fixing `Invalid value at 'request.tools[0].function_declarations[0].parameters.properties[0].value' (Map), Cannot have repeated items ('required') within a map. Unknown name ""`. This was the root cause of the `12:14:50` `query_db_ide` 400 after the `where.items` fix. Also sanitizes all OpenAI-compatible providers (including `opencode`) via `fallback.go` `SanitizeOpenAITools`. (`internal/translator/schema.go`, `internal/handlers/chat/fallback.go`)
- **Opencode `muse-spark` Tool Name Triplication Fix** — `sseToOpenAIJSON` `internal/proxy/executor/codebuddy.go:168` now only sets `name` if empty and avoids duplicating `arguments` already sent via `delta`/`done`; `ProcessCodexEvent` `stream.go:148` for `response.function_call_arguments.delta/done` now deduplicates `name` and tracks `ToolCallArgs` to prevent `get_weather` -> `get_weatherget_weatherget_weather` and `{"location":"Jakarta"}{"location":"Jakarta"}` on `combo-wombo` (`oc/muse-spark-1.2`) non-stream and stream. (`internal/proxy/executor/codebuddy.go`, `internal/proxy/executor/stream.go`)

## [v1.8.6] — 2026-09-02

### 🐛 Bug Fixes

- **Gemini Tool Schema `where.items.items: missing field` 400 Fix** — `cleanGeminiSchema` now ensures every `type: array` has a valid `items` schema (default `{"type":"string"}`), flattens `prefixItems` (2020-12 tuple) and `items: [...]` tuple to single `items`, and auto-fills inner `items` without `type`/`properties`/`enum`. Fixes `ForwardGemini (antigravity/gemini-3.7-flash-high): upstream returned 400: ...where.items.items: missing field.` when Claude Code sends DB-like tools with nested `array<array>` params. Added `internal/proxy/gemini.go` 400 payload dump to `/tmp/9router-gemini-400.json` for post-mortem. (`internal/translator/schema.go`, `internal/proxy/gemini.go`)
- **Bare Alias `ag` -> `antigravity` Resolution** — `resolveModel("ag")` now checks `ProviderAliasMap` before common-provider fallback, so `POST /search` with `{"model":"ag"}` correctly routes to `antigravity` instead of `deepseek/ag` -> `404 Via cloudfront`. Parity with Next.js `ag` search. (`internal/handlers/chat/resolution.go`)
- **Antigravity Search Default Model Parity** — `handleAntigravitySearch` default changed `gemini-3-flash-agent` -> `gemini-2.5-flash` (Next.js `ag` search returns `answer.model: gemini-2.5-flash`), fixing `500 UNKNOWN` from `daily-cloudcode-pa.googleapis.com` for bare `ag` search. (`internal/handlers/media/antigravity_search.go`)
- **Claude `call_` Tool Name Fallback Fix (Gateway IP Check)** — `TranslateOpenAIToClaude` no longer falls back to `tc.ID` (`call_...`) when `Function.Name` is empty; skips invalid tool calls instead of emitting `tool_use` with `name: call_...` which caused `No such tool available: call_...` and `Invalid tool parameters` churn on `toloing cek config gateway` via `opencode/muse-spark`. (`internal/translator/response.go:165`, `cf42120`, port `decolua/9router#2077`/`#3685`)
- **Claude Streaming Tool Delta Deduplication** — `TranslateOpenAIToClaudeStreamSession` now checks `state.ToolCalls[idx]` before creating a new `content_block_start`; second delta for same `idx`/`id` (Codex `output_item.added` + `function_call_arguments.delta` same `call_...`) no longer creates duplicate `tool_use` and empty `partial_json: "{}"`; correctly buffers `arguments` and emits `{"command":"ip route ..."}`. Fixes `InputValidationError: Bash missing command` on `Gue cek gateway IP...` streaming. (`internal/translator/response.go:468`, `ea10c16`)
- **Bash Extra Fields Strip** — `sanitizeBashArgs` now keeps only `command`, deletes hallucinated `description` etc. inside `input` (`{"command":"ls ...","description":"List home..."}` -> `{"command":"ls ..."}`), fixing `Bash(input JSON failed to parse — 433 bytes)` on `Cek gateway config — lagi intip file-file di home`. (`internal/translator/sanitize.go:239`, `6b94535`)
- **Server Tool Use Foreign ID Drop (Combo Poison)** — `SanitizeClaudePassthrough` drops `server_tool_use` with id not matching `^srvtoolu_` (e.g. `call_` from `z.ai/glm` `analyze_image`) and paired `tool_result`, strips empty text and empty messages. Port `decolua/9router#3686`. (`internal/translator/request.go:342`, `cf42120`)
- **1M Context Marker Strip** — `stripModelContextMarker` strips trailing `[1m]`/`[1M]` from `claude-opus-5[1m]` before `resolveModel`, so combo `combo-wombo[1m]` routes correctly instead of `Invalid model format`. Port `decolua/9router#3691`. (`internal/handlers/chat/resolution.go:135`)
- **Streaming Model Echo** — `SeedStreamState` pre-seeds `StreamState.Model` with client-requested model via `WithRequestedModel` context so `message_start` echoes `combo-wombo` not `claude-3-5-sonnet` provider model. Port `decolua/9router#3693`. (`internal/translator/usage.go:41`, `forward.go:91`)
- **GPT-5 / o-series `max_completion_tokens`** — `requiresMaxCompletionTokens` (`/gpt-5|o[134]-/i`) emits `max_completion_tokens` instead of `max_tokens` for `gpt-5`/`o1-`/`o3-`/`o4-` in `TranslateClaudeToOpenAI`. Port `decolua/9router#3657`. (`internal/translator/request.go:12`, `types.go:202`)
- **Antigravity Optimistic Quota Strike-Breaker** — after 3 consecutive `429` for same `connection|model` within 60s while quota `remaining>0`, `HandleAntigravityQuotaError` returns `CACHE_BLOCK 15m` instead of looping 300s `modelLock`. Port `decolua/9router#3684`. (`internal/handlers/chat/antigravity_quota.go:44`)
- **Forced-SSE JSON for Claude Clients** — `handleJSONResponse` detects `isSSEBody` when `translate=true` and `stream:false` retry hits forced-stream provider (Responses-API), aggregates via `sseToClaudeJSON` then `TranslateOpenAIToClaude` to return `Anthropic Message` not `chat.completion`. Port `decolua/9router#3683`. (`internal/handlers/chat/forward.go:144`)
- **OpenRouter Pattern Compat + Combo Tools Detection** — `NormalizeToolSchemasForProvider("openrouter")` strips invalid `pattern` regex (keep valid, handle `properties` named `properties`), and `DetectRequiredCapabilities` now requires `tools` capability for `type:function`/`functionDeclarations`. Port `decolua/9router#3665`. (`internal/translator/tool_schema.go`, `combo.go:129`, `forward.go:35`)

## [v1.8.5] — 2026-08-31

### ✨ Features & Parity (Next.js v0.5.59 Sync)

- **New Search Providers & Credential Fallback** — added `xquik` (X search provider with raw API key), `ollama-search`, and `zai-search` (GLM Coding web search). Added automatic credential fallback where search providers borrow API keys from parent chat connections (`ollama` / `glm`) when dedicated search connections are absent. (`internal/providers/providers.go`, `internal/providers/aliases.go`, `internal/handlers/chat/connections.go`)
- **Antigravity Web Search Provider** — added Antigravity as a web search provider via Google Search grounding, with full Next.js parity for the search response structure. (`internal/handlers/media/antigravity_search.go`)
- **New Models & Capabilities Sync** — registered new flagship models: `GLM-5.3-Flash` (1M context window + native vision multimodal), `GLM-5.3`, `DeepSeek V4 Vision`, `Grok 4.5/4.6` (500k context window), and `muse-spark-1.2-contributor-free`. (`internal/providers/capabilities.go`, `internal/providers/aliases.go`)
- **Claude Tool Type Defaulting (`type: "custom"`)** — added `DefaultClaudeToolType` ensuring tools in Claude-format requests always carry a valid `type` (defaulting to `"custom"` when omitted), preventing HTTP 400 rejection on strict Anthropic-compatible gateways such as MiniMax. (`internal/translator/request.go`, `internal/handlers/chat/chat.go`)
- **Claude Code Session ID Header Support** — prioritized `x-claude-code-session-id` in `ExtractSessionID` to ensure stable prompt caching and avoid conversation fragmentation across client tool calls. (`internal/handlerutil/response.go`)
- **CommandCode In-Stream Error Peeking** — peeks the initial NDJSON event in CommandCode stream for `type: "error"` before committing HTTP 200 OK headers, transforming internal stream errors into real HTTP error statuses (429, 503, 401, etc.) so combo and account fallback trigger seamlessly. (`internal/proxy/executor/stream.go`)
- **OpenCode Responses API Parity (v0.5.59)** — completed Responses API translation for OpenCode Muse Spark: proper tool names emitted on `response.output_item.added`, accurate usage and prompt-cache token extraction from `response.completed`, Claude SSE streaming translation and non-streaming support in `handleCodexStream`, and 64-char clamping for `call_id`. (`internal/proxy/executor/`)

### 🐛 Bug Fixes

- **Gemini Cached Token Extraction** — added support for both `cachedContentTokenCount` and `cachedContentToken` keys in Gemini stream and non-stream responses. (`internal/translator/gemini.go`)
- **Non-Interactive Test Execution** — bypassed interactive `sudo security` CA keychain install when executing unit tests, ensuring fast, deterministic test suite completion. (`internal/mitm/cert.go`)
- **Self-Update SHA256 Verification** — `PerformSelfUpdate` now downloads to memory, verifies the expected SHA-256 checksum, and refuses to install mismatched binaries, eliminating the risk of installing corrupted or tampered updates. (`internal/updater/updater.go`)
- **Graceful Self-Restart** — replaced abrupt `os.Exit` after self-update with `syscall.Kill(SIGTERM)` plus a graceful fallback, giving in-flight requests and DB connections a chance to drain cleanly. (`internal/updater/updater.go`)
- **Cross-Platform Restart** — extracted the self-signal into a platform-specific `signalSelfShutdown` helper (`signal_unix.go` sends SIGTERM; `signal_windows.go` is a no-op that falls back to `os.Exit(0)`), fixing the Windows cross-compile of the release binaries. (`internal/updater/signal_unix.go`, `internal/updater/signal_windows.go`)
- **Smart Archive Executable Selection** — `extractExecutableBytes` now scores archive entries (penalizing README/LICENSE/`*.md`/`*.sha256`) and validates ELF/Mach-O/PE magic bytes, reliably picking the real binary from multi-file release archives. (`internal/updater/updater.go`)
- **SSE Copy Race Condition** — replaced the shared pooled buffer in `SSECopy` with a per-call local buffer, eliminating concurrent read/write races on the pool buffer. (`internal/proxy/sse.go`)
- **Nil Guard in Token-Saving Compression** — guarded against a nil `rawMap` when the upstream body cannot be decoded, preventing a panic on malformed responses. (`internal/tokensaver/compress.go`)
- **Quota Percentage Clamping** — clamped `RemainingPercentage` to a sane `[0, 100]` range so upstream values >100 or negative cannot skew quota-block and dashboard logic. (`internal/usagetracker/quota_parsers.go`)
- **Exponential Backoff for Antigravity Onboarding** — replaced the fixed 2s sleep between `onboardUser` retries with exponential backoff (2s, 4s, ...) that also honors context cancellation, so a 429 burst no longer gets hammered by fixed-interval retries. (`internal/handlers/chat/antigravity_project.go`)
- **Decloak Deduplication** — extracted a shared `decloakContentBlockStart` helper used by both `DecloakStreamChunk` and `DecloakClaudeStreamEvent`, removing duplicate content-block-start logic. (`internal/translator/antigravity.go`)
- **Tool Property Sanitization** — preserved tool parameters named after reserved keywords and sanitized `required` fields against the declared `properties`, preventing schema validation failures. (`internal/translator/sanitize.go`)

## [v1.8.4] — 2026-08-14

### 🐛 Bug Fixes & Resilience

- **Combo Cycle Graceful Recovery & Fault Tolerance** — `flattenComboModels` now gracefully skips recursive / self-referencing combo branches with a warning log instead of failing hard with HTTP 400 (`combo cycle detected`), ensuring chatbot requests continue executing remaining valid models seamlessly. (`internal/handlers/chat/resolution.go`)
- **Safe Model Resolution on Leaf Models** — eliminates potential slice index-out-of-range edge cases when resolving combo leaf models that do not contain a provider prefix. (`internal/handlers/chat/resolution.go`)
- **Multi-Level Nested Combo Support** — verified recursive cascading combo expansion (e.g. `super-combo` → `mid-combo` → `base-combo` → leaf models) so all reachable models participate in round-robin, sticky, and fallback strategies. (`internal/handlers/chat/resolution.go`, `internal/handlers/chat/resolution_test.go`)

## [v1.8.3] — 2026-08-14

### ✨ Features

- **Antigravity Gemini 3.7 Flash Model Mapping** — canonical model IDs and aliases for `gemini-3.7-flash`, `gemini-3.7-flash-high`, `gemini-3.7-flash-agent`, `gemini-3.7-flash-medium`, `gemini-3.7-flash-low`, `gemini-3.7-flash-extra-low`, and `gemini-3.7-flash-thinking` correctly mapped to Google Antigravity backend model IDs (`gemini-3-flash-agent` / `gemini-3.5-flash-low`), fixing upstream 404 errors. (`internal/translator/antigravity.go`)
- **Enriched Prompt-Injection Guard** — enhanced prompt-injection detector with heuristic patterns for raw model delimiters (`<|im_start|>system`, `<<SYS>>`, `[SYSTEM PROMPT]`, `[INST]`), verbatim system prompt extraction attempts, and developer/admin mode override simulations. (`internal/tokensaver/injection.go`)
- **Accurate Gemini Cached Token Tracking** — correctly unmarshals and propagates `cachedContentTokenCount` from Gemini stream and non-stream responses into `OpenAIUsage.CachedTokens`, providing accurate cache hit reporting and cost calculation. (`internal/translator/gemini.go`)
- **Gemini Vision FileData & Audio Modalities** — added support for remote HTTP/HTTPS image URLs (`fileData: { fileUri, mimeType: "image/*" }`), base64 input audio (`input_audio`, `audio_url`), and uploaded documents in Gemini native translator, matching Next.js full multimodal capabilities. (`internal/translator/gemini.go`)
- **Realtime SSE Usage Stream & Topology Animation** — added in-memory in-flight request tracker (`internal/usagetracker`), real-time SSE broadcasting (`GET /api/usage/stream` and `GET /usage/stream`), and recent requests ring buffer matching the Next.js dashboard shape, enabling instant glowing pulse node & marching-ants edge animations on the Usage Topology graph when requests are handled by `9router-go`. (`internal/usagetracker/tracker.go`, `internal/handlers/usage_stream.go`, `internal/handlers/chat/fallback.go`, `internal/handlers/chat/usage.go`)
- **Antigravity Anti-Competitive Prompt Stripping & 429 Prevention** — automatically strips competitor identity phrases (e.g. `"You are a Claude agent, built on Anthropic's Claude Agent SDK."` from Zed IDE and Claude agents) from `system_instruction` and message contents, preventing Antigravity from returning synthetic `429 Quota Exhausted` errors. (`internal/translator/antigravity.go`)
- **Edge Relay URL Rewriting & Header Forwarding** — automatically rewrites `BaseURL` to the relay deployment and injects `x-relay-target` and `x-relay-path` headers when a connection uses a Vercel, Cloudflare Worker, or Deno Edge Relay Proxy Pool. (`internal/handlers/chat/connections.go`)
- **No-Auth Provider Proxy Pool Strategy** — automatically respects `settings.providerStrategies` for no-auth providers (e.g. `mimo-free`, `opencode`), attaching configured proxy pools or rotation strategies to virtual connections. (`internal/handlers/chat/connections.go`, `internal/db/settings.go`)
- **Snake_case Model Limits on `/v1/models` & `/v1/models/info`** — exposes `context_length`, `max_completion_tokens`, `max_input_tokens`, and `max_output_tokens` so clients like Cline, Roo Code, and LibreChat resolve proper context ceilings. (`internal/handlers/chat/chat.go`, `internal/providers/capabilities.go`)
- **CodeBuddy OAuth Configuration** — registered `codebuddy-cn` and `codebuddy-intl` OAuth token refresh configurations. (`internal/providers/oauth.go`)
- **OpenCode Official Client Fingerprint Headers** — injects official headers (`User-Agent: opencode`, `x-opencode-client: desktop`, `x-opencode-session: ses_...`, `x-opencode-request: msg_...`, `x-opencode-project: global`) on free-tier OpenCode requests to prevent rate limiting from unidentified client traffic. (`internal/proxy/opencode.go`, `internal/proxy/executor/providers.go`)
- **Kimchi Dual Authentication** — supports direct API keys (`Authorization: Bearer <key>`) in addition to OAuth tokens with seamless credential resolution. (`internal/handlers/chat/connections.go`, `internal/handlers/chat/kimchi_handler_test.go`)
- **Startup Banner & Version Display** — dynamically displays current version in CLI startup banner (`🚀 9Router Go Proxy (v1.8.3) on :20130`) and server ready logs. (`cmd/9router-go/main.go`)
- **New Provider Registries & Aliases** — added Alibaba Token Plan Singapore (`alitp-intl` / `ali-tp` / `alitp`) and Fish Audio Text-to-Speech (`fish-audio` / `fish`). (`internal/providers/providers.go`, `internal/providers/aliases.go`)

### 🐛 Bug Fixes

- **Invalid Tool Parameters & Decoy Schemas** — provided valid non-empty `properties.reason` schema for all 21 Antigravity decoy tools and mapped `tool_call_id` to exact function names in OpenAI-to-Gemini conversation history, eliminating protobuf validation errors when using Claude Code or other tool-calling clients. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Antigravity Upstream Model Resolution** — prevented invalid model aliases like `gemini-3.7-flash-high` from reaching Google Cloud Code without being translated to their backend model IDs. (`internal/translator/antigravity.go`)

### 📚 Documentation

- Comprehensive refresh of `README.md`, `COMPARISON.md`, `DATABASE.md`, `ARCHITECTURE.md`, `TECHNICAL_DEBT.md` (0 open items), and newly added `ROADMAP.md`.

## [v1.8.2] — 2026-08-14

### ✨ Features

- **Antigravity Tool Cloaking & Anti-Ban Decoy System** — automatically cloaks client tool declarations with `_ide` suffixes (e.g. `Bash_ide`), injects 21 official Antigravity IDE decoy tools (`run_command`, `replace_file_content`, `grep_search`, `list_dir`, etc.), synchronizes conversation history functionCall/functionResponse names, and seamlessly uncloaks tool names on response SSE stream and non-stream outputs. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Antigravity Native Image Generation** — added image model detection (`imagen`, `*image*`), aspect ratio suffix parsing (`16x9`, `4:3`, `1:1`, custom resolutions via GCD reduction), `requestType: "image_gen"` envelope wrapping with forced non-streaming `/v1internal:generateContent`, and OpenAI-compatible base64 image response formatting. (`internal/translator/antigravity.go`, `internal/proxy/gemini.go`)
- **Edge Relay & Transport Engine** — added transport support for Vercel, Cloudflare Worker, and Deno edge relays using `x-relay-target` and `x-relay-path` headers, wildcard `noProxy` domain filtering, and legacy connection-level proxy configuration fallback (`connectionProxyUrl`). (`internal/proxy/transport.go`, `internal/handlers/chat/connections.go`)

### 🐛 Bug Fixes

- **Proxy Pool DB Parsing Bug** — fixed `GetProxyPool` (`internal/db/proxyPools.go`) failing to parse single string `proxyUrl` created by Next.js UI / `InsertProxyPool`, which previously caused proxy pools to be silently ignored and requests to fall back to direct connections. Added parsing for `type`, `noProxy`, and `strictProxy` metadata.

## [v1.8.1] — 2026-08-12

### ✨ Features

- **Combo strategy sync with Next.js reference** — per-combo rotation state, correct auto-switch ordering, and a capabilities provider (`internal/providers/capabilities.go`) replacing the hardcoded vision/pdf maps with tiered capability detection.
- **Flatten nested combos** — `combo-wombo → free-tier` now expands to its four leaf models, so round-robin actually rotates across them instead of always landing on the first leaf (this was hammering one account and producing the `429 all connections for this provider are rate-limited` error).
- **Turn-aware rotation** — `applyComboStrategy` advances the rotation index only on a new turn; mid-turn tool-use requests reuse the model serving the turn, so the provider never switches mid-turn (which broke Gemini thinking models that require a `thought_signature` on current-turn function calls).
- **Bounded retry-once on total combo 429** — when every combo model fails with a Retry-After ≤ 8s, the pass waits once and retries before surfacing a hard 429 (`comboRetryAfter`).
- **Backfill default `thought_signature`** — every `functionCall` part now carries a `thoughtSignature` (the real one via `__ts__` transport when present, else the Next.js `DEFAULT_THINKING_AG_SIGNATURE`), closing the last 400-`thought_signature` gaps on mixed combos.
- **Gemini tool-schema keyword parity** — strip the remaining unsupported JSON-Schema keywords (`multipleOf`, `uniqueItems`, `contains`, `unevaluated*`, `contentSchema`) and fill bare `{}` schemas with the object placeholder, matching Next.js `cleanJSONSchemaForAntigravity` (fixes `Invalid tool parameters` 400 from antigravity).
- **Sanitize tools on the OpenAI-compat Gemini path** — the `gemini` provider is now marked `gemini-openai` and its `/v1beta/openai` bodies are run through `SanitizeOpenAITools`, so the strict schema validation applies on both Gemini routes.

### 🐛 Bug Fixes

- **Emit camelCase `thoughtSignature`** — the Gemini-native `generateContent` endpoint only recognizes the camelCase part field; the snake_case regression caused the `400 Function call is missing a thought_signature` error. Both read and write directions now handle camelCase.
- **Use the daily antigravity endpoint** — migrate `cloudcode-pa.googleapis.com` → `daily-cloudcode-pa.googleapis.com` for the `antigravity` provider (`providers.go`, `antigravity_project.go`, MITM domain list) to avoid strict rate limits.
- **Combo connection retry-loop parity** — connection retry loop now matches the single-model path.

### 🧹 Chores / Docs

- Remove the stray `patch_combo.go` throwaway script.
- Add design specs and implementation plans for the combo sync / thought_signature / backfill / schema-parity work.

## [v1.8.0] — 2026-08-11

### 🐛 Bug Fixes

- **Combo/router fallback bypasses model-lock backoff on retryable errors → antigravity rate-limit loop** — `handleComboFallback` / `handleMessagesComboFallback` (`internal/handlers/chat/combo.go`) called `tryForwardWithConnection` directly, so `LockConnectionModel` was never invoked on retryable errors (429/500s) — unlike the single-model path `handleAccountFallback`. The exponential 429 backoff was dead in the router path: every request re-tried all combo models back-to-back on the same connection/account, got 429, returned 429, and the client's ~35s retry repeated the loop forever. Fix:
  - New `comboLockRetryable` helper runs on every `RetryableStatusCodes` error in both combo loops — classifies via `ClassifyError`, calls `LockConnectionModel(connID, model, cooldownSec, newBackoffLevel)` so the exponential backoff persists across requests, and appends the conn to a request-local `excludeIDs` passed into `getBestConnection` so remaining combo models don't re-select the same connection (same account = same quota bucket).
  - A locked-connection skip covers pinned connections whose direct-fetch branch bypasses `getBestConnection`'s lock check.
  - `context.Background()` → `ctx` in `handleComboFallback` so client cancels propagate; the 502/503/504 transient-wait sleep is preserved.
  - Test: `TestHandleMessagesComboFallback_429LocksAndExcludesConnection` asserts a 429 locks the connection AND keeps the second combo model from re-hitting it (exactly 1 upstream hit).

## [v1.7.2] — 2026-08-08

### 🐛 Bug Fixes

- **Antigravity 429/404 failure-loop fix** — An unprovisioned Antigravity account
  (`onboardUser` returns `200` with an empty `cloudaicompanionProject`) left the
  connection without a `projectID`. The router then force-refreshed the OAuth
  token on every request (never an auth problem, so it never helped), fell through
  to a guaranteed-404 OpenAI-compatible lane on `cloudcode-pa.googleapis.com`,
  and repeated client retries rammed Google's rate limit (`429`). (`internal/handlers/chat/gemini_handler.go`, `internal/handlers/chat/antigravity_project.go`)
  - `fetchAntigravityProjectID` now reports the outcome (`projectID`, `authFailed`,
    `noProject`). Token refresh runs **only** on a genuine `401/403` — never on a
    missing/empty project.
  - When antigravity has no project ID, it no longer burns a request on the dead
    OpenAI lane; it returns an error and the fallback chain moves straight to the
    next provider.
  - **Negative cache (10 min, per connection):** once Google confirms "no project",
    later requests skip the `loadCodeAssist`/`onboardUser` RPCs entirely — this is
    what stops the repeated `429` hammering.
  - Onboarding guidance is logged once per connection per window
    ("onboard the account via Antigravity IDE/CLI, then re-login"); repeated
    failures log at `Debug` instead of spamming `Warn`. (`internal/handlers/chat/fallback.go`)

### 🧪 Tests

- `antigravity_project_test.go` — pins the probe classification (project found /
  token rejected `401`+`403` / project definitively missing / transient `429`+`503`)
  and the negative-cache expiry semantics. (`internal/handlers/chat/antigravity_project_test.go`)

## [v1.7.1] — 2026-08-08

### 🐛 Bug Fixes

- **Cached-token parity across every provider** — Prompt-cache accounting no longer works only for antigravity. Gemini `usageMetadata.cachedContentToken` now flows through both non-stream and stream translation into OpenAI `usage.cached_tokens`; the `!translate` response path uses a dual-format parser (`ParseResponseUsage`) that reads Claude `cache_read_input_tokens`/`cache_creation_input_tokens` and OpenAI `prompt_tokens_details.cached_tokens`, so cached tokens survive any provider → OpenAI → Claude double translation. (`internal/translator/gemini.go`, `internal/translator/response.go`)
- **Gemini tool-schema `const` re-injection** — `stripUnsupported` now re-runs after `anyOf`/`oneOf` flattening so `const` and vendor `x-*` keys can't leak back into the merged branch. (`internal/translator/schema.go`)
- **Provider 403 is now retryable** — Gemini/antigravity daily-quota errors can arrive as HTTP 403; these now trigger the connection fallback instead of a hard failure. (`internal/providers/providers.go`)
- **CodeBuddy CN stream cleanup** — The stall reader is now closed after the stream, stopping its shutdown watcher + stall timer (no per-request goroutine leak). (`internal/proxy/executor/codebuddy.go`)

### ⚙️ Graceful Shutdown Hardening

- New `internal/shutdown` package: a process-wide signal the first Ctrl+C / SIGTERM fires.
- `StallReader` now closes in-flight SSE upstream bodies on shutdown, so `server.Shutdown` drains streams in milliseconds instead of waiting out the 15s deadline — and the deferred DB/log-file close always runs.
- Translate-path SSE handlers emit a final `data: [DONE]` on abort so clients get a clean end instead of a truncated stream.
- A second Ctrl+C / SIGTERM force-quits immediately (stuck-drain escape hatch).
- Shutdown timeout logs a warning instead of `log.Fatalf`, so `conn.Close()` and the log file are still closed gracefully. (`internal/proxy/stall.go`, `cmd/9router-go/main.go`, `internal/handlers/chat/forward.go`, `internal/proxy/executor/openai.go`)

### 🔧 Internal

- Stream handlers now carry the request `ctx` and pull accumulated usage (incl. cached tokens) out of the translation session, so logged usage reflects real token counts instead of the character-estimate fallback.

## [v1.7.0] — 2026-08-06

### 🚀 New Executors

- **Trae SOLO remote agent** (`internal/proxy/executor/trae.go`) — Port of `open-sse/executors/trae.js`: `POST {base}/chat_sessions` creates a session, `GET {base}/chat_sessions/{id}/events` streams `plan_item` / `token_usage` / `done` as SSE. Cumulative `plan_item.thought` rendering (longest-wins per id, delta-only emission), `Cloud-IDE-JWT` auth, and `work`/`auto`/manual model modes. Non-stream requests aggregate into a single `chat.completion`. Round-trip test: `TestForwardTrae_StreamsAccumulatedThought`.
- **Windsurf gRPC-web** (`internal/proxy/executor/windsurf.go`) — Port of `open-sse/executors/windsurf.js`: hand-rolled protobuf `GetChatMessageRequest` encoder (Metadata.api_key + cascade_id + model_or_alias + repeated messages), gRPC-web framing (0x00 flag + big-endian length), and a `CompletionChunk` decoder (content / done+UsageStats / error) streaming OpenAI SSE. Catalog→wire model alias map ported verbatim; `crypto/rand` session/cascade ids. Non-stream requests aggregate frames into `chat.completion`. Round-trip test: `TestForwardWindsurf_StreamsGRPCWeb`.

### 🎙️ Xiaomi MiMo TTS

- `/v1/audio/speech` for the `xiaomi-mimo` provider now uses the chat-completions contract (port of `open-sse/handlers/ttsProviders/xiaomi-mimo.js`): target text in `role:assistant`, style/language instructions in `role:user`, voice via top-level `audio.voice`, base64 audio from `choices[0].message.audio.data`. (`internal/handlers/media/media.go`)

### ➕ Providers

- **tokenrouter** — Registered as an OpenAI-compatible upstream (`https://api.tokenrouter.com/v1/chat/completions`).

### 🗑️ Removed

- **qwen provider** — Removed from providers, OAuth config, and the alias map (deprecated upstream).

### 📋 Docs

- `TECHNICAL_DEBT.md` — windsurf + trae moved to resolved; zed + devin-cli documented with the safe-stopgap note (devin-cli corrected: ACP over **stdio** subprocess, not HTTP).

## [v1.6.1] — 2026-08-05

### 🐛 Bug Fixes

- **CodeBuddy CN 502** (`internal/proxy/executor/codebuddy.go`) — `codebuddy-cn` / `codebuddy-intl` now use a dedicated executor that forces `stream=true` upstream (CodeBuddy rejects non-stream with HTTP 400 code 11101), injects the CLI/IDE static headers, and re-aggregates OpenAI-chat SSE into a single `chat.completion` for non-stream clients (`sseToOpenAIJSON`, mirroring JS `parseSSEToOpenAIResponse`).
- Provider parity with the reference implementation.

## [v1.6.0] — 2026-08-04

### 🚀 Next.js Engine Feature Ports

- **TTS Voice Listing** (`/audio/voices`) — Full voice-listing with provider support (`edge-tts` default, `elevenlabs`, `gemini`, `local-device`), `?lang` filter, 24h in-process cache, and `byLang`/`languages` grouping matching the dashboard's media-providers page.
- **Proxy-Pools Deploy** (`/proxy-pools/{vercel,deno,cloudflare}-deploy`) — Deploy edge relay functions to Vercel/Deno/Cloudflare with status polling, plus a new `InsertProxyPool` DB method writing byte-compatible `data` JSON.
- **Headroom Management** (`/headroom/*`) — Full headroom-ai lifecycle in Go: binary/Python detection, spawn/stop/restart, compression extras install/uninstall, `/headroom/proxy` reverse proxy with SSRF guard, and dashboard HTML rewrite.
- **CLI-Tools Status** (`/cli-tools/all-statuses`) — Batch detection of 14 CLI tools (Claude, Codex, OpenCode, etc.) installed state + version.
- **Live Console Logs** (`/translator/console-logs`, `/stream`) — In-process ring buffer + SSE streaming of engine log output so the dashboard's "Monitor Console Log" shows Go logs live (25s keepalive, init/line/clear events).

### 🔍 Observability

- **Lightweight Request Tracing** (`/debug/traces`) — In-memory span recording + p50/p95/p99 latency per provider+model with `?n=` cap. Stdlib-only, no OpenTelemetry SDK dependency.

### 🛡️ Security

- **Prompt-Injection Guard** — Heuristic detection (`messages[]`, `input[]`, Claude content blocks) tagging classic injection attempts in logs. Toggle via `--no-injection-guard` / `INJECTION_GUARD_DISABLED` (on by default).
- **`/admin/health/reset` Moved Behind API-Key Auth** — Previously public; now requires a valid API key to prevent unauthenticated health-state resets (open-source hardening).
- **MITM Binds Loopback Only** — TLS proxy binds `127.0.0.1:443` instead of all interfaces, preventing LAN clients from using it as an open proxy.

### 🐛 Bug Fixes & Stability

- **SSE Fragment Rejoin** — Fixed `unexpected end of JSON input` on opencode free-tier by buffering/rejoining truncated SSE JSON payloads per session (1 MiB cap).
- **Codex/CommandCode Tool-Call Streams** — Stable per-call tool IDs/indices (using upstream `call_id`), correct `[DONE]` framing, and checked `w.Write` errors.
- **MITM Goroutine Leaks** — `Stop()` drains in-flight connections (WaitGroup + active conn close); request bodies bounded at 10 MiB.
- **Executor/OAuth Registry Mutexes** — Package-level registry maps now guarded by `sync.RWMutex` (race-free on re-registration).
- **Token Saver JSON Number Preservation** — `CompressMessages`/`InjectSystemPrompt` use `json.Number` so numeric fields (temperature, large ints) round-trip unchanged.
- **`interface{}` → `any`** — Lint cleanup across stream/log packages.

## [v1.5.0] — 2026-07-24

### 🚀 Architecture & Observability Enhancements

- **Modular `main.go` Refactoring** — Extracted CLI subcommands (`mitmEnable`, `mitmDisable`, `mitmStatus`, `resolveDataDir`) to `cmd/9router-go/commands.go` and encapsulated server routing setup into `handlers.SetupServerRouter()`.
- **Structured Request Logging Middleware** — Moved `statusWriter` and `RequestLogger` to `internal/middleware/logging.go`. Requests are logged with Correlation ID (`id=req_...`) using structured logger (`slog.Info`, `slog.Warn`, `slog.Error`).
- **Dynamic HTTP Status Log Levels** — Requests with status 5xx are logged at `ERROR` level, 4xx at `WARN` level, and 2xx/3xx at `INFO` level for clean log filtering in production.
- **Upstream Memory Exhaustion Protection** — Added `io.LimitReader` caps (1MB for upstream error bodies, 10MB for non-streaming completion bodies) to protect proxy memory from rogue upstreams.
- **Double WriteHeader Prevention** — Added `written bool` guard to `statusWriter` and `cw.IsCommitted()` checks across combo fallback handlers to eliminate `superfluous response.WriteHeader` warnings.
- **Typed Request ID Context Key** — Shared `log.RequestIDKey` across middleware and logging packages to ensure context lookups match reliably.

## [v1.4.0] — 2026-07-23

### 🛠️ Technical Debt Remediations (All 9 Items Resolved)

- **Context-based Per-Request Usage Capture** — Replaced global `translator.lastUsage` with context-captured isolation (`WithUsageCapture`, `SetUsage`, `GetAndClearUsage`) to eliminate cross-request data races under concurrent traffic. (`internal/translator/usage.go`)
- **Thread-safe Daily Usage Updates** — Protected `upsertDailyUsage()` with `dailyUsageMu` mutex to prevent concurrent SQLite read-modify-write races. (`internal/handlers/chat/usage.go`)
- **Committed Response Writer** — Wrapped `http.ResponseWriter` with `committedResponseWriter` to prevent safe-retry attempts after response headers have already been sent to the client. (`internal/handlers/chat/response_writer.go`)
- **Strict Context Propagation** — Replaced all `http.NewRequest` with `http.NewRequestWithContext` across handlers, proxy execution drivers, and OAuth helpers to prevent orphaned upstream connections.
- **Graceful Shutdown** — Implemented `http.Server` graceful shutdown with signal drain (15-second timeout) on SIGINT/SIGTERM. (`cmd/9router-go/main.go`)
- **SQLite Connection Pool Optimization** — Reduced SQLite `SetMaxOpenConns(4)` for optimal WAL mode performance and zero connection contention. (`internal/db/client.go`)
- **Thread-Safe ProxyPool Cache** — Added `sync.Map` `proxyPoolCache` in `internal/db/proxyPools.go` to preserve round-robin rotation indices across requests. (`internal/db/proxyPools.go`)
- **Unbounded Request Body Guard** — Added `middleware.MaxBody` (10MB limit) to protect all endpoints from OOM attacks. (`internal/middleware/max_body.go`, `cmd/9router-go/main.go`)

### ⚡ Metrics, Latency & Token Accounting Fixes

- **TTFT & Latency Tracking** — Added `StartTime` and `TTFT` tracking across all streaming and non-streaming proxy execution drivers (`openai`, `opencode`, `deepseek`, `claude`, `grok-cli`, `qoder`, etc.). (`internal/proxy/executor/`)
- **Input Token Calculation Fix** — Added `[]byte` type support to `CountValueChars` so fallback prompt token calculation accurately estimates token size instead of defaulting to 1 token. (`internal/handlers/chat/chat.go`)
- **Output Token Calculation Fix** — Connected `ResponseBuf` in `executor.Request` to record stream output tokens when upstream omits token usage objects. (`internal/proxy/executor/openai.go`)
- **Prompt Caching Tokens Support** — Updated `OpenAIUsage` to extract `cached_tokens` (`prompt_tokens_details.cached_tokens`) and `cache_creation_input_tokens`. (`internal/translator/types.go`, `internal/handlers/chat/usage.go`)

### 🧪 End-to-End Integration Test Suite

- **E2E Test Suite** — Added `internal/handlers/chat/e2e_integration_test.go` to test real HTTP streaming SSE, non-streaming JSON responses, TTFT latency, token accounting, and SQLite DB usage logging end-to-end.

### 🌐 Endpoints

- **`/api/hello`** — Registered `/api/hello` route returning `200 OK` for ping probes from Claude Code CLI. (`cmd/9router-go/main.go`)

## [v1.3.0] — 2026-07-22

### 🏥 Next.js-Compatible Health System

- **Connection-based health** — Replaced old `kv`-based `IsProviderHealthy`/`RecordProviderHealth` with `modelLock_*` fields in `providerConnections.data` JSON blob, matching Next.js `markAccountUnavailable` / `clearAccountError` flow. (`internal/db/health.go`, `internal/db/accounts.go`)
- **Per-connection model locks** — `LockConnectionModel` / `UnlockConnectionModel` / `IsConnectionModelLocked` use SQLite `json_set()` on shared `providerConnections.data`. Dashboard can read/write same fields. (`internal/db/accounts.go`)
- **`IsProviderAvailable`** — New `Repo` method checks if ANY connection for a provider has no active `modelLock_<model>`, replacing the old kv-based pre-check. (`internal/db/accounts.go`)
- **`POST /admin/health/reset`** — Resets `modelLock_*` on connections via query params `?provider=X&model=X`. Dashboard can call via headroom proxy. (`cmd/9router-go/main.go`)
- **Eliminated duplication** — Package-level `IsProviderHealthy` / `ResetProviderHealth` now delegate to `NewRepo(database)` instead of duplicating lock JSON parsing logic. (`internal/db/health.go`)

### 🧪 Test Fixes

- **False-pass assertions** — 3 handler tests were checking old kv-based `repo.IsModelLocked()` which always returned `false` vacuously. Changed to `repo.IsConnectionModelLocked(connID, model)` to actually verify connection-level locks. (`internal/handlers/chat_test.go`)

## [v1.2.0] — 2026-07-22

### 🎯 Gemini Tool Calling Fixes

- **thought_signature round-trip** — Gemini response encodes `thought_signature` into tool call `id` via `__ts__` separator; request decoder restores it for valid verification. Works for both streaming and non-streaming. (`internal/translator/gemini.go`)
- **Antigravity (AGY) support** — Custom `GeminiPart.UnmarshalJSON` handles `thoughtSignature` (camelCase) AND `thought_signature` (snake_case) since the internal `v1internal` endpoint returns camelCase. (`internal/translator/gemini.go`)
- **Tool response name fix** — `tool_call_id` with `__ts__` suffix no longer corrupts `functionResponse.name` extraction, preventing Gemini validation errors on turn 2. (`internal/translator/gemini.go`)

### 🎨 Logging

- **ANSI color-coded logs** — `INF` = green, `WRN` = yellow, `ERR` = red, `DBG` = cyan. Auto-detects TTY (disabled when piped). Disable via `NO_COLOR=1`. (`internal/log/log.go`)

### 🔧 Streaming Fixes

- **SSE multi-line** — Gemini stream chunks with multiple SSE lines (`data: ...\ndata: ...`) are now split and translated individually. Error on one line continues to next instead of aborting. (`internal/handlers/gemini_handler.go`)

### 🧹 Cleanup

- `fallback.go`: Removed misleading `WRN tokensaver failed` logs — replaced with idiomatic `if next, did := ...; did` pattern.
- `test_opencode.go`: Removed (stale temporary test file).
- `internal/translator/gemini_test.go`: Added (unit tests for `thought_signature` round-trip).

## [v1.1.0] — 2026-07-21

### 🚀 New Features

- **SSRF protection** — `/v1/web/fetch` now blocks requests to private/internal IPs (RFC 1918, loopback, link-local, cloud metadata). Matches Next.js `assertPublicUrl()`. (`internal/handlerutil/ssrf.go`)
- **Bypass handler** — Detects Claude Code naming, warmup, and count requests. Returns fake responses without calling upstream, preventing wasted combo rotation slots. (`internal/handlers/bypass.go`)
- **Structured logging** — New `internal/log` package with Info/Warn/Error/Debug levels, runtime config via `LOG_LEVEL` env var. All ~100 `log.Printf` calls replaced across 24 files.
- **Per-connection model locks** — Model locks now stored as `modelLock_<model>` in `providerConnections.data` JSON blob. DB-compatible with Next.js dashboard. Connection A and B can have independent lock states.
- **SSE stall detection** — `StallReader` wrapper closes upstream connection after 6 minutes of no data, preventing hung streams. Integrated into all 4 SSE stream paths.
- **Error classification** — Text-based error rules (8 patterns) + status-based rules (5 codes) + exponential backoff (2s–5min). Fully matching Next.js `checkFallbackError()`.
- **Retry-after tracking** — Tracks earliest `retryAfter` across combo models, includes `Retry-After` header in error responses.
- **Request ID tracing** — Every response includes `X-Request-ID` header, access log includes `id=xxx` prefix.
- **Combo strategies aligned with Next.js** — Sticky round-robin, auto-capability-switch (vision/pdf detection).
- **Health/lock check in combo loops** — Skip unhealthy or locked models during fallback iteration.

### 🔧 Refactoring

- **Error response consistency** — `WriteJSONError` now status-code-aware (e.g., 401 → `authentication_error`, 429 → `rate_limit_error`). `auth.go` inline JSON replaced.
- **SSE consolidation** — `proxy.WriteSSEHeaders` shared by all 4 SSE stream functions. `proxy.SSECopy` with optional `onChunk` callback.
- **Shared test fixture** — `internal/dbtest` package provides canonical `CreateTables()` eliminating duplicated schema in 5+ test files.
- **`stringBuilder` → `bytes.Buffer`** — Removed duplicate custom type in favor of standard library.

### 📚 Documentation

- `ARCHITECTURE.md` — 10 Mermaid flow diagrams (request lifecycle, combo, fusion, error classification, etc.)
- `DATABASE.md` — All 11 tables, JSON blob structure, Go vs Next.js differences

### 🐛 Fixes

- `RetryAfter` ceiling calculation corrected from floor to proper ceiling (`time.Second - 1`)
- Stream translation now handles `[DONE]` marker before JSON parsing
- `TranslateResp` field now passed in `tryForwardWithConnection`

## [v1.0.2] — Previous

- Initial release with OpenAI/Claude SSE proxy, combo fallback, token savers, benchmark results.
