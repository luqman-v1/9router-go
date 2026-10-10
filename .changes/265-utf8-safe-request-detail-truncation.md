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
- **Fiks**: helper `truncateDetailText` dipakai di keempat titik potong; `strings.ToValidUTF8`
  membuang byte menggantung yang justru dibaca v2 sebagai data rusak, dan no-op untuk input valid.
- **Verifikasi**: `go test ./internal/handlers/chat/...` (778 pass), `go test ./...` (3886 pass),
  `go vet`. Subtest baru `rune straddling the byte limit stays marshalable` menguji 3-byte CJK,
  2-byte latin-1 supplement, dan 4-byte emoji, lalu benar-benar memarshal payload-nya.
  Dibuktikan menangkap bug: dijalankan terhadap `usage.go` pra-fix, subtest CJK gagal dengan
  `jsontext: invalid UTF-8 within "/messages/0/co...` — pesan yang sama dengan log produksi.
