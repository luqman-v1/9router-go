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
  /** The panel's natural width, measured after it renders. */
  panelWidth: number
  /** The panel's preferred minimum width. Capped so it can never exceed the
   * viewport. */
  minWidth?: number
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
  bottom: number | null
  width: number
  minWidth: number
}

/** The current window size, or `null` where there is no window at all. */
function currentViewport(): ViewportSize | null {
  if (typeof window === 'undefined') return null
  return { width: window.innerWidth, height: window.innerHeight }
}

/** The CSS `left`/`top`/`bottom`/`width` for a panel placed at `rect`. */
export function placePanel({
  align = 'right',
  rect,
  panelWidth,
  viewport = currentViewport(),
  minWidth = 0,
}: PanelPlacementOptions): PanelPlacement {
  const viewportWidth = viewport?.width ?? 0
  const viewportHeight = viewport?.height ?? 0
  const availableWidth = Math.max(0, viewportWidth - GUTTER * 2)
  const width = Math.min(Math.max(panelWidth, 1), availableWidth)

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
    width,
    // CSS `min-width` beats `width`, so a hard min-width on a narrow viewport
    // would render wider than the clamp computed and overflow anyway.
    minWidth: Math.min(minWidth, width),
  }
}

/**
 * Renders a placement as an inline style string. A `bottom` that is `null`
 * contributes nothing, which is how a downward panel keeps `top` as its anchor.
 */
export function placementStyle(placement: PanelPlacement): string {
  const parts = [
    `left:${Math.round(placement.left)}px`,
    `width:${Math.round(placement.width)}px`,
  ]
  if (placement.bottom === null) {
    parts.push(`top:${Math.round(placement.top)}px`)
  } else {
    parts.push(`bottom:${Math.round(placement.bottom)}px`)
  }
  if (placement.minWidth > 0) {
    parts.push(`min-width:${Math.round(placement.minWidth)}px`)
  }
  return parts.join(';')
}