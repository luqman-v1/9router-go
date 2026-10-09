### ✨ feat(cli-tools): kartu Pi dan Oh My Pi di halaman CLI Tools — Closes #165

- **Latar belakang**: halaman CLI Tools hanya memuat 22 tool. `pi` (pi.dev) dan `omp`
  (Oh My Pi) tidak ada di katalog maupun di peta status, padahal keduanya sudah bisa
  memakai 9router-go sebagai gateway — keduanya hanya butuh satu blok provider yang
  menunjuk `baseUrl` ke `{origin}/v1`. Pengguna harus mencari sendiri snippet-nya di
  komentar issue.
- **Backend**: `internal/handlers/media/clitools.go` menambahkan dua `toolDef` — `pi`
  (bin `pi`) dan `omp` (bin `omp`, plus `--version` seperti `devin`, jadi kartu
  menampilkan nomor versi). `TestCLIDetectors_HasAllToolIDs` ikut diperbarui;
  tes itu mengunci set id, jadi penambahan kartu tanpa detektor gagal di suite Go
  alih-alih diam-diam di browser.
- **Frontend**: dua kartu baru di `CliToolsView.svelte` dengan `configType: 'guide'`,
  karena kedua agen dikonfigurasi lewat file, bukan env var. Snippet-nya diinterpolasi
  dari origin hidup dan API key aktif: JSON `models.json` untuk Pi, YAML `models.yml`
  untuk Oh My Pi (path default `~/.omp/agent/models.yml`, dengan catatan direktori
  profil `~/.omp/profiles/<name>/agent/`). Keduanya menyebut fallback
  `openai-responses` karena gateway menyajikan dua lane.
- **Detail kecil**: kartu "Oh My Pi" tanpa perubahan lain akan memakai monogram
  `name.slice(0, 2)` → "OH". `ToolItem` sekarang punya `abbr` opsional dan sebuah
  helper `initials()`, jadi kartu tersebut terbaca "OMP" di grid maupun di modal.
- **Verifikasi**: `web/e2e/cliToolsPi.test.ts` baru menjalankan gateway sungguhan dan
  memeriksa (a) id `pi`/`omp` benar-benar ada di payload `all-statuses` dengan
  `installed` bertipe boolean — bukan `null`, yang akan membuat badge jatuh ke
  "Guide", (b) teks modal memuat path konfigurasi yang benar, `{origin}/v1`, kedua
  nilai `api`, dan tidak pernah memuat fallback SSR `localhost:20130` atau
  `<your-api-key>`, (c) monogram Oh My Pi = "OMP". Diuji gagal sebelum fix (2 dari 5
  gagal saat `clitools.go` dikembalikan ke keadaan semula) dan lolos sesudahnya.
  `go test ./internal/handlers/...` (1528 pass), `bun test` (351 pass), `bun run build`
  dan `make vet-svelte` (0 unresolved, 83 error = baseline) hijau. Halaman juga dicek
  langsung di Chromium terhadap binary yang baru dibangun.