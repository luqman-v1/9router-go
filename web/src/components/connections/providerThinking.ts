export interface ProviderThinkingOptionsInput {
  modelThinkingLevels: Iterable<readonly string[] | undefined>
  isCompatibleNode: boolean
  currentMode?: string
}

const RESERVED_SWITCHES = new Set(['auto', 'off', 'on'])

/**
 * Computes the selectable options for a provider's "Thinking:" control.
 *
 * Compatible nodes (openai-compatible-* / anthropic-compatible-*) always expose
 * explicit `auto`, `off`, and `on` controls even when custom models carry no
 * static thinkingLevels metadata. Built-in providers keep upstream's hiding
 * behavior when no model supports reasoning (unless a persisted override is
 * currently active, so the operator can still inspect and reset it), and expose
 * `off` whenever any model allows disabling reasoning (`'none'`).
 */
export function computeProviderThinkingOptions({
  modelThinkingLevels,
  isCompatibleNode,
  currentMode,
}: ProviderThinkingOptionsInput): string[] | null {
  const levels = new Set<string>()
  let canDisable = false

  for (const entry of modelThinkingLevels) {
    for (const level of entry ?? []) {
      if (level === 'none') {
        canDisable = true
      } else if (level && !RESERVED_SWITCHES.has(level)) {
        levels.add(level)
      }
    }
  }

  const mode = currentMode?.trim()

  if (isCompatibleNode) {
    const opts = ['auto', 'off', 'on', ...levels]
    if (mode && !opts.includes(mode)) {
      opts.push(mode)
    }
    return opts
  }

  if (levels.size === 0 && !canDisable) {
    if (mode && mode !== 'auto') {
      const opts = ['auto', 'off', 'on']
      if (!opts.includes(mode)) {
        opts.push(mode)
      }
      return opts
    }
    return null
  }

  const opts = ['auto']
  if (canDisable) {
    opts.push('off')
  }
  opts.push(...levels)
  if (mode && !opts.includes(mode)) {
    opts.push(mode)
  }
  return opts
}

/**
 * Returns the `(level)` suffix appended to a copied/displayed model ID, or null
 * when the provider-wide mode is a switch (`auto`, `off`, `on`) or unsupported
 * by that specific model.
 */
export function resolveProviderThinkingSuffix(
  thinkingLevel: string,
  modelThinkingLevels: readonly string[] | undefined,
): string | null {
  if (!thinkingLevel || RESERVED_SWITCHES.has(thinkingLevel)) {
    return null
  }
  return modelThinkingLevels && modelThinkingLevels.includes(thinkingLevel)
    ? thinkingLevel
    : null
}

/** Formats a thinking mode value for display inside the selector option. */
export function formatThinkingOptionLabel(opt: string): string {
  if (!opt) return ''
  return opt.charAt(0).toUpperCase() + opt.slice(1)
}
