### Fixes

- **Cache Analytics dan Compression Analytics mengabaikan pilihan rentang
  waktu.** Dua section itu sudah punya pemilih periode di header, tapi tidak
  satupun angkanya yang mengikutinya.

  **Cache Analytics** tidak punya window sama sekali di totals-nya:
  `GetPromptCacheMetrics` menjalankan `SELECT … FROM usageHistory` tanpa
  `WHERE` untuk kartu KPI, `byProvider` dan `byModel`, jadi "Last 24 hours"
  menampilkan cache rate, cached tokens dan cost saved dari **seluruh** ledger.
  Satu-satunya yang dibatasi adalah `GetPromptCacheTrend`, lewat parameter
  `trendHours` sendiri — jadi kartu di atas grafik dan grafiknya menggambarkan
  periode berbeda pada satu layar.

  **Compression Analytics** lebih halus: endpoint-nya menerima `since`, dan
  scalars/`byMode`/trend/top-savers memang memakainya, tapi tiga query punya
  cutoff-nya sendiri — branch `usageHistory`, breakdown per provider/model, dan
  agregat dolar per pasangan `(provider, model)`. Saat `since` menentukan
  fallback (`GetCompressionAnalyticsSummary` memakai `cutoff == ""` untuk
  `all`), seluruh statement itu kehilangan batasnya dan membaca tabel penuh,
  sementara scalars di sebelahnya tetap terbatasi. Pilihan "All time" pada
  dropdown karena itu mengembalikan grafik 24 jam di atas angka all-time.

  Sekarang **satu window** mengikat kartu, breakdown dan grafik di kedua
  section, dan keduanya memakai kosakata periode yang sama dengan Overview
  (`today`, `24h`, `7d`, `30d`, `60d`, `all`, plus `<n>d`/`<n>h`) lewat
  `internal/analyticsrange`, bukan dua dropdown dengan dua daftar berbeda.
  `trendHours` dan `since` tetap diterima sebagai fallback supaya SPA lama dan
  tautan tersimpan tidak ikut membaca window yang salah.

  `last24h` diganti jadi `trend`, karena seri itu sekarang mengikuti window yang
  dipilih dan nama lamanya akan berbohong setiap kali window-nya bukan 24 jam.
  Untuk window tanpa batas server tidak mengirim grafik sama sekali — satu
  bucket per jam sejak ledger dimulai tidak bisa dirender, dan grafik periode
  lain di samping kartu yang lebih jujur adalah dua kesalahan, bukan satu.

  **Breakdown juga dibatasi** ke 50 baris terbesar per provider/model, diurut
  berdasarkan token tersimpan. Tanpa itu satu ledger yang sudah melihat ribuan
  string model menghasilkan respons yang tidak bisa dirender tabel mana pun.
  Response melaporkan `truncatedProviders`/`truncatedModels` dan dashboard
  menampilkan "Top 50 of 80 models", jadi tabel yang terpotong tidak pernah
  terbaca sebagai tabel utuh.

  **Indeks untuk `compressionAnalytics` diganti.** Di ledger 300.000 baris,
  limiter window ternyata **bukan** penyebab lamanya — `all` justru terukur
  lebih cepat dari `24h`, yang tidak mungkin kalau window yang jadi biaya.
  Penyebabnya indeks yang tidak menutup query berat: SQLite memakai
  `idx_ca_ts` untuk mencari rentang `timestamp`, lalu karena `timestamp` bukan
  rowid, ia kembali ke tabel untuk setiap kolom yang dibaca — dua lookup per
  baris. Empat indeks baru, semuanya bentuknya dipaksa hasil `EXPLAIN QUERY
  PLAN` dan bukan tebakan:

  - `idx_ca_stat_p/m (provider|model, timestamp, tokensSaved, originalTokens,
    durationMs)` — **group key di depan, bukan timestamp**. Index yang diawali
    timestamp hanya bisa *seek* pada range, dan sort untuk `ORDER BY` tetap
    butuh temp B-tree. Dengan group key di depan, window tetap terfilter,
    group sudah terklaster, dan kolom yang dibaca ikut ter-cover sehingga pass
    ini index-only: breakdown model **1659 ms → 50 ms**, provider **351 → 37 ms**.
  - `idx_ca_saved (tokensSaved, timestamp)` — top-10 savers mengurutkan seluruh
    window untuk mengembalikan sepuluh baris: **971 ms → <1 ms**, dengan 10 baris
    yang identik dengan hasil sort.
  - `idx_ca_receipt (timestamp, actualPromptTokens, actualTotalTokens)` —
    agregat receipts memfilter dua kolom tapi tidak membacanya, jadi indeks
    timestamp mengirimnya ke tabel sekali per baris: **940 ms → 17 ms**.

  `idx_ca_ts_prov` dan `idx_ca_ts_model` — composite yang diawali timestamp —
  **dihapus di bootstrap**. Tidak muncul di satu pun plan, tapi tetap di-update
  di setiap insert. `idx_ca_provider` dan `idx_ca_model` juga dilepas karena
  digantikan `idx_ca_stat_p/m`.

  **Penghitungan truncation tidak lagi jalan di setiap request.** `droppedGroups`
  hanya menghitung saat breakdown benar-benar kena batas; sebelumnya setiap
  request membayar satu pass grup tambahan untuk angka yang hanya dibaca kalau
  ada yang terpotong.

  **Yang sengaja tidak diperbaiki.** Fold `usageHistory` untuk Cache Analytics
  tetap ~1.4 s. DiUkur: `SELECT id` 71 ms tapi `SELECT tokens` 1170 ms pada baris
  yang sama — biayanya di kolom TEXT `tokens`, bukan di `json_extract`. Indeks
  `(timestamp, tokens)` diuji dan SQLite **tidak memilihnya** (plan tetap
  `idx_uh_ts`), jadi tidak ada yang hilang dengan tidak mengambilnya; DB juga
  tumbuh 236 → 293 MB. Memperbaikinya berarti denormalisasi kolom `tokens` ke
  tabel lain atau cache delta seperti yang sudah dipakai
  `/api/usage/stats` — keduanya pekerjaan sendiri, bukan perbaikan sepihak
  dalam PR range.

  **Angka sebelum/sesudah** (300.000 baris, indeks identik dengan produksi):
  total ketujuh statement analytics **6453 ms → 1685 ms**. End-to-end lewat
  HTTP di binary branch ini, port 20301: Compression `24h` **9.7 s → 4.3 s**,
  `7d` **19.4 s → 8.5 s**, `30d` 19.4 s → 8.6 s, `all` 3.9 s → 2.3 s. Cache
  `24h` 4.3 s, `7d` 8.5 s, `all` 1.7 s dengan trend kosong. `trendReqs` sama
  dengan `totalRequests` di setiap window.

  **Verification.** `internal/analyticsrange` (baru, dengan tabel window),
  `TestGetPromptCacheMetrics_HonoursWindow`,
  `TestGetPromptCacheTrend_UsesTheGivenWindow`,
  `TestGetPromptCacheMetrics_ReportsTruncation`,
  `TestCompressionAnalytics_SummaryHonoursWindow`,
  `TestCompressionAnalytics_BackfillHonoursWindow` dan
  `TestHandleGetCache_PeriodBoundsTotalsAndTrend`. Dua yang pertama
  dikonfirmasi gagal dengan perbaikan dibatalkan — satu melaporkan 4 request
  untuk window 24h yang hanya memuat 2, satu melaporkan 3 bucket trend untuk
  window all-time yang harusnya tidak punya.

  Angka di atas diambil dari ledger 300.000 baris dengan indeks yang disalin
  verbatim dari `schema.go`. Harness-nya ada di `.bench/` dan sengaja tidak
  masuk commit: yang di-commit adalah angkanya dan nama test-nya, bukan alat
  ukurnya. `go vet ./...` bersih, `go test ./internal/...` hijau, `bun test`
  392/392, `bun run build`, `make vet-svelte` (0 unresolved, 83 = baseline).