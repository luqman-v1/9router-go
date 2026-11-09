// Usage detail panels (Activity Heatmap, Node Health) start collapsed; the
// Settings > Display switch flips that default. Each panel can still be opened
// on its own for the current view, so this decides the starting state only.
// Mirrors lib/compactNumber.ts: a localStorage preference, no server round trip.
const DETAILS_EXPANDED_KEY = 'detailsExpandedByDefault'

export function isDetailsExpandedByDefault(): boolean {
  if (typeof localStorage === 'undefined') return false
  return localStorage.getItem(DETAILS_EXPANDED_KEY) === '1'
}

export function setDetailsExpandedByDefault(on: boolean): void {
  if (typeof localStorage === 'undefined') return
  localStorage.setItem(DETAILS_EXPANDED_KEY, on ? '1' : '0')
}

// The heavy Settings blocks — the currency rate grid, the per-panel Usage list
// with its arrangement controls, and the sidebar feature-visibility row — stay
// folded behind the Display switch until an operator asks for them. Nothing is
// turned off by folding them: the currency still formats every cost and the
// arrangement still drives the Usage page, so this is a disclosure preference
// rather than a feature flag, and it sits with the other display defaults.
const ADVANCED_SETTINGS_KEY = 'advancedSettingsVisible'

export function isAdvancedSettingsVisible(): boolean {
  if (typeof localStorage === 'undefined') return false
  return localStorage.getItem(ADVANCED_SETTINGS_KEY) === '1'
}

export function setAdvancedSettingsVisible(on: boolean): void {
  if (typeof localStorage === 'undefined') return
  localStorage.setItem(ADVANCED_SETTINGS_KEY, on ? '1' : '0')
}

// Animation level (Settings > Display). One attribute on <html> decides what
// every surface in the dashboard animates: `full` and `default` are the same
// (current behaviour, so an untouched install is unchanged), `minimal` drops
// the backdrop blur and every transition, and `off` also stops the looping
// animations — spinners, pings and pulses — leaving a static icon in place of
// a rotating one. The CSS that reads the attribute lives in src/index.css; no
// component carries a per-level class.
export type AnimationLevel = 'full' | 'default' | 'minimal' | 'off'

const ANIMATION_LEVEL_KEY = 'displayAnimationLevel'

export const ANIMATION_LEVELS: { id: AnimationLevel; label: string; hint: string }[] = [
  { id: 'full', label: 'Full', hint: 'Every animation and backdrop blur, as designed.' },
  { id: 'default', label: 'Default', hint: 'Same as Full — kept so an untouched install reads as the default.' },
  { id: 'minimal', label: 'Minimal', hint: 'No backdrop blur and no transitions; loading spinners still rotate.' },
  { id: 'off', label: 'Off', hint: 'Nothing moves: no blur, no transitions, no spinning icons.' },
]

export function normalizeAnimationLevel(value: unknown): AnimationLevel {
  return value === 'full' || value === 'minimal' || value === 'off' ? value : 'default'
}

export function getAnimationLevel(): AnimationLevel {
  if (typeof localStorage === 'undefined') return 'default'
  return normalizeAnimationLevel(localStorage.getItem(ANIMATION_LEVEL_KEY))
}

export function setAnimationLevel(level: AnimationLevel): void {
  if (typeof localStorage === 'undefined') return
  localStorage.setItem(ANIMATION_LEVEL_KEY, level)
  applyAnimationLevel(level)
}

/** Writes the attribute the CSS in index.css keys off. Safe to call at boot. */
export function applyAnimationLevel(level: AnimationLevel = getAnimationLevel()): void {
  if (typeof document === 'undefined') return
  document.documentElement.dataset.animLevel = level
}
