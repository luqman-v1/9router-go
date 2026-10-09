### 🐛 fix(dashboard): menu pojok kanan atas terbuka tapi tidak kelihatan atau bisa diklik — Closes #235

- **Latar belakang**: di v1.9.11-exp.7, menu akun di pojok kanan atas (`<header>`: donasi, tema,
  bahasa, changelog, logout) **tidak memberi respons apa pun** di seluruh halaman. Menunya
  benar-benar terbuka — `aria-expanded` berubah, panel-nya ada di DOM dengan lebar 205px dan
  tinggi 232px — tapi tidak pernah terlihat dan tidak pernah menerima klik.
- **Akar masalahnya bukan penempatan, melainkan containing block**. `lib/ui/Menu.svelte`
  membuat panel `position: fixed` lalu memposisikannya dari `trigger.getBoundingClientRect()`
  lewat `menuPosition.placePanel` — dan itu benar, karena `placePanel` memang menghitung
  `left`/`top` **terhadap viewport**. Tapi `position: fixed` hanya ikut viewport selama tidak
  ada ancestor yang membentuk containing block, dan `backdrop-filter` (**juga** `transform`,
  `filter`, `perspective`, `contain`) membuatnya. `<header>` TopBar membawa `backdrop-blur-xl`,
  jadi panel di dalamnya diukur dari **padding box header**, bukan dari window: pada viewport
  1440px panel itu muncul di `left=1491, top=55` — sepenuhnya di luar layar. `left=1491` itu
  persis `rect.right - width` (1408 − 205) yang **tidak pernah di-clamp**, karena clamp di
  `placePanel` mengasumsikan koordinat sudah relatif terhadap viewport.
- **Perbaikan**: action `use:portal` (`web/src/lib/ui/portal.ts`) memindahkan panel ke
  `<body>` selama terbuka, lalu melepaskannya saat `{#if open}` menutup. Menempelkan panel di
  `<body>` menghapus ketergantungan pada ancestor sepenuhnya — bukan mengurangi offset
  header, yang harus dihitung ulang untuk setiap trigger baru. Diterapkan pada ketiga panel
  `fixed` yang memakai `placePanel`: `Menu.svelte`, `SectionMenu.svelte`, dan
  `PeriodSelect.svelte`.
- **Kenapa tidak cukup sekarang saja di header**: ketiga komponen itu berdiri sendiri dengan
  helper yang sama, jadi hanya memperbaiki satu berarti bug yang sama tetap bisa muncul di
  halaman lain. SectionMenu dan PeriodSelect memang belum terpengaruh hari ini, tapi keduanya
  sudah memakai `placePanel`, jadi tidak ada yang perlu ditunggu sampai triase ulang.
- **Verifikasi**: `web/e2e/headerMenuPanel.test.ts` baru menelusuri ancestor chain tiap panel dan
  gagal kalau ada `transform`/`filter`/`backdrop-filter`/`perspective`/`contain` di antara panel
  dan `<body>`, selain itu mengecek panel benar-benar di dalam viewport dan **bisa diklik**
  (`elementFromPoint` di titik tengah panel harus mendarat di dalam panel itu sendiri).
  Diverifikasi gagal sebelum fix (`Received: 1696, Expected: <= 1440`) dan lolos sesudahnya.
  Suite e2e penuh, `bun test src scripts` (321 pass), `bun run lint`, `make vet-svelte`, dan
  `go test ./...` semuanya hijau.