// Token amounts are written as plain numbers ("1000000") but read as scales
// ("1M"), and the two have to round-trip: an operator who typed 2M must see
// 2M again, not 2000000.
//
// The suffixes are decimal, not binary: 1M is a million tokens, and a budget
// written 1M means exactly that. A parser that used 1<<20 would silently hand
// the operator a different budget than the one they typed.
const UNITS: Record<string, number> = {
  k: 1_000,
  m: 1_000_000,
  b: 1_000_000_000,
}

// parseTokenAmount reads a token amount written as a number or a shorthand
// ("1M", "500K", "1.5M", "1 B", "1,000,000").
//
// It returns null for anything it cannot read exactly, including a value that
// would only be a guess: a suffix with a second letter ("1Mb"), a negative
// number, an exponent ("1e9"). A caller that gets null must not silently
// store 0 — that reads as "unlimited", which is the opposite of what the
// operator asked for.
export function parseTokenAmount(input: string): number | null {
  const raw = input.trim()
  if (!raw) return null

  // The number part may carry thousands separators.
  const cleaned = raw.replace(/,/g, '')
  const match = /^(\d+(?:\.\d+)?)\s*([a-zA-Z]*)$/.exec(cleaned)
  if (!match) return null

  const amount = Number(match[1])
  if (!Number.isFinite(amount)) return null

  const suffix = match[2].toLowerCase()
  if (suffix === '') {
    // A bare number is a whole count of tokens.
    if (!Number.isInteger(amount)) return null
    return amount
  }
  const unit = UNITS[suffix]
  if (unit === undefined) return null

  // The multiplication can land a hair off a whole token (1.0001M), so the
  // result is rounded and the rounding error is checked rather than the raw
  // product being required to be an integer.
  const scaled = amount * unit
  const rounded = Math.round(scaled)
  if (Math.abs(scaled - rounded) > 1e-6) return null
  return rounded
}

// formatTokenAmount writes a token count back as the shorthand an operator
// would type it as. A count below a thousand is written out, because "0.5K"
// is harder to read than "500".
export function formatTokenAmount(tokens: number): string {
  if (!Number.isFinite(tokens) || tokens <= 0) return ''
  if (tokens >= 1_000_000_000) return `${tokens / 1_000_000_000}B`
  if (tokens >= 1_000_000) return `${tokens / 1_000_000}M`
  if (tokens >= 1_000) return `${tokens / 1_000}K`
  return String(Math.round(tokens))
}
