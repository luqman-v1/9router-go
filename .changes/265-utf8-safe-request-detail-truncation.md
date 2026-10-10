### 🐛 fix(usage): pemotongan request-detail tidak lagi memecah rune UTF-8

- **Latar belakang**: baris detail request dipotong per-byte (`content[:MaxMessageContentLen]`,
  `[:MaxResponseContentLen]`, `[:maxPersistedErrorLen]`) lalu di-marshal dengan `encoding/json/v2`.
  Pemotongan per-byte bisa jatuh di tengah rune 2/3/4-byte (CJK, aksen latin-1, emoji) sehingga
  menyisakan fragmen bukan UTF-8. Berbeda dengan `encoding/json` v1 yang menggantinya dengan U+FFFD,
  v2 **menolak** string tersebut, jadi satu request dengan teks CJK/emoji yang melewati batas menghasilkan
  `usage marshal request detail failed error=jsontext: invalid UTF-8 within "/request/messages/N/content"`.
- **Dampak**: `logUsage` return duluan setelah marshal gagal — sebelum `upsertDailyUsage` dan
  `PushRecent`. Request tetap berbillet normal (`usageHistory` ditulis lebih dulu dan baris
  `usage logged` tetap tercetak, status 200), tetapi tidak masuk agregat harian, tidak masuk
  recent-requests tracker, dan tidak ada row di tab Details — cukup satu karakter yang memotong batas.
- **Fiks**: helper `truncateDetailText` dipakai di keempat titik potong. `strings.ContainsFunc`
  mendeteksi byte rusak lebih dulu — jalur yang tidak perlu perbaikan mengembalikan string asli,
  sehingga tetap 0 allocs/op terukur; hanya jalur yang terpotong atau memang tidak valid yang
  memanggil `strings.ToValidUTF8`. Byte yang sudah rusak sebelum dipotong ikut dibersihkan,
  karena byte rusak sama saja membuat marshal gagal.
- **Verifikasi**: `go test ./...` (3897 pass, 48 paket), `go test -race ./internal/handlers/chat/`,
  `go test -tags=integration ./internal/integration/`, `go vet`, `make build`, dan CI PR
  (test, race detector, integration tests, docker build) hijau.
  Subtest `rune straddling the byte limit stays marshalable` menguji 3-byte CJK, 2-byte latin-1
  supplement, dan 4-byte emoji lalu benar-benar memarshal payload-nya.
  Dibuktikan menangkap bug: dijalankan terhadap `usage.go` pra-fix, subtest CJK gagal dengan
  `jsontext: invalid UTF-8 within "/messages/0/co...` — pesan yang sama dengan log produksi.
