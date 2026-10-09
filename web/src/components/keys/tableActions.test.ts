// The API-key row menu collapses seven per-row controls into one trigger, and
// the batch bar adds actions over a selection. The rules the table depends on
// are stated once in tableActions.ts and pinned here.
import { describe, expect, it } from 'bun:test'
import {
  batchSummary,
  isBatchBusy,
  isSelected,
  runBatch,
  selectAllState,
  toggleAll,
  toggleOne,
} from './tableActions'

describe('isBatchBusy', () => {
  it('is busy while a run is in flight', () => {
    expect(isBatchBusy(true, ['a'])).toBe(true)
  })

  it('is busy with nothing selected, because there is nothing to act on', () => {
    expect(isBatchBusy(false, [])).toBe(true)
  })

  it('is idle once a selection exists and no run is in flight', () => {
    expect(isBatchBusy(false, ['a'])).toBe(false)
  })
})

describe('runBatch', () => {
  it('counts every key that succeeded', async () => {
    const outcome = await runBatch(['a', 'b'], async () => {})
    expect(outcome).toEqual({ ok: 2, failed: 0, failedIds: [] })
  })

  // A batch that stopped at the first failure would leave the operator unable
  // to tell which keys had already been changed.
  it('keeps going after a failure and reports the failed keys', async () => {
    const attempted: string[] = []
    const outcome = await runBatch(['a', 'bad', 'c'], async (id) => {
      attempted.push(id)
      if (id === 'bad') throw new Error('nope')
    })
    expect(attempted).toEqual(['a', 'bad', 'c'])
    expect(outcome.ok).toBe(2)
    expect(outcome.failed).toBe(1)
    expect(outcome.failedIds).toEqual(['bad'])
  })

  it('labels failures with the caller-provided label', async () => {
    const outcome = await runBatch(
      ['a'],
      async () => {
        throw new Error('nope')
      },
      (id) => `key-${id}`,
    )
    expect(outcome.failedIds).toEqual(['key-a'])
  })
})

describe('batchSummary', () => {
  it('pluralises a fully successful multi-key run', () => {
    expect(batchSummary('enabled', { ok: 3, failed: 0, failedIds: [] })).toBe('3 keys enabled.')
  })

  it('uses the singular for one key', () => {
    expect(batchSummary('enabled', { ok: 1, failed: 0, failedIds: [] })).toBe('1 key enabled.')
  })

  it('names the keys that failed', () => {
    expect(batchSummary('enabled', { ok: 1, failed: 2, failedIds: ['b', 'c'] })).toBe(
      '1 key was enabled. 2 failed: b, c.',
    )
  })

  it('says so when nothing succeeded', () => {
    expect(batchSummary('deleted', { ok: 0, failed: 1, failedIds: ['a'] })).toBe(
      'No key was deleted. 1 failed: a.',
    )
  })
})

describe('selection', () => {
  it('reports a row selected only when its id is listed', () => {
    expect(isSelected(['a'], 'a')).toBe(true)
    expect(isSelected(['a'], 'b')).toBe(false)
  })

  it('treats an empty selection as none selected', () => {
    expect(selectAllState([], ['a', 'b'])).toBe('none')
  })

  it('reports none when there are no keys at all', () => {
    expect(selectAllState([], [])).toBe('none')
  })

  it('reports indeterminate when only some keys are selected', () => {
    expect(selectAllState(['a'], ['a', 'b'])).toBe('some')
  })

  it('reports all when every key is selected', () => {
    expect(selectAllState(['a', 'b'], ['a', 'b'])).toBe('all')
  })

  it('selects everything from a partial selection', () => {
    expect(toggleAll(['a'], ['a', 'b'])).toEqual(['a', 'b'])
  })

  it('clears the selection when everything is already selected', () => {
    expect(toggleAll(['a', 'b'], ['a', 'b'])).toEqual([])
  })

  it('adds a row without disturbing the others', () => {
    expect(toggleOne(['a'], 'b')).toEqual(['a', 'b'])
  })

  it('removes a row without disturbing the others', () => {
    expect(toggleOne(['a', 'b'], 'a')).toEqual(['b'])
  })
})