### Cut the provider artwork that never reaches the screen

The icons in `public/providers/` are drawn in a 32×32 box (`size-8 object-contain` in CliToolsView). Three of them were not icons:

- `crush.png` was a **2700×1392 screenshot at 792 KB** — 19× the next-largest asset in the directory, downloaded whole to render inside 32×32. 99.98% of its bytes were discarded. Now 128×66 at 9 KB.
- `nebius.png`, `reka.png` and `siliconflow.png` were **JPEG files carrying a `.png` extension**. The browser renders them by sniffing the body, so nothing looked wrong — but every consumer that reads the extension (the budget test below, a future image pipeline, `file` on a support ticket) gets the wrong answer.

Measured, not assumed: `dist/` went from 4.5 MB to 3.9 MB, and the crush asset is served as 9.015 bytes.

Verified in a real browser, not just by file size: the resized icon loads (`complete: true`), renders at 30×32, and its aspect ratio is preserved (128/66 against the original 2700/1392, within 0.05) — so the card is not distorted. At 32×32 the two versions differ on 1.7% of pixels, below the threshold of a visible change.

A new test pins the budget so a screenshot pasted into this directory fails the suite instead of reaching a phone. It bounds **bytes**, not dimensions: a 512×512 PNG that compresses to 4 KB costs nothing on a slow uplink, while a 792 KB screenshot does. The directory ceiling is 1.5 MB, which catches the single-large-asset case the crush screenshot was rather than the long tail of legitimate 40 KB icons.
