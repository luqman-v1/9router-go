# 9Router Go — Future Roadmap & Feature Proposals

Dokumen ini mencatat rencana pengembangan fitur dan peningkatan kapabilitas untuk rilis mendatang pada **9Router Go**.

---

## 🎯 Prioritas & Kategori Fitur

### 1. 🧠 Smart Semantic & Exact Prompt Caching
* **Deskripsi**: Menyimpan cache respon dari query atau prompt yang identik (misal: static code analysis, linting berulang, atau template queries).
* **Mekanisme**:
  * Exact cache berbasis SHA-256 hash dari `(model, messages, tools, temperature)`.
  * Opsi storage: In-memory LRU cache dengan TTL atau SQLite persistent cache.
* **Manfaat**:
  * **0 ms TTFT** untuk query yang berulang.
  * **$0 token cost** pada prompt yang sudah pernah dieksekusi.

---

### 2. 🔀 Dynamic Cost-Aware Model Routing
* **Deskripsi**: Routing cerdas otomatis berdasarkan ukuran token dan kompleksitas instruksi pengguna.
* **Mekanisme**:
  * **Small / Simple Prompts** (< 2.000 tokens): Diarahkan ke model murah & ultra cepat (seperti `gemini-2.5-flash` atau `claude-haiku-4.5`).
  * **Complex / Heavy Coding** (> 20.000 tokens atau ada instruksi reasoning/tool complex): Diarahkan ke model flagship (`claude-sonnet-4.6`, `gemini-3-flash-agent`, atau `gpt-5`).
* **Manfaat**:
  * Mengurangi biaya token hingga 60-80% tanpa mengorbankan kualitas pada tugas-tugas berat.

---

### 3. 🔔 Webhook & Instant Alerting System
* **Deskripsi**: Sistem notifikasi real-time ke channel chat tim jika terjadi anomali atau kuota menipis.
* **Integrasi**: Telegram Bot, Discord Webhook, Slack Incoming Webhook.
* **Triggers**:
  * Upstream `429 Rate Limit` atau kuota provider habis.
  * Akumulasi biaya harian/bulanan melewati batas ambang (threshold budget).
  * OAuth token refresh failure atau koneksi upstream down.

---

### 4. 👥 Multi-Tenant Quota & Budget Enforcement
* **Deskripsi**: Kontrol akses dan kuota berbasis API Key atau User ID internal.
* **Fitur**:
  * Batas pengeluaran harian/bulanan per API Key (misal: max $20/bulan per user).
  * Auto-rate limiting jika request per menit (RPM) melebihi batas.
  * Dashboard view untuk memantau konsumsi token per tim / proyek.

---

### 5. 🛡️ PII & Secret Redaction Filter (Data Privacy)
* **Deskripsi**: Filter otomatis pada request body untuk menyamarkan (*masking*) data sensitif sebelum dikirim ke server AI publik.
* **Deteksi**:
  * API Keys (AWS, OpenAI, GitHub token, private keys).
  * Data pribadi (Nomor kartu kredit, email, IP address publik).

---

### 6. 📊 Advanced Prompt Analytics & Latency Heatmaps
* **Deskripsi**: Metrik visual analitik mendalam pada web dashboard.
* **Fitur**:
  * Distribusi p50, p95, dan p99 latency per provider dan per model.
  * Diagram token efficiency (perbandingan token input vs token output).
  * Visualisasi penghematan token oleh RTK, Caveman, dan Ponytail.
