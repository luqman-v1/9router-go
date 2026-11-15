# Antigravity Streaming Stutter — Akar Masalah & Rencana Optimasi

Tanggal: 2026-11-15 · Model diuji: `ag/gemini-3.8-flash-high` · Gateway: 9router-go @ `921db441b`

## Gejala

Render streaming di UI klien terasa "patah-patah" (teks melonjat per blok besar, bukan mengalir), padahal metrik klien menampilkan ~45 tok/s.

## Before (hasil pengukuran, 7 run)

| Metrik | Nilai terukur |
|---|---|
| TTFT (request → first byte) | **2.7–3.7 s** |
| Jumlah SSE chunk untuk output 80–400 kata | **1–3 chunk total** |
| Waktu kedatangan seluruh isi teks | **< 200 ms (one-shot burst)** |
| Gap chunk terakhir → `[DONE]` | +50–200 ms |
| TTFT di log relay (`[usage]` line) | 16–127 ms |
| `latency_ms` di log relay | ~10 s untuk completion 30 token |

**Pembanding penentu** — `kr/claude-opus-5` melalui **path gateway yang identik** (transport, `ScanStream`, `flusher.Flush()` per event):

| Metrik | kiro/claude-opus-5 | ag/gemini-3.8-flash-high |
|---|---|---|
| Chunk rate | ~37 chunk/detik, fine-grained | 1–3 chunk per seluruh respons |
| Pola visual | mengalir halus | jeda 3 s → blob sekaligus → idle |

## Kenapa (akar masalah)

1. **Bukan buffering gateway.** Relay sudah meneruskan per-event: `internal/proxy/executor/gemini.go` memakai `proxy.ScanStream` + `flusher.Flush()` pada setiap chunk yang diterjemahkan. Log relay mencatat `chunks=3` untuk respons 1000+ byte — jumlah event yang diteruskan == jumlah event upstream. Tidak ada agregasi di tengah.
2. **Bukan network buffering perantara** (Cloudflare/reverse proxy dsb.) — jalur ini tanpa perantara, dan claude lewat jalur yang sama tetap halus.
3. **Sumbernya upstream Google Cloud Code (antigravity).** Dengan thinking budget `high`, model menyelesaikan seluruh jawaban terlebih dahulu (TTFT ~3 detik = waktu berpikir), lalu mengirimkan hasilnya sebagai **satu blok besar**. UI menerima: jeda 3 detik → seluruh teks muncul sekaligus → idle sampai stream ditutup.
4. **Angka 45 tok/s adalah ilusi rata-rata.** Klien menghitung `total_token ÷ total_durasi` (30 token ÷ 10 detik ≈ 3 tok/s efektif sesaat, lalu burst ratusan token dalam 0,1 detik di awal). Tok/s efektif saat render: burst besar sekali, sisanya nol.

## After (target)

Idealnya output antigravity tiba sebagai rangkaian chunk bertahap seperti provider fine-grained, sehingga UI merender mengalir.

## Rencana optimasi (saat ketemu / kesempatan)

- **Opsi A — typewriter smoothing di relay (anti-pattern, hindari):** memecah blok besar menjadi pseudo-chunks artifisial. Menambah latensi semu dan menyalahi kontrak passthrough `open-sse`/translator; angka chunk menjadi bohongan bagi klien yang mengukur. Tidak dikerjakan kecuali upstream Google mengubah perilakunya.
- **Opsi B — transparansi di UI klien (direkomendasikan):** tampilkan indikator "thinking…" selama TTFT berjalan, lalu render blob sekaligus dengan animasi typewriter **di sisi renderer** (bukan di jaringan). Ini memindahkan smoothing ke tempat yang benar.
- **Opsi C — pilihan varian model:** `gemini-3.8-flash-medium` / `-low` (thinking budget lebih kecil) memperpendek TTFT dan berpeluang pengiriman bertahap; atau arahkan ke provider yang streaming-nya fine-grained bila kelancaran visual yang diprioritaskan.

## Verifikasi

- Pengukuran: `curl -N` dengan timestamp per baris (`date +%s%N`) di sisi klien, 7 run, model sama, prompt 80–400 kata.
- Log relay: `E:/project/database/9router/run/gateway.log` (`stream ended chunks=... bytesWritten=...` dan `[usage] ... ttft_ms=... latency_ms=...`).
- Buktikan klaim "gateway tidak menggumpalkan" dengan: log `chunks=N` == jumlah `data:` event yang diterima klien untuk stream yang sama.
