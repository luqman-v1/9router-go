/**
 * Display currency for every cost surface.
 *
 * Costs are always measured and stored in USD (internal/pricing, usageHistory.cost).
 * This module only decides how they are shown: the code, the rate table the
 * operator keeps in Settings, and locale-aware formatting.
 */

/** One selectable currency: its label and how its amounts are written. */
export interface CurrencyOption {
  code: string
  label: string
  /** Locale that decides grouping, separators and symbol placement. */
  locale: string
  /** Fraction digits a normal-sized amount carries. */
  decimals: number
}

export const CURRENCY_CATALOG: CurrencyOption[] = [
  { code: 'USD', label: 'US Dollar', locale: 'en-US', decimals: 2 },
  { code: 'IDR', label: 'Indonesian Rupiah', locale: 'id-ID', decimals: 0 },
  { code: 'EUR', label: 'Euro', locale: 'de-DE', decimals: 2 },
  { code: 'SGD', label: 'Singapore Dollar', locale: 'en-SG', decimals: 2 },
  { code: 'MYR', label: 'Malaysian Ringgit', locale: 'ms-MY', decimals: 2 },
  { code: 'JPY', label: 'Japanese Yen', locale: 'ja-JP', decimals: 0 },
  { code: 'CNY', label: 'Chinese Yuan', locale: 'zh-CN', decimals: 2 },
  { code: 'INR', label: 'Indian Rupee', locale: 'en-IN', decimals: 2 },
  { code: 'GBP', label: 'Pound Sterling', locale: 'en-GB', decimals: 2 },
  { code: 'AUD', label: 'Australian Dollar', locale: 'en-AU', decimals: 2 },
  { code: 'PHP', label: 'Philippine Peso', locale: 'en-PH', decimals: 2 },
  { code: 'THB', label: 'Thai Baht', locale: 'th-TH', decimals: 2 },
  { code: 'VND', label: 'Vietnamese Dong', locale: 'vi-VN', decimals: 0 },
]

/**
 * Seed rates, one USD in each catalog currency. They are a starting point the
 * operator edits in Settings (or refreshes from a public provider), not a
 * source of truth: a rate that is missing everywhere falls back to USD display.
 */
export const DEFAULT_RATES: Record<string, number> = {
  USD: 1,
  IDR: 16_400,
  EUR: 0.92,
  SGD: 1.34,
  MYR: 4.7,
  JPY: 151,
  CNY: 7.2,
  INR: 84,
  GBP: 0.79,
  AUD: 1.52,
  PHP: 58,
  THB: 36,
  VND: 25_400,
}

/** What Settings stores under `displayCurrency` (internal/handlers/dashboard/currency.go). */
export interface DisplayCurrency {
  code: string
  rates: Record<string, number>
  /** Show the USD amount next to the converted one. */
  showBoth: boolean
  /** When the rates were last refreshed, RFC3339. */
  updatedAt?: string
}

/**
 * The default is deliberately the current behaviour: USD, a single amount, so a
 * deployment that never opens the panel sees exactly what it saw before.
 */
export const DEFAULT_DISPLAY_CURRENCY: DisplayCurrency = {
  code: 'USD',
  rates: DEFAULT_RATES,
  showBoth: false,
}

const CODE_PATTERN = /^[A-Z]{3}$/
const MAX_RATES = 24

/** The catalog entry for a code, or a plain fallback for a code only a rate knows. */
export function currencyOption(code: string): CurrencyOption {
  const found = CURRENCY_CATALOG.find((option) => option.code === code)
  if (found) return found
  return { code, label: code, locale: 'en-US', decimals: 2 }
}

/**
 * Coerces whatever the server stored into a usable setting, mirroring
 * internal/handlers/dashboard/currency.go. The server is the authority; this
 * keeps a hand-edited row from breaking a cost surface.
 */
export function normalizeDisplayCurrency(value: unknown): DisplayCurrency {
  const container = (value ?? {}) as Record<string, unknown>
  const rawCode = typeof container.code === 'string' ? container.code.trim().toUpperCase() : ''
  const code = CODE_PATTERN.test(rawCode) ? rawCode : DEFAULT_DISPLAY_CURRENCY.code

  const rates: Record<string, number> = {}
  const rawRates = container.rates
  if (rawRates !== null && typeof rawRates === 'object') {
    for (const [key, candidate] of Object.entries(rawRates as Record<string, unknown>)) {
      const rateCode = key.trim().toUpperCase()
      if (!CODE_PATTERN.test(rateCode)) continue
      // Only a real number is a rate: the server drops "1" too
      // (internal/handlers/dashboard/currency.go), so both sides agree.
      if (typeof candidate !== 'number') continue
      if (!Number.isFinite(candidate) || candidate <= 0 || candidate > 1e9) continue
      if (Object.keys(rates).length >= MAX_RATES) break
      rates[rateCode] = candidate
    }
  }
  // A row that predates the feature carries no flag; it showed one amount in one
  // currency, so that is what it keeps.
  const showBoth = typeof container.showBoth === 'boolean' ? container.showBoth : false
  const updatedAt = typeof container.updatedAt === 'string' ? container.updatedAt : undefined
  return updatedAt ? { code, rates, showBoth, updatedAt } : { code, rates, showBoth }
}

/** The rate for a code: stored first, then the seed, then nothing. */
export function rateFor(code: string, currency: DisplayCurrency): number | null {
  if (code === 'USD') return 1
  const stored = currency.rates[code]
  if (typeof stored === 'number' && Number.isFinite(stored) && stored > 0) return stored
  const seeded = DEFAULT_RATES[code]
  if (typeof seeded === 'number' && seeded > 0) return seeded
  return null
}

/** Converts a USD amount, or null when no rate is known for the code. */
export function convertFromUsd(usd: number, currency: DisplayCurrency): number | null {
  const rate = rateFor(currency.code, currency)
  if (rate === null) return null
  return usd * rate
}

/** The USD form every existing surface used: `$` and a fixed number of digits. */
export function formatUsd(usd: number, decimals = 2): string {
  return '$' + (Number.isFinite(usd) ? usd : 0).toFixed(decimals)
}

/** A converted amount, grouped and symbolised the way its locale writes it. */
export function formatAmount(amount: number, code: string): string {
  const option = currencyOption(code)
  const value = Number.isFinite(amount) ? amount : 0
  try {
    return new Intl.NumberFormat(option.locale, {
      style: 'currency',
      currency: option.code,
      minimumFractionDigits: option.decimals,
      maximumFractionDigits: option.decimals,
    }).format(value)
  } catch {
    // An unknown three-letter code still deserves a readable amount.
    return `${formatUsd(value, option.decimals)} ${code}`
  }
}

/**
 * The amount a cost surface renders. With USD selected this is byte-identical to
 * the previous `$0.00` output, so the default costs nothing visually; with
 * `showBoth` on, the converted amount leads and the USD figure follows.
 */
export function formatCost(usd: number, currency: DisplayCurrency, decimals = 2): string {
  const amount = Number.isFinite(usd) ? usd : 0
  if (currency.code === 'USD') return formatUsd(amount, decimals)
  const converted = convertFromUsd(amount, currency)
  if (converted === null) return formatUsd(amount, decimals)
  const primary = formatAmount(converted, currency.code)
  return currency.showBoth ? `${primary} (${formatUsd(amount, decimals)})` : primary
}

/** A one-line summary of where the rates stand, for the settings panel. */
export function describeRates(currency: DisplayCurrency): string {
  const codes = Object.keys(currency.rates).length
  const noun = `rate${codes === 1 ? '' : 's'}`
  if (!currency.updatedAt) return `${codes} ${noun} set manually`
  const when = new Date(currency.updatedAt)
  if (Number.isNaN(when.getTime())) return `${codes} ${noun}`
  return `${codes} ${noun} refreshed ${when.toLocaleString()}`
}

/**
 * The line the panel shows after a refresh. The server overlays the provider's
 * rates on the stored table, so a code the provider did not publish keeps its
 * manual value; the count alone would read as if those rates were lost.
 */
export function describeRefreshRates(stored: number, missing: string[], host: string): string {
  const base = `Refreshed ${stored} rates from ${host}.`
  if (missing.length === 0) return base
  return `${base} ${missing.join(', ')} kept at your manual rate.`
}
