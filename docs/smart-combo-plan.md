# Smart Select Combo — keanggotaan otomatis lewat keyword & regex

Status: draft rencana, belum ada kode. Ditulis 2026-10-23.
Ruang lingkup: tabel `combos` + halaman Combos di dashboard + jalur tulis model di dashboard API.

Dokumen ini **bukan** port KeiRouter dan **tidak** menyentuh `docs/keirouter-port-plan.md` (F-1…F-15).
Fitur ini ekstensi Go-only murni: upstream `decolua/9router` tidak punya combo berpola
(terverifikasi: `src/app/api/combos/route.js` hanya membaca `name`, `models`, `kind`),
jadi tidak ada tabrakan paritas — cukup ikut aturan `goOnlyColumns` (schema additive).

---

## 1. Tujuan

Pengguna menempelkan **aturan** (pola) pada sebuah combo. Setiap model yang terdaftar di gateway
dan cocok dengan pola itu menjadi anggota combo secara otomatis; setiap model yang hilang dari
gateway keluar dari combo secara otomatis — tanpa pengguna menyunting daftar satu per satu.

Contoh: combo `deepseek-all` berisi aturan `kenari/deepseek*`. Saat pengguna menambah model
`kenari/deepseek-v4` (lewat `POST /api/models/custom`), model itu langsung muncul di combo.
Saat baris model itu dihapus, ia langsung keluar dari combo.

Keputusan Cak Helmi (2026-10-23), mengunci seluruh desain di bawah:

| # | Keputusan | Alasan |
|:--|:---|:---|
| D1 | **Snapshot materialisasi** — `combos.models` tetap kebenaran routing; aturan memperbarui kolom itu saat tulis | resolver, `/v1/models`, capacity adapter, fusion tidak disentuh; nol biaya jalur panas; batas anggota deterministik |
| D2 | Sintaks **glob + keyword + regex `re:`** | glob memakai matcher yang sudah ada; regex eksplisit lewat awalan, divalidasi saat simpan |
| D3 | Hapus otomatis **hanya saat baris DB hilang** (hapus manual, hapus massal, hapus node) | `models_sync.go:20-26`: katalog live sering parsial, absen ≠ mati |
| D4 | Model yang dimatikan (`kv disabledModels`) **ikut dibuang** dari combo | selaras dengan `auto-family` yang mengecualikannya; model diaktifkan kembali masuk lagi sebagai auto-add baru |
| D5 | **Batas anggota per aturan** (default 20) + pratayang jumlah cocok di UI | mencegah pola `*` membuat rantai fallback ratusan slot di box 1 vCPU (§4.G) |

---

## 2. Non-tujuan

- Bukan routing dinamis: resolver TIDAK mengekspansi pola saat request.
- Tidak mengubah kontrak kabel `combos.models` (tetap array string polos — `normalizeComboModelIDs`, `combos.go:265`).
- Tidak menyentuh `/v1/models`, capacity adapter, fusion, fallback, round-robin, guard nama (`name_guard.go`).
- Tidak menambah dependensi baru (Go maupun npm).
- Tidak menghapus combo yang jadi kosong setelah prune.

---

## 3. Fakta terverifikasi (basis rencana)

**Combo.**
- Tabel: `internal/db/schema.go:103-116` → `id, name, kind, models TEXT NOT NULL, createdAt, updatedAt` + indeks `idx_combo_name`.
- Model Go: `internal/models/types.go:88-96` — `Models string` (JSON string), `Strategy string`.
- Kolom aditif repo: `internal/db/schema.go:307-364` `goOnlyColumns` + `EnsureAdditiveColumns` (`:585`), idempoten, upstream mengabaikannya.
- Tulisan combo: `internal/handlers/dashboard/combos.go` — `HandleCreateCombo:35`, `HandleUpdateCombo:95` (field absen = biarkan; `models` dinormalkan sama seperti create), `HandleDeleteCombo`.
- Combo otomatis yang sudah ada (preseden): `auto-free` (`combos.go:173-229`, upsert pada id stabil, bisa diedit) dan `auto-family` (`combos_family.go:25-73`, `kind="auto-family"`, regenerasi = upsert).
- Pembaca: `comboModels(v any) ([]string, error)` di `combos.go:265` — menerima JSON string mentah atau array sudah-terdekode.
- Rename node sudah menulis ulang anggota combo yang materialisasi: `internal/db/provider_node_rename.go:345-391` `renameComboMembers`.

**Registrasi & penghapusan model (sumber pemicu).**
- Simpan: `internal/handlers/dashboard/models.go:42` `HandleSaveCustomModel` (tunggal + bentuk array `models` → `saveCustomModelsBatch`), kunci kv `"<alias>|<id>|<type>"`.
- Hapus tunggal: `models.go:141` `HandleDeleteCustomModel`.
- Hapus massal per provider: `routes.go:70` `DELETE /api/providers/{id}/models` → `HandleDeleteProviderModels`.
- Hapus node: `internal/db/providernodes.go:213-259` `pruneCustomModels` (di dalam transaksi hapus node).
- Matikan model: `models.go:272` `HandleSaveDisabledModels` → kv scope `disabledModels`.
- Katalog registri (in-memory, disegarkan sinkronisasi latar models.dev): `providers.GetProviderModels(providerID)`, `providers.GetProviderAlias(providerID)`; dipakai `combos_family.go:96-112`.
- Koneksi aktif: `internal/db/connections.go:195` `GetConnectedProviders`.

**Matcher.**
- `internal/providers/modelpattern.go:17` `MatchModelPattern` — glob per-segmen: `*` TIDAK menyeberangi `/`, jadi tidak bisa menyatakan `kenari/deepseek*`.
- `internal/providers/modelpattern.go` pada commit base branch ini **belum** punya `MatchAllowPattern`
  (glob allowlist yang `*`-nya menyeberangi `/`, paritas upstream #365). Fungsi itu ada di branch lain,
  bukan di `origin/main` tempat branch ini dipotong.
- Karena itu glob allowlist diimplementasikan sebagai `matchGlob` di
  `internal/combos/smart/smart.go`, dengan semantik yang sama persis dan catatan bahwa ia harus
  menjadi delegasi ke `providers.MatchAllowPattern` begitu fungsi itu ada di base branch. Dua glob
  dengan semantik berbeda adalah jalan menuju drift; di sini tidak ada glob kedua yang hidup —
  hanya satu yang menunggu dipindahkan.
- `IsDenyEntry`/`MatchDenyEntry` (`:64`, `:85`) tidak dipakai sebagai sintaks: sebuah aturan adalah
  objek terstruktur dengan field `effect`, jadi pola berawalan `!`/`-` justru DITOLAK
  (`INVALID_RULE_PATTERN`) supaya satu maksud punya satu bentuk.
- Doktrin "satu matcher": `internal/handlers/chat/access.go:43-45`.

**Fixture tes.**
- Semua fixture yang membuat `CREATE TABLE combos` manual memanggil `EnsureAdditiveColumns`: `internal/db/dashboard_test.go:82`, `internal/db/repos_test.go:90`, `internal/handlers/dashboard/dashboard_test.go:109`, `internal/dbtest/dbtest.go:115`.
- Tes paket `chat` memakai `dbtest.CreateTables` (`chat_test.go:39`, `access_failclosed_test.go:52`) → ikut additive columns.

**Frontend.**
- `web/src/components/combos/`: `CombosView.svelte` (487 baris), `ComboCard.svelte` (203), `CreateComboModal.svelte` (280), `ModelPickerModal.svelte` (225), `types.ts`, `pickerData.ts`.
- Tidak ada kerangka i18n di `web/src` (terverifikasi lewat pencarian) → string dashboard hardcoded bahasa Inggris, tidak ada pekerjaan paritas locale.

---

## 4. Skema data

### 4.1 Kolom baru

`internal/db/schema.go` `goOnlyColumns`:

```go
{"combos", "matchRules", "TEXT DEFAULT '[]'"},
```

Satu kolom, JSON array aturan. Idempoten lewat `EnsureAdditiveColumns`; backup lama tanpa kolom
mendapat default `'[]'` = tanpa aturan = perilaku identik dengan hari ini.

**`comboColumns`/`scanCombo` TIDAK diubah.** Aturan hanya dibaca lapisan dashboard; `GetComboByName`
(dipakai `resolveModel`, jalur panas) tetap query yang sama persis.

### 4.2 Bentuk aturan

```json
[
  {
    "id": "r1",
    "pattern": "kenari/deepseek*",
    "mode": "glob",
    "effect": "include",
    "enabled": true,
    "maxMembers": 20,
    "allKinds": false,
    "resolved": ["kenari/deepseek-v4"],
    "excluded": []
  }
]
```

| Field | Tipe | Makna |
|:---|:---|:---|
| `id` | string | Stabil dalam satu combo; server menetapkan `r1, r2, …` bila absen |
| `pattern` | string | Pola; untuk `mode:"regex"` WAJIB berawalan `re:` |
| `mode` | `glob` \| `keyword` \| `regex` | Semantik pencocokan (§5) |
| `effect` | `include` \| `exclude` | Exclude selalu menang atas include |
| `enabled` | bool | Aturan mati tidak berkontribusi dan tidak di-prune |
| `maxMembers` | int | 0 = tak terbatas; default 20 (D5) |
| `allKinds` | bool | `true` = ikut embedding/image/tts/stt; default `false` (hanya chat/LLM) |
| `resolved` | []string | **Snapshot**: id yang saat ini dikontribusikan aturan ini ke combo |
| `excluded` | []string | Id yang dicabut pengguna dari combo; rekonsiliasi tidak akan memasukkan kembali |

Invarian yang harus dijaga:
- `combos.models` selalu array string polos, tanpa duplikat, bisa dibaca `comboModels`.
- Anggota combo = pin pengguna (urutannya dipertahankan) + gabungan `resolved` seluruh aturan aktif, ditambahkan di ekor.
- `resolved` dan `excluded` saling lepas (lepas himpunan).
- Satu aturan tidak pernah memasukkan id yang sudah ada di `models` sebagai pin pengguna.

### 4.3 Field turunan pada `models.Combo`

```go
MatchRules   string   `json:"matchRules,omitempty"`   // JSON mentah kolom baru
AutoMembers  []string `json:"autoMembers,omitempty"`  // turunan: union(resolved aturan aktif)
```

`AutoMembers` tidak disimpan; diturunkan saat membaca supaya UI bisa memberi lencana "auto"
tanpa menebak. `GET /api/combos` mengisinya.

---

## 5. Semantik pencocokan

Kandidat yang dicocokkan: **kedua ejaan** setiap model — `alias/model` dan `model` telanjang —
mengikuti preseden `accessCandidates` (`internal/handlers/chat/access.go:289-292`), supaya
`deepseek*` dan `kenari/deepseek*` sama-sama masuk akal.

| Mode | Fungsi | Perilaku |
|:---|:---|:---|
| `glob` | `providers.MatchAllowPattern` | `*` menyeberangi `/`, case-insensitive, anchored kedua ujung; tanpa `*` = cocok penuh |
| `keyword` | substring case-insensitive | `deepseek` cocok dengan `kenari/deepseek-v4` dan `deepseek-v4` |
| `regex` | `regexp.Compile` (RE2) | sisa string setelah awalan `re:`; dibungkus `^(?:…)$` (cocok penuh); `(?i)` inline untuk case-insensitive |

Aturan validasi saat tulis (PUT/POST combo), gagal → `400` dengan `code`:

| Kondisi | `code` |
|:---|:---|
| `mode` tidak dikenal | `INVALID_RULE_MODE` |
| `mode:"regex"` tanpa awalan `re:` atau regex tidak kompilasi | `INVALID_REGEX` |
| `effect` tidak dikenal | `INVALID_RULE_EFFECT` |
| `maxMembers` negatif | `INVALID_RULE_LIMIT` |
| pola berawalan `!` atau `-` | `INVALID_RULE_PATTERN` — pesan mengarahkan ke `effect:"exclude"` |

Keputusan yang saya ambih (bisa dibatalkan Cak Helmi): **tolak sigil `!term`/`-term`** walaupun
`IsDenyEntry` ada. Sebuah aturan adalah objek terstruktur, bukan daftar tempel; dua sintaks untuk
satu maksud adalah jalan menuju drift. `MatchAllowPattern`/`MatchDenyEntry` tetap dipakai di bawah
sana sebagai mesin, bukan sebagai sintaks.

### 5.1 Himpunan kandidat

```
kandidat = model registri provider yang punya koneksi aktif
          + semua baris kv customModels (terdaftar = dialamatkan)
disingkat: yang kind-nya bukan chat/LLM hanya masuk bila aturan allKinds=true
```

- Kind chat/LLM: gate yang sama dengan `isUsableChatModel` (`combos_family.go:123`) + `customLLMModels` (`:246`, hanya `type == "llm"`).
- Model yang sedang dimatikan (`disabledModels`) **tidak** menjadi kandidat (D4).
- Provider tanpa koneksi: model registrinya tidak menjadi kandidat **baru**, tetapi anggota yang
  sudah ada tidak di-prune karena itu — prune hanya dipicu D3. Ini invarian penting: himpunan
  kandidat (siapa yang boleh masuk) ≠ pemicu prune (siapa yang harus keluar).

---

## 6. Algoritma rekonsiliasi

Fungsi murni, tanpa DB dan tanpa HTTP, supaya bisa diuji tabel-driven:

```go
// internal/combos/smart/smart.go (nama paket final saat implementasi)
func Reconcile(models []string, rules []Rule, candidates []string) (newModels []string, newRules []Rule, changed bool)
```

```
models = comboModels(combo.Models)                 // urutan pin pengguna dipertahankan
includes := rules aktif ber-effect include
excludes := rules aktif ber-effect exclude
denySet  := union(cocokkan(e, candidates) untuk e di excludes)

for r in includes (urutan daftar):
    if !r.enabled: continue
    desired := sort(cocokkan(r, candidates) - denySet - r.excluded)
    if r.maxMembers > 0 && len(desired) > r.maxMembers:
        desired = desired[:r.maxMembers]           // + tanda capped untuk pratayang UI
    userRemoved := r.resolved - models             // pengguna mencabut dari UI
    r.excluded   := union(r.excluded, userRemoved)
    toRemove    := r.resolved - desired
    toAdd       := desired - r.resolved - models
    models      := (models - toRemove) + toAdd     // append di ekor
    r.resolved   := desired
    changed      := changed || len(toRemove)+len(toAdd) > 0 || len(userRemoved) > 0
```

Sifat yang harus dipegang:
- **Idempoten**: rekonsiliasi dua kali berturut-turut tanpa perubahan di antaranya → `changed == false`, tidak ada tulisan.
- **Deterministik**: kontribusi tiap aturan diurutkan; hasil bisa di-diff antar regenerasi.
- **Pengguna menang**: pin pengguna tidak pernah dihapus aturan; auto yang dicabut pengguna masuk `excluded` dan tidak pernah kembali.
- **Exclude menang**: model yang cocok exclude tidak masuk meski pola include cocok.

---

## 7. Pemicu rekonsiliasi

Semua pemicu best-effort: tulis utama (simpan/hapus model) sukses dulu, rekonsiliasi setelahnya
dalam transaksi terpisah. Kegagalan rekonsiliasi dicatat log terstruktur dan tidak menggagalkan
tulis model — keanggotaan combo adalah kenyamanan, bukan kontrak tulis.

| Pemicu | Lokasi | Catatan |
|:---|:---|:---|
| Simpan model kustom (tunggal + array) | `models.go:42` + `saveCustomModelsBatch` | setelah `SetKV` sukses |
| Hapus model kustom | `models.go:141` | setelah delete |
| Hapus massal model provider | `HandleDeleteProviderModels` (`routes.go:70`) | setelah delete |
| Simpan model dimatikan | `models.go:272` | D4: anggota yang kini dimatikan dibuang |
| Hapus provider node | `providernodes.go` `DeleteProviderNode` | di dalam transaksi yang sudah ada |
| Tombol rekonsiliasi | `POST /api/combos/reconcile` | semua combo, atau `?id=` |
| Opsional: loop latar | samping `providers.StartBackgroundCatalogSync` (`internal/app/server.go:97`) | menangkap model baru dari sinkronisasi katalog models.dev yang bukan peristiwa DB |

Pintu keluar awal (wajib): satu kueri
`SELECT COUNT(*) FROM combos WHERE matchRules IS NOT NULL AND matchRules NOT IN ('', '[]')`.
Hasil 0 → tidak ada pekerjaan sama sekali. Tanpa ini, setiap simpan model di instalasi tanpa aturan
membayar enumerasi katalog sia-sia.

---

## 8. Kontrak API (Application Programming Interface)

Semua di bawah `/api`, sama dengan rute combo yang ada (`internal/handlers/dashboard/routes.go:87-93`).

| Metode | Rute | Perilaku |
|:---|:---|:---|
| `POST` | `/api/combos` | Menerima field opsional `matchRules` (array). Combo dengan aturan langsung direkonsiliasi sekali. |
| `PUT` | `/api/combos/{id}` | Menerima `matchRules` opsional. Absen = biarkan; ada = ganti seluruh daftar (validasi §5). Setelah ganti, rekonsiliasi combo itu. |
| `POST` | `/api/combos/{id}/smart/preview` | Pratayang kering: `{"matched":[…],"total":120,"applied":20,"capped":true,"unreachable":3}`. Tidak menulis apa pun. |
| `POST` | `/api/combos/reconcile` | Rekonsiliasi semua (atau `?id=`). Balasan: `{"status":"ok","reconciled":2,"added":[…],"removed":[…]}`. |
| `GET` | `/api/combos` | Setiap combo kini membawa `matchRules` + `autoMembers` (turunan). |

Bentuk galat memakai amplop standar paket (`handlerutil.WriteJSONError` + `code`), sama seperti
`INVALID_ID` di `models.go:94-101`.

---

## 9. Frontend (`web/src/components/combos/`)

- `types.ts`: antarmuka `SmartRule`, konstanta `SMART_RULE_MODES`, helper `smartRuleSummary(rule)`
  (mis. `glob · 20 dipakai dari 120 cocok`), dan perluasan `getComboModels` agar bisa menandai
  mana yang auto.
- `ComboCard.svelte`: seksi **Aturan pintar** — daftar aturan dengan sakelar aktif, editor
  (pola + mode + batas + allKinds), pratayang jumlah cocok (panggilan preview, di-debounce),
  dan lencana `auto` pada pill yang ada di `autoMembers`.
- `CombosView.svelte`: aksi **Rekonsiliasi** di header (preseden: aksi "Auto …" yang sudah ada),
  serta penanda peringatan saat combo kosong setelah prune.
- `CreateComboModal.svelte`: field aturan saat membuat combo baru.
- `ModelPickerModal.svelte` (fase 2, opsional): tandai entri yang akan masuk lewat aturan.

Aturan wajib: Svelte 5 runes saja (`$props`, `$state`, `$derived`, `$effect`, `onclick`), komponen
UI yang sudah ada (`Input`, `Button`, `Badge`, `Toggle`), tanpa dependensi baru, dan lolos
`make vet-svelte` (§6.E) plus anggaran berat klien (§6.F).

---

## 10. Rencana uji

**Unit, tabel-driven dengan `testing.T` (§5).**
- Matcher: tiap mode, anchoring, case, kedua ejaan, exclude menang, batas, `allKinds`.
- Rekonsiliasi: tambah, buang, batas (capped), `excluded` bertahan, idempoten (dua kali = nol perubahan),
  pin pengguna tidak tersentuh, urutan deterministik.
- Validasi aturan: regex tidak kompilasi, mode/effect tak dikenal, `maxMembers` negatif, pola bersigil.

**Handler (`internal/handlers/dashboard/combos_smart_test.go`).**
- Buat combo beraturan → anggota termaterialisasi.
- Simpan model kustom yang cocok → anggota muncul; hapus → hilang (D3).
- Matikan model yang cocok → hilang (D4); aktifkan kembali → muncul lagi.
- `PUT /api/combos/{id}` menghapus anggota auto → rekonsiliasi berikutnya tidak memasukkan kembali.
- Pratayang tidak menulis; rekonsiliasi mengembalikan `added`/`removed` yang benar.

**Integrasi (`internal/integration`, tag `integration`).**
- Router asli + DB sementara + upstream palsu: model terdaftar di node palsu masuk combo, dan
  permintaan melalui nama combo sampai ke upstream tersebut.

**E2E (`web/e2e/`, butuh biner ter-build).**
- Alur dashboard: buat combo beraturan → tambah model → pill auto muncul.

**Jangan diuji** (larangan §5 alur kerja): wiring/forwarding, tiruan mock, teks sumber,
"panjang bertambah", atau tautologi.

---

## 11. Migrasi, cadangan, rename

- Ekspor cadangan: `internal/handlers/dashboard/settings.go:579` daftar kolom `SELECT` combo += `matchRules`.
- Impor cadangan: `settings.go:798` daftar kolom `INSERT OR REPLACE` += `matchRules`.
- Daftar wipe (`settings.go:652`) tidak berubah — baris combo dihapus utuh.
- Rename combo: aturan berkunci pada `id` combo → tidak ada yang perlu dipindahkan.
- Rename provider node: anggota yang sudah materialisasi ditulis ulang `renameComboMembers`
  (`provider_node_rename.go:348`); pola mencocoki ulang sendiri pada rekonsiliasi berikutnya.
- Backup lama tanpa `matchRules` → default `'[]'` → nol aturan → perilaku identik dengan hari ini.

---

## 12. Risiko & mitigasi

| Risiko | Mitigasi |
|:---|:---|
| Pola luas membanjiri combo | `maxMembers` default 20 (D5), pratayang jumlah cocok, penanda `capped` |
| Rekonsiliasi gagal setelah simpan model | best-effort, transaksi terpisah, log terstruktur; tulis model tetap sukses |
| Balapan tulis dengan `PUT /api/combos/{id}` | `BEGIN IMMEDIATE`, baca ulang baris combo di dalam transaksi |
| Biaya di box 1 vCPU | pintu keluar awal saat nol aturan; enumerasi in-memory + satu baca kv; tanpa jaringan |
| `models` jadi campuran pin + auto | `autoMembers` turunan dari snapshot aturan; UI memberi lencana `auto` |
| Combo kosong setelah prune | baris dipertahankan (jangan hapus combo pengguna), UI memperingatkan |
| Regex berbahaya | RE2 (waktu linear), kompilasi + validasi saat simpan, cocok penuh |
| Model baru dari sinkronisasi katalog models.dev bukan peristiwa DB | pemicu loop latar opsional + tombol rekonsiliasi manual |
| Daftar aturan diganti, anggota aturan lama jadi "pin" | `HandleUpdateCombo` menghitung `AutoMembers(lama) \ AutoMembers(baru)` dan membuangnya dari `models` sebelum rekonsiliasi |
| Pin pengguna ber-ejaan sama dengan klaim aturan | dibatasi dan didokumentasikan di `internal/combos/smart/smart.go`: aturan yang memegang id itu, jadi mengubah pola mencabutnya; UI mencegah jalur umum (pemilih tidak menawarkan model yang sudah jadi anggota) |

---

## 13. Urutan implementasi

1. Kolom `matchRules` + metode repo baca/tulis aturan (tanpa perubahan perilaku).
2. Paket fungsi murni matcher + rekonsiliasi + unit test.
3. API dashboard: `matchRules` di POST/PUT combo, pratayang, rekonsiliasi + test handler.
4. Pemicu pada jalur tulis model (simpan/hapus/matikan/hapus node).
5. Frontend seksi Aturan pintar + lencana auto + aksi rekonsiliasi.
6. Integrasi + e2e.
7. Fragmen changelog `.changes/<pr>-smart-combo-rules.md`.

Semua tujuh langkah sudah dilaksanakan di branch `feat/smart-combo-rules`.

---

## 14. Branch & PR (pull request)

Cabang dari `origin/main` yang bersih. **Jangan `checkout -b` di working tree saat ini** — pada
2026-10-23 terukur 12 berkas tracked termodifikasi, 7 ter-stage, 4 untracked
(`.changes/511-combo-error-body-json-escape.md`, `.changes/559-commandcode-live-catalog.md`,
`internal/handlers/chat/combo_error_body_test.go`, `internal/providers/commandcode_catalog_test.go`)
milik sesi lain. Pakai worktree supaya pekerjaan itu tidak tercampur:

```bash
git fetch origin
git worktree add ../9router-go-smart-combo -b feat/smart-combo-rules origin/main
cp -r web/dist ../9router-go-smart-combo/web/   # agar //go:embed dan lint tidak gagal
```

PR (pull request) ditargetkan ke `luqman-v1/9router-go` cabang `main` (§7.7):

```bash
gh pr create --repo luqman-v1/9router-go --base main --head feat/smart-combo-rules \
  --title "feat(combos): smart select combo via keyword/glob/regex rules" --body "..."
```

---

## 15. Verifikasi

```bash
cd web && bun run build                 # prasyarat build Go (web/embed.go)
go test ./internal/combos/... ./internal/handlers/dashboard/... ./internal/providers/... ./internal/db/...
make test-integration                   # tag integration: router asli + upstream palsu
make vet-svelte                         # ratchet svelte-check (§6.E)
go test ./web/...                       # kontrak aset ter-embed
```

Bukti penerimaan utama (bukan tes wiring): menambah `kenari/deepseek-v4` lewat dashboard membuatnya
menjadi anggota combo tanpa menyentuh halaman Combo, dan permintaan melalui nama combo sampai ke
model tersebut; menghapus baris model mengeluarkannya dari combo pada permintaan `/api/combos`
berikutnya.

Hasil di branch `feat/smart-combo-rules` (2026-10-23):

| Perintah | Hasil |
|:---|:---|
| `go build ./...` | bersih |
| `go test ./...` | bersih |
| `go test -tags=integration ./internal/integration/...` | `ok` (2 paket) |
| `bun run build` | `✓ built` — JS 1.108 MB / 281 KB brotli, CSS 104 KB / 16.8 KB brotli |
| `bun run ratchet:svelte` | `✓ 83 errors (baseline 83)`, 0 pengenal tak terpecahkan |
| `bun test src scripts` | 354 lulus, 0 gagal |
| `go test ./web/...` | `ok` |

Catatan pengukuran: font ikon pada base branch ini masih versi paket npm `material-symbols`
(4 MB) karena pekerjaan self-host font (PR #511) belum ada di `origin/main`. Itu kondisi base,
bukan sesuatu yang diff ini ubah — angka JS/CSS di atas adalah yang relevan untuk §6.F.
