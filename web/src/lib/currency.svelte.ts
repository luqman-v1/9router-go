import { DEFAULT_DISPLAY_CURRENCY, formatCost, normalizeDisplayCurrency, type DisplayCurrency } from './currency'

/**
 * The dashboard's display currency, in one place: App.svelte pushes the stored
 * setting in after every load, the Settings panel writes it back, and every cost
 * surface formats through the helpers below, so a change reaches the analytics
 * without a reload.
 *
 * The state stays private because Svelte 5 forbids exporting a `$state` binding
 * that is later reassigned (svelte.dev/e/state_invalid_export); the exported
 * functions read it at call time, which is what makes them reactive.
 */
let current = $state<DisplayCurrency>(DEFAULT_DISPLAY_CURRENCY)

/** The currency, code, rate table and dual-display flag the panel needs. */
export function currentCurrency(): DisplayCurrency {
  return current
}

/** Applies whatever the server returned (or the last panel edit). */
export function setDisplayCurrency(value: unknown): void {
  current = normalizeDisplayCurrency(value)
}

/** Formats a USD cost for display: the one call every cost surface makes. */
export function costIn(usd: number, decimals = 2): string {
  return formatCost(usd, current, decimals)
}
