import { describe, expect, test } from 'bun:test'
import { getCheckAllTargetModelIds } from './checkAllTargets'

describe('getCheckAllTargetModelIds', () => {
  test('returns all model ids when disabled list is empty', () => {
    const models = [{ id: 'm1' }, { id: 'm2' }, { id: 'm3' }]
    expect(getCheckAllTargetModelIds(models, [])).toEqual(['m1', 'm2', 'm3'])
  })

  test('filters out disabled models cleanly', () => {
    const models = [{ id: 'm1' }, { id: 'm2' }, { id: 'm3' }]
    expect(getCheckAllTargetModelIds(models, ['m2'])).toEqual(['m1', 'm3'])
  })

  test('returns empty array when all models are disabled', () => {
    const models = [{ id: 'm1' }, { id: 'm2' }]
    expect(getCheckAllTargetModelIds(models, ['m1', 'm2'])).toEqual([])
  })

  test('handles empty input gracefully', () => {
    expect(getCheckAllTargetModelIds([])).toEqual([])
  })
})
