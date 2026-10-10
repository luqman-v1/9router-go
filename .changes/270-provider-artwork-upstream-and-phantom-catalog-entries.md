### 🖼️ Aset provider upstream diambil, kartu "header semu" dihapus — 404 `/providers/*.png` hilang total

- **Aset dari upstream lengkap.** `decolua/9router` sudah shipping 9 berkas yang repo ini
  tidak punya: `bedrock.png`, `bedrock-xai.png`, `codewhale.svg`, `crush.png`,
  `forge.png`, `pi.svg`, `smelt.svg`, plus `hermes.png` dan `omp.png` yang di-refresh ke
  versi upstream. Kesembilananya identik dengan `public/providers/` upstream
  (`raw.githubusercontent.com/decolua/9router/master`), kecuali tiga SVG yang hanya
  berbeda line-ending CRLF↔LF. Seluruh aset upstream yang sudah ada di sini juga
  dibandingkan ulang satu per satu — tidak ada yang berubah diam-diam.
- **Akar 404 sisanya bukan aset yang hilang, tapi katalog yang bohong.** Delapan entri
  di `web/src/lib/providers.ts` — `x-codebuddy-request`, `x-github-api-version`,
  `x-requested-with`, `x-vscode-user-agent-library-version`, `anthropic-version`,
  `openai-intent`, `originator`, `user-agent` — bukan provider. Mereka hanya
  cerminan `StaticHeaders` backend. Sejak `afcab2ed`
  (17 Sep 2026) masuk sebagai kartu. Akibatnya setiap surface yang memanggil
  `getIconPath` meminta `/providers/user-agent.png` yang memang tidak pernah ada: satu
  404 per entri per render, plus **delapan kartu provider palsu** yang connect-nya
  mustahil (`resolution.go:311` — tidak ada model yang me-resolve ke sana).
- **Catatan lama "jangan dihapus tanpa mengganti UI custom-header" ternyata usang.**
  Baseline `ProviderHeaderOverridesModal` dibaca dari
  `GET /api/providers/{id}/overrides` → `builtinProviderHeaders` → `cfg.StaticHeaders`
  (`internal/handlers/dashboard/provider_overrides.go`), bukan dari katalog. Delapan
  entri itu tidak pernah menyentuh UI mana pun: satu `grep` di `web/src` hanya
  menemukan nama mereka sendiri.
- **`zai-search` diperbaiki, bukan dihapus.** Ini satu-satunya entri Go tanpa
  pasangan upstream yang **nyata** (`KnownProviders["zai-search"]` →
  `https://api.z.ai/api/mcp/web_search`, dan `CredentialFallbacks` memetakan
  kuncinya ke `glm`). Entri di katalog hanya placeholder tanpa nama maupun display,
  jadi kartunya kosong dan logonya 404. Sekarang jadi entri web-search yang utuh —
  "Z.ai Web Search", warna & ikon GLM, notice yang menyebut API key GLM yang dipakai
  ulang — dan `getIconPath` memetakannya ke `glm.png` lewat `ICON_ALIASES` (tabel
  yang sama dengan `ICON_ALIASES` upstream). Upstream meng-*alias* `minimax-code`,
  `opencode-zen`, `ollama-search`, `perplexity-agent`, `vercel-ai-gateway` juga, tapi
  repo ini sudah punya logo berbeda untuk kelimanya — meng-*alias*-nya di sini hanya
  membuang logo asli.
- **`503 Service Unavailable` pada `providers:` dijelaskan.** Itu bukan route server.
  `web/public/sw.js` menjawab **503 "Network error or server daemon offline"** dari
  `fetch(...).catch(...)` setiap request yang gagal — termasuk navigasi `/providers`
  dan `/dashboard/providers` saat daemon mati atau sedang restart. Chrome menamai
  lokasi (`providers:`), bukan URL, saat resource di luar `<img>`/`<script>` gagal.
  Tidak ada handler 503 di `internal/handlers/**`; `GET /providers` dan `/providers/`
  menjawab `200 text/html` dari SPA shell, dan cache SW-nya kosong
  (`caches.open('9router-go-static-v1').keys()` → 0 entri).
- **Verifikasi.** (1) Regresi baru di `web/src/lib/providers.test.ts` membaca direktori
  aset dan gagal pada entri tanpa logo — terbukti gagal sebelum fix (`ERR_ASSERTION`
  saat `phantom-test-id` disisipkan) dan lolos sesudahnya; plus tabel `getIconPath` di
  `types.test.ts`. (2) Semua **141** URL artwork yang bisa dihasilkan SPA dijawab `200`
  oleh binary hasil build baru. (3) Chromium headless: `/dashboard/providers` dengan
  "Show all" diklik → **114 request, 0 non-2xx, 0 pesan konsol 404/503**; eager-load
  memverifikasi **95 `<img>`, `naturalWidth > 0` untuk semuanya**. (4) Binary
  **sebelum** fix pada port yang sama: 7 PNG rusak (`user-agent`, `originator`,
  `openai-intent`, `x-codebuddy-request`, `x-github-api-version`, `x-requested-with`,
  `x-vscode-user-agent-library-version`) — 7 dari 8 yang dilaporkan user
  (`anthropic-version` tidak terlihat karena layar pertama belum memuat seluruh grid
  API-Key). (5) `bun test src scripts` 351 pass / 0 fail, `bun run lint` bersih,
  `make vet-svelte` 0 unresolved & 83 error = baseline,
  `go test ./internal/providers/... ./internal/handlers/dashboard/...` hijau. Halaman
  detail `/dashboard/providers/zai-search` dirender dengan logo GLM.