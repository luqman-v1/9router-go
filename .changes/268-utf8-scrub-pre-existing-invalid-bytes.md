### 🐛 fix(usage): byte rusak yang sudah ada ikut dibersihkan saat menyimpan request detail

- **Latar belakang**: `truncateDetailText` (diperkenalkan pada #267) mengembalikan string
  apa adanya bila panjangnya di bawah batas, sehingga byte yang sudah rusak sebelum masuk
  helper ikut lolos ke `json.Marshal` dan menggagalkan marshal dengan cara yang sama.
- **Keterjangkauan**: jalur ini terbukti tidak terjangkau pada kode saat ini.
  `ScanStream` hanya menyerahkan `onChunk` event SSE utuh yang dirakit dari baris penuh,
  jadi `ResponseBuf` tidak pernah berisi rune terbelah. Di sisi request,
  `extractRequestMessages` melewati `json.Unmarshal` yang lebih ketat dari marshaler dan
  mengembalikan `nil` saat body memuat byte mentah yang tidak valid. Jalur ini tetap
  ditutup agar perubahan berikutnya yang memperluas isi row tidak memunculkan bug yang sama.
- **Fiks**: `strings.ContainsFunc` mendeteksi byte rusak lebih dulu dan mengembalikan string
  asli ketika tidak ada yang perlu diperbaiki, sehingga jalur umum tetap bebas alokasi
  (terukur 0 allocs/op). Hanya jalur terpotong atau memang tidak valid yang memanggil
  `strings.ToValidUTF8`.
- **Verifikasi**: `TestTruncateDetailText` baru menutup batas rune 2/3/4-byte, byte rusak di
  bawah batas, byte rusak di dalam jendela potong, byte rusak di atas potong, serta teks
  multibyte valid yang harus tetap utuh. Setiap kasus juga memeriksa `utf8.ValidString` dan
  bahwa hasilnya dapat di-marshal. `go test ./...` (3897 pass, 48 paket),
  `go test -race ./internal/handlers/chat/`, `go test -tags=integration ./internal/integration/`,
  `go vet`, dan CI PR (test, race detector, integration tests, docker build) hijau.
