// menuPosition is DOM-dependent by design, so the suite hands it the viewports
// and rects it needs rather than reaching for a real layout engine.
import { describe, expect, it } from 'bun:test'
import { placePanel, placementStyle } from './menuPosition'

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
    expect(placement.left + placement.width).toBeLessThanOrEqual(PHONE.width - 8)
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

  it('shrinks the panel rather than overflowing a narrow viewport', () => {
    const placement = placePanel({ rect: rect(), panelWidth: 224, viewport: { width: 200, height: 700 } })
    expect(placement.width).toBe(200 - 16)
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
    expect(placementStyle({ left: 10, top: 144, bottom: null, width: 224 })).toBe(
      'left:10px;width:224px;top:144px',
    )
  })

  it('emits bottom instead of top for a flipped panel', () => {
    expect(placementStyle({ left: 10, top: 0, bottom: 44, width: 224 })).toBe(
      'left:10px;width:224px;bottom:44px',
    )
  })
})