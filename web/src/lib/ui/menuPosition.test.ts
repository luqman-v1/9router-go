// menuPosition is DOM-dependent by design, so the suite hands it the viewports
// and rects it needs rather than reaching for a real layout engine.
import { describe, expect, it } from 'bun:test'
import { maxPanelWidth, placePanel, placementStyle } from './menuPosition'

const DESKTOP = { width: 1200, height: 800 }
const PHONE = { width: 375, height: 700 }

function rect(overrides: Partial<DOMRect> = {}): DOMRect {
  return {
    top: 100,
    bottom: 140,
    left: 200,
    right: 260,
    width: 60,
    height: 40,
    ...overrides,
  } as DOMRect
}

describe('placePanel', () => {
  it('anchors a right-aligned panel to the trigger right edge', () => {
    const placement = placePanel({ rect: rect(), panelWidth: 224, viewport: DESKTOP })
    expect(placement.left).toBe(260 - 224)
    expect(placement.bottom).toBeNull()
  })

  it('anchors a left-aligned panel to the trigger left edge', () => {
    const placement = placePanel({
      align: 'left',
      rect: rect(),
      panelWidth: 224,
      viewport: DESKTOP,
    })
    expect(placement.left).toBe(200)
  })

  // The reported bug: the panel was laid out inside an `overflow-x-auto` table,
  // so a trigger near the right edge pushed it off the window entirely.
  it('keeps a panel near the right edge fully on screen', () => {
    const placement = placePanel({
      rect: rect({ left: 330, right: 366 }),
      panelWidth: 224,
      viewport: PHONE,
    })
    expect(placement.left + 224).toBeLessThanOrEqual(PHONE.width - 8)
    expect(placement.left).toBeGreaterThanOrEqual(8)
  })

  it('keeps a panel near the left edge fully on screen', () => {
    const placement = placePanel({
      align: 'left',
      rect: rect({ left: 4, right: 48 }),
      panelWidth: 224,
      viewport: PHONE,
    })
    expect(placement.left).toBe(8)
  })

  // A panel the window cannot show has to be anchored from the gutter, not
  // from a rect that assumes room for it: a 224px panel at left=200 on a 200px
  // window otherwise sits entirely off-screen to the right.
  it('anchors a panel wider than the viewport against the right gutter', () => {
    const placement = placePanel({
      rect: rect({ left: 200, right: 260 }),
      panelWidth: 224,
      viewport: { width: 200, height: 700 },
    })
    expect(placement.left).toBe(8)
  })

  it('flips above the trigger when there is no room below', () => {
    const placement = placePanel({
      rect: rect({ top: 560, bottom: 600 }),
      panelWidth: 224,
      viewport: { width: 1200, height: 600 },
    })
    expect(placement.bottom).toBe(600 - 560 + 4)
    expect(placement.top).toBe(0)
  })

  it('leaves a panel below the trigger when there is room', () => {
    const placement = placePanel({ rect: rect(), panelWidth: 224, viewport: DESKTOP })
    expect(placement.bottom).toBeNull()
    expect(placement.top).toBe(144)
  })
})

describe('placementStyle', () => {
  it('emits top for a downward panel', () => {
    expect(placementStyle({ left: 10, top: 144, bottom: null })).toBe('left:10px;top:144px')
  })

  it('emits bottom instead of top for a flipped panel', () => {
    expect(placementStyle({ left: 10, top: 0, bottom: 44 })).toBe('left:10px;bottom:44px')
  })

  // `panelWidth` is read back from the rendered panel, so writing it out again
  // as an inline width pinned the panel to whatever width it had when
  // measured, and every label wider than that floor ellipsised for good
  // (issue #261). Sizing is the panel's own CSS; this helper places it.
  it('never pins a width the panel would have to grow out of', () => {
    const style = placementStyle({ left: 10, top: 144, bottom: null })
    expect(style).not.toContain('width')
    expect(style).not.toContain('min-width')
  })
})

describe('maxPanelWidth', () => {
  it('caps a panel to the viewport minus both gutters', () => {
    expect(maxPanelWidth(DESKTOP)).toBe('1184px')
  })

  // The clamp that placePanel used to apply by writing an inline width has to
  // survive as CSS: a 224px panel on a 200px window reported right=232.
  it('keeps a panel that cannot be shown inside the window', () => {
    expect(maxPanelWidth({ width: 200, height: 600 })).toBe('184px')
  })

  it('is zero rather than negative with no viewport at all', () => {
    expect(maxPanelWidth(null)).toBe('0px')
  })
})
