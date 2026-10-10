/**
 * Dropdown placement, shared by every menu in this dashboard.
 *
 * The menus are `position: fixed` and placed against the trigger's measured
 * rect rather than `position: absolute` inside the trigger's wrapper. An
 * absolute panel is clipped by any ancestor with `overflow` set, and these
 * menus sit inside `overflow-x-auto` tables and an `overflow-y-auto` page —
 * which is what pushed the Cache Analytics dropdown off the right edge on a
 * phone (issue #224).
 *
 * The panel also flips above its trigger when the space below is too short,
 * so a menu opened near the bottom of a short viewport stays on screen.
 *
 * This module places a panel; it does not size one. Sizing belongs to the
 * panel's own CSS (`w-max` plus the `max-width` from `maxPanelWidth`), and the
 * two are deliberately separate: `panelWidth` is read back from the rendered
 * panel, so writing it out again as an inline width pinned the panel to
 * whatever width it had when measured, and every label wider than that floor
 * ellipsised without ever recovering (issue #261).
 */

/** Viewport gap between a panel and the edge of the window, in CSS pixels. */
const GUTTER = 8

/**
 * Space below the trigger, in CSS pixels, under which the panel opens upward
 * instead of downward.
 */
const MIN_SPACE_BELOW = 240

export interface PanelPlacementOptions {
  align?: 'left' | 'right'
  /** The trigger's viewport rect. */
  rect: DOMRect
  /**
   * The panel's rendered width, used to keep the panel on screen. It decides
   * the anchor and the flip, never the width that is written back.
   */
  panelWidth: number
  /**
   * The window the panel is placed inside. Defaults to `window`, which is what
   * every caller wants; the tests pass an explicit viewport because a test
   * runner has no window to measure.
   */
  viewport?: ViewportSize | null
}

export interface ViewportSize {
  width: number
  height: number
}

export interface PanelPlacement {
  left: number
  top: number
  /** Set instead of `top` when the panel opens upward. */
  bottom: number | null
}

/** The current window size, or `null` where there is no window at all. */
function currentViewport(): ViewportSize | null {
  if (typeof window === 'undefined') return null
  return { width: window.innerWidth, height: window.innerHeight }
}

/** Where a panel of `panelWidth` sits when placed against `rect`. */
export function placePanel({
  align = 'right',
  rect,
  panelWidth,
  viewport = currentViewport(),
}: PanelPlacementOptions): PanelPlacement {
  const viewportWidth = viewport?.width ?? 0
  const viewportHeight = viewport?.height ?? 0
  const width = Math.min(Math.max(panelWidth, 1), Math.max(0, viewportWidth - GUTTER * 2))

  const anchored = align === 'right' ? rect.right - width : rect.left
  const left = Math.min(
    Math.max(anchored, GUTTER),
    Math.max(GUTTER, viewportWidth - width - GUTTER),
  )
  const spaceBelow = viewportHeight - rect.bottom
  const flipAbove = spaceBelow < MIN_SPACE_BELOW && rect.top > spaceBelow

  return {
    left,
    top: flipAbove ? 0 : rect.bottom + 4,
    bottom: flipAbove ? viewportHeight - rect.top + 4 : null,
  }
}

/**
 * Renders a placement as an inline style string. A `bottom` that is `null`
 * contributes nothing, which is how a downward panel keeps `top` as its anchor.
 */
export function placementStyle(placement: PanelPlacement): string {
  const left = `left:${Math.round(placement.left)}px`
  return placement.bottom === null
    ? `${left};top:${Math.round(placement.top)}px`
    : `${left};bottom:${Math.round(placement.bottom)}px`
}

/**
 * The widest a panel may render, as a CSS length, leaving a viewport gutter on
 * both sides. This is the clamp that `placePanel` no longer applies by writing
 * a width: a 224px panel on a 200px window used to report right=232, and a
 * panel must never again be wider than the window showing it.
 */
export function maxPanelWidth(viewport: ViewportSize | null = currentViewport()): string {
  return `${Math.max(0, (viewport?.width ?? 0) - GUTTER * 2)}px`
}
