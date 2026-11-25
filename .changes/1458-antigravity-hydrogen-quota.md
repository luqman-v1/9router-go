### Fixed

- Antigravity quota tracker: tampilkan varian `gemini-4-hydrogen-*` (xhigh/high/medium/low) yang sebelumnya tersaring dari daftar model penting, plus `gemini-4-neon-xhigh` dan tier `max` Claude 5.5, mengikuti upstream.
- Koneksi `agy-acp` sekarang memakai RPC kuota Google yang sama dengan `antigravity` (token sendiri atau dari koneksi sumber), sehingga halaman tracker ACP menampilkan baris kuota per model alih-alih hanya status kunci.
