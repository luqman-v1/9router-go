/**
 * Step ids are built from slugified combo names and targets.
 * Strips edge dashes with a quantifier-free regex to ensure linear-time execution on long runs
 * (OmniRoute parity #17906, CodeQL #1165 js/polynomial-redos).
 */

export function slugify(value: string): string {
  // The first replace leaves at most one "-" at each edge, so the strip needs no quantifier
  // (CodeQL #1165, js/polynomial-redos).
  const slug = value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '')
  return slug.length > 0 ? slug : 'step'
}

export interface NormalizeComboStepOptions {
  comboName?: string | null
  index?: number
}

export function normalizeComboStepId(value: string, options: NormalizeComboStepOptions = {}): string {
  const prefix = options.comboName ? slugify(options.comboName) : 'combo'
  const idx = (options.index ?? 0) + 1
  const targetSlug = slugify(value)
  return `${prefix}-model-${idx}-${targetSlug}`
}
