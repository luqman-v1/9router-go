# 9Router (Next.js) vs 9router-go (Go) — Komparasi Feature

> **Konteks:** `htdocs/9router` adalah versi awal (Next.js, dashboard + engine monolitik).
> `9router-go` adalah swap engine ke Go untuk performa (proxy/streaming/SSE).
> Dokumen ini mencatat kesenjangan fitur agar keputusan port bisa diambil.

**Tanggal:** 2026-08-04

---

## 1. Arsitektur

| | Next.js | Go |
|---|---------|-----|
| Routing | Next App Router + Express + `http-proxy-middleware` | chi router |
| SSE/streaming | Node stream + `custom-server.js` (anti-XFF IP) | native, `sse_scanner` + StallReader + eventstream parser |
| SQLite | sql.js (+ better-sqlite3 optional) | `modernc.org/sqlite` (WAL) |
| CLI | `cli/` npm | urfave/cli (`version`, `update`, `mitm`) |
| Auth | login/OIDC/API key | API key middleware (`RequireApiKey`) |

**Kesimpulan:** engine proxy/streaming sudah di-swap penuh ke Go dan setara Next
pada semua format proxy (chat, messages, embeddings, images, audio, videos,
responses, media, model listing).

---

## 2. Per-Endpoint — Proxy/Formats (✅ setara di Go)

| Next route | Go route | Status |
|-----------|----------|--------|
| `v1/chat/completions` | `POST /chat/completions` | ✅ |
| `v1/messages` | `POST /messages` | ✅ |
| `v1/messages/count_tokens` | `POST /messages/count_tokens` | ✅ |
| `v1/embeddings` | `POST /embeddings` | ✅ |
| `v1/responses` | `POST /responses` | ✅ |
| `v1/responses/compact` | `POST /responses/compact` | ✅ |
| `v1/images/generations` | `POST /images/generations` | ✅ |
| `v1/audio/speech` | `POST /audio/speech` | ✅ |
| `v1/audio/transcriptions` | `POST /audio/transcriptions` | ✅ |
| `v1/audio/voices` | `GET /audio/voices` | ✅ |
| `v1/videos/generations\|edits\|extensions` | `POST /videos/*` | ✅ |
| `v1/videos/{id}` | `GET /videos/{id}` | ✅ |
| `v1/search` | `POST /search` | ✅ |
| `v1/scrape` | `POST /scrape` | ✅ |
| `v1/web/fetch` | `POST /web/fetch` | ✅ |
| `v1/models` | `GET /models` | ✅ |
| `v1/models/info` | `GET /models/info` | ✅ |
| `v1/models/[kind]` | `GET /models/{kind}` | ✅ |
| `v1beta/models` + `[...path]` | — | ❌ **Gap** |
| `v1/api/chat` (Ollama) | `POST /api/chat` | ✅ |

Catatan: Next pakai prefix `/api/v1/...`, Go di root `/...`. Hanya `v1beta/models/{path}` yang belum ada.

---

## 3. Per-Endpoint — Dashboard/Admin (⚠️ GAP besar di Go)

| Next route | Go | Prioritas |
|-----------|-----|-----------|
| `translator/*` (translate, save, load, send, console-logs + SSE stream) | ❌ | 🔴 engine-feature |
| `mcp/[plugin]/message`, `/sse` | ❌ | 🔴 engine-feature (standalone ISV, bukan tool-name saja) |
| `pxpipe/*` (daemon status/start/stop/logs/stats) | ❌ | 🟠 infra |
| `tunnel/*` (cloudflare + tailscale enable/disable/status/check/install) | ❌ | 🟠 infra |
| `headroom/*` (start/stop/restart/status/proxy/extras) | ⚠️ comment saja | 🟡 |
| `cli-tools/*` (18 tools: codex/copilot/cline/opencode/etc + all-statuses) | ❌ | 🟡 |
| `oauth/...` (gitlab PAT, iflow cookie, cursor import, kiro api-key, codex import-token) | ⚠️ kiro social + codex bulk saja | 🟡 |
| `providers/*` (CRUD, client, validate, test-batch, suggested-models, kilo free-models, test-models) | ⚠️ db-layer ada, handler HTTP kurang | 🟠 |
| `proxy-pools/*` (CRUD + vercel/deno/cloudflare deploy + test) | ⚠️ db-layer ada | 🟠 |
| `combos/*` (CRUD + kind) | ⚠️ db-layer ada | 🟠 |
| `keys/*` (CRUD) | ⚠️ auth middleware saja | 🟠 |
| `settings/*` (GET/PATCH, database, proxy-test, require-login) | ⚠️ `GetSettings` dipakai, handler HTTP kurang | 🟠 |
| `usage/*` (chart, history, logs, providers, stats, stream, request-details/logs) | ⚠️ db-layer ada, endpoint HTTP kurang | 🟠 |
| `auth/*` (login, logout, oidc, reset-password) | ❌ | 🟡 |
| `pricing`, `tags`, `init`, `locale`, `shutdown`, `version/shutdown` | ❌ | 🟡 |

---

## 4. Database Schema — Kompatibel ~95%

| Table | Next | Go | Kolom sama? |
|-------|------|----|-------------|
| `providerConnections` | ✅ | ✅ | ✅ + Go tambah `lastUsedAt`, `consecutiveUseCount` |
| `providerNodes` | ✅ | ✅ | ✅ |
| `proxyPools` | ✅ | ✅ | ✅ |
| `apiKeys` | ✅ | ✅ | ✅ |
| `combos` | ✅ | ✅ | ✅ |
| `kv` | ✅ | ✅ | ✅ |
| `usageHistory` | ✅ | ✅ | ✅ (12 kolom identik) |
| `usageDaily` | ✅ | ✅ | ✅ |
| `requestDetails` | ✅ | ✅ | ✅ |
| `settings` | ✅ | ✅ | ✅ (`data` TEXT JSON blob) |
| `_meta` | ✅ | ✅ | ✅ |

**Kesimpulan:** Kolom & tipe 1:1, plus 2 kolom Go-only di `providerConnections`.
Dashboard Next bisa langsung baca DB Go tanpa migrasi. `_meta` di Go dipakai untuk
versioning (setara `migrate.js`).

---

## 5. Kesimpulan & Keputusan

### Asumsi kunci (dari pemilik repo, 2026-08-04)
> **9router-go TIDAK butuh dashboard.** Next.js tetap jadi dashboard (UI) dan
> membaca SQLite yang sama. Go hanya engine proxy/streaming di belakangnya.

Konsekuensi:
- **Jangan buat UI/handler CRUD di Go.** CRUD providers, keys, combos,
  proxy-pools, settings, usage = read/write DB → dashboard Next baca langsung
  dari SQLite bersama. Handler HTTP untuk ini di Go **tidak perlu**.
- Yang wajib ada di Go = **behavior yang dieksekusi engine** dan dipicu/ditampilkan
  dashboard, yang sekarang hidup di Next dan akan hilang saat engine pindah.

**Sudah di-swap penuh (jangan sentuh):** semua format proxy, SSE/streaming,
model listing, combos, oauth import, token saver, usage tracking, version/update.

### Fitur yang WAJIB di-port ke Go (berefek UI dashboard, behavior engine)
| Fitur | Next route (dashboard picu) | Efek di dashboard |
|-------|------------------------------|-------------------|
| **Proxy-pools deploy** | `proxy-pools/vercel\|deno\|cloudflare-deploy` | Tombol "Deploy" |
| **Headroom** | `headroom/start\|stop\|restart\|status\|proxy\|extras` | Control lifecycle |
| **PxPipe** | `pxpipe/*` (status, stats, logs, start/stop/restart) | Status daemon |
| **Tunnel** | `tunnel/*` (cloudflare/tailscale enable/disable/status/check/install) | Status koneksi |
| **MCP server** | `mcp/[plugin]/message\|sse` + registry | Tools + plugin |
| **Media TTS voices** | `media-providers/tts/*/voices` (elevenlabs/deepgram/minimax/inworld) | Daftar suara TTS |
| **CLI-tools status** | `cli-tools/all-statuses` | Status per-CLI (codex/copilot/...) |

> *Translator console* (`translator/translate`, `console-logs`/SSE) termasuk
> engine-behavior untuk tool-call/stream-translation — pertimbangkan apakah
> dashboard mengkonsumsinya, karena sisanya CRUD DB bisa langsung dibaca.

### Gap yang TIDAK perlu di-port (baca DB langsung oleh dashboard)
- `providers` (list/CRUD), `proxy-pools` (CRUD non-deploy), `combos`,
  `keys`, `settings`, `usage` (read), `pricing`, `tags`, `models/custom/
  disabled/alias` — semua operasi DB pada SQLite bersama.

### Rencana prioritas (revisi)
- **P1:** Proxy-pools deploy (vercel/deno/cloudflare) — tombol dashboard
- **P2:** Media TTS voices + headroom
- **P3:** PxPipe, tunnel, MCP server (butuh desain daemon)

---

## Lampiran: Referensi file

| Repo | Path |
|------|------|
| Go routes | `internal/handlers/router.go` |
| Go server | `cmd/9router-go/main.go` |
| Go DB schema | `DATABASE.md` |
| Next routes | `src/app/api/**/route.js` |
| Next DB schema | `src/lib/db/schema.js` |
| Next server wrap | `custom-server.js` |
