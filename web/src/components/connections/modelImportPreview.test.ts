import { describe, expect, test } from 'bun:test'
import {
  buildImportPreviewActiveIds,
  filterImportPreviewByState,
  filterImportPreviewModels,
  importPreviewSelectedIds,
  importPreviewSelectionState,
  sortImportPreviewModels,
  toggleImportPreviewSelectAll,
} from './modelImportPreview'

describe('filterImportPreviewModels', () => {
  const models = [
    { id: 'deepseek-chat' },
    { id: 'deepseek-reasoner' },
    { id: 'gpt-4o' },
    { id: 'gemini-2.5-flash' },
  ]

  test('empty query lists every model', () => {
    expect(filterImportPreviewModels(models, '')).toHaveLength(4)
    expect(filterImportPreviewModels(models, '   ')).toHaveLength(4)
  })

  test('substring match is case-insensitive over the id', () => {
    const got = filterImportPreviewModels(models, 'DEE')
    expect(got.map((m) => m.id)).toEqual(['deepseek-chat', 'deepseek-reasoner'])
    expect(filterImportPreviewModels(models, 'flash')).toEqual([{ id: 'gemini-2.5-flash' }])
  })

  test('no match returns an empty list (the panel shows its empty state)', () => {
    expect(filterImportPreviewModels(models, 'claude')).toEqual([])
  })
})

describe('buildImportPreviewActiveIds', () => {
  test('a chip is active when its id is on any of the three source lists', () => {
    const ids = buildImportPreviewActiveIds(['imported-1'], ['custom-2'], ['builtin-3'])
    expect(ids.has('imported-1')).toBe(true)
    expect(ids.has('custom-2')).toBe(true)
    expect(ids.has('builtin-3')).toBe(true)
    expect(ids.has('not-added')).toBe(false)
  })
})

describe('sortImportPreviewModels', () => {
  const models = [{ id: 'b' }, { id: 'A' }, { id: 'c' }]

  test('A–Z is case-insensitive', () => {
    expect(sortImportPreviewModels(models, 'az', {}).map((m) => m.id)).toEqual(['A', 'b', 'c'])
  })

  test('Z–A reverses it', () => {
    expect(sortImportPreviewModels(models, 'za', {}).map((m) => m.id)).toEqual(['c', 'b', 'A'])
  })

  test('latency ascending puts the unmeasured rows last, not first', () => {
    const latency = { b: 800, A: 2500 }
    expect(sortImportPreviewModels(models, 'latency-asc', latency).map((m) => m.id)).toEqual([
      'b',
      'A',
      'c',
    ])
  })

  test('latency descending reverses only the measured prefix', () => {
    const latency = { b: 800, A: 2500 }
    expect(sortImportPreviewModels(models, 'latency-desc', latency).map((m) => m.id)).toEqual([
      'A',
      'b',
      'c',
    ])
  })

  test('the input array is not mutated', () => {
    const input = [{ id: 'b' }, { id: 'A' }]
    sortImportPreviewModels(input, 'az', {})
    expect(input.map((m) => m.id)).toEqual(['b', 'A'])
  })
})

describe('selection', () => {
  const filtered = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]

  test('state reports none selected', () => {
    expect(importPreviewSelectionState(new Set(), filtered)).toEqual({
      selected: 0,
      total: 3,
      allSelected: false,
      indeterminate: false,
    })
  })

  test('state reports a partial selection as indeterminate', () => {
    const state = importPreviewSelectionState(new Set(['b']), filtered)
    expect(state.selected).toBe(1)
    expect(state.allSelected).toBe(false)
    expect(state.indeterminate).toBe(true)
  })

  test('state reports a full selection', () => {
    const state = importPreviewSelectionState(new Set(['a', 'b', 'c']), filtered)
    expect(state.allSelected).toBe(true)
    expect(state.indeterminate).toBe(false)
  })

  test('select-all turns every filtered id on', () => {
    expect([...toggleImportPreviewSelectAll(new Set(['outside']), filtered, true)].sort()).toEqual([
      'a',
      'b',
      'c',
      'outside',
    ])
  })

  test('select-all off clears only the filtered ids, keeping the rest', () => {
    const next = toggleImportPreviewSelectAll(new Set(['a', 'b', 'outside']), filtered, false)
    expect([...next]).toEqual(['outside'])
  })

  test('selected ids are limited to what is still filtered in', () => {
    const narrowed = [{ id: 'b' }]
    expect(importPreviewSelectedIds(new Set(['a', 'b']), narrowed)).toEqual(['b'])
  })
})

describe('filterImportPreviewByState', () => {
  const models = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]
  const active = new Set(['b'])

  test('all passes the catalog through untouched', () => {
    expect(filterImportPreviewByState(models, 'all', active)).toBe(models)
  })

  test('added keeps only the ids on the active list', () => {
    expect(filterImportPreviewByState(models, 'added', active).map((m) => m.id)).toEqual(['b'])
  })

  test('available keeps only the ids missing from the active list', () => {
    expect(filterImportPreviewByState(models, 'available', active).map((m) => m.id)).toEqual(['a', 'c'])
  })
})
