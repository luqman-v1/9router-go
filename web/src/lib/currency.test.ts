import { describe, expect, it } from 'bun:test'
import {
  CURRENCY_CATALOG,
  DEFAULT_DISPLAY_CURRENCY,
  DEFAULT_RATES,
  convertFromUsd,
  currencyOption,
  describeRates,
  describeRefreshRates,
  formatAmount,
  formatCost,
  formatUsd,
  normalizeDisplayCurrency,
  rateFor,
} from './currency'

const idr = (showBoth: boolean, rates: Record<string, number> = { IDR: 16_400 }) =>
  normalizeDisplayCurrency({ code: 'IDR', rates, showBoth })

const digits = (text: string) => text.replace(/[^\d.,]/g, '')

describe('currency catalog', () => {
  it('lists USD first, unique, three-letter codes', () => {
    expect(CURRENCY_CATALOG[0].code).toBe('USD')
    const codes = CURRENCY_CATALOG.map((option) => option.code)
    expect(new Set(codes).size).toBe(codes.length)
    for (const code of codes) {
      expect(code).toMatch(/^[A-Z]{3}$/)
    }
  })

  it('seeds a rate for every code it offers', () => {
    for (const { code } of CURRENCY_CATALOG) {
      expect(rateFor(code, { code, rates: {}, showBoth: false }), code).toBeGreaterThan(0)
    }
    expect(Object.keys(DEFAULT_RATES).sort()).toEqual(
      CURRENCY_CATALOG.map((option) => option.code).sort()
    )
  })

  it('falls back to a plain option for a code only a rate knows', () => {
    expect(currencyOption('IDR').decimals).toBe(0)
    expect(currencyOption('ZZZ').label).toBe('ZZZ')
  })
})

describe('normalizeDisplayCurrency', () => {
  it('defaults to the current behaviour when nothing was stored', () => {
    expect(normalizeDisplayCurrency(undefined)).toEqual({
      code: 'USD',
      rates: {},
      showBoth: false,
    })
    expect(DEFAULT_DISPLAY_CURRENCY.showBoth).toBe(false)
  })

  it('upper-cases the code, drops unusable rates and keeps the flag', () => {
    const currency = normalizeDisplayCurrency({
      code: ' idr ',
      showBoth: true,
      updatedAt: '2026-10-18T09:00:00Z',
      rates: { idr: 16_400, eur: 0.92, usd: '1', gbp: -1, jpy: 0, 'not-a-code': 5, zzz: 3 },
    })
    expect(currency.code).toBe('IDR')
    expect(currency.showBoth).toBe(true)
    expect(currency.updatedAt).toBe('2026-10-18T09:00:00Z')
    expect(Object.keys(currency.rates).sort()).toEqual(['EUR', 'IDR', 'ZZZ'])
  })

  it('falls back to USD for a malformed code and to a single amount for a missing flag', () => {
    expect(normalizeDisplayCurrency({ code: 'RUPIAH' }).code).toBe('USD')
    expect(normalizeDisplayCurrency({ code: 'IDR' }).showBoth).toBe(false)
  })
})

describe('conversion and formatting', () => {
  it('prefers the stored rate over the seed', () => {
    expect(rateFor('IDR', idr(false, { IDR: 17_000 }))).toBe(17_000)
    expect(rateFor('IDR', idr(false, {}))).toBe(DEFAULT_RATES.IDR)
    expect(rateFor('USD', idr(false))).toBe(1)
    expect(rateFor('ZZZ', idr(false))).toBeNull()
  })

  it('keeps the previous USD output byte for byte', () => {
    expect(formatUsd(0)).toBe('$0.00')
    expect(formatUsd(0.012_3, 4)).toBe('$0.0123')
    expect(formatCost(1.2345, DEFAULT_DISPLAY_CURRENCY)).toBe('$1.23')
    expect(formatCost(0.012_3, DEFAULT_DISPLAY_CURRENCY, 4)).toBe('$0.0123')
  })

  it('converts into the selected currency, grouped the way its locale writes it', () => {
    const converted = convertFromUsd(1.23, idr(false))
    expect(converted).toBeCloseTo(1.23 * 16_400, 6)
    const text = formatCost(1.23, idr(false))
    expect(text).toContain('Rp')
    expect(digits(text)).toBe(digits(formatAmount(1.23 * 16_400, 'IDR')))
    // IDR carries no decimals, so a normal amount never shows a fraction.
    expect(text).not.toContain(',00')
  })

  it('shows the USD figure alongside when dual display is on', () => {
    const text = formatCost(1.23, idr(true))
    expect(text).toContain('Rp')
    expect(text).toContain('($1.23)')
  })

  it('falls back to USD when the selected currency has no usable rate', () => {
    const unknown = normalizeDisplayCurrency({ code: 'ZZZ', rates: {}, showBoth: false })
    expect(convertFromUsd(2, unknown)).toBeNull()
    expect(formatCost(2, unknown)).toBe('$2.00')
  })

  it('describes the rate table for the panel', () => {
    expect(describeRates({ code: 'IDR', rates: { IDR: 1 }, showBoth: false })).toBe(
      '1 rate set manually'
    )
    expect(
      describeRates({
        code: 'IDR',
        rates: { IDR: 1, EUR: 1 },
        showBoth: false,
        updatedAt: '2026-10-18T09:00:00Z',
      })
    ).toContain('2 rates refreshed')
  })
})

describe('describeRefreshRates', () => {
  it('reports the provider count when every requested code was published', () => {
    expect(describeRefreshRates(3, [], 'open.er-api.com')).toBe(
      'Refreshed 3 rates from open.er-api.com.'
    )
  })

  it('names the codes that kept their manual rate', () => {
    expect(describeRefreshRates(1, ['VND'], 'api.frankfurter.app')).toBe(
      'Refreshed 1 rates from api.frankfurter.app. VND kept at your manual rate.'
    )
  })

  it('joins several kept codes', () => {
    expect(describeRefreshRates(2, ['VND', 'THB'], 'open.er-api.com')).toContain(
      'VND, THB kept at your manual rate.'
    )
  })
})
