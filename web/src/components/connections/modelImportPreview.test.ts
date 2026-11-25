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
