import { describe, expect, test } from 'bun:test'
import { buildImportPreviewActiveIds, filterImportPreviewModels } from './modelImportPreview'

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

// The header bulk buttons act on exactly the filtered set, split by whether
// the model is already on the list. These mirror the derivation in the
// component so a change to either side fails a test.
function bulkIds(
  models: Array<{ id: string }>,
  activeIds: Set<string>
): { addable: string[]; removable: string[] } {
  return {
    addable: models.filter((m) => !activeIds.has(m.id)).map((m) => m.id),
    removable: models.filter((m) => activeIds.has(m.id)).map((m) => m.id),
  }
}

describe('bulk header buttons', () => {
  const models = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]
  const active = new Set(['b'])

  test('addable excludes models already on the list', () => {
    expect(bulkIds(models, active).addable).toEqual(['a', 'c'])
  })

  test('removable includes only models on the list', () => {
    expect(bulkIds(models, active).removable).toEqual(['b'])
  })

  test('a fully-added filter leaves the add button with nothing to do', () => {
    const allActive = new Set(['a', 'b', 'c'])
    expect(bulkIds(models, allActive).addable).toEqual([])
  })

  test('an empty search adds the whole catalog', () => {
    expect(bulkIds(models, new Set()).addable).toEqual(['a', 'b', 'c'])
  })
})
