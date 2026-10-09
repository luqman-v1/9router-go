### 🔍 feat(dashboard): atributkan model pada lastError koneksi dan redam visual saat testStatus aktif (#226)

- **Latar belakang**: ketika model tertentu gagal diuji atau gagal dipanggil (misal `401 Model ... is not supported`),
  `RecordConnectionError` menulis string error tanpa atribut model. Di dashboard, baris koneksi menampilkan
  badge `Active` di samping teks error merah mencolok tanpa kejelasan model mana yang gagal atau apakah
  error tersebut hanya berlaku per-model.
- **Fiks**: `RecordConnectionScopedError` di layer database kini menyimpan `model` dan `source` pada objek
  `lastError`. `GET /api/connections` memancarkan `lastErrorModel` dan `lastErrorSource`. Pada baris koneksi
  dashboard, error model-scoped diberi tag `[model]` dan diwarnai mengikuti verdict badge baris tersebut —
  merah bila badge `error`, muted bila `active` — sehingga error per-model tidak lagi terbaca sebagai
  kerusakan kredensial menyeluruh, tanpa meredam alarm saat probe benar-benar gagal.
- **Verifikasi**: `go test ./internal/db/... ./internal/handlers/{dashboard,chat}/...`, `bun test src scripts`
  (321 pass), `bun run ratchet:svelte` (0 unresolved, 83 = baseline), dan pemeriksaan DOM di dashboard:
  baris dengan probe gagal tetap merah di kedua lokasi tampilan, sedangkan baris `active` meredam.