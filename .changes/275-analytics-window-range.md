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

  **Verification.** `internal/analyticsrange` (baru, dengan tabel window),
  `TestGetPromptCacheMetrics_HonoursWindow`,
  `TestGetPromptCacheTrend_UsesTheGivenWindow`,
  `TestGetPromptCacheMetrics_ReportsTruncation`,
  `TestCompressionAnalytics_SummaryHonoursWindow`,
  `TestCompressionAnalytics_BackfillHonoursWindow` dan
  `TestHandleGetCache_PeriodBoundsTotalsAndTrend`. Dua yang pertama
  dikonfirmasi gagal dengan perbaikan dibatalkan — satu melaporkan 4 request
  untuk window 24h yang hanya memuat 2, satu melaporkan 3 bucket trend untuk
  window all-time yang harusnya tidak punya. Live di binary yang dibangun dari
  branch ini, port 20299, DB dengan 4000 baris `usageHistory` dan 4000 baris
  `compressionAnalytics`: `period=24h` → 1794 request, `7d` → 2909, `30d` →
  3620, `all` → 4000, dan `trendReqs` sama dengan `totalRequests` di setiap
  window. `go vet ./...` bersih, `go test ./internal/...` hijau, `bun test`
  392/392, `bun run build` dan `make vet-svelte` (0 unresolved, 83 = baseline).