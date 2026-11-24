// Unit abbreviation toggle (localStorage: compactUnits, default ON).
// When enabled: zh locale ≥1e8 → X.X亿, ≥1e4 → X.X万; en locale ≥1e9 → B, ≥1e6 → M, ≥1e3 → K.
// When disabled: always full thousand-separated numbers.
// Toggle entry: Settings > Security > "Unit abbreviation".
const COMPACT_KEY = 'compactUnits';

export function isCompactUnitsEnabled(): boolean {
  if (typeof localStorage === 'undefined') return true;
  const v = localStorage.getItem(COMPACT_KEY);
  return v === null ? true : v !== '0';
}

export function setCompactUnits(on: boolean): void {
  if (typeof localStorage === 'undefined') return;
  localStorage.setItem(COMPACT_KEY, on ? '1' : '0');
}

// Provider card toggle persistence (localStorage: providerTogglePersistent).
// When ON the on/off switch on each provider card is always visible on every
// width; when OFF it stays hidden behind a hover reveal at the sm breakpoint.
const TOGGLE_PERSISTENT_KEY = 'providerTogglePersistent';

export function isProviderTogglePersistent(): boolean {
  if (typeof localStorage === 'undefined') return true;
  const v = localStorage.getItem(TOGGLE_PERSISTENT_KEY);
  return v === null ? true : v !== '0';
}

export function setProviderTogglePersistent(on: boolean): void {
  if (typeof localStorage === 'undefined') return;
  localStorage.setItem(TOGGLE_PERSISTENT_KEY, on ? '1' : '0');
}

const COMPACT_TOKEN_UNITS = [
  { value: 1_000_000_000_000, suffix: 'T' },
  { value: 1_000_000_000, suffix: 'B' },
  { value: 1_000_000, suffix: 'M' },
  { value: 1_000, suffix: 'K' },
]

export function fmtTokens(n?: number, locale = 'en', force = false): string {
  const v = n || 0;
  if (!force && !isCompactUnitsEnabled()) return v.toLocaleString();
  const zh = String(locale).startsWith('zh');
  if (zh) {
    if (v >= 1e8) return `${(v / 1e8).toFixed(1)}亿`;
    if (v >= 1e4) return `${(v / 1e4).toFixed(1)}万`;
    return v.toLocaleString();
  }
  const abs = Math.abs(v);
  const sign = v < 0 ? '-' : '';
  for (let i = 0; i < COMPACT_TOKEN_UNITS.length; i++) {
    const { value, suffix } = COMPACT_TOKEN_UNITS[i];
    if (abs >= value) {
      const scaled = abs / value;
      if (Number(scaled.toFixed(1)) >= 1000 && i > 0) {
        const upper = COMPACT_TOKEN_UNITS[i - 1];
        return `${sign}${(abs / upper.value).toFixed(1)}${upper.suffix}`;
      }
      return `${sign}${scaled.toFixed(1)}${suffix}`;
    }
  }
  if (Math.round(abs) >= 1000) {
    return `${sign}1.0K`;
  }
  return v.toLocaleString();
}