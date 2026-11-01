import { describe, expect, it } from 'bun:test'
import { buildPaletteIndex, resetPaletteIndex, searchPalette } from './commandPalette'

describe('command palette index', () => {
  it('indexes pages, settings, providers and models', () => {
    const index = buildPaletteIndex()
    const groups = new Set(index.map((row) => row.group))
    expect(groups.has('nav')).toBe(true)
    expect(groups.has('settings')).toBe(true)
    expect(groups.has('provider')).toBe(true)
    expect(groups.has('model')).toBe(true)
    // The model catalog is the bulk of it; a build that indexes only the
    // sidebar would make the palette useless for what it exists to find.
    expect(index.length).toBeGreaterThan(500)
  })

  it('builds the index once and reuses it', () => {
    resetPaletteIndex()
    const first = buildPaletteIndex()
    const second = buildPaletteIndex()
    expect(second).toBe(first)
  })

  it('runs nothing until the operator asks', () => {
    // The whole point of the explicit trigger: an empty or whitespace query
    // must not scan the index, so typing never costs a pass over 1,600 rows.
    expect(searchPalette({ text: '' }).total).toBe(0)
    expect(searchPalette({ text: '   ' }).total).toBe(0)
  })

  it('ranks pages and settings above models', () => {
    const result = searchPalette({ text: 'settings' })
    const firstModel = result.rows.findIndex((row) => row.group === 'model')
    const lastNav = result.rows.reduce(
      (acc, row, i) => (row.group === 'nav' || row.group === 'settings' ? i : acc),
      -1
    )
    if (firstModel !== -1 && lastNav !== -1) {
      expect(lastNav).toBeLessThan(firstModel)
    }
  })

  it('requires every term to match', () => {
    const both = searchPalette({ text: 'claude sonnet' })
    const one = searchPalette({ text: 'claude' })
    expect(both.total).toBeGreaterThan(0)
    expect(both.total).toBeLessThan(one.total)
  })

  it('caps the result list so a short query cannot render everything', () => {
    const result = searchPalette({ text: 'gpt', limit: 10 })
    expect(result.rows.length).toBeLessThanOrEqual(10)
    // The total is reported even when the list is capped, so the operator can
    // narrow the query rather than wonder whether the search finished.
    expect(result.total).toBeGreaterThanOrEqual(result.rows.length)
  })

  it('finds a page by its label', () => {
    const result = searchPalette({ text: 'quota tracker' })
    expect(result.rows[0]?.group).toBe('nav')
    expect(result.rows[0]?.label).toBe('Quota Tracker')
  })

  it('finds a settings section by its name', () => {
    const result = searchPalette({ text: 'currency' })
    expect(result.rows.some((row) => row.group === 'settings')).toBe(true)
  })
})
