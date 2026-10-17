# KeiRouter Feature Port Plan — 9router-go

> **Dibuat**: 2026-10-07
> **Sumber**: KeiRouter `https://github.com/mydisha/keirouter` `backend/internal/*` (41 paket), `store/migrations/*` (MIT)
> **Target**: `https://github.com/luqman-v1/9router-go` (Go 1.27 + Chi + Uber Fx + Svelte 5, SQLite WAL, single-binary)
> **Status**: Analisis & spesifikasi siap implementasi

> **BACA BAGIAN INI DULU.** Beberapa fitur di bawah sudah selesai di 9router-go; jangan dikerjakan ulang.

---

## Status & Rencana Eksekusi (2026-10-16)

**Koreksi sumber.** Dokumen ini ditulis 2026-10-07 dari klon KeiRouter `3d8b702` (fork `helmiau`, 2026-09-08). Upstream `mydisha/keirouter` kini `0918b9e` (**v0.2.0**, 2026-10-13): **20 commit di depan, 453 file berubah**, +3 paket (`audit`, `errclass`, `skills`) dan +4 migrasi (`0030`–`0033`). Worktree baca-saja upstream: `D:/GithubRepository/keirouter-upstream`. Semua status di bawah diverifikasi ulang ke kode 9router-go `d995986` (1.9.14-exp.1), bukan dari dokumen ini.

### A. Status per fitur (terverifikasi 2026-10-16)

| # | Fitur | Status | Bukti |
|---|-------|--------|-------|
| F-1 | Rate limit per-key (RPM/TPM/konkurensi + antrean) | **SELESAI** | `internal/middleware/ratelimit.go:888-894`; kuota token `:762`; kolom `internal/db/schema.go:279-285` |
| F-2 | Budget USD + hard-cutoff | **PARSIAL** | hanya `tokenQuota` seumur hidup — tanpa periode, `alert_pct`, cutoff 402; `usageHistory.cost` masih telemetri |
| F-3 | Plans (template reusable) | **SELESAI** (PR #354) | resolver tunggal `internal/policy/policy.go`; repo `internal/db/plans.go`; tabel `plans` + `apiKeys.planId` di `internal/db/schema.go`; API `internal/handlers/dashboard/plans.go`; UI `web/src/components/plans/PlansView.svelte` |
| F-4 | Guardrails | **SELESAI (MVP)** | `internal/guardrails/detectors.go:31,62` (PII + injeksi), `middleware.go`, `resolver.go`, `outbound.go`; tanpa toxicity/topics/bias |
| F-5 | Credential vault AES-GCM | **SELESAI** | `internal/vault/vault.go`; kolom tersegel `schema.go:272-277` |
| F-6 | Argon2id key hashing | **SELESAI** | `internal/keikey/apikey.go:1-45`; jalur auth `internal/middleware/auth.go:102-107`; kolom `schema.go:287-289` |
| F-7 | Model access per-key (allowlist + wildcard) | **SELESAI** | repo `internal/db/modelaccess.go:24`; gate `internal/handlers/chat/access.go:134,159`; API `dashboard/apikeys_models.go`; route `router.go:245-246`; UI `web/src/components/keys/ApiKeyPolicyModal.svelte:333` |
| F-8 | Portal penggunaan publik | BELUM | — |
| F-9 | Response cache | **SELESAI** (melampaui KeiRouter) | `internal/semanticcache/cache.go:17-51` (LRU + TTL 24 jam + ambang kemiripan 0,95); wiring `handlers/chat/chat.go:111`, `forward.go:423` |
| F-10 | Prometheus `/metrics` | **SELESAI** | `internal/observ/metrics.go:1-45`; route `internal/handlers/router.go:248` |
| F-11 | Multi-tenant | TUNDA | — |
| F-12 | System tray | SKIP | — |
| F-13 | Terse / token saver | **SELESAI** | `internal/tokensaver/` |
| F-14 | Metadata resale (expiry/usedCount/metadata) | **SELESAI** | `schema.go:291-294`; `dashboard/apikeys_policy.go:47-60` |
| F-15 | Branding white-label | BELUM | — |

**Kesimpulan:** dari Tier A, **F-3 (Plans) sudah selesai** (PR #354) dan **F-2 (Budget) masih parsial** — itu PR berikutnya. F-7 sudah tuntas end-to-end, jadi ia tidak lagi jadi PR tersendiri: allowlist-nya kini diwarisi lewat plan (union).

### B. Rencana eksekusi — scope terpilih: Plans + Budget + Model-access

F-7 sudah tuntas end-to-end (lihat §A), jadi scope tersisa = **2 PR berurutan** ke `main` (AGENTS.md §7.7), branch dari `origin/main`. Detail desain per fitur tetap ada di §2 (F-3, F-2) — bagian ini menegaskan urutan, cakupan, dan definisi selesai. Bagian §3 (Rekomendasi Roadmap lama) **ditimpa** oleh bagian ini untuk scope yang dipilih.

#### B1. PR-1 — F-3 Plans (payung policy) — **SELESAI (PR #354)**

1. **Skema.** Tabel `plans` di `keiRouterTables` (`internal/db/schema.go`): `id TEXT PRIMARY KEY`, `tenant_id TEXT NOT NULL DEFAULT 'default'`, `name`, `description`, `limit_micros INTEGER NOT NULL DEFAULT 0`, `limit_tokens INTEGER NOT NULL DEFAULT 0`, `rpm_limit`, `tpm_limit`, `concurrency_limit INTEGER NOT NULL DEFAULT 0`, `period TEXT NOT NULL DEFAULT 'monthly'` (enum `daily|weekly|monthly|total`), `alert_pct INTEGER NOT NULL DEFAULT 80`, `hard_cutoff INTEGER NOT NULL DEFAULT 1`, `allowed_models TEXT DEFAULT ''` (dipisah koma), `created_at`, `updated_at` (RFC3339 UTC, mengikuti `0015_plans.sql`). `goOnlyColumns` += `{"apiKeys","planId","TEXT DEFAULT ''"}`. Seed: satu plan `default` dengan literal RFC3339 (bukan `datetime('now')`, agar portabel), disisipkan di `EnsureKeiRouterTables` sehingga setiap jalur bootstrap ikut memilikinya. **Divergensi dari draft awal:** `UPDATE apiKeys SET planId='default' WHERE planId=''` sengaja **tidak** dijalankan — plan `default` semuanya nol jadi perilaku key yang ada tidak berubah, sementara backfill akan menambah satu bacaan plan per request untuk setiap key. Key baru cukup dipetakan lewat `PUT /api/keys/{id}` dengan `planId`.
2. **Repo.** `internal/db/plans.go` — `Create/Get/List/Update/Delete/CountKeys`, mengikuti pola `internal/db/modelaccess.go` (termasuk pembacaan massal satu query `GetPlansForKeys` untuk daftar key).
3. **Resolver.** `internal/policy/policy.go` — `Resolve(key *models.APIKey) EffectivePolicy`. Algoritma: mulai dari nol → timpa dari plan (bila `planId` ada dan plan ditemukan) → timpa dengan kolom key bila nilainya > 0. `EffectivePolicy` = `{PlanID, RPM, TPM, Concurrency, QueueTimeoutMs, TokenQuota, LimitMicros, LimitTokens, BudgetPeriod, AlertPct, HardCutoff, AllowedModels}`. Satu-satunya sumber kebenaran; dipanggil ratelimit (`internal/middleware/ratelimit.go:888-894`), budget (B2), dan model-gate (`internal/handlers/chat/access.go:52`) — **tidak boleh** ada logika limit yang tersebar.
4. **Middleware.** Rate limit membaca `policy.RPM/TPM/Concurrency`; kolom key lama tetap berlaku hanya sebagai override eksplisit (nilai > 0).
5. **API dashboard.** `internal/handlers/dashboard/plans.go`: `GET /api/plans`, `POST /api/plans`, `PUT /api/plans/{id}`, `DELETE /api/plans/{id}`, `GET /api/plans/{id}/keys` (memakai `CountKeys`); rute di `internal/handlers/router.go` dekat `:245`. Validasi: `period` ∈ enum, `alertPct` 0–100, semua limit ≥ 0, `allowedModels` pola dengan `*` akhir. `DELETE` → **409** bila masih dipakai key.
6. **UI.** `web/src/components/plans/PlansView.svelte` (daftar + editor, Svelte 5 runes) dan klien di `web/src/api/client.ts` (pola `:1123`); `ApiKeyPolicyModal.svelte:132` menambah pemilih plan sehingga `planId` ikut terkirim di `APIKeyPolicy` (tipe `web/src/api/client.ts:102`). Allowlist per key tetap ada sebagai override.
7. **Uji.** `internal/policy/policy_test.go` tabel kasus (tanpa plan → nol; plan only; override key; warisan `allowedModels`; `period` tidak valid ditolak). `internal/db/plans_test.go` CRUD + `CountKeys` pada SQLite in-memory. **Wajib** menambah `planId` ke fixture `apiKeys` yang ditulis tangan — `internal/handlers/dashboard/dashboard_test.go`, `internal/db/dashboard_test.go`, `internal/db/repos_test.go`, `internal/middleware/auth_test.go` — kalau tidak: `no such column: planId`.

#### B2. PR-2 — F-2 Budget (enforcement) — estimasi 1–1,5 hari

1. **Skema.** `goOnlyColumns` += `limitMicros INTEGER DEFAULT 0`, `budgetPeriod TEXT DEFAULT 'monthly'`, `alertPct INTEGER DEFAULT 80`, `hardCutoff INTEGER DEFAULT 1`. Periode memakai enum yang sama dengan plan.
2. **Belanja.** Dihitung dari `usageHistory` dalam jendela periode: `SUM(cost)` × 1e6 → micros, `SUM(promptTokens+completionTokens)` → token. Tanpa counter Redis dan tanpa job reset (KeiRouter memakai Redis + `reservations`; pendekatan SQL lebih sederhana — trade-off: tidak ada proteksi overshoot sub-detik, ditutup langkah 4). Konversi float→micros di **satu** helper, tidak dicampur float di penjagaan.
3. **Middleware.** `internal/middleware/budget.go`, dipasang setelah `RequireRateLimit` di rantai middleware: sebelum dispatch, 402 dengan payload `{"error":"budget_exhausted","period":...,"limit_micros":...}` saat `hardCutoff=1` dan belanja ≥ limit; header `X-Budget-Alert: <n>%` saat lewat `alertPct`. `GET /api/keys/{id}/budget` mengembalikan belanja + sisa.
4. **Reservasi in-flight.** `map[apiKeyID]reservation` + `sync.RWMutex`, mengikuti `budget/budget.go` KeiRouter: catat estimasi biaya saat mulai, lepaskan saat selesai, tolak saat estimasi membuat total melampaui limit. Mencegah N permintaan paralel menembus batas.
5. **UI.** Indikator di `ApiKeyPolicyModal` (limit, periode, alert %, cutoff) + ringkasan di halaman Usage.
6. **Uji.** `internal/middleware/budget_test.go`: tepat di batas (diizinkan/ditolak), pergantian periode, `hardCutoff=0` (hanya peringatan), reservasi paralel, konversi tanpa pembulatan salah.

#### B3. Urutan dan alasan

Plan adalah payung yang memuat limit RPM/TPM/konkurensi, kuota token, dan `allowedModels`; budget membaca policy hasil resolver yang sama, jadi mengerjakannya lebih dulu berarti menulis enforcement dua kali.

### C. Backlog baru dari delta v0.2.0 (belum ada di dokumen ini)

| Fitur upstream | Commit / migrasi | Nilai | Verdict |
|----------------|------------------|-------|---------|
| Audit trail tamper-evident + ekspor OTLP (OpenTelemetry Protocol) | `6cae829`, `0032_audit_log_otel.sql` | Tinggi (kepatuhan) | Ambil |
| Paket `skills` operator (blok instruksi per scope key/plan/model) | `backend/internal/skills/skills.go` | Sedang | Ambil (kecil) |
| `errclass` klasifikasi error agnostik-provider | `backend/internal/errclass/` | Sedang | Ambil |
| Meter: cache-write 1 jam + tagihan web search | `c6885de`, `0031` | Sedang | Ambil |
| Usage: `chain_id`/`fallback_count` + hasil guardrail per request | `0030`, `0033` | Sedang | Ambil |
| Leksikon toksisitas Indonesia + corpus test | `024bd84` | Sedang | Ambil (murah) |
| Topik berbasis LLM (Large Language Model) | `fae103d` | Sedang | Nanti |
| ML (Machine Learning) classifier injeksi + LLM judge + sidecar PromptGuard | `11bf55b` | Tinggi | Tunda (butuh deploy Python) |

### D. Jebakan yang sudah diketahui

- Fixture `apiKeys` yang ditulis tangan tidak mengikuti `goOnlyColumns`; setiap kolom baru harus ditambahkan di keempat berkas uji pada PR-1 langkah 7.
- Uang disimpan sebagai **micros integer**; konversi dari `usageHistory.cost REAL` dilakukan di satu helper, jangan dicampur float.
- Semua skema baru lewat `goOnlyColumns`/`keiRouterTables` (`internal/db/schema.go:268,299`) — tabel milik upstream tidak boleh diubah.
- Setiap PR menulis satu fragmen `.changes/<nomor-pr>-<slug>.md`; `CHANGELOG.md` tidak disentuh (AGENTS.md §7.6).

### E. Definisi selesai dan gate

**Gate global (setiap PR):** `go test ./...` hijau; `make vet-lua` **tidak** diperlukan; `make vet-svelte` untuk perubahan UI; `cd web && bun run build` bersih; fragmen `.changes/<nomor-pr>-<slug>.md` ditulis, `CHANGELOG.md` **tidak** disentuh; branch dari `origin/main`, PR ke `main` (AGENTS.md §7.6–§7.7).

| PR | Definisi selesai |
|----|------------------|
| PR-1 | `policy.Resolve` menghasilkan identik untuk key tanpa plan; ratelimit menolak/izinkan sama seperti sebelum untuk key `planId` kosong; `apiKeys.planId` tersimpan dan dapat diubah via `PUT /api/keys/{id}`; tabel `plans` CRUD penuh; fixture `apiKeys` di 4 berkas uji memuat `planId`. |
| PR-2 | 402 benar saat `hardCutoff` terlampaui; peringatan muncul saat `alertPct`; reservasi paralel tidak menembus batas; `GET /api/keys/{id}/budget` konsisten dengan `usageHistory`; konversi float→micros tidak ada pembulatan salah. |

### F. Estimasi total

| Pekerjaan | Unit konkret |
|-----------|---------------|
| PR-1 (F-3 Plans) | 1–1,5 hari kerja: skema + repo ± 3 jam; resolver + middleware ± 3 jam; API + UI ± 4 jam; uji + gate ± 2 jam |
| PR-2 (F-2 Budget) | 1–1,5 hari kerja: skema + middleware ± 4 jam; kueri belanja + reservasi ± 4 jam; uji + gate ± 2 jam |
| Total scope terpilih | **2–3 hari kerja** |

### G. Keputusan yang masih terbuka

1. **Gabungan allowlist** — plan `allowedModels` dan allowlist per key: digabung (union) atau key menimpa? Rekomendasi: **union** (key boleh menambah, bukan mempersempit).
2. **Agregasi budget** — `SUM` langsung atas `usageHistory` vs membaca `UsageDaily` (tabel agregasi harian sudah ada di `internal/models/types.go`) sebagai cache. Rekomendasi: mulai `SUM`, pindah ke `UsageDaily` bila tabel besar.
3. **Seed plan `default`** — di migrasi (sekali, portabel) vs di kode startup (mudah diubah). Rekomendasi: **migrasi**, mengikuti `0015_plans.sql`.
4. **UI plan** — halaman penuh `PlansView` vs modal. Rekomendasi: halaman penuh (sudah ada pola `SettingsView`).
5. **PR-2 `hardCutoff` default** — 1 (mati) atau 0 (peringatan saja) untuk plan `default`. Rekomendasi: **0** agar perilaku lama tidak berubah untuk key yang sudah ada.

---


## 0. Ruang Lingkup

Dokumen ini **hanya** mencakup fitur KeiRouter yang **BELUM ADA** di 9router-go dan **perlu di-port**.

**Dikecualikan** (sudah ada di 9router-go, jangan port):
- RTK / kompresi tool output (`internal/tokensaver/compress.go`)
- Caveman / Ponytail / ADHD injection (`internal/tokensaver/injection.go`)
- Headroom proxy (`internal/headroom/`)
- Combos / fallback chain (`internal/handlers/chat/combo.go`)
- Proxy pools (`internal/handlers/dashboard/proxypools.go`)
- OAuth refresh + 401 retry (`internal/proxy/oauth/`)
- Custom provider nodes (setara `providerNodes` table)
- Pricing model (`internal/pricing/`)
- Usage tracker / SSE (`internal/usagetracker/`)
- Self-update / MITM / tracing (`internal/updater`, `mitm`, `tracing`)

---


---

## 1. Ringkasan Kandidat Port

Skor = **value untuk use-case 9router-go** (developer proxy / resale gateway) ÷ **effort port**.

### Tier A — Governance inti, value tinggi

#### 1.1 Per-key Rate Limiting (RPM / TPM / Concurrency)
- **Asli**: `internal/limits/limits.go` (+ `repo_plans.go` untuk plan override).
- **Kenapa penting**: 9router-go adalah gateway multi-akun publik. Tanpa limit, satu klien boros bisa habiskan kuota upstream untuk semua. Ini pelengkap alami `RequireApiKey` yang sudah ada.
- **Fit arsitektur**: middleware baru `middleware/ratelimit.go`, dipasang setelah `RequireApiKey` di `router.go`. Backend in-memory dulu (sama seperti KeiRouter `backend: memory`).
- **Skema**: kolom `apiKeys.rateLimitRPM/TPM/concurrency` (additive, sudah ada di [§F-14](#f-14-api-key-time-limit--resale-metadata)).
- **Effort**: Rendah–Sedang. **Prioritas: #1.**

#### 1.2 Budget Engine (USD + Token Hard-Cutoff)
- **Asli**: `internal/budget/budget.go` + `repo_budgets.go`, migration `0008_token_budgets_and_model_access.sql`.
- **Model**: `limit_micros` (USD micro-dollar, hindari float), `limit_tokens`, `period` (daily/weekly/monthly/total), `alert_pct`, `hard_cutoff`.
- **Kenapa penting**: 9router-go sudah catat usage per request (`usagetracker`, `usageHistory`). Tinggal tambahkan **lapisan enforcement**: sebelum dispatch, cek spending key vs budget → 402/blokir. Tanpa ini, `cost REAL` di `usageHistory` cuma telemetry, bukan kontrol.
- **Fit**: pre-dispatch check di `chat.HandleChatCompletions`; increment di post-response meter (sudah ada `usage.go`).
- **Effort**: Sedang. **Prioritas: #2.**

#### 1.3 Plans (Template Reusable)
- **Asli**: `0015_plans.sql` + `0019_plan_rate_limits.sql` (`rpm_limit/tpm_limit/concurrency_limit`).
- **Model**: `plans(id, tenant_id, name, limit_micros, limit_tokens, period, alert_pct, hard_cutoff, allowed_models, rpm/tpm/concurrency)`; `api_keys.plan_id` FK.
- **Kenapa penting**: satu preset → tempel ke banyak key. Ini **gabungkan** §1.1 + §1.2 + model-access (§1.7) dalam satu objek. Sangat pas untuk skenario jual-key 9router-go.
- **Catatan**: KeiRouter punya `tenant_id`; 9router-go single-tenant → pakai `'default'` konstan dulu, kolom tetap ada biar multi-tenant gampang nanti.
- **Effort**: Sedang. **Prioritas: #3.**

### Tier B — Keamanan & efisiensi

#### 1.4 Guardrails (PII / Injection / Toxicity / Topics / Bias)
- **Asli**: `internal/guardrails/*` (20 file: `pii.go`, `injection.go`, `toxicity.go`, `topics.go`, `bias.go`, `engine.go`, `hub.go`, `resolver.go`, `policy.go`, `audit.go`, `presidio.go`, `embedding.go`…), migration `0017_guardrails.sql`.
- **Kekuatan desain**: layering `global → provider → model → chain → apikey`, merge saat request (most-specific menang). Aksi `log_only/warn/mask/block`. Streaming-safe (buffer 256 char scan output). PII lokal (NIK/NPWP/passport ID). Engine opsional: Presidio sidecar / OpenAI Moderation / embedding.
- **Kenapa penting**: 9router-go jadi titik transit **semua** prompt/response. Untuk resale/enterprise, filter PII sebelum keluar ke upstream = selling point compliance besar.
- **Fit**: middleware/interceptor sebelum `chat` dispatch + tap di stream translator.
- **Trade-off**: paket guardrails KeiRouter **besar** (lexicon bilingual, presidio, retention). Port penuh = beban. **Rekomendasi MVP**: `injection` (regex) + `pii` (regex native) + action `log_only|mask|block`, skip Presidio/embedding/bias dulu.
- **Effort**: Tinggi (full) / Sedang (MVP). **Prioritas: #4 (MVP), #8 (full).**

#### 1.5 Credential Vault (AES-256-GCM Envelope)
- **Asli**: `internal/vault/vault.go` + `internal/crypto/envelope.go`.
- **Masalah 9router-go SEKARANG**: kredensial provider disimpan **plaintext JSON** di `providerConnections.data` (DATABASE.md: *"provider credentials … protect the file and volume as secrets"*). Untuk resale/multi-user, ini risiko nyata.
- **Desain KeiRouter**: envelope encryption, `master_key` sebagai root of trust. Credential di-encrypt at-rest, di-decrypt in-memory saat dispatch.
- **Fit**: bungkus `connection.Data` dengan vault sebelum write, buka sebelum `proxy/executor`. Perlu migrasi satu-arah (plaintext → encrypted) + fallback read plaintext untuk kompat.
- **Effort**: Sedang (encrypt) + Tinggi (migrasi aman tanpa corrupt data existing). **Prioritas: #5.**
- **Risiko**: kehilangan `master_key` = kehilangan semua kredensial. Wajib dokumentasi recovery.

#### 1.6 API Key Argon2id Hashing
- **Asli**: `internal/identity/identity.go` — key tak pernah plaintext; simpan argon2id verifier + SHA-256 lookup index; auth cache 5 dtk untuk hemat argon2 (64 MiB/verifikasi).
- **Masalah 9router-go**: `apiKeys.key` plaintext, lookup `WHERE key = ?`. Bocor DB = bocor semua key.
- **Fit**: ganti `GetApiKeyByKey` → hash lookup + constant-time argon2. Plaintext ditampilkan sekali saat create (sudah begitu).
- **Trade-off**: lookup hash SHA-256 dulu (index) baru argon2 confirm → tetap cepat. Auth cache wajib, kalau tidak argon2 per-request = CPU jebol (Claude Code 10+ req/s, disebut di kode KeiRouter).
- **Catatan**: `API-KEY-TIMELIMIT...` spec pakai model plaintext. Kalau vault/argon diambil, spec itu harus di-revisi (lookup by hash index).
- **Effort**: Sedang. **Prioritas: #6.**

#### 1.7 Per-key Model Access (Allowlist)
- **Asli**: `0008...api_key_model_access` (tabel) + `allowed_models` wildcard di plans.
- **Model**: baris `(api_key_id, model)`; kosong = semua boleh (backward-compat). Wildcard `claude-*`, `gpt-4*` via plans.
- **Kenapa penting**: resale gateway → jual paket "hanya model X". Ini **satu tema** dengan spec `?combo=` scope yang kamu minta sebelumnya.
- **Fit**: cek di `resolveModel` sebelum dispatch.
- **Effort**: Rendah. **Prioritas: #7** (sebenarnya seirama spec combo-scope).

### Tier C — UX / Ops

#### 1.8 Public Usage Portal
- **Asli**: `internal/usagehub/hub.go` + frontend `KeyPortal.tsx`; endpoint publik `/v1/portal/branding`, `/v1/portal/usage` (auth = API key itu sendiri, tanpa sesi admin).
- **Kenapa penting**: pembeli key lihat pemakaian sendiri tanpa akses admin. Lengkapi §1.1–1.3 untuk skenario jual-key.
- **Fit**: route publik baru yang validasi API key via query/header (bukan sesi). 9router-go sudah punya `RequireApiKey` untuk domain mesin → reuse.
- **Effort**: Sedang (backend kecil, UI Svelte portal perlu baru). **Prioritas: #9.**

#### 1.9 Semantic Response Cache
- **Asli**: `internal/cache/{cache.go,embedder.go,embedder_api.go,redis.go}` — cosine similarity, threshold (default 0.95), TTL, eviction; backend in-memory (brute-force) / Redis.
- **Kenapa penting**: hemat biaya untuk prompt berulang. **Tapi**: untuk *coding* gateway, request jarang identik → hit-rate rendah. Cache persis (hash exact-match) jauh lebih murah dan tetap berguna untuk health-check/keepalive (9router-go sudah bypass `warmup/keepalive` di `chat.go`).
- **Trade-off**: semantic cache butuh **embedding model** (biaya + latensi extra). ROI meragukan untuk use-case utama 9router-go.
- **Rekomendasi**: **skip semantic**; kalau perlu cache, mulai dari **exact-match cache** (hash) untuk response deterministik. Prioritas rendah.
- **Effort**: Tinggi (semantic) / Rendah (exact). **Prioritas: #12.**

#### 1.10 Prometheus /metrics
- **Asli**: `internal/observ/metrics.go` — `keirouter_guardrail_decisions_total`, `keirouter_guardrail_eval_seconds` di `/metrics`.
- **Kenapa penting**: ops-ready. 9router-go cuma `tracing` (per-request). Metrics endpoint standar = gampang pantau di VPS/Coolify.
- **Fit**: handler publik/ter-guard `/metrics`.
- **Effort**: Rendah. **Prioritas: #10.**

#### 1.11 System Tray (tray)
- **Asli**: `internal/tray/` (desktop tray icon), `keirouter tray`.
- **Trade-off**: 9router-go fokus single-binary + daemon (`start/stop/status/logs`) headless. Tray = dep GUI (fenster/energye systray), bentrok dengan ethos CGO-free/lightweight. **Rekomendasi skip** kecuali target desktop-user.
- **Prioritas: #13.**

#### 1.12 Multi-tenant
- **Asli**: tabel `tenants`, semua tabel lain punya `tenant_id`.
- **Trade-off**: 9router-go dirancang single-user self-host (satu DB, satu password). Multi-tenant = refactor auth + isolasi query + resolusi tenant (mirum `X-Tenant` / subdomain). Besar dan mengubah DNA produk.
- **Rekomendasi**: **tunda**. Untuk resale cukup **plans + key-scoping** single-tenant (§1.3, §1.1, §1.7). Kolom `tenant_id` opsional dibiarkan konstan `'default'` supaya upgrade path terbuka tanpa migrasi ulang nanti.
- **Prioritas: #11.**

#### 1.13 Terse Output Saver (REQUEST-SIDE SERIALIZATION)
- **Asli**: `internal/terse/terse.go` — Token-Efficient Serialization (bukan cuma directive output seperti Caveman).
- **Kenapa penting**: Terse mengompaksi bentuk pesan/tools ke format ringkas + tambah directive → hemat token input. Berbeda dari Caveman (directive output saja).
- **Gap di 9router-go**: `injection.go`/`prompts.go` punya Caveman (directive output), tapi belum ada request-side message/tool serialization.
- **Fit**: `internal/tokensaver/terse.go` baru. Level `light` (directive saja) / `medium` (directive + serialisasi messages+tools). Eksklusif vs Caveman, stackable vs Ponytail.
- **Effort**: Rendah. **Prioritas: #14** (verifikasi coverage dulu).

---


> Detail penuh tiap fitur (masalah, desain, skema, API, testing, risiko): lihat [§2](#2-detail-per-fitur).

---

## 2. Detail Per Fitur

Setiap sub-bagian: masalah + bukti terverifikasi → desain KeiRouter → desain 9router-go → API/frontend → testing → rollout → risiko.

### F-1. Rate Limiting per-Key (RPM / TPM / Concurrency)

**Tier A · Ambil · Effort: Rendah–Sedang · Depends-on: — · Depended-by: 3 Plans**

#### 1.1 Masalah

9router-go punya `apiKeys` dengan `isActive` saja. Tidak ada pembatasan laju per key.

**Bukti terverifikasi:**
- `internal/middleware/` = `auth.go`, `dashboard_auth.go`, `logging.go`, `max_body.go`, `request_id.go`. **Tidak ada** `ratelimit.go`.
- `RequireApiKey` (`auth.go`) hanya: extract → `GetApiKeyByKey` → `IsActive == 1` → inject context.
- `apiKeys` (`schema.go:89`) = `id, key, name, machineId, isActive, createdAt` + index `idx_ak_key`.

**Dampak:** satu klien boros menghabiskan kuota upstream untuk semua akun. Ini justru alasan utama proyek gateway ini ada.

#### 1.2 Desain KeiRouter (sumber)

- `internal/limits/limits.go` — limiter in-memory, backend `memory`.
- `0019_plan_rate_limits.sql` — `rpm_limit`, `tpm_limit`, `concurrency_limit` per plan (`0` = unlimited).
- Konfigurasi global hanya berlaku untuk key **tanpa** plan.

```yaml
limits:
  enabled: true
  backend: memory
  default_rpm: 600
  default_tpm: 200000
  default_concurrency: 50
  window: 1m
```

#### 1.3 Desain untuk 9router-go

**Kolom additive** (`internal/db/schema.go` → `goOnlyColumns`):

```go
{"apiKeys", "rateLimitRPM",         "INTEGER DEFAULT 0"},
{"apiKeys", "rateLimitTPM",         "INTEGER DEFAULT 0"},
{"apiKeys", "rateLimitConcurrency", "INTEGER DEFAULT 0"},
```

`0` = unlimited. **Catatan:** setelah fitur 3 (Plans) masuk, kolom ini menjadi *override eksplisit*; sumber utama limit tetap `plans`.

**Global default** di `settings` JSON (single-row, pola `HandleGetSettings` yang sudah ada):

```jsonc
{ "rateLimitEnabled": true,
  "defaultRpm": 600,
  "defaultTpm": 200000,
  "defaultConcurrency": 50,
  "rateWindowSeconds": 60 }
```

**Limiter** (`internal/middleware/ratelimit.go`):

```go
type Limiter struct {
    mu      sync.Mutex
    windows map[string]*window  // keyID -> rolling counters
    active  map[string]int32    // keyID -> in-flight
}

type window struct {
    stamps      []time.Time
    tokens      int64
    windowStart time.Time
}

func NewLimiter(repo *db.Repo) *Limiter
func (l *Limiter) Check(keyID string, rpm, tpm, conc int) (ok bool, retryAfterSec int)
```

Algoritma: **sliding window** atas slice timestamp. MVP slice + prune cukup (entri dibatasi jumlah key). Bucket-array hanya bila profil menunjukkan lock contention.

**Wiring** (`internal/handlers/router.go`), dipasang **setelah** `RequireApiKey`:

```go
r.Group(func(r chi.Router) {
    r.Use(middleware.RequireApiKey(repo))
    r.Use(middleware.RequireRateLimit(limiter, repo))  // NEW
    SetupRoutes(r, repo, ts)
})
```

Resolusi limit: **kolom key → plan → global default** (berhenti di nilai pertama yang bukan 0).

**TPM dua fase** (token belum diketahui sebelum dispatch):
1. **Reservasi** rough-estimate (hitung prompt tokens lokal) saat masuk.
2. **Kompensasi** exact (usage dari response) saat selesai — di jalur meter yang sudah ada (`usage.go`).

**Concurrency:** `atomic.AddInt32` saat masuk, `defer` kurangi. SSE menahan slot sampai stream selesai (disengaja, sama seperti KeiRouter).

#### 1.4 Response & Error

```
HTTP/1.1 429 Too Many Requests
Retry-After: 12
X-RateLimit-Limit: 600
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1759795200
{"error":{"message":"Rate limit exceeded for API key. Retry after 12s.","type":"rate_limit_error"}}
```

Format mengikuti envelope OpenAI-style `handlerutil.WriteJSONError`.

#### 1.5 API & Frontend

| Method | Path | Auth | Fungsi |
|--------|------|------|--------|
| `GET/PUT` | `/api/settings` | dashboard | global default (reuse `HandleUpdateSettings`) |
| `PUT` | `/api/keys/{id}` | dashboard | **handler baru** — partial update kolom per-key |

`ApiKeysView.svelte`: 3 input numeric + badge "uses plan / uses default".

#### 1.6 Testing

| Test | Target |
|------|--------|
| `TestRateLimit_RPM` | 601 request dalam 60s → ke-601 `429` |
| `TestRateLimit_PlanOverride` | key dengan plan pakai limit plan, bukan default |
| `TestRateLimit_UnlimitedZero` | `rpm=0` tak pernah blokir |
| `TestRateLimit_Concurrency` | N+1 stream paralel → satu ditolak |
| `TestRateLimit_TPMReserve` | reservasi rough lalu kompensasi exact; tak negatif |
| `TestRetryAfter` | header = detik ke slide window berikutnya |

#### 1.7 Rollout

1. Kolom additive + `PUT /api/keys/{id}` (no behavior change).
2. Limiter disabled-by-default (`rateLimitEnabled=false`); aktifkan bertahap.
3. `Retry-After` + docs.

#### 1.8 Risiko

- **SSE menahan slot concurrency** → `defaultConcurrency` salah bisa menjerat traffic stream. Mitigasi: concurrency opsional (0 = tak cek), dokumentasikan.
- **Lock contention** di bawah 32K RPS → sharding per keyID hash bila perlu.
- **Restart flush counter** → burst gratis pasca-restart. Diterima untuk single-instance.

---

### F-2. Budget Engine (USD Micros + Token Cutoff)

**Tier A · Ambil · Effort: Sedang · Depends-on: — · Depended-by: 3 Plans, 8 Portal**

#### 2.1 Masalah

9router-go **mencatat** biaya tapi **tidak menegakkan** batas.

**Bukti terverifikasi:**
- `usageHistory` punya `cost REAL`; `internal/pricing/` menghitung harga model.
- Tidak ada konsep *budget*, *cutoff*, *limit*. Search `budget|cutoff` = 0 hasil.
- Yang ada cuma kuota upstream pihak ketiga (`codexquota`, `usage_providers.go`).

**Dampak:** `cost` hanya telemetry. Tidak ada "berhenti saat $X keburu".

#### 2.2 Model KeiRouter

```
limit_micros  INTEGER  -- USD micro-dollar, presisi penuh (hindari float)
limit_tokens  INTEGER
period        TEXT     -- daily | weekly | monthly | total
alert_pct     INTEGER  -- 1..100 ambang peringatan
hard_cutoff   INTEGER  -- 1 blokir saat habis, 0 catat saja
```

#### 2.3 Skema (Go-only, additive)

```sql
CREATE TABLE IF NOT EXISTS budgets (
  id            TEXT PRIMARY KEY,
  tenant_id     TEXT NOT NULL DEFAULT 'default',
  subject_type  TEXT NOT NULL,       -- 'apikey' | 'org'
  subject_id    TEXT NOT NULL,       -- apiKeys.id
  limit_micros  INTEGER NOT NULL DEFAULT 0,
  limit_tokens  INTEGER NOT NULL DEFAULT 0,
  spent_micros  INTEGER NOT NULL DEFAULT 0,
  spent_tokens  INTEGER NOT NULL DEFAULT 0,
  period        TEXT NOT NULL DEFAULT 'monthly',
  period_start  TEXT NOT NULL,
  alert_pct     INTEGER NOT NULL DEFAULT 80,
  hard_cutoff   INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_budgets_subject
  ON budgets(subject_type, subject_id);
```

**Kenapa kolom `spent_*` terpisah, bukan join `usageHistory`:** agregasi per-key dari `usageHistory` (baris sebanyak request) mahal tiap request. Counter ter-normalisasi + reset saat rollover = **O(1) pre-dispatch**. Konsistensi dijaga rekonsiliasi periodik (backstop).

#### 2.4 Micros, bukan float

`usageHistory.cost REAL` → `micros = round(cost * 1_000_000)` saat meter. Akumulasi **integer** (aman dari drift). `internal/pricing/` tetap float internal, tapi simpan/limit = micros.

#### 2.5 Titik Enforcement

```
RequireApiKey → RequireRateLimit(1) → [BUDGET PRE-CHECK] → chat.HandleChatCompletions
   spend>limit && hard_cutoff=1  →  402 Payment Required
   spend>limit && hard_cutoff=0  →  lanjut + alert sekali
   spend>alert_pct               →  header X-Budget-Status: approaching
[meter/usage.go POST-response] → budget.AddUsage(subject, costMicros, tokens)
   crossing threshold → tulis audit/event + (opsional) SSE ke dashboard
```

Pre-check **read-only murah** (SELECT counter). Write kompensasi terjadi di jalur usage yang sama tempat `usageHistory` di-insert → **satu transaksi** dengan insert usage (atomic).

#### 2.6 Rollover Period

`daily/weekly/monthly/total`. Saat pre-check: bila `now` melewati batas `period_start`, reset `spent_* = 0` dan set `period_start` baru (**lazy rollover** dalam transaksi yang sama). `total` tidak pernah reset.

#### 2.7 API & Frontend

| Method | Path | Auth |
|--------|------|------|
| `POST` | `/api/budgets` | dashboard |
| `GET` | `/api/budgets/{subjectType}/{subjectId}` | dashboard |
| `DELETE` | `/api/budgets/{id}` | dashboard |
| `GET` | `/api/budgets?subjectType=apikey` | dashboard |

Svelte: kartu **Budgets** di `SettingsView.svelte` atau view sendiri — progress bar `spent/limit`, toggle `hard_cutoff`, pilih `period`, slider `alert_pct`.

#### 2.8 Testing

| Test | Target |
|------|--------|
| `TestBudget_HardCutoff` | spend≥limit & cutoff=1 → 402; request **tidak** sampai provider |
| `TestBudget_SoftTrack` | cutoff=0 → lanjut, tak pernah blokir |
| `TestBudget_Rollover` | daily reset; `total` tak reset |
| `TestBudget_MicrosPrecision` | 3×$0.000001 = 3 micros tepat (no drift) |
| `TestBudget_Alert` | lewati alert_pct → 1 event, tak spam tiap request |
| `TestBudget_AtomicMeter` | insert usage + update budget satu transaksi (rollback uji) |
| `TestBudget_Reconcile` | rekonsiliasi dari usageHistory menutupi counter tertinggal |

#### 2.9 Risiko

- **Race meter** → `UPDATE ... SET spent=spent+?` atomic di SQL; hindari read-modify-write di Go.
- **Model tanpa harga** → cost 0 → budget tak berarti. Mitigasi: izinkan `limit_tokens` murni (tanpa USD).
- **402 bukan status lazim** di klien coding — sertakan `error.type` jelas + dokumentasi.

---

### F-3. Plans (Template Policy Reusable)

**Tier A · Ambil · Effort: Sedang · Depends-on: 1, 2, 7 · Depended-by: 8**

#### 3.1 Ini Payungnya

Plans = **satu objek** yang menggabungkan rate limit + budget + model access. Di KeiRouter ketiganya sudah satu objek sejak awal.

> **Aturan emas:** jangan deploy 1/2/7 terpisah tanpa 3, atau `apiKeys` akan punya kolom limit yang duplikat dengan `plans`. Ambil 3 sebagai **owner kolom**.

#### 3.2 Masalah

9router-go: key datar (`internal/models/types.go` — `id, key, name, machineId, isActive, createdAt`). Tidak ada template, tidak ada pengelompokan kebijakan, tidak ada model-access list. Semua key identik secara kebijakan.

#### 3.3 Skema (Go-only)

```sql
CREATE TABLE IF NOT EXISTS plans (
  id                TEXT PRIMARY KEY,
  tenant_id         TEXT NOT NULL DEFAULT 'default',
  name              TEXT NOT NULL,
  description       TEXT NOT NULL DEFAULT '',
  -- budget (2)
  limit_micros      INTEGER NOT NULL DEFAULT 0,
  limit_tokens      INTEGER NOT NULL DEFAULT 0,
  period            TEXT NOT NULL DEFAULT 'monthly',
  alert_pct         INTEGER NOT NULL DEFAULT 80,
  hard_cutoff       INTEGER NOT NULL DEFAULT 1,
  -- rate limit (1)
  rpm_limit         INTEGER NOT NULL DEFAULT 0,
  tpm_limit         INTEGER NOT NULL DEFAULT 0,
  concurrency_limit INTEGER NOT NULL DEFAULT 0,
  -- model access (7)
  allowed_models    TEXT NOT NULL DEFAULT '',   -- comma/newline wildcard; ''=all
  allowed_combos    TEXT NOT NULL DEFAULT '',   -- JSON array
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plans_name ON plans(tenant_id, name);
```

Kolom `apiKeys` (additive):

```go
{"apiKeys", "planId", "TEXT DEFAULT ''"},
```

**Keputusan penting:** kolom limit/budget/model **hanya** di `plans`, **bukan** di `apiKeys`. Key mewarisi lewat `planId`. Ini menghindari dua sumber kebenaran. Override per-key = fitur lanjutan (YAGNI sekarang).

#### 3.4 Seed & Migration

Ikuti pola `EnsureCoreSchema` (`INSERT OR IGNORE`):

```go
`INSERT OR IGNORE INTO plans(id,tenant_id,name,...,created_at,updated_at)
 VALUES('default','default','Default', 0,0,'monthly',80,1,0,0,0,'','', ?, ?)`
```

`apiKeys.planId` untuk key existing = `''` (artinya "pakai default plan"), **bukan** diisi `'default'` — supaya "no plan set" vs "explicit default" tetap bisa dibedakan di UI. Resolver treat `''` → default.

#### 3.5 Resolver Precedence (ditulis sekali, dipakai 1/2/7)

```go
type EffectivePolicy struct {
    Rpm, Tpm, Concurrency int
    LimitMicros, LimitTokens int
    Period string
    AlertPct int
    HardCutoff bool
    AllowedModels []string
    AllowedCombos []string
}

func (r *Repo) ResolvePolicy(apiKey *models.APIKey) EffectivePolicy {
    // planId == '' -> plans['default']; else plans[planId]; missing -> zero/unlimited
    // field 0/'' = "no limit / all allowed" (backward-compatible default)
}
```

Middleware [§F-1](#f-1-rate-limiting-per-key-rpm--tpm--concurrency), budget [§F-2](#f-2-budget-engine-usd-micros--token-cutoff), model-gate [§F-7](#f-7-per-key-model-access-allowlist--wildcard--combo-scope) **semuanya** memanggil `ResolvePolicy` — tidak ada logika limit tersebar.

#### 3.6 CRUD API + Guard

| Method | Path | Handler |
|--------|------|---------|
| `GET` | `/api/plans` | `HandleGetPlans` |
| `POST` | `/api/plans` | `HandleCreatePlan` |
| `PUT` | `/api/plans/{id}` | `HandleUpdatePlan` |
| `DELETE` | `/api/plans/{id}` | `HandleDeletePlan` |

- **Plan `default` tak bisa dihapus** (pola `name_guard.go`).
- Delete plan lain → key yang menunjuknya di-**fallback** ke default (`apiKeys.planId=''` di transaksi yang sama).
- Nama plan **unik per tenant** (`idx_plans_name`).

#### 3.7 Frontend (Svelte 5)

- `PlansView.svelte` (sudah ada) — tabel plan; form create/edit: nama, deskripsi, budget (USD/token/period/alert/cutoff), rate (rpm/tpm/concurrency), model access (multi-select + wildcard), combo scope.
- `ApiKeysView.svelte` — kolom **Plan** (dropdown) per key; badge kebijakan efektif.
- Kartu preset: "Free / Pro / Reseller Bronze-Gold" yang bisa diklon.

#### 3.8 Testing

| Test | Target |
|------|--------|
| `TestPlans_ResolveInheritance` | key dengan plan A dapat limit A |
| `TestPlans_DefaultFallback` | `planId=''` → policy plan `default` |
| `TestPlans_DeleteReassign` | delete plan → key fallback default, tak ada orphan |
| `TestPlans_NameUnique` | duplikat nama per tenant ditolak (409) |
| `TestPlans_ProtectDefault` | hapus `default` → 400 |
| `TestPlans_ZeroMeansUnlimited` | rpm=0/allowed_models='' tidak membatasi |
| `TestPlans_PreviewEffective` | endpoint GET mengembalikan policy gabungan |

#### 3.9 Risiko

- **Dua sumber kebenaran** bila kolom juga ditaruh di `apiKeys` → **dilarang**.
- **Delete plan = kunci kehilangan limit** → fallback default yang *unlimited* justru melonggarkan. Konfirmasi destruktif di UI + audit.
- **`allowed_models` wildcard** wajib pakai engine glob yang **sama** dengan `resolveModel` 9router-go — jangan bikin matcher kedua.
- **Skema bersama upstream:** `plans` Go-only, upstream Next.js mengabaikan.

---

### F-4. Guardrails (PII / Injection / Toxicity / Topics / Bias)

**Tier B · Ambil (MVP dulu) · Effort: Sedang (MVP) → Tinggi (full) · Depends-on: 3 · Depended-by: 10**

#### 4.1 Masalah

9router-go jadi titik transit **semua** prompt & response, tapi tak punya lapisan safety.

**Bukti:** tidak ada paket `guardrails`; `internal/middleware/` = auth/logging/maxbody/requestid; tak ada tabel policy/audit; search `guardrail` = 0.

#### 4.2 Semantik KeiRouter (verbatim dari `types.go`)

```go
Action: allow | log_only | warn | mask | block   // rank: Block>Mask>Warn>LogOnly>Allow
StrictestAction(a,b)                              // paling ketat antar-detector menang
Direction: inbound | outbound
Severity:  low | medium | high
Finding{ Entity, Score, Start, End, Redacted }    // Redacted dipakai saat Mask
Decision{ Detector, Action, Findings, Mutated }   // Mutated = konten hasil rewrite
```

Paket KeiRouter = 20 file (`pii.go`, `injection.go`, `toxicity.go`, `topics.go`, `bias.go`, `engine.go`, `hub.go`, `resolver.go`, `policy.go`, `presidio.go`, `embedding.go`, `lexicon.go`, `patterns.go`…). Migration `0017_guardrails.sql`.

Layering: policy per `scope` ∈ `global | provider | model | chain | apikey`, satu baris per `(tenant,scope,scope_id)`, merge saat request (**most-specific menang**).

#### 4.3 MVP (yang diambil)

Paket `internal/guardrails/` minimal:
- Detektor native **regex-only**: `pii` (email/phone/CC/IBAN/IP/URL + pola **NIK/NPWP/passport ID**) dan `injection` (ignore-previous, role override, DAN, prompt-leak, safety bypass).
- Engine + `StrictestAction` + resolver layering.
- Actions `allow | log_only | warn | mask | block`.
- Streaming-safe output scanner (buffer geser 256 char).
- 2 tabel Go-only + audit.

**Ditunda ke "full":** Presidio sidecar, OpenAI Moderation, embedding topics, toxicity lexicon bilingual, bias outbound, retention job.

#### 4.4 Skema

```sql
CREATE TABLE IF NOT EXISTS guardrail_policies (
  id         TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL DEFAULT 'default',
  scope      TEXT NOT NULL,        -- global|provider|model|chain|apikey
  scope_id   TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1,
  config     TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_grp_scope
  ON guardrail_policies(tenant_id, scope, scope_id);

CREATE TABLE IF NOT EXISTS guardrail_logs (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, request_id TEXT DEFAULT '',
  api_key_id TEXT DEFAULT '', provider TEXT DEFAULT '', model TEXT DEFAULT '',
  chain_id TEXT DEFAULT '', detector TEXT NOT NULL, direction TEXT NOT NULL,
  action TEXT NOT NULL, severity TEXT DEFAULT '', reason TEXT DEFAULT '',
  findings TEXT DEFAULT '[]', created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_grl_ts ON guardrail_logs(created_at DESC);
```

`settings`: `guardrailsEnabled`, `guardrailsDefaultAction`.

#### 4.5 Titik Integrasi (dua tap)

```
RequireApiKey → RateLimit(1) → Budget(2) → ModelAccess(7)
   → [GR INBOUND] scan messages[].content ; block→400, mask→rewrite request body
   → chat.HandleChatCompletions → translator/executor
   → [GR OUTBOUND] JSON: scan response.content ; stream: tap tiap event SSE
```

**Wajib dua jalur** (JSON + SSE per-event), bukan body mentah saja. Implementasi: wrapper `http.ResponseWriter` + SSE scanner per-event; `block` → potong stream + emit terminal framing error (9router-go `response_writer.go` sudah punya konsep terminal framing).

#### 4.6 API & Frontend

| Method | Path | Fungsi |
|--------|------|--------|
| `GET/POST/PUT/DELETE` | `/api/guardrails/policies[/{id}]` | CRUD policy per scope |
| `GET` | `/api/guardrails/logs?...` | query audit |
| `POST` | `/api/guardrails/test` | dry-run teks (rate-cap 10/min) |
| `GET` | `/api/guardrails/templates` | preset (Indonesia PII, Strict, Compliance) |
| `GET/POST` | `/api/guardrails/bundle` | export/import JSON |

Svelte `GuardrailsView.svelte`: editor policy per scope, tabel audit live (pola `ConsoleLogStream.svelte`), test-box, template picker.

#### 4.7 Testing

| Test | Target |
|------|--------|
| `TestGr_PII_InboundBlock` | email di prompt + block → 400, provider tak dipanggil |
| `TestGr_PII_Mask` | mask → request body di-rewrite sebelum dispatch |
| `TestGr_Injection_Detect` | "ignore previous instructions" kena |
| `TestGr_IDN_NIK` | pola NIK/NPWP terdeteksi |
| `TestGr_StreamMask` | PII di tengah SSE → cabut + terminal framing |
| `TestGr_LayerPrecedence` | model-policy overrides global |
| `TestGr_StrictestWins` | dua detector (warn+block) → block |
| `TestGr_Audit` | tiap keputusan → satu baris `guardrail_logs` |
| `TestGr_Offline` | `allow_external_engines=false` → tak ada HTTP keluar |

#### 4.8 Rollout

1. Tabel + engine + PII/injection regex + resolver + inbound tap, **log_only default** (observasi dulu).
2. Outbound + streaming mask/block.
3. Audit UI + templates + export/import.
4. (Full) Presidio/embedding/toxicity/bias, retention.

#### 4.9 Risiko

- **False positive blokir traffic sah** → mulai `log_only`/`warn`, naikkan per scope sadar.
- **Latensi per-request** — batasi panjang scan, skip tool-heavy base64; ukur via 10.
- **Streaming rewrite tidak bisa "un-send"** byte yang terlanjur di-flush → buffer geser harus menahan cukup context.
- **Konsistensi dua jalur** (JSON/SSE) — uji paritas.

---

### F-5. Credential Vault (AES-256-GCM Envelope)

**Tier B · Ambil · Effort: Sedang (encrypt) + Tinggi (migrasi aman) · Depends-on: — · Depended-by: 6**

#### 5.1 Masalah — Temuan Keamanan Paling Tajam

Kredensial provider 9router-go disimpan **plaintext JSON**:

- `providerConnections.data` = plaintext JSON berisi `apiKey`, `accessToken`, `refreshToken`, client secrets (DATABASE.md §providerConnections, eksplisit).
- DATABASE.md: *"Provider credentials are stored in the database; protect the file and volume as secrets."*
- Tidak ada `internal/vault`/`crypto`; search `AES|envelope` = 0.
- Satu-satunya "rahasia" = `jwt-secret` (`internal/auth/session.go`), bukan enkripsi at-rest.

**Dampak:** bocor file DB / backup export = bocor **semua** token upstream.

#### 5.2 Desain KeiRouter

- `internal/vault/vault.go` — `Seal(acc, secret)` menyandikan apiKey/accessToken/refreshToken **in place**; `Open(acc)` membuka saat satu panggilan.
- `internal/crypto/envelope.go` — **envelope**: tiap secret punya **DEK** baru; DEK disegel **master key (KEK)** AES-256-GCM. `encrypt()` = `nonce || ciphertext`.
- Sifat penting: rotasi master key = **re-wrap DEK saja**, tak re-encrypt semua secret → murah.

#### 5.3 Kolom Go-only (granular)

```go
{"providerConnections","secretWrappedDEK", "TEXT DEFAULT ''"},
{"providerConnections","secretCiphertext", "TEXT DEFAULT ''"},
{"providerConnections","tokenWrappedDEK",  "TEXT DEFAULT ''"},
{"providerConnections","tokenCiphertext",  "TEXT DEFAULT ''"},
{"providerConnections","refreshWrappedDEK","TEXT DEFAULT ''"},
{"providerConnections","refreshCiphertext","TEXT DEFAULT ''"},
```

`data` tetap ada; field kredensial **dipindah** ke kolom sealed, `data` menyisakan config non-rahasia.

> **Keputusan penting: granular, bukan blob penuh.** `enabledModels` harus tetap bisa dibaca resolver model (`models_list.go` `parseConnectionModelData`) tanpa decrypt tiap listing. Whitelist field yang disegel: `apiKey`, `accessToken`, `authToken`, `refreshToken`, `clientSecret` — jangan tebak.

#### 5.4 Master Key (KEK)

- Env `ROUTER_MASTER_KEY` (32 B, base64). Tambah field `MasterKey` ke `internal/config`.
- **Belum di-set → mode disabled** (fallback plaintext), server boot normal + logger warning. Ini kunci migrasi tanpa merusak install existing.

#### 5.5 Alur Tulis/Baca

```
Dashboard create/update connection
  → vault.Seal(secret fields) → simpan WrappedDEK+Ciphertext; kosongkan plaintext di data
Dispatch / OAuth refresh
  → vault.Open(conn) in-memory → credentials untuk 1 panggilan → jangan cache plaintext global
Connection listing / probe
  → baca kolom non-sealed saja; tidak menyentuh vault
```

#### 5.6 Migrasi Plaintext → Sealed

Reuse pola `EnsureAdditiveColumns` saat startup:
1. Jika `MasterKey` ada: baris dengan kredensial plaintext tapi kolom sealed kosong → Seal + kosongkan plaintext.
2. **Backup pra-migrasi**: snapshot ke `DATA_DIR/db/pre-vault-<ts>.sqlite` sebelum ubah.
3. Gagal decrypt satu baris → jangan corrupt DB; tandai status + audit, biarkan plaintext (**fail-safe**).

#### 5.7 API & Ops

- `POST /api/vault/rotate` (admin + password header) → re-wrap semua DEK ke KEK baru.
- `GET /api/vault/status` → `{enabled, sealedCount, plaintextCount}`.
- Export DB **wajib** redaksi — jangan keluarkan ciphertext sebagai backup portabel.

#### 5.8 Testing

| Test | Target |
|------|--------|
| `TestVault_SealOpen_Roundtrip` | apiKey masuk → sealed → Open = plaintext asli |
| `TestVault_PerSecretDEK` | dua secret beda DEK |
| `TestVault_NonceUnique` | Seal dua kali plaintext sama → ciphertext beda |
| `TestVault_GCMAuth` | tamper ciphertext → Open error |
| `TestVault_DisabledFallback` | tanpa MasterKey → plaintext, boot OK |
| `TestVault_MigrateIdempotent` | jalankan migrasi 2× → tak double-seal |
| `TestVault_ListNoDecrypt` | `GET /v1/models` tak memanggil Open |
| `TestVault_Rotate` | re-wrap KEK → Open tetap benar |
| `TestVault_ExportNoPlaintext` | export tak memuat plaintext |

#### 5.9 Risiko

- **Kehilangan KEK = kredensial permanen hilang**. Mitigasi: backup pra-vault, docs recovery, opsi re-OAuth.
- **Dep stdlib cukup** (`cipher.NewGCM`, `golang.org/x/crypto/argon2`) — `go.mod` sudah pakai `x/crypto`.
- **Double-seal bug** saat restart → guard "kolom sealed terisi → skip".

---

### F-6. API Key Argon2id Hashing

**Tier B · Ambil · Effort: Sedang · Depends-on: — · Depended-by: 1/3 (lookup path)**

#### 6.1 Masalah

API key klien disimpan & dicocokkan **plaintext**:

**Bukti terverifikasi** (`internal/db/apikeys.go`):
```go
// ValidateApiKey
"SELECT isActive FROM apiKeys WHERE key = ? LIMIT 1"
// GetApiKeyByKey
"SELECT id, key, name, machineId, isActive, createdAt FROM apiKeys WHERE key = ? LIMIT 1"
```
Schema (`schema.go:89`): `apiKeys.key TEXT UNIQUE NOT NULL` + `idx_ak_key`.

**Dampak:** bocor DB = bocor semua API key aktif. Password dashboard sudah bcrypt, tapi key klien **tidak** — asimetri keamanan.

#### 6.2 Desain KeiRouter (`internal/identity/identity.go`)

> Keys are never stored in plaintext: creation returns the plaintext once … the store keeps only an **argon2id verifier** plus a **fast SHA-256 lookup index**. Authentication looks up the candidate row by the lookup index, then confirms with a **constant-time argon2** comparison.

- **LookupHash** = SHA-256(plaintext) → kolom berindeks, pencarian O(1) tanpa plaintext.
- **KeyHash** = argon2id verifier (~64 MiB + 4 thread/verifikasi).
- **authCache** TTL 5s, maks 256 entri, keyed by LookupHash — supaya burst (Claude Code 10+ req/s) tak menjalankan argon2 tiap request.

#### 6.3 Kolom (Go-only additive)

```go
{"apiKeys","keyHash",    "TEXT DEFAULT ''"},  // argon2id verifier
{"apiKeys","lookupHash", "TEXT DEFAULT ''"},  // SHA-256 hex, berindeks
{"apiKeys","keyDisplay", "TEXT DEFAULT ''"},  // mis. "sk-abc…wxyz" untuk list UI
```

Indeks: `CREATE UNIQUE INDEX IF NOT EXISTS idx_ak_lookup ON apiKeys(lookupHash) WHERE lookupHash<>''`.

`key` (plaintext) **dibiarkan** dulu untuk backward-compat read; dikosongkan bertahap.

#### 6.4 Alur Auth Baru (`RequireApiKey`)

```
plaintextKey = ExtractApiKey(r)
lookup = sha256(plaintextKey)
if cache valid(lookup) -> pass
row = repo.FindApiKeyByLookup(lookup)      // WHERE lookupHash = ?
if row==nil:
    row = repo.GetApiKeyByKey(plaintext)   // fallback legacy masa migrasi
    jika ketemu -> hash-kan baris itu (self-heal)
if row==nil || !active -> 401
ok = argon2id.Verify(plaintext, row.KeyHash)   // constant-time
if !ok -> 401
cache(lookup, row, ttl=5s); inject context
```

Argon2 param disamakan `crypto/apikey.go` KeiRouter; simpan sebagai konstanta package + dokumentasikan agar hash bisa diverifikasi lintas build.

#### 6.5 Create Path

- generate plaintext → tampilkan **sekali** di response (sudah begitu).
- simpan `keyHash`=argon2(plaintext), `lookupHash`=sha256(plaintext), `keyDisplay`=masked, **kosongkan `key`**.

#### 6.6 Migrasi Existing (self-heal)

Pola idempoten startup:
- Baris dengan `key` terisi tapi `keyHash` kosong → hash-kan → isi kolom → **set `key=''`**.
- Selama transisi, auth fallback [§F-6](#f-6-api-key-argon2id-hashing) menerima plaintext lama.
- Backup pra-migrasi sama seperti vault ([§F-5](#f-5-credential-vault-aes-256-gcm-envelope)).

#### 6.7 Revisi Spec Terkait

**Wajib:** [§F-14](#f-14-api-key-time-limit--resale-metadata) (time-limit) asumsikan `GetApiKeyByKey(WHERE key=?)` plaintext. Setelah [§F-6](#f-6-api-key-argon2id-hashing), lookup = `WHERE lookupHash=?`.

- `HandleGetApiKeys`: jangan kembalikan `key` plaintext; tampilkan `keyDisplay`.
- **"Reveal full key" tidak lagi mungkin** — plaintext hilang. Ganti dengan **Regenerate**.
- **Perubahan perilaku UI** — deklarasikan eksplisit di changelog.

#### 6.8 Frontend

`ApiKeysView.svelte`: hapus "copy/reveal full key"; tambah tombol **Regenerate** (`POST /api/keys/{id}/rotate` baru) yang menghasilkan plaintext sekali.

#### 6.9 Testing

| Test | Target |
|------|--------|
| `TestArgon_VerifyRoundtrip` | create→auth key benar lolos, key salah 401 |
| `TestArgon_LookupOnly` | `WHERE lookupHash` dipakai, bukan plaintext |
| `TestArgon_ConstantTime` | mismatch tetap jalankan verify (no early-return timing) |
| `TestArgon_CacheTTL` | disable key →生效 ≤5s |
| `TestArgon_CacheBound` | >256 entri tak tumbuh tanpa batas |
| `TestArgon_SelfHeal` | baris plaintext lama ter-upgrade sekali akses |
| `TestArgon_NoPlaintextAtRest` | `SELECT key FROM apiKeys` → `key=''` pasca migrasi |
| `TestArgon_RegenerateRotate` | rotate hasil plaintext sekali, hash baru |

#### 6.10 Risiko

- **Argon2 CPU per request** → auth cache **WAJIB**. Tanpa itu gateway tersedak (klaim 32K RPS mustahil dengan argon2/req).
- **Reveal hilang** → operator harus regenerate. Komunikasi di UI + changelog.
- **Regen param beda antar versi** → bekukan param; versi-bump hash (prefix `$argon2id$v=...`).
- **Bocor lookupHash** tak membuka plaintext, tapi mengizinkan enumerate bila DB bocor bersamaan — mitigasi: rate-limit auth failures (1).

---

### F-7. Per-Key Model Access (Allowlist + Wildcard + Combo-Scope)

**Tier B · Ambil · Effort: Rendah · Depends-on: 3 · Depended-by: 8**

#### 7.1 Masalah

9router-go membatasi model per **connection** (`enabledModels`, `strictModelAssignment`), tapi **tidak per API-key klien**.

**Bukti:**
- Tidak ada tabel allowlist per key.
- `/v1/models` (`HandleModels`) mengembalikan daftar penuh berdasar mode `?connected/?all`, **tidak peduli key mana** yang bertanya.

**Dampak:** tak bisa jual paket "hanya model X".

#### 7.2 Dua Mekanisme, Satu Gate

1. **Allowlist eksplisit** (tabel baru, Go-only):

```sql
CREATE TABLE IF NOT EXISTS api_key_model_access (
  api_key_id TEXT NOT NULL,   -- → apiKeys.id
  model      TEXT NOT NULL,   -- id persis ATAU pola wildcard
  created_at TEXT NOT NULL,
  PRIMARY KEY (api_key_id, model)
);
```

2. **Plan-scope** (dari 3): `plans.allowed_models` (wildcard) + `plans.allowed_combos` (JSON array).

Gate final = **union** allowlist tabel + policy plan. Kosong di keduanya = tak membatasi.

#### 7.3 Lokasi Enforcement (satu fungsi, dua jalur)

```go
// internal/handlers/chat/access.go (baru)
func (h *ChatHandler) checkModelAccess(r *http.Request, modelID string) error {
    key := middleware.GetAuthenticatedApiKey(r)  // sudah ada di auth.go
    if key == nil { return nil }                  // non-key callers tak kena
    patterns := h.effectiveAllowedModels(r.Context(), key)
    if len(patterns) == 0 { return nil }           // all allowed
    if !matchAny(patterns, modelID) && !comboAllowed(...) {
        return errForbiddenModel                   // → 403
    }
    return nil
}
```

- **Dispatch:** panggil di awal `resolveModel`/`HandleChatCompletions` (sebelum connection selection). Taruh setelah `handleBypassRequest`.
- **Listing (`/v1/models`):** filter keluaran lewat `effectiveAllowedModels` yang **sama**.

#### 7.4 Matcher

Gunakan engine glob **sama** dengan model-resolver 9router-go (jangan bikin matcher kedua — risiko beda perilaku). Wildcard `*` per segmen; cocok pada `provider/model` dan bare id.

#### 7.5 Backward-compat

Kosong = all-allowed → **tidak ada key existing terpengaruh**. Fitur aktif hanya saat admin mengisi allowlist / assign plan.

#### 7.6 API & Frontend

| Method | Path | Fungsi |
|--------|------|--------|
| `GET` | `/api/keys/{id}/models` | allowlist key |
| `PUT` | `/api/keys/{id}/models` | set array model/pattern (replace) |
| `GET` | `/v1/models` | daftar tersaring per key |

`ApiKeysView.svelte`: editor model multiselect + input wildcard + preview "policy efektif".

#### 7.7 Testing

| Test | Target |
|------|--------|
| `TestAccess_EmptyAllowsAll` | tak ada pattern → semua lolos |
| `TestAccess_ExplicitAllow` | hanya model di tabel lolos; lain 403 |
| `TestAccess_Wildcard` | `claude-*` lolos `claude-x`, tolak `gpt-y` |
| `TestAccess_PlanInherit` | key dengan plan A kena `allowed_models` A |
| `TestAccess_ListFiltered` | `/v1/models` hanya tampilkan yang boleh |
| `TestAccess_ComboScope` | `allowed_combos` membatasi listing |
| `TestAccess_DispatchAndListAgree` | model muncul di list ⟺ bisa dipanggil |
| `TestAccess_NilKeyBypass` | caller sesi/CLI tak kena gate |

#### 7.8 Risiko

- **Mismatch dispatch vs listing** → pakai **SATU** `effectiveAllowedModels`.
- **Matcher ganda** → wajib reuse resolver glob existing.
- **Combo sebagai model** — pastikan gate memahami nama combo sebagai id sah.
- **Wildcard `*` vs `''`** — dokumentasikan bedanya.

---

### F-8. Public Usage Portal

**Tier C · Ambil · Effort: Sedang (UI > backend) · Depends-on: 2, 3, 7**

#### 8.1 Masalah

9router-go menampilkan usage **hanya di dashboard** (butuh sesi login).

**Bukti:** `/usage/stats`, `/usage/stream` berada di grup `RequireApiKey`/`RequireDashboardAuth`. Tidak ada route publik `/portal` / `/api/portal/*` yang mengautentikasi dengan **key itu sendiri**.

**Dampak:** pembeli key tak bisa lihat kuota/spend sendiri.

#### 8.2 Desain

**Autentikasi = API key itu sendiri:**

```
GET  /portal                  -> Svelte SPA route (tanpa sesi), input key
GET  /api/portal/usage        -> header X-API-Key; resolve key -> hanya data key itu
GET  /api/portal/summary      -> budget/spend/plan (2/3), hemat kompresi
GET  /api/portal/usage/stream -> SSE live (reuse usage_stream.go), scoped ke key
GET  /v1/models               -> sudah discoping 7
```

Gate: **bukan** `RequireDashboardAuth`. Handler publik yang `ExtractApiKey` → `ValidateApiKey` → batasi query ke `apiKey` milik key tsb saja.

> **Keamanan:** key via query mudah bocor di log/referrer. **Wajib** header `X-API-Key`/`Authorization` untuk portal API; query `?key=` hanya untuk halaman awal (lalu migrasi ke session-cookie portal-scoped pendek). Ikuti disiplin `ExtractApiKey` yang sudah membatasi query-key hanya untuk path `/stream`.

#### 8.3 Data yang Ditampilkan

| Widget | Sumber |
|--------|--------|
| Spend vs limit | `budgets.spent_micros/limit_micros` (2) |
| Token used / period | `usageHistory` WHERE apiKey = key; `budgets.period` |
| Usage over time | agregat `usageDaily` per key |
| Compression savings | RTK/caveman metrics per key |
| Plan / model allowed | `ResolvePolicy` (3/7) |

#### 8.4 Frontend (Svelte 5)

Halaman `PortalView.svelte` (rute publik `/portal`):
- input key → simpan di `sessionStorage` (bukan localStorage permanen).
- kartu spend/limit, chart usage (pola `AnalyticsView.svelte`), hemat kompresi, daftar model, progress plan.
- badge branding dari portal-branding bila fitur 15 aktif.
- i18n: reuse `language` di settings.

Sidebar/TopBar **tidak** menampilkan tautan portal ke admin; portal = surface terpisah.

#### 8.5 Testing

| Test | Target |
|------|--------|
| `TestPortal_KeyScoped` | key A tak bisa lihat data key B |
| `TestPortal_NoSessionNeeded` | akses tanpa cookie sesi OK |
| `TestPortal_BadKey` | key invalid → 401, tak bocor struktur |
| `TestPortal_StreamScoped` | SSE hanya event milik key |
| `TestPortal_BudgetDisplay` | spent/limit/period tampil benar |
| `TestPortal_QueryKeyOnlyStream` | `?key=` ditolak untuk non-stream |

#### 8.6 Risiko

- **Key di query/log** → paksa header, cookie pendek pasca-input.
- **Isolasi data** = beban keamanan. Uji `TestPortal_KeyScoped` keras; audit semua query portal menyertakan filter key.
- **Abuse endpoint publik** → rate-limit publik lebih ketat; endpoint publik tak boleh query mahal.
- **CORS** → konfig sadar, jangan wildcard dengan kredensial.

---

### F-9. Response Cache (Exact-Match Dulu)

**Tier C · Ambil (exact-only) · Effort: Rendah (exact) → Tinggi (semantic) · Depended-by: 10**

#### 9.1 Masalah

9router-go **tidak punya cache respons**; setiap request identik → dispatch ulang ke provider (bayar token lagi).

**Bukti:** tidak ada `internal/cache`. Yang ada hanya bypass request sintetik (`handleBypassRequest` untuk warmup/keepalive Claude Code).

#### 9.2 Kenapa EXACT Dulu

Use-case utama 9router-go = **coding proxy**. Prompt coding hampir tak pernah identik bit-for-bit → **semantic cache hit-rate rendah**, tapi butuh **embedding model** (biaya + latensi + dep).

**Rekomendasi bertingkat:**
- **Exact-match (PR ini):** hash kunci request → simpan response. Murah, nol-dep, tetap berguna (retry, fan-out combos identical, health-check).
- **Semantic (tunda, flag off):** hanya bila operator punya embedding endpoint & trafik berulang.

#### 9.3 Desain Exact-Match

```go
// internal/cache/exact.go
func CacheKey(req *core.ChatRequest) string {
    // sha256 over normalized: model + messages + tools + temperature/top_p/seed
    // NORMALISASI deterministik (key-sort JSON) — hindari beda urutan field = miss palsu
}
type Store interface{ Get(key)(resp,ok); Set(key,resp,ttl); }
// MemoryStore (LRU, maxEntries, TTL)
```

**Placement:**
```
RequireApiKey → RateLimit → Budget → ModelAccess → [CACHE LOOKUP] (exact)
   hit  -> return stored (no provider, cost 0)
   miss -> dispatch -> [CACHE STORE] hanya bila response non-stream OK
```

- **Stream:** default **non-stream only** (SSE tak bisa di-cache utuh tanpa buffer penuh).
- **Determinism gate:** cache **hanya** saat `temperature==0` — jawaban sampling tak boleh dilayani ulang identik.

#### 9.4 API & Frontend

- `settings`: `cacheEnabled`, `cacheTTLSeconds`, `cacheMaxEntries`, `cacheSemantic`(false).
- `GET /api/cache/stats` → size, hits, misses (sumber 10).
- `POST /api/cache/clear` → flush.
- UI: toggle di `SettingsView.svelte`.

#### 9.5 Testing

| Test | Target |
|------|--------|
| `TestCache_ExactHit` | request identik ke-2 → hit, provider tak dipanggil, cost 0 |
| `TestCache_Normalization` | beda urutan JSON field → key sama |
| `TestCache_TempGuard` | temperature>0 → tak di-cache |
| `TestCache_TTLLapse` | lewat TTL → miss |
| `TestCache_LRUBound` | >maxEntries → evict |
| `TestCache_StreamSkipped` | stream:true tak masuk cache-exact |
| `TestCache_SemanticOffDefault` | tanpa embedder → jalur semantic no-op |

#### 9.6 Risiko

- **Stale/incorrect** — provider perbaiki model → cache melayani jawaban lama. Mitigasi: TTL pendek, key menyertakan model-id, `clear` manual.
- **Privacy** — cache menyimpan isi prompt/response (bisa PII). Default off + jangan cache saat guardrail PII aktif kecuali di-mask dulu.
- **Cross-key leak** — kunci cache namespace per key.
- **False economy** — ukur hit-rate dulu sebelum naik ke semantic.

---

### F-10. Prometheus Metrics

**Tier C · Ambil · Effort: Rendah · Depended-by: 4**

#### 10.1 Masalah

9router-go punya **tracing per-request** (`internal/tracing/`), bukan **metrics agregat**.

**Bukti:** tidak ada `observ`/`metrics`; tak ada endpoint `/metrics`; grep `prometheus/promhttp` = 0.

**Dampak:** tak bisa pantau laju/latensi/TTFT/error via tooling ops standar (VPS/Coolify).

#### 10.2 Paket `internal/observ`

```go
type Metrics struct {
    RequestsTotal      *prometheus.CounterVec   // provider, model, endpoint, status
    RequestDuration    *prometheus.HistogramVec // provider, model
    TimeToFirstToken   *prometheus.HistogramVec
    TokensTotal        *prometheus.CounterVec   // kind=prompt|completion
    CostMicros         *prometheus.CounterVec   // ← reuse unit micros (2)
    Fallbacks          *prometheus.CounterVec
    UpstreamErrors     *prometheus.CounterVec
    RateLimitRejects   *prometheus.CounterVec   // ← 1
    BudgetBlocks       *prometheus.CounterVec   // ← 2
    GuardrailDecisions *prometheus.CounterVec   // ← 4
    GuardrailEval      *prometheus.HistogramVec // ← 4
}
func New() *Metrics   // registry sendiri (bukan default) untuk isolasi test
```

Dep: `github.com/prometheus/client_golang` (murni Go, CGO-free).

#### 10.3 Titik Instrumentasi (reuse hook existing)

- `handlers/chat/usage.go` — tempat `usageHistory` di-insert → `RequestsTotal`, `TokensTotal`, `CostMicros`, `RequestDuration`.
- Fallback path (`fallback.go`) → `Fallbacks`.
- Error class → `UpstreamErrors`.
- TTFT: sudah dilacak → expose histogram.
- Middleware 1/2/4 → counter tolakan/blokade/keputusan.

#### 10.4 Endpoint `/metrics`

**Rekomendasi:** di grup **dashboard-auth** (jangan publik mentah) — label `model/provider/apiKey` bisa membocor metadata. Redaksi label key (id pendek), jangan plaintext.

#### 10.5 Testing

| Test | Target |
|------|--------|
| `TestMetrics_RequestCounted` | 1 request → `requests_total` +1, label benar |
| `TestMetrics_TTFT` | stream → histogram ttft terisi |
| `TestMetrics_CostMicros` | cost REAL meter → Counter micros (presisi) |
| `TestMetrics_Guardrail` | keputusan guardrail → counter by detector/action |
| `TestMetrics_EndpointAuth` | `/metrics` butuh dashboard-auth |
| `TestMetrics_LabelRedaction` | tak ada plaintext apiKey di label |

#### 10.6 Risiko

- **Cardinality tinggi** (provider×model×status) → batasi label (`provider` saja, atau top-N + bucket `_other`); hindari `apiKey` sebagai label.
- **Registry global** tabrakan saat test paralel → registry per-`Metrics`.
- **Histogram buckets** harus wajar (latensi gateway ms–s).

---

### F-11. System Tray

**Tier C · SKIP · Effort: Sedang · Alasan: bentrok filosofi single-binary headless**

#### 11.1 Yang Ditawarkan KeiRouter

- `internal/tray/` — ikon system tray desktop; CLI `keirouter tray`.
- Kenyamanan desktop: lihat status, buka dashboard, stop/start dari tray.

#### 11.2 Kenapa Tidak Cocok

9router-go **sengaja** headless:
- Model lifecycle = **daemon** (`internal/daemon` + `internal/proc`): `start/stop/restart/status/logs`, PID file, health-wait, spawn detached.
- Filosofi binary: **single Go binary, CGO-free, 42 MB RAM, 32K RPS**.
- Tray butuh **dep GUI** (`getlantern/systray` / `energye/systray` → Win32/Cocoa/GTK, sering **CGO**) → mengancam: CGO-free build, `make cross` 5 target, ukuran/footprint.

#### 11.3 Fungsi Sudah Tercakup

| Kebutuhan tray | Padanan 9router-go |
|----------------|--------------------|
| Status | `9router-go status` (pid/dashboard/log) |
| Start/stop | `start` / `stop` / `restart` |
| Live log | `logs -n 100` + `DATA_DIR/run/gateway.log` |
| Buka dashboard | URL di output start |
| Graceful shutdown | tombol **Shutdown** di dashboard |

Yang kurang cuma kenyamanan klik desktop — bukan gap kapabilitas.

#### 11.4 Kalau Tetap Diinginkan

Buat sebagai **build-tag terpisah**, bukan inti:
```go
//go:build tray   // build terpisah, boleh CGO
package tray
```
Atau lebih ringan: **PWA manifest + Service Worker** ("Install app" di browser) — tanpa dep GUI, tetap CGO-free.

#### 11.5 Keputusan

**Skip.** Tidak ada PR yang dibuka dari spec ini.

---

### F-12. Multi-Tenant

**Tier C · TUNDA · Effort: Tinggi · Depends-on: semua**

#### 12.1 Posisi

KeiRouter **multi-tenant sejak dasar** (tabel `tenants`, hampir semua tabel punya `tenant_id`). 9router-go **single-user self-host**: satu DB, satu dashboard password, satu set connection.

Multi-tenant = **mengubah DNA produk** (isolasi query di semua repo, resolusi tenant, auth boundary, per-tenant KEK).

#### 12.2 Kenapa Ditunda (bukan ditolak)

Skenario jual-key bisa dilayani **single-tenant** dengan plans + model-access + portal (1–8): operator = satu "owner", key = "pembeli", **satu trust domain** (semua kredensial upstream milik operator). Itu model bisnis resale gateway yang realistis.

Multi-tenant penuh hanya perlu bila **beberapa organisasi** masing-masing punya provider connection sendiri dalam satu instance.

#### 12.3 Fondasi yang Disiapkan SEKARANG

Agar 12 tak menuntut migrasi ulang, spec lain sudah menulis **kolom `tenant_id` konstan `'default'`**:
- `guardrail_policies`, `budgets`, `plans` → `tenant_id TEXT NOT NULL DEFAULT 'default'`.
- Resolver menyertakan `WHERE tenant_id = ?` sejak awal (nilai `'default'`) → tambah tenant = ganti nilai, bukan refactor query.
- `apiKeys` → tambah `tenantId` (additive) saat 12 aktif.

#### 12.4 Perubahan yang Dibutuhkan (sketsa)

| Area | Kerja |
|------|-------|
| Schema | tabel `tenants`, `tenant_id`+indeks di semua tabel Go-only |
| Auth | resolusi tenant di middleware; dashboard per-tenant vs global admin |
| Repos | semua query +`tenant_id` filter (audit menyeluruh) |
| Vault | KEK per-tenant |
| Guardrails | policy per-tenant + `allow_external_engines` |
| Frontend | tenant switcher / admin super-user |

#### 12.5 Rekomendasi

**Jangan** buka PR 12 sekarang. Pastikan fitur 2/3/4 menulis `tenant_id='default'` + resolver tenant-scoped. Dengan itu 12 jadi *config + UI*, bukan migrasi database berisiko.

#### 12.6 Risiko Kalau Dipaksa Sekarang

- **Kebocoran antar-tenant** (satu query lupa filter) = insiden keamanan lintas pelanggan.
- **SQLite writer contention** lintas tenant (upstream resmi satu-writer).
- **Backup/export** jadi harus tenant-scoped; salah = cross-tenant leak.
- **Complexity vs nilai** → overbuild (YAGNI).

---

### F-13. Terse Output Saver (Request-Side Serialization)

**Tier — · Verifikasi coverage dulu · Effort: Rendah**

#### 13.1 Pertanyaan: Apakah 9router-go Sudah Punya?

**Hasil audit terverifikasi** (`internal/tokensaver/`):

| File | Isi | Sisi |
|------|-----|------|
| `prompts.go` | `CavemanLite/Full/Ultra`, `ADHPLite/Full` | **directive output** |
| `injection.go` | `GetCavemanPrompt`, `GetADHDPrompt`, dst | **directive output** |
| `compress.go` | `CompressMessages`, `CompressText`, `isGitDiff/GitLog/Grep/Tree` | **kompresi konten tool-result** |

**Kesimpulan: TIDAK ADA request-side message/tool serialization.** Semua saver existing = directive output atau kompresi konten, bukan restrukturisasi bentuk pesan.

#### 13.2 Yang Sebenarnya Beda

KeiRouter `internal/terse/terse.go` — **Token-Efficient Serialization**:

```
// komentar terse.go: "into the compact TERSE format and injects them into the
// system prompt … a request-side transform: only modifies the system prompt"
const sentinel = "<!-- keirouter:terse -->"
Levels: light  = directive saja (tanpa serialisasi message/tool)
        medium = directive + serialisasi messages + tools (DEFAULT)
Apply(req *core.ChatRequest, cfg) // ubah system prompt + pesan
```

Terse **mengompaksi bentuk pesan/tools** (hemat token input) + directive. Berbeda dari Caveman (hanya directive gaya output).

#### 13.3 Relasi Antar Saver (KeiRouter)

| Saver | Arah | Sifat |
|-------|------|-------|
| **Terse** | Output directive + request serialization | **eksklusif** dengan Caveman |
| **Caveman** | Output directive (agresif, 65–75% hemat) | **eksklusif** dengan Terse |
| **Ponytail** | Output directive (`lite/full/ultra`) | **stackable** di atas keduanya |

#### 13.4 Desain (Gap Terbukti)

Reuse pipeline token-saver existing (`tokensaver` + `TokenSaverConfig` di `app/handlers.go`, order: `normalizer → RTK → Headroom → Terse/Caveman → Ponytail → translate`):

```go
// internal/tokensaver/terse.go (baru)
const TerseSentinel = "<!-- 9router:terse -->"
type TerseLevel string // light|medium|heavy
func ApplyTerse(body []byte, level TerseLevel) ([]byte, bool)
```

- Toggle `terseEnabled` + `terseLevel` di settings (sejajar `cavemanEnabled`).
- **Eksklusif** vs Caveman (pilih satu) — enforce di config apply. Ponytail tetap boleh menumpuk.
- Ukur penghematan lewat metrik `TokensTotal` (10).

#### 13.5 Testing

| Test | Target |
|------|--------|
| `TestTerse_Light` | directive saja; messages utuh |
| `TestTerse_Medium` | messages+tools diserialisasi + directive |
| `TestTerse_Idempotent` | Apply 2× tak dobel sentinel |
| `TestTerse_ExclusiveCaveman` | terse+caveman aktif bersamaan ditolak |
| `TestTerse_StackPonytail` | terse+ponytail boleh |
| `TestTerse_TokenDrop` | token input medium < baseline |

#### 13.6 Verdict

**Gap terbukti** (audit selesai). Ambil sebagai fitur kecil di bawah naungan `tokensaver`, dengan guard mutually-exclusive vs Caveman. Prioritas rendah — jangan tumpang-tindih dua saver yang fungsi sama.

---

### F-14. API Key Time-Limit & Resale Metadata

**Tier A/B · Ambil · Effort: Sedang · Depends-on: 3, 6 · Bagian dari spec 14**

#### 14.1 Ini PR Tersendiri

Sebagian besar isi spec `14` sudah dimiliki spec kanonik (1/2/3/6/7). Yang **unik** dan tetap PR tersendiri:

| Kolom | Fungsi |
|-------|--------|
| `expiresAt` | kontrak berakhir mutlak (bukan fitur KeiRouter) |
| `lastUsedAt` | audit |
| `usedCount` | total request |
| `metadata` | JSON `{customerId, priceCents, billingCycle}` |

> `expiresAt` (kontrak mutlak) **bukan** fitur KeiRouter — KeiRouter pakai `period` reset rolling. Keduanya berdampingan: **expiry = akhir kontrak, period = kuota rolling**.

#### 14.2 Skema (additive Go-only)

```go
{"apiKeys", "expiresAt",  "TEXT DEFAULT ''"},  // RFC3339 UTC; '' = never
{"apiKeys", "lastUsedAt", "TEXT DEFAULT ''"},
{"apiKeys", "usedCount",  "INTEGER DEFAULT 0"},
{"apiKeys", "metadata",   "TEXT DEFAULT ''"},  // JSON
```

**Bukan** di sini: rate limit (1), budget (2), plan (3), allowed models (7), key hashing (6).

#### 14.3 Model & Repo

```go
type APIKey struct {
    ID        string  `json:"id"`
    Key       string  `json:"key"` // kosong setelah 6
    Name      *string `json:"name,omitempty"`
    MachineID *string `json:"machineId,omitempty"`
    IsActive  int     `json:"isActive"`
    CreatedAt string  `json:"createdAt"`
    // Resale:
    ExpiresAt  *string `json:"expiresAt,omitempty"`
    LastUsedAt *string `json:"lastUsedAt,omitempty"`
    UsedCount  *int    `json:"usedCount,omitempty"`
    Metadata   *string `json:"metadata,omitempty"`
    // Kebijakan (via 3):
    PlanID *string `json:"planId,omitempty"`
}

func (r *Repo) UpdateApiKeyUsage(keyID string) error {
    now := time.Now().UTC().Format(time.RFC3339)
    _, err := r.db.Exec(
        `UPDATE apiKeys SET lastUsedAt = ?, usedCount = usedCount + 1 WHERE id = ?`,
        now, keyID)
    return err
}
```

#### 14.4 Cek Expiry di Middleware

Setelah verifikasi key (6) dan `isActive`:

```go
if apiKeyObj.ExpiresAt != nil && *apiKeyObj.ExpiresAt != "" {
    expires, err := time.Parse(time.RFC3339, *apiKeyObj.ExpiresAt)
    if err == nil && time.Now().After(expires) {
        handlerutil.WriteJSONError(w, http.StatusUnauthorized, "API key expired.")
        return
    }
}
```

**Combo-scope diserahkan ke 7** (gate model-access), bukan di middleware auth.

#### 14.5 Backfill

Lewat `EnsureAdditiveColumns` (idempoten, jalan tiap startup) — sama seperti kolom Go-only lain (`providerConnections.lastUsedAt`, `consecutiveUseCount`).

#### 14.6 Testing

| Test | Target |
|------|--------|
| `TestKey_ExpiryRejects` | `expiresAt` lampau → 401, provider tak dipanggil |
| `TestKey_ExpiryEmptyNever` | `expiresAt=''` → tak pernah expire |
| `TestKey_UsageCounter` | tiap request sukses → `usedCount+1`, `lastUsedAt` terisi |
| `TestKey_MetadataRoundtrip` | metadata JSON tersimpan & terbaca kembali |

---

### F-15. Branding (White-Label)

**Tier C · Ambil · Effort: Sedang · Terkait: 8 (portal)**

#### 15.1 Goal

Rebranding visual dashboard tanpa rebuild binary: nama produk, palet, tipografi, radius, shadow, custom CSS — dengan live preview.

#### 15.2 Persistensi = File JSON (bukan SQLite)

```
DATA_DIR/
  branding/
    default.json          # Global default brand
    tenants/
      acme-corp.json      # tenant override
    assets/
      acme-corp/
        logo.png
        favicon.png
```

**Kenapa file, bukan DB:** human-editable tanpa tool DB, version-controllable, separable dari runtime data, multi-tenant friendly. Tidak melanggar aturan additive-only skema upstream.

> Sentuhan DB satu-satunya: kolom opsional `apiKeys.brandId` (Go-only, additive) — gabungkan bila se-PR dengan 6/14.

#### 15.3 Schema Konfigurasi

```json
{
  "id": "acme-corp",
  "name": "Acme AI Gateway",
  "tagline": "Internal AI routing platform",
  "logo_url": "/branding/assets/acme-corp/logo.png",
  "favicon_url": "/branding/assets/acme-corp/favicon.png",
  "theme": { "default_mode": "dark", "allow_toggle": true },
  "palette": { "preset": "terracotta", "custom": { "brand_500": "#E56A4A" } },
  "typography": { "font_headline": "Inter", "font_body": "Inter", "font_code": "JetBrains Mono", "scale": "default" },
  "shape": { "radius_brand": "10px", "radius_brand_lg": "14px", "shadow_intensity": "warm" },
  "custom_css": "",
  "login": { "background_url": "", "welcome_text": "" },
  "updated_at": "2026-10-06T00:00:00Z"
}
```

#### 15.4 Preset Palet

| Preset | Brand 500 | Brand 600 | Karakter |
|--------|-----------|-----------|----------|
| `terracotta` (default) | `#E56A4A` | `#CC5236` | Warm, identitas 9router saat ini |
| `sage-terra` | `#6a7450` | `#5d6745` | Earthy, grounded |
| `ocean` | `#2563eb` | `#1d4ed8` | Fresh, professional |
| `midnight` | `#4f46e5` | `#4338ca` | Premium |
| `forest` | `#059669` | `#047857` | Natural |
| `rose` | `#be185d` | `#9d174d` | Elegant |
| `lavender` | `#7c3aed` | `#6d28d9` | Creative |
| `mono` | `#374151` | `#1f2937` | Minimal |
| `sunset` | `#ea580c` | `#c2410c` | Energetic |
| `arctic` | `#0891b2` | `#0e7490` | Cool |
| `cherry` | `#dc2626` | `#b91c1c` | Authoritative |

Setiap preset menghasilkan skala penuh 50–900 via interpolasi HSL (algoritma sama `color-utils.ts` KeiRouter).

#### 15.5 Resolusi Multi-Tenant

```
1. API key → tenant_id (dari apiKeys extension)
2. Subdomain/Host header → tenant mapping
3. Query param ?brand=<id> (dev/testing only)
4. Fallback: default.json
```

**Option A (recommended):** kolom `apiKeys.brandId` — brand di-resolve saat request + diteruskan ke SPA via bootstrap endpoint.
**Option B:** map `Host` header ke `branding/tenants/<host>.json` (tanpa perubahan DB, cocok reverse-proxy).

Spec mengimplementasikan **keduanya**; Host-based menang bila dikonfigurasi.

#### 15.6 Endpoints

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| `GET` | `/api/branding` | Dashboard session | Get current tenant config |
| `PUT` | `/api/branding` | Dashboard session | Update config |
| `GET` | `/api/branding/tenants` | Dashboard session | List all (admin) |
| `GET/PUT` | `/api/branding/tenants/:id` | Dashboard session | Get/update specific |
| `POST` | `/api/branding/preview` | Dashboard session | Validate + return CSS tanpa save |
| `GET` | `/v1/portal/branding` | **None (public)** | Branding publik (dipakai [§F-8](#f-8-public-usage-portal)) |
| `GET` | `/branding/assets/*` | **None (public)** | Serve logo/favicon |

> `/v1/portal/branding` **hanya** field non-sensitif (nama, logo, palet). Jangan ekspos `custom_css` mentah ke publik tanpa sanitasi.

#### 15.7 Backend (Go)

```
internal/branding/
  branding.go       # Core types, defaults, validation
  store.go          # JSON file CRUD, atomic writes, file watching
  css.go            # Palette → CSS custom property generation
  handler.go        # Chi route handlers
  branding_test.go
```

`generated_css` dihitung **server-side** (frontend tak perlu shade-generation logic):

```json
{ "...": "...", "generated_css": ":root { --color-brand-500: #6a7450; ... }" }
```

#### 15.8 Testing

| Test | Target |
|------|--------|
| `TestBranding_DefaultFallback` | tanpa file → default config |
| `TestBranding_CSSGeneration` | preset → CSS vars lengkap 50–900 |
| `TestBranding_CustomOverride` | `palette.custom` menimpa preset |
| `TestBranding_HostResolution` | Host header → tenant config |
| `TestBranding_PortalNoSensitive` | `/v1/portal/branding` tak memuat `custom_css` |
| `TestBranding_AtomicWrite` | write gagal tak corrupt file |

---

---

## 3. Rekomendasi Roadmap

Skenario 9router-go = **jual/berbagi key dengan kontrol** (resale gateway):

```
Fase 1 — Control plane (saling bergantung):
  §1.1 Rate limit ─┐
  §1.2 Budget      ├─→ §1.3 Plans (objek payung)
  §1.7 Model access┘

Fase 2 — Security at-rest:
  §1.6 Argon2 key hashing  (sebelum vault, karena lookup path)
  §1.5 Credential vault     (master_key + migrasi plaintext→encrypted)

Fase 3 — Distribution & visibility:
  §1.8 Public usage portal
  §1.10 Prometheus metrics
  §1.13 Terse serialization (setelah verifikasi coverage)

Fase 4 — Governance safety (opsional, berat):
  §1.4 Guardrails MVP → full

Tunda / Skip:
  §1.9 semantic cache (exact-match saja), §1.11 tray, §1.12 multi-tenant
```

**Alasan pengelompokan**: Rate limit, budget, plans, dan model-access di KeiRouter **satu objek `plans`**. Memecahnya jadi 4 PR terpisah = duplikasi migrasi + inkonsistensi enforcement.

---


---

## 4. Risiko Porting Lintas-Kodebase

| Risiko | Detail | Mitigasi |
|--------|--------|----------|
| **Stack frontend beda** | KeiRouter React/TanStack; 9router-go Svelte 5 | Port **logika + API**, bukan komponen. Tulis ulang view di Svelte. |
| **Skema DB beda** | KeiRouter punya `tenants/plans/budgets/guardrail_*`; 9router-go skema upstream-v0.5.85 (additive-only) | Tabel baru Go-only; jangan ubah tabel upstream. `EnsureCoreSchema`-style idempotent. |
| **Auth model beda** | KeiRouter argon2 + lookup index; 9router-go plaintext `WHERE key=?` | Ganti `db/apikeys.go` + middleware; pertahankan `apiKeys.key` untuk kompat read, tambah kolom hash. |
| **Budget float vs micros** | 9router-go `usageHistory.cost REAL`; KeiRouter `limit_micros INTEGER` | Simpan budget sebagai **micros** (integer) untuk presisi; konversi dari REAL di meter. |
| **Guardrails di stream** | 9router-go SSE translator (claude/openai) punya framing sendiri | Interceptor perlu tap **dua** jalur (JSON + per-event SSE), bukan cuma body mentah. |
| **Vault = single point of failure** | Hilang `master_key` → kredensial hilang | Fallback read plaintext (selama migrasi) + docs recovery; jangan langsung matikan plaintext path. |
| **License** | KeiRouter MIT | Compatible — kreditasi sumber saat port. |

---


---

## 5. Ringkasan Skor

| # | Fitur | Value | Effort | Verdict |
|---|-------|-------|--------|---------|
| 1 | Rate limiting per-key | Tinggi | Rendah–Sedang | **Ambil** |
| 2 | Budget engine (USD/token) | Tinggi | Sedang | **Ambil** |
| 3 | Plans (template) | Tinggi | Sedang | **Ambil** (payung #1/#2/#7) |
| 4 | Guardrails MVP (pii+inj) | Sedang–Tinggi | Sedang | **Ambil** (MVP dulu) |
| 5 | Credential vault AES-GCM | Tinggi (keamanan) | Sedang–Tinggi | **Ambil** (hati-hati migrasi) |
| 6 | Argon2 key hashing | Sedang–Tinggi | Sedang | **Ambil** |
| 7 | Per-key model access | Tinggi | Rendah | **Ambil** (= combo-scope) |
| 8 | Guardrails full | Sedang | Tinggi | **Tunda** |
| 9 | Usage portal publik | Sedang | Sedang | **Ambil** |
| 10 | Prometheus metrics | Sedang | Rendah | **Ambil** |
| 11 | Multi-tenant | Sedang (kontekstual) | Tinggi | **Tunda** |
| 12 | Semantic cache | Rendah–Sedang | Tinggi | **Skip** (exact-match saja) |
| 13 | System tray | Rendah | Sedang | **Skip** |
| 14 | Terse serialization | Rendah | Rendah | **Cek cakupan** |

---


---

## 6. Konvensi Bersama (berlaku untuk semua port)

- **DB**: semua skema baru lewat pola `EnsureCoreSchema`/`EnsureAdditiveColumns` (`internal/db/schema.go`). `CREATE TABLE IF NOT EXISTS` + `ALTER ... ADD COLUMN` guarded, idempoten, jalan tiap startup.
- **Tenant**: kolom `tenant_id TEXT NOT NULL DEFAULT 'default'` tetap ditulis walau single-tenant sekarang, supaya `11-multi-tenant` tak perlu migrasi ulang.
- **Waktu**: string RFC3339 UTC di kolom `TEXT` (konsisten `createdAt`/`updatedAt` upstream).
- **Uang**: **micros integer** (`limit_micros`, `cost_micros`), bukan float. Konversi dari `usageHistory.cost` di meter.
- **Frontend**: port logika + API, bukan komponen React. Tulis view Svelte 5 mengikuti pola `SettingsView.svelte`/`ApiKeysView.svelte`.
- **Auth boundary**: engine routes di bawah `middleware.RequireApiKey`; dashboard di bawah `RequireDashboardAuth` (`internal/handlers/router.go`).
- **License**: sumber KeiRouter MIT — kreditasi saat port.

---


---

## 7. Catatan Spec Mentah

- `14-api-key-timelimit-and-combo-scope.md` (dulu di `docs/keirouter-port-specs/`, kini digabung ke [§F-14](#f-14-api-key-time-limit--resale-metadata)) — **bundle**; bagian rate-limit/plans/combo-scope/hashing didelegasikan ke [§F-1](#f-1-rate-limiting-per-key-rpm--tpm--concurrency)/[§F-3](#f-3-plans-template-policy-reusable)/[§F-7](#f-7-per-key-model-access-allowlist--wildcard--combo-scope)/[§F-6](#f-6-api-key-argon2id-hashing). Yang unik & tetap PR tersendiri: `expiresAt`/`usedCount`/`metadata` resale.
- `15-branding.md` (dulu di `docs/keirouter-port-specs/`, kini digabung ke [§F-15](#f-15-branding-white-label)) — persistensi **file JSON** (`DATA_DIR/branding/`), bukan SQLite, jadi tidak melanggar aturan additive-only; `08-usage-portal.md` bergantung padanya (`/v1/portal/branding`, [§F-8](#f-8-public-usage-portal)).
- Bila [§F-6](#f-6-api-key-argon2id-hashing) (argon2) diambil, `14` mencatat: lookup `WHERE lookupHash=?`, dan "reveal key" → **Regenerate**.
- `expiresAt` (kontrak mutlak) bukan fitur KeiRouter — KeiRouter pakai `period` reset rolling. Keduanya boleh berdampingan untuk jual langganan.

---
