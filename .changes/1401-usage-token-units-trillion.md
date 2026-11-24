### fix(usage): format trillion token units and boundary carry-over in token display

- Add `t: 1_000_000_000_000` to `UNITS` in `web/src/lib/tokenUnits.ts` so `parseTokenAmount` reads `1T`.
- In `formatTokenAmount`, add trillion handling (`>= 1e12` -> `x.xT`).
- Add and export `fmtCompactTokens(n)` matching upstream `tokenDisplay.js` with K/M/B/T units and 999.95+ boundary carry-over (e.g. 999,950 -> 1.0M).
- Add unit tests for trillion parsing/formatting and boundary promotion.

Upstream parity: decolua/9router#6e3c16a1d (#5670). Closes #1398.
