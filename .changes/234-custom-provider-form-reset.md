### 🐛 Kolom isian custom provider tidak lagi kembali ke nilai awal (#234)

- **Gejalanya**: di dialog **Edit OpenAI/Anthropic Compatible**, semua yang diketik
  hilang sendiri setelah beberapa detik — persis saat polling dashboard berjalan.
  Dilaporkan user: "ubah data di kolom apapun, tunggu beberapa saat, nilainya
  kembali ke value awal".
- **Penyebabnya bukan polling itu sendiri, tapi cara dialog membaca datanya.**
  `App.svelte` memanggil `/api/provider-nodes` tiap 10 detik lalu **mengganti seluruh
  array** (`providerNodes = nodesRes`). Di `ProviderDetailView`, `selectedNode`
  dihitung dengan `$derived(providerNodes.find(...))`, jadi setiap tick memberi
  objek baru dengan isi yang identik. `EditCompatibleNodeModal` men-seed form dari
  `$effect` yang bergantung pada `node`, sehingga efek itu ikut jalan ulang dan
  menulis ulang `formName`/`formPrefix`/`formUrlSuffix`/`formApiType`/`formBaseUrl`
  dengan nilai tersimpan — menghapus semua ketikan yang belum disimpan.
- **Perbaikannya mengunci seed pada identitas yang stabil, bukan pada objek.**
  Id node adalah identitas yang benar-benar stabil; isi objeknya yang berubah
  setiap 10 detik. Efek sekarang hanya men-seed saat dialog dibuka atau saat
  node yang diedit **berganti id** — termasuk kasus node yang baru muncul
  setelah dialog terbuka. Variabel penanda (`seededNodeId`, `wasOpen`) sengaja
  `let` biasa, bukan `$state`: menulisnya tidak boleh dibaca efek itu sendiri,
  kalau tidak akan memicu dirinya sendiri.
- **`EditConnectionModal` sudah aman dan tidak ikut diubah.** Ia men-seed di luar
  `$effect`, jadi array `connections` yang berganti tiap 10 detik tidak pernah
  menimpa form-nya. Hanya dialog yang bergantung pada `providerNodes` yang
  bermasalah.
- **Tes regresi**: `web/e2e/customProviderForm.test.ts` membuat node asli lewat
  UI, membuka dialog Edit, mengetik perubahan, lalu **menunggu melewati satu tick
  polling** sebelum membaca nilainya kembali. Pada binary tanpa perbaikan, tes
  gagal persis pada gejala yang dilaporkan (`"Poll Survivor Edited"` kembali
  menjadi `"Poll Survivor"`), dan lulus setelah perbaikan. Kasus kedua
  memastikan dialog yang ditutup lalu dibuka lagi tetap men-seed dari baris
  tersimpan — perbaikan separuh akan menjadi bug form basi, jadi keduanya diuji.